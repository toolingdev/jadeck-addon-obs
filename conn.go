package main

// One OBS connection: keeps a live obs-websocket session (reconnecting
// with backoff), a picture of OBS built from events (scenes, program,
// scene items, audio inputs, mutes, meters) and, once a second, the
// stream / record status and OBS's stats. Nothing in OBS changes on
// connect.

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/toolingdev/jadeck/sdk/addon"
)

type config struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Password string `json:"password"`
	ReadOnly bool   `json:"readOnly"`
}

type sceneItem struct {
	ID      int    `json:"sceneItemId"`
	Source  string `json:"sourceName"`
	Enabled bool   `json:"sceneItemEnabled"`
	Index   int    `json:"sceneItemIndex"`
}

type audioInput struct {
	Name   string
	Kind   string
	Muted  bool
	Db     float64   // fader, dB (-100 … 26)
	Levels []float64 // meter, per channel, amplitude 0-1 (peak)
}

type outStatus struct {
	Active       bool    `json:"outputActive"`
	Reconnecting bool    `json:"outputReconnecting"`
	Paused       bool    `json:"outputPaused"`
	Duration     int64   `json:"outputDuration"` // ms
	Bytes        int64   `json:"outputBytes"`
	Skipped      int64   `json:"outputSkippedFrames"`
	Total        int64   `json:"outputTotalFrames"`
	Congestion   float64 `json:"outputCongestion"`
}

type obsStats struct {
	CPU           float64 `json:"cpuUsage"`
	FPS           float64 `json:"activeFps"`
	RenderSkipped int64   `json:"renderSkippedFrames"`
	RenderTotal   int64   `json:"renderTotalFrames"`
	OutputSkipped int64   `json:"outputSkippedFrames"`
	OutputTotal   int64   `json:"outputTotalFrames"`
}

// Conn is one OBS.
type Conn struct {
	cfg    config
	url    string
	where  string // "THIS MAC" / the host, for widget headers
	ctx    context.Context
	cancel context.CancelFunc
	now    func() time.Time
	dial   func(ctx context.Context) (*obsClient, error)

	mu      sync.Mutex
	cl      *obsClient
	err     error
	notify  addon.Notifier
	version string
	wsver   string
	scenes  []string // OBS's order, top of OBS's list first
	program string
	preview string
	next    string // the next scene outside Studio Mode (jadeck's preview)
	studio  bool
	items   map[string][]sceneItem
	inputs  map[string]string // name → kind
	audio   map[string]*audioInput
	stream  outStatus
	record  outStatus
	stats   obsStats
	kbps    int
	lastB   int64
	lastAt  time.Time
	shot    map[string]shotCache
	sample  bool // a made-up OBS for the Settings previews
}

type shotCache struct {
	at   time.Time
	data []byte
}

func newConn(cfg config) *Conn {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Conn{cfg: cfg, ctx: ctx, cancel: cancel, now: time.Now,
		url:   "ws://" + net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		items: map[string][]sceneItem{}, inputs: map[string]string{}, audio: map[string]*audioInput{}, shot: map[string]shotCache{}}
	c.where = strings.ToUpper(cfg.Host)
	if cfg.Host == "127.0.0.1" || cfg.Host == "localhost" || cfg.Host == "::1" {
		c.where = "THIS COMPUTER"
	}
	c.dial = func(ctx context.Context) (*obsClient, error) {
		return dialOBS(ctx, c.url, cfg.Password, subscribeDefault|subInputMeters, c.onEvent)
	}
	return c
}

func (c *Conn) SetNotify(n addon.Notifier) { c.mu.Lock(); c.notify = n; c.mu.Unlock() }

func (c *Conn) changed() {
	c.mu.Lock()
	n := c.notify
	c.mu.Unlock()
	if n != nil {
		n()
	}
}

func (c *Conn) Close() error {
	c.cancel()
	c.mu.Lock()
	cl := c.cl
	c.mu.Unlock()
	if cl != nil {
		cl.Close()
	}
	return nil
}

// client is the live session, or why there is none.
func (c *Conn) client() (*obsClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sample {
		return nil, nil
	}
	if c.cl != nil && !c.cl.closed() {
		return c.cl, nil
	}
	if c.err != nil {
		return nil, c.err
	}
	return nil, errors.New("connecting to OBS…")
}

// start keeps a session up until Close.
func (c *Conn) start() { go c.run() }

func (c *Conn) run() {
	back := time.Second
	for c.ctx.Err() == nil {
		cl, err := c.dial(c.ctx)
		if err != nil {
			c.mu.Lock()
			c.err = friendly(err, c.cfg)
			c.mu.Unlock()
			c.changed()
			// a wrong password won't fix itself quickly: try less often
			wait := back
			if errors.Is(err, errWrongPassword) || errors.Is(err, errNeedPassword) {
				wait = 15 * time.Second
			}
			select {
			case <-c.ctx.Done():
				return
			case <-time.After(wait):
			}
			back = min(back*2, 10*time.Second)
			continue
		}
		back = time.Second
		c.mu.Lock()
		c.cl, c.err = cl, nil
		c.wsver = cl.WSVersion
		c.mu.Unlock()
		c.refreshAll(cl)
		c.changed()
		c.poll(cl)
		cl.Close()
		c.mu.Lock()
		c.cl = nil
		if c.ctx.Err() == nil {
			c.err = errors.New("OBS closed (or was quit); reconnecting…")
		}
		c.stream, c.record = outStatus{}, outStatus{}
		c.mu.Unlock()
		c.changed()
	}
}

// friendly turns a dial error into what to do about it.
func friendly(err error, cfg config) error {
	if errors.Is(err, errWrongPassword) || errors.Is(err, errNeedPassword) {
		return err
	}
	s := err.Error()
	if strings.Contains(s, "refused") {
		return errors.New("OBS isn't answering on port " + strconv.Itoa(cfg.Port) + ": open OBS, and tick Tools → WebSocket Server Settings → Enable WebSocket server")
	}
	if strings.Contains(s, "timeout") || strings.Contains(s, "deadline") || strings.Contains(s, "no route") {
		return errors.New("can't reach " + cfg.Host + ": is that computer on, and on this network or Tailscale?")
	}
	return err
}

// poll reads stream / record / stats once a second until the session ends.
func (c *Conn) poll(cl *obsClient) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		c.readOutputs(cl)
		select {
		case <-c.ctx.Done():
			return
		case <-cl.done:
			return
		case <-t.C:
		}
	}
}

func (c *Conn) readOutputs(cl *obsClient) {
	ctx := c.ctx
	var st, rec outStatus
	var stats obsStats
	errS := cl.call(ctx, "GetStreamStatus", nil, &st)
	errR := cl.call(ctx, "GetRecordStatus", nil, &rec)
	cl.call(ctx, "GetStats", nil, &stats)
	c.mu.Lock()
	was := c.stream.Active != st.Active || c.record.Active != rec.Active
	if errS == nil {
		now := c.now()
		if st.Active && !c.lastAt.IsZero() && st.Bytes >= c.lastB {
			if dt := now.Sub(c.lastAt).Seconds(); dt > 0.2 {
				c.kbps = int(float64(st.Bytes-c.lastB) * 8 / 1000 / dt)
			}
		}
		if !st.Active {
			c.kbps = 0
		}
		c.lastB, c.lastAt = st.Bytes, now
		c.stream = st
	}
	if errR == nil {
		c.record = rec
	}
	c.stats = stats
	c.mu.Unlock()
	_ = was
	c.changed() // the clock and numbers move every second
}

// refreshAll reads everything the widgets show.
func (c *Conn) refreshAll(cl *obsClient) {
	var v struct {
		OBSVersion string `json:"obsVersion"`
	}
	if cl.call(c.ctx, "GetVersion", nil, &v) == nil {
		c.mu.Lock()
		c.version = v.OBSVersion
		c.mu.Unlock()
	}
	var sm struct {
		Enabled bool `json:"studioModeEnabled"`
	}
	if cl.call(c.ctx, "GetStudioModeEnabled", nil, &sm) == nil {
		c.mu.Lock()
		c.studio = sm.Enabled
		c.mu.Unlock()
	}
	c.refreshScenes(cl)
	c.refreshInputs(cl)
}

func (c *Conn) refreshScenes(cl *obsClient) {
	var r struct {
		Program string `json:"currentProgramSceneName"`
		Preview string `json:"currentPreviewSceneName"`
		Scenes  []struct {
			Name  string `json:"sceneName"`
			Index int    `json:"sceneIndex"`
		} `json:"scenes"`
	}
	if cl.call(c.ctx, "GetSceneList", nil, &r) != nil {
		return
	}
	// OBS numbers scenes from the bottom of its list: show the top first.
	sort.Slice(r.Scenes, func(i, j int) bool { return r.Scenes[i].Index > r.Scenes[j].Index })
	names := make([]string, 0, len(r.Scenes))
	for _, s := range r.Scenes {
		names = append(names, s.Name)
	}
	items := map[string][]sceneItem{}
	for _, n := range names {
		var il struct {
			Items []sceneItem `json:"sceneItems"`
		}
		if cl.call(c.ctx, "GetSceneItemList", map[string]any{"sceneName": n}, &il) == nil {
			items[n] = il.Items
		}
	}
	c.mu.Lock()
	c.scenes, c.program, c.preview, c.items = names, r.Program, r.Preview, items
	c.mu.Unlock()
}

func (c *Conn) refreshItems(cl *obsClient, scene string) {
	var il struct {
		Items []sceneItem `json:"sceneItems"`
	}
	if cl.call(c.ctx, "GetSceneItemList", map[string]any{"sceneName": scene}, &il) == nil {
		c.mu.Lock()
		c.items[scene] = il.Items
		c.mu.Unlock()
	}
}

func (c *Conn) refreshInputs(cl *obsClient) {
	var r struct {
		Inputs []struct {
			Name string `json:"inputName"`
			Kind string `json:"unversionedInputKind"`
		} `json:"inputs"`
	}
	if cl.call(c.ctx, "GetInputList", nil, &r) != nil {
		return
	}
	inputs := map[string]string{}
	audio := map[string]*audioInput{}
	for _, in := range r.Inputs {
		inputs[in.Name] = in.Kind
		var m struct {
			Muted bool `json:"inputMuted"`
		}
		if cl.call(c.ctx, "GetInputMute", map[string]any{"inputName": in.Name}, &m) != nil {
			continue // no audio
		}
		var v struct {
			Db float64 `json:"inputVolumeDb"`
		}
		cl.call(c.ctx, "GetInputVolume", map[string]any{"inputName": in.Name}, &v)
		audio[in.Name] = &audioInput{Name: in.Name, Kind: in.Kind, Muted: m.Muted, Db: v.Db}
	}
	c.mu.Lock()
	c.inputs, c.audio = inputs, audio
	c.mu.Unlock()
}

// onEvent keeps the picture current. It runs on the reading goroutine,
// so anything that needs a request goes to its own goroutine.
func (c *Conn) onEvent(typ string, d json.RawMessage) {
	cl, _ := c.client()
	later := func(f func(*obsClient)) {
		if cl != nil {
			go func() { f(cl); c.changed() }()
		}
	}
	switch typ {
	case "InputVolumeMeters":
		var e struct {
			Inputs []struct {
				Name   string      `json:"inputName"`
				Levels [][]float64 `json:"inputLevelsMul"`
			} `json:"inputs"`
		}
		if json.Unmarshal(d, &e) != nil {
			return
		}
		c.mu.Lock()
		for _, in := range e.Inputs {
			if a := c.audio[in.Name]; a != nil {
				lv := make([]float64, 0, len(in.Levels))
				for _, ch := range in.Levels {
					if len(ch) >= 2 {
						lv = append(lv, ch[1]) // peak
					}
				}
				a.Levels = lv
			}
		}
		c.mu.Unlock()
		return // meters are read by the audio widget, not pushed
	case "CurrentProgramSceneChanged":
		var e struct {
			Name string `json:"sceneName"`
		}
		json.Unmarshal(d, &e)
		c.mu.Lock()
		c.program = e.Name
		c.mu.Unlock()
	case "CurrentPreviewSceneChanged":
		var e struct {
			Name string `json:"sceneName"`
		}
		json.Unmarshal(d, &e)
		c.mu.Lock()
		c.preview = e.Name
		c.mu.Unlock()
	case "StudioModeStateChanged":
		var e struct {
			Enabled bool `json:"studioModeEnabled"`
		}
		json.Unmarshal(d, &e)
		c.mu.Lock()
		c.studio = e.Enabled
		c.mu.Unlock()
	case "CurrentSceneCollectionChanged", "CurrentProfileChanged":
		later(c.refreshAll) // a whole other set of scenes and sources
		return
	case "SceneCreated", "SceneRemoved", "SceneNameChanged", "SceneListChanged":
		later(c.refreshScenes)
		return
	case "SceneItemEnableStateChanged":
		var e struct {
			Scene   string `json:"sceneName"`
			ID      int    `json:"sceneItemId"`
			Enabled bool   `json:"sceneItemEnabled"`
		}
		json.Unmarshal(d, &e)
		c.mu.Lock()
		for i, it := range c.items[e.Scene] {
			if it.ID == e.ID {
				c.items[e.Scene][i].Enabled = e.Enabled
			}
		}
		c.mu.Unlock()
	case "SceneItemCreated", "SceneItemRemoved", "SceneItemListReindexed":
		var e struct {
			Scene string `json:"sceneName"`
		}
		json.Unmarshal(d, &e)
		later(func(cl *obsClient) { c.refreshItems(cl, e.Scene) })
		return
	case "InputCreated", "InputRemoved", "InputNameChanged":
		later(func(cl *obsClient) { c.refreshInputs(cl); c.refreshScenes(cl) })
		return
	case "InputMuteStateChanged":
		var e struct {
			Name  string `json:"inputName"`
			Muted bool   `json:"inputMuted"`
		}
		json.Unmarshal(d, &e)
		c.mu.Lock()
		if a := c.audio[e.Name]; a != nil {
			a.Muted = e.Muted
		}
		c.mu.Unlock()
	case "InputVolumeChanged":
		var e struct {
			Name string  `json:"inputName"`
			Db   float64 `json:"inputVolumeDb"`
		}
		json.Unmarshal(d, &e)
		c.mu.Lock()
		if a := c.audio[e.Name]; a != nil {
			a.Db = e.Db
		}
		c.mu.Unlock()
	case "StreamStateChanged", "RecordStateChanged":
		later(c.readOutputs)
		return
	case "ExitStarted":
		if cl != nil {
			cl.Close()
		}
		return
	default:
		return
	}
	c.changed()
}

// ampDb is an amplitude (0-1) in dB, -144 for silence.
func ampDb(a float64) float64 {
	if a <= 0 {
		return -144
	}
	return math.Max(-144, 20*math.Log10(a))
}

func msDur(ms int64) time.Duration { return time.Duration(ms) * time.Millisecond }

func jsonBytes(v any) ([]byte, error) { return json.Marshal(v) }
