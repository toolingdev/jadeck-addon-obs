package main

// fakeOBS speaks enough obs-websocket v5 (server side, password and all)
// for the tests: scenes, scene items, inputs with audio, stream / record,
// stats, screenshots, and making scenes and inputs. Requests it doesn't
// know answer code 204 (unknown request), like OBS.

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type fakeItem struct {
	ID      int
	Source  string
	Enabled bool
}

type fakeOBS struct {
	t        *testing.T
	srv      *httptest.Server
	password string

	mu       sync.Mutex
	scenes   []string // top of OBS's list first
	program  string
	preview  string
	studio   bool
	items    map[string][]*fakeItem
	inputs   map[string]string // name → kind
	muted    map[string]bool
	volume   map[string]float64
	stream   bool
	record   bool
	kinds    []string
	nextID   int
	requests []string
	conns    []*fakeWS
}

func newFakeOBS(t *testing.T, password string) *fakeOBS {
	f := &fakeOBS{t: t, password: password,
		scenes: []string{"Pastor", "Pastor + lyrics"}, program: "Pastor",
		items:  map[string][]*fakeItem{"Pastor": {{1, "Zoom", true}}, "Pastor + lyrics": {{2, "Zoom", true}, {3, "Proclaim", true}}},
		inputs: map[string]string{"Zoom": "screen_capture", "Proclaim": "ndi_source", "Mic/Aux": "coreaudio_input_capture"},
		muted:  map[string]bool{"Mic/Aux": true}, volume: map[string]float64{"Zoom": 0, "Mic/Aux": -6},
		kinds:  []string{"screen_capture", "sck_audio_capture", "ndi_source", "color_source_v3", "text_ft2_source_v2", "coreaudio_input_capture"},
		nextID: 10,
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOBS) hostPort() (string, int) {
	h, p, _ := net.SplitHostPort(strings.TrimPrefix(f.srv.URL, "http://"))
	n, _ := strconv.Atoi(p)
	return h, n
}

// fakeWS is the server end of one websocket.
type fakeWS struct {
	c  net.Conn
	br *bufio.Reader
	mu sync.Mutex
}

func (w *fakeWS) write(v any) {
	b, _ := json.Marshal(v)
	w.frame(0x1, b)
}

func (w *fakeWS) frame(op byte, b []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	h := []byte{0x80 | op}
	switch {
	case len(b) < 126:
		h = append(h, byte(len(b)))
	case len(b) < 65536:
		h = append(h, 126, byte(len(b)>>8), byte(len(b)))
	default:
		h = append(h, 127)
		var x [8]byte
		binary.BigEndian.PutUint64(x[:], uint64(len(b)))
		h = append(h, x[:]...)
	}
	w.c.Write(append(h, b...))
}

func (w *fakeWS) read() ([]byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(w.br, h[:]); err != nil {
		return nil, err
	}
	n := uint64(h[1] & 0x7f)
	switch n {
	case 126:
		var x [2]byte
		io.ReadFull(w.br, x[:])
		n = uint64(binary.BigEndian.Uint16(x[:]))
	case 127:
		var x [8]byte
		io.ReadFull(w.br, x[:])
		n = binary.BigEndian.Uint64(x[:])
	}
	var mask [4]byte
	if h[1]&0x80 != 0 {
		io.ReadFull(w.br, mask[:])
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(w.br, b); err != nil {
		return nil, err
	}
	for i := range b {
		b[i] ^= mask[i%4]
	}
	if h[0]&0x0f == 0x8 {
		return nil, io.EOF
	}
	return b, nil
}

func (f *fakeOBS) serve(rw http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Sec-WebSocket-Key")
	hj, _ := rw.(http.Hijacker)
	c, brw, err := hj.Hijack()
	if err != nil {
		return
	}
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
	brw.Flush()
	ws := &fakeWS{c: c, br: brw.Reader}
	defer c.Close()
	hello := map[string]any{"obsWebSocketVersion": "5.5.4", "rpcVersion": 1}
	const salt, challenge = "c2FsdA==", "Y2hhbGxlbmdl"
	if f.password != "" {
		hello["authentication"] = map[string]any{"salt": salt, "challenge": challenge}
	}
	ws.write(map[string]any{"op": 0, "d": hello})
	b, err := ws.read()
	if err != nil {
		return
	}
	var id struct {
		D struct {
			Auth string `json:"authentication"`
		} `json:"d"`
	}
	json.Unmarshal(b, &id)
	if f.password != "" && id.D.Auth != obsAuth(f.password, salt, challenge) {
		ws.frame(0x8, append([]byte{0x0f, 0xa9}, "Authentication failed."...)) // 4009
		return
	}
	ws.write(map[string]any{"op": 2, "d": map[string]any{"negotiatedRpcVersion": 1}})
	f.mu.Lock()
	f.conns = append(f.conns, ws)
	f.mu.Unlock()
	for {
		b, err := ws.read()
		if err != nil {
			return
		}
		var m struct {
			Op int `json:"op"`
			D  struct {
				Type string         `json:"requestType"`
				ID   string         `json:"requestId"`
				Data map[string]any `json:"requestData"`
			} `json:"d"`
		}
		if json.Unmarshal(b, &m) != nil || m.Op != 6 {
			continue
		}
		data, code, comment := f.request(m.D.Type, m.D.Data)
		status := map[string]any{"result": code == 100, "code": code}
		if comment != "" {
			status["comment"] = comment
		}
		ws.write(map[string]any{"op": 7, "d": map[string]any{"requestType": m.D.Type, "requestId": m.D.ID, "requestStatus": status, "responseData": data}})
	}
}

// event sends an event to every client.
func (f *fakeOBS) event(typ string, data map[string]any) {
	f.mu.Lock()
	conns := append([]*fakeWS(nil), f.conns...)
	f.mu.Unlock()
	for _, w := range conns {
		w.write(map[string]any{"op": 5, "d": map[string]any{"eventType": typ, "eventIntent": 1, "eventData": data}})
	}
}

func (f *fakeOBS) did(req string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.requests {
		if r == req {
			return true
		}
	}
	return false
}

func (f *fakeOBS) request(typ string, d map[string]any) (any, int, string) {
	f.mu.Lock()
	f.requests = append(f.requests, typ)
	var events [][2]any
	defer func() {
		f.mu.Unlock()
		for _, e := range events {
			f.event(e[0].(string), e[1].(map[string]any))
		}
	}()
	str := func(k string) string { s, _ := d[k].(string); return s }
	switch typ {
	case "GetVersion":
		return map[string]any{"obsVersion": "31.1.2", "obsWebSocketVersion": "5.5.4"}, 100, ""
	case "GetStudioModeEnabled":
		return map[string]any{"studioModeEnabled": f.studio}, 100, ""
	case "GetSceneList":
		var sc []map[string]any
		for i, s := range f.scenes {
			sc = append(sc, map[string]any{"sceneName": s, "sceneIndex": len(f.scenes) - 1 - i})
		}
		return map[string]any{"currentProgramSceneName": f.program, "currentPreviewSceneName": f.preview, "scenes": sc}, 100, ""
	case "GetSceneItemList":
		var it []map[string]any
		for i, x := range f.items[str("sceneName")] {
			it = append(it, map[string]any{"sceneItemId": x.ID, "sourceName": x.Source, "sceneItemEnabled": x.Enabled, "sceneItemIndex": i})
		}
		return map[string]any{"sceneItems": it}, 100, ""
	case "GetInputList":
		var in []map[string]any
		for n, k := range f.inputs {
			in = append(in, map[string]any{"inputName": n, "unversionedInputKind": k, "inputKind": k})
		}
		return map[string]any{"inputs": in}, 100, ""
	case "GetInputMute":
		if _, ok := f.volume[str("inputName")]; !ok {
			return nil, 604, "The specified input does not support audio."
		}
		return map[string]any{"inputMuted": f.muted[str("inputName")]}, 100, ""
	case "GetInputVolume":
		return map[string]any{"inputVolumeDb": f.volume[str("inputName")]}, 100, ""
	case "SetInputMute":
		f.muted[str("inputName")] = d["inputMuted"] == true
		events = append(events, [2]any{"InputMuteStateChanged", map[string]any{"inputName": str("inputName"), "inputMuted": d["inputMuted"]}})
		return nil, 100, ""
	case "SetInputVolume":
		f.volume[str("inputName")], _ = d["inputVolumeDb"].(float64)
		return nil, 100, ""
	case "GetStreamStatus":
		return map[string]any{"outputActive": f.stream, "outputDuration": 61000, "outputBytes": 1000000, "outputSkippedFrames": 3, "outputTotalFrames": 1800}, 100, ""
	case "GetRecordStatus":
		return map[string]any{"outputActive": f.record, "outputDuration": 0}, 100, ""
	case "GetStats":
		return map[string]any{"cpuUsage": 12.5, "activeFps": 30}, 100, ""
	case "StartStream", "StopStream":
		on := typ == "StartStream"
		if f.stream == on {
			return nil, 501, "The stream output is already in that state."
		}
		f.stream = on
		events = append(events, [2]any{"StreamStateChanged", map[string]any{"outputActive": on}})
		return nil, 100, ""
	case "StartRecord", "StopRecord":
		f.record = typ == "StartRecord"
		return nil, 100, ""
	case "SetCurrentProgramScene":
		f.program = str("sceneName")
		events = append(events, [2]any{"CurrentProgramSceneChanged", map[string]any{"sceneName": f.program}})
		return nil, 100, ""
	case "SetCurrentPreviewScene":
		f.preview = str("sceneName")
		events = append(events, [2]any{"CurrentPreviewSceneChanged", map[string]any{"sceneName": f.preview}})
		return nil, 100, ""
	case "TriggerStudioModeTransition":
		f.program, f.preview = f.preview, f.program
		events = append(events, [2]any{"CurrentProgramSceneChanged", map[string]any{"sceneName": f.program}})
		return nil, 100, ""
	case "SetCurrentSceneTransition", "SetCurrentSceneTransitionDuration", "SetSceneItemTransform":
		return nil, 100, ""
	case "SetSceneItemEnabled":
		id := int(d["sceneItemId"].(float64))
		for _, x := range f.items[str("sceneName")] {
			if x.ID == id {
				x.Enabled = d["sceneItemEnabled"] == true
			}
		}
		events = append(events, [2]any{"SceneItemEnableStateChanged", map[string]any{"sceneName": str("sceneName"), "sceneItemId": id, "sceneItemEnabled": d["sceneItemEnabled"]}})
		return nil, 100, ""
	case "GetSourceScreenshot":
		return map[string]any{"imageData": "data:image/jpg;base64," + base64.StdEncoding.EncodeToString(fakePicture(f.program))}, 100, ""
	case "GetInputKindList":
		return map[string]any{"inputKinds": f.kinds}, 100, ""
	case "GetVideoSettings":
		return map[string]any{"baseWidth": 1920, "baseHeight": 1080}, 100, ""
	case "CreateScene":
		f.scenes = append(f.scenes, str("sceneName"))
		f.items[str("sceneName")] = nil
		return map[string]any{"sceneUuid": "x"}, 100, ""
	case "RemoveScene":
		for i, x := range f.scenes {
			if x == str("sceneName") {
				f.scenes = append(f.scenes[:i], f.scenes[i+1:]...)
				break
			}
		}
		return nil, 100, ""
	case "CreateInput":
		if !strings.HasSuffix(str("inputKind"), "_v3") && !strings.HasSuffix(str("inputKind"), "_v2") && !contains(f.kinds, str("inputKind")) || str("inputKind") == "color_source" {
			return nil, 605, "Your specified input kind is not supported by OBS."
		}
		if _, ok := f.inputs[str("inputName")]; ok {
			return nil, 601, "A source already exists by that input name."
		}
		f.inputs[str("inputName")] = str("inputKind")
		f.nextID++
		f.items[str("sceneName")] = append(f.items[str("sceneName")], &fakeItem{f.nextID, str("inputName"), true})
		return map[string]any{"sceneItemId": f.nextID}, 100, ""
	case "CreateSceneItem":
		f.nextID++
		f.items[str("sceneName")] = append(f.items[str("sceneName")], &fakeItem{f.nextID, str("sourceName"), true})
		return map[string]any{"sceneItemId": f.nextID}, 100, ""
	}
	return nil, 204, "Your request type is not valid."
}

// fakePicture is a 16:9 JPEG: a blue-grey gradient (a different tint per
// scene) with a lower-third band, like Pastor + lyrics.
func fakePicture(scene string) []byte {
	const w, h = 640, 360
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	tint := uint8(len(scene) * 9)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{uint8(30 + y/8), uint8(40 + y/7), uint8(60+y/6) + tint/4, 255}
			if dx, dy := x-w/2, y-h/3; dx*dx+dy*dy*2 < 2800 || (y > h/2 && dx*dx < 9000-(y-h/2)*10) {
				c = color.RGBA{90, 100, 118, 255} // a person
			}
			if y > h*3/4 && strings.Contains(scene, "lyrics") {
				c = color.RGBA{0, 0, 0, 255}
			}
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	jpeg.Encode(&b, img, &jpeg.Options{Quality: 70})
	return b.Bytes()
}
