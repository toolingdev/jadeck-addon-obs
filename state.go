package main

// What the widgets read (AddonStateFor), the lists their settings pick
// from (AddonOptions), and the pictures and meters they fetch (Resource).

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/toolingdev/jadeck/sdk/addon"
)

type wset map[string]any

func (w wset) str(k, d string) string {
	if v, ok := w[k].(string); ok && v != "" {
		return v
	}
	return d
}

func (w wset) bool(k string, d bool) bool {
	switch v := w[k].(type) {
	case bool:
		return v
	case string:
		return v == "true"
	}
	return d
}

func (w wset) num(k string, d float64) float64 {
	switch v := w[k].(type) {
	case float64:
		return v
	case string:
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return d
}

func (w wset) list(k string) []string {
	switch v := w[k].(type) {
	case []any:
		var out []string
		for _, x := range v {
			if s, ok := x.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if v != "" {
			return strings.Split(v, ",")
		}
	}
	return nil
}

func (c *Conn) AddonState(ctx context.Context) (map[string]any, error) {
	return c.AddonStateFor(ctx, nil)
}

// AddonStateFor: an error when OBS isn't connected (so Test says why in
// red), else everything the widgets draw.
func (c *Conn) AddonStateFor(ctx context.Context, settings map[string]any) (map[string]any, error) {
	if _, err := c.client(); err != nil {
		return nil, err
	}
	w := wset(settings)
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	st := map[string]any{
		"connected": true,
		"source":    "OBS · " + c.where,
		"version":   c.version,
		"program":   c.program,
		"preview":   c.preview,
		"studio":    c.studio,
		"readOnly":  c.cfg.ReadOnly,
	}
	// stream and record
	st["streamOn"] = c.stream.Active
	st["streamReconnecting"] = c.stream.Reconnecting
	if c.stream.Active {
		st["streamSince"] = now.Add(-msDur(c.stream.Duration)).UTC().Format("2006-01-02T15:04:05.000Z")
	}
	st["recordOn"] = c.record.Active
	st["recordPaused"] = c.record.Paused
	if c.record.Active {
		st["recordSince"] = now.Add(-msDur(c.record.Duration)).UTC().Format("2006-01-02T15:04:05.000Z")
	}
	st["kbps"] = c.kbps
	dropped := 0.0
	if c.stream.Total > 0 {
		dropped = 100 * float64(c.stream.Skipped) / float64(c.stream.Total)
	}
	st["dropped"] = math.Round(dropped*10) / 10
	st["cpu"] = math.Round(c.stats.CPU*10) / 10
	st["fps"] = math.Round(c.stats.FPS*10) / 10
	lag := 0.0
	if c.stats.RenderTotal > 0 {
		lag = 100 * float64(c.stats.RenderSkipped) / float64(c.stats.RenderTotal)
	}
	st["health"], st["healthText"] = health(c.stream, dropped, lag, c.stats.CPU)

	hold := int(w.num("holdSeconds", 5))
	if hold < 3 || hold > 10 {
		hold = 5
	}
	st["hold"] = hold

	// scenes, as buttons: red on program, green in Studio Mode's preview
	pick := c.scenes
	if want := w.list("scenes"); len(want) > 0 {
		pick = nil
		for _, s := range want {
			if c.items[s] != nil || contains(c.scenes, s) {
				pick = append(pick, s)
			}
		}
	}
	var sb []map[string]any
	for i, s := range pick {
		b := map[string]any{"id": "scene:" + s, "label": s, "action": "scene", "params": map[string]any{"scene": s}}
		switch {
		case s == c.program:
			b["tone"], b["sub"] = "pgm", "ON AIR"
		case (c.studio && s == c.preview) || (!c.studio && s == c.next):
			b["tone"], b["sub"] = "pvw", "NEXT"
		default:
			b["sub"] = strconv.Itoa(i + 1)
		}
		if c.cfg.ReadOnly {
			b["disabled"] = true
		}
		sb = append(sb, b)
	}
	st["sceneButtons"] = sb

	// lyrics: the lyrics source's item in the scene on air
	src := w.str("source", "")
	if src == "" {
		src = c.guessLyrics()
	}
	st["lyricsSource"] = src
	on, found := false, false
	for _, it := range c.items[c.program] {
		if it.Source == src {
			found, on = true, it.Enabled
		}
	}
	st["lyricsOn"] = on
	lb := map[string]any{"id": "lyrics", "action": "lyrics", "params": map[string]any{"on": !on, "source": src}}
	switch {
	case src == "":
		lb["label"], lb["sub"], lb["disabled"] = "Lyrics", "NO PROCLAIM SOURCE IN OBS", true
	case !found:
		lb["label"], lb["sub"], lb["disabled"] = "Lyrics", "NOT IN THIS SCENE", true
	case on:
		lb["label"], lb["sub"], lb["tone"] = "On screen", "TAP TO HIDE", "pvw"
	default:
		lb["label"], lb["sub"] = "Hidden", "TAP TO SHOW"
	}
	if c.cfg.ReadOnly {
		lb["disabled"] = true
	}
	lyr := []any{lb}
	for _, k := range []struct{ key, def, label string }{{"lowerScene", "Pastor + lyrics", "Lower third"}, {"fullScene", "Slide full screen", "Full screen slide"}} {
		s := w.str(k.key, "")
		if s == "" && contains(c.scenes, k.def) {
			s = k.def
		}
		if s == "" || !contains(c.scenes, s) {
			continue
		}
		b := map[string]any{"id": k.key, "label": k.label, "sub": s, "action": "scene", "params": map[string]any{"scene": s}}
		if s == c.program {
			b["active"] = true
		}
		if c.cfg.ReadOnly {
			b["disabled"] = true
		}
		lyr = append(lyr, b)
	}
	st["lyricsButtons"] = lyr

	// audio strips
	st["mixerStrips"] = c.strips(w.list("strips"))
	return st, nil
}

// strips is the audio inputs to show: the picked ones in order, else every
// audio input (desktop audio and mics first, then the rest by name).
func (c *Conn) strips(want []string) []map[string]any {
	names := want
	if len(names) == 0 {
		for n := range c.audio {
			names = append(names, n)
		}
		sortAudio(names, c.audio)
	}
	var out []map[string]any
	for _, n := range names {
		a := c.audio[n]
		if a == nil {
			continue
		}
		m := []float64{}
		for _, l := range a.Levels {
			m = append(m, math.Round(l*1000)/1000)
		}
		out = append(out, map[string]any{"id": a.Name, "name": a.Name, "kind": kindTag(a.Kind), "muted": a.Muted, "db": math.Round(a.Db*10) / 10, "meter": m})
	}
	return out
}

// guessLyrics is the source that's most likely Proclaim's lyrics: an NDI
// source, or one named Proclaim / Lyrics.
func (c *Conn) guessLyrics() string {
	best, score := "", 0
	for n, k := range c.inputs {
		s := 0
		ln := strings.ToLower(n)
		if strings.Contains(ln, "proclaim") || strings.Contains(ln, "lyric") {
			s += 2
		}
		if strings.Contains(k, "ndi") {
			s++
		}
		if s > score || (s == score && s > 0 && n < best) {
			best, score = n, s
		}
	}
	return best
}

func health(st outStatus, dropped, lag, cpu float64) (string, string) {
	switch {
	case !st.Active:
		return "off", "Off air"
	case st.Reconnecting:
		return "bad", "Reconnecting to YouTube… check the internet"
	case dropped >= 5 || st.Congestion > 0.5:
		return "bad", "Your upload is struggling: lower to 720p or a lower bitrate in OBS → Settings → Output"
	case dropped >= 1 || st.Congestion > 0.2:
		return "warn", "Some frames dropped: the upload is close to its limit"
	case lag >= 5 || cpu >= 90:
		return "warn", "The computer is struggling: close other apps"
	}
	return "ok", "Healthy"
}

func kindTag(k string) string {
	switch {
	case strings.Contains(k, "input_capture"), strings.Contains(k, "coreaudio_input"), strings.Contains(k, "pulse_input"), strings.Contains(k, "alsa"):
		return "MIC"
	case strings.Contains(k, "output_capture"), strings.Contains(k, "desktop"):
		return "DESKTOP"
	case strings.Contains(k, "screen_capture"), strings.Contains(k, "window_capture"), strings.Contains(k, "sck_audio"):
		return "APP"
	case strings.Contains(k, "ndi"):
		return "NDI"
	case strings.Contains(k, "ffmpeg") || strings.Contains(k, "vlc"):
		return "MEDIA"
	}
	return ""
}

func sortAudio(names []string, a map[string]*audioInput) {
	rank := func(n string) int {
		switch kindTag(a[n].Kind) {
		case "APP":
			return 0
		case "DESKTOP":
			return 1
		case "MIC":
			return 2
		}
		return 3
	}
	for i := 1; i < len(names); i++ {
		for j := i; j > 0; j-- {
			x, y := names[j-1], names[j]
			if rank(y) < rank(x) || (rank(y) == rank(x) && y < x) {
				names[j-1], names[j] = y, x
			} else {
				break
			}
		}
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// AddonOptions: the live lists widget settings pick from.
func (c *Conn) AddonOptions(ctx context.Context, name string) ([]addon.Option, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []addon.Option
	switch name {
	case "obs.scenes":
		for _, s := range c.scenes {
			out = append(out, addon.Option{Value: s, Label: s})
		}
	case "obs.sources":
		for n := range c.inputs {
			out = append(out, addon.Option{Value: n, Label: n})
		}
		sortOpts(out)
	case "obs.audio":
		var names []string
		for n := range c.audio {
			names = append(names, n)
		}
		sortAudio(names, c.audio)
		for _, n := range names {
			out = append(out, addon.Option{Value: n, Label: n})
		}
	default:
		return nil, addon.ErrNotFound
	}
	return out, nil
}

func sortOpts(o []addon.Option) {
	for i := 1; i < len(o); i++ {
		for j := i; j > 0 && strings.ToLower(o[j].Label) < strings.ToLower(o[j-1].Label); j-- {
			o[j], o[j-1] = o[j-1], o[j]
		}
	}
}

// ResourceResult serves "program.jpg?w=640" (what's on air, from OBS's
// own renderer) and "meters" (audio levels, ~20 a second).
func (c *Conn) ResourceResult(ctx context.Context, path string) (addon.ResourceResult, error) {
	p, q, _ := strings.Cut(path, "?")
	vals, _ := url.ParseQuery(q)
	switch p {
	case "meters":
		c.mu.Lock()
		s := c.strips(nonEmpty(strings.Split(vals.Get("ids"), ",")))
		c.mu.Unlock()
		b, _ := jsonBytes(map[string]any{"strips": s})
		return addon.ResourceResult{ContentType: "application/json", Data: b, MaxAge: addon.Age(0)}, nil
	case "program.jpg":
		w, _ := strconv.Atoi(vals.Get("w"))
		if w < 160 || w > 1920 {
			w = 640
		}
		data, err := c.screenshot(ctx, w)
		if err != nil {
			return addon.ResourceResult{}, err
		}
		return addon.ResourceResult{ContentType: "image/jpeg", Data: data, MaxAge: addon.Age(1)}, nil
	}
	return addon.ResourceResult{}, addon.ErrNotFound
}

func nonEmpty(l []string) []string {
	var out []string
	for _, s := range l {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// screenshot is the program scene as a JPEG, w pixels wide (at most one
// request to OBS per size a second).
func (c *Conn) screenshot(ctx context.Context, w int) ([]byte, error) {
	cl, err := c.client()
	if err != nil {
		return nil, err
	}
	key := strconv.Itoa(w)
	c.mu.Lock()
	scene := c.program
	if sc, ok := c.shot[key]; ok && c.now().Sub(sc.at).Seconds() < 0.9 {
		c.mu.Unlock()
		return sc.data, nil
	}
	c.mu.Unlock()
	if scene == "" {
		return nil, errors.New("no scene on air")
	}
	var r struct {
		Data string `json:"imageData"`
	}
	if err := cl.call(ctx, "GetSourceScreenshot", map[string]any{"sourceName": scene, "imageFormat": "jpg", "imageWidth": w, "imageCompressionQuality": 70}, &r); err != nil {
		return nil, err
	}
	_, b64, ok := strings.Cut(r.Data, ",")
	if !ok {
		return nil, fmt.Errorf("OBS sent an odd picture")
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("OBS sent an odd picture: %w", err)
	}
	if len(data) > addon.MaxResource {
		return nil, errors.New("picture too big: lower the width")
	}
	c.mu.Lock()
	c.shot[key] = shotCache{at: c.now(), data: data}
	c.mu.Unlock()
	return data, nil
}
