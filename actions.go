package main

// The add-on's actions (Manifest.Actions): dashboard buttons, macro steps
// and agent tools. Starting or stopping the stream or the recording needs
// confirmed: true (the widgets send it after a press-and-hold).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/toolingdev/jadeck/sdk/addon"
)

func actionDefs() []addon.ActionDef {
	scene := addon.Field{Key: "scene", Label: "Scene", Type: "select", OptionsFrom: "obs.scenes", Required: true}
	return []addon.ActionDef{
		{ID: "scene", Label: "Switch scene", Description: "Put a scene on air (in Studio Mode: into the preview; take it with Transition).", MacroStep: true,
			Params: []addon.Field{scene}},
		{ID: "transition", Label: "Transition", Description: "Studio Mode: take the preview scene to program.", MacroStep: true},
		{ID: "stream", Label: "Start / stop streaming", Description: "Needs confirmed: true (the widget asks for a press-and-hold).", MacroStep: true,
			Params: []addon.Field{{Key: "on", Label: "On", Type: "bool", Required: true}, {Key: "confirmed", Label: "Confirmed", Type: "bool"}}},
		{ID: "record", Label: "Start / stop recording", Description: "Needs confirmed: true.", MacroStep: true,
			Params: []addon.Field{{Key: "on", Label: "On", Type: "bool", Required: true}, {Key: "confirmed", Label: "Confirmed", Type: "bool"}}},
		{ID: "lyrics", Label: "Show / hide lyrics", Description: "Shows or hides the lyrics source (Proclaim) in the scene on air.", MacroStep: true,
			Params: []addon.Field{{Key: "on", Label: "On", Type: "bool", Required: true}, {Key: "source", Label: "Lyrics source", Type: "select", OptionsFrom: "obs.sources"}}},
		{ID: "mute", Label: "Mute / unmute audio", MacroStep: true,
			Params: []addon.Field{{Key: "input", Label: "Audio", Type: "select", OptionsFrom: "obs.audio", Required: true}, {Key: "muted", Label: "Muted", Type: "bool", Required: true}}},
		{ID: "volume", Label: "Audio level", MacroStep: true,
			Params: []addon.Field{{Key: "input", Label: "Audio", Type: "select", OptionsFrom: "obs.audio", Required: true},
				{Key: "db", Label: "Level (dB)", Type: "number", Min: addon.Num(-100), Max: addon.Num(26), Required: true}}},
		{ID: "make_scenes", Label: "Make the scenes for me",
			Description: "Adds Starting soon, Pastor, Pastor + lyrics, Slide full screen, Be right back and Ending to OBS, using the Zoom and Proclaim sources (made if missing). Scenes that already exist are left alone."},
	}
}

type params map[string]any

func (p params) str(k string) string { s, _ := p[k].(string); return strings.TrimSpace(s) }
func (p params) boolean(k string) (bool, bool) {
	switch v := p[k].(type) {
	case bool:
		return v, true
	case string:
		return v == "true", v == "true" || v == "false"
	}
	return false, false
}

var errReadOnly = errors.New("this OBS connection is read-only (Settings → Connections)")

// AddonAction runs one action.
func (c *Conn) AddonAction(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	p := params{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("parameters must be a JSON object: %w", err)
		}
	}
	if name == "make_scenes" {
		return c.makeScenesField(ctx, p), nil
	}
	if c.cfg.ReadOnly {
		return nil, errReadOnly
	}
	cl, err := c.client()
	if err != nil {
		return nil, err
	}
	switch name {
	case "scene":
		s := p.str("scene")
		c.mu.Lock()
		ok, studio := contains(c.scenes, s), c.studio
		c.mu.Unlock()
		if !ok {
			return nil, fmt.Errorf("OBS has no scene %q", s)
		}
		req := "SetCurrentProgramScene"
		if studio {
			req = "SetCurrentPreviewScene"
		}
		if err := cl.call(ctx, req, map[string]any{"sceneName": s}, nil); err != nil {
			return nil, err
		}
		// OBS took it: show it now. Its own "scene changed" event can come
		// after the widget's refresh, which left the old scene red for a
		// poll or two (so a tap felt like it needed a second one).
		c.mu.Lock()
		if studio {
			c.preview = s
		} else {
			c.program = s
		}
		c.mu.Unlock()
		c.changed()
		return map[string]any{"scene": s}, nil
	case "transition":
		return nil, cl.call(ctx, "TriggerStudioModeTransition", nil, nil)
	case "stream", "record":
		on, ok := p.boolean("on")
		if !ok {
			return nil, errors.New("on: true or false")
		}
		if conf, _ := p.boolean("confirmed"); !conf {
			return nil, errors.New("starting or stopping needs confirmed: true (press and hold the button)")
		}
		req := map[[2]string]string{{"stream", "1"}: "StartStream", {"stream", "0"}: "StopStream", {"record", "1"}: "StartRecord", {"record", "0"}: "StopRecord"}[[2]string{name, b01(on)}]
		if err := cl.call(ctx, req, nil, nil); err != nil {
			var oe *obsError
			if errors.As(err, &oe) && oe.Code == 501 { // OutputRunning / OutputNotRunning
				return map[string]any{"on": on, "note": "already " + map[bool]string{true: "on", false: "off"}[on]}, nil
			}
			if name == "stream" && on {
				return nil, fmt.Errorf("%w (check OBS → Settings → Stream: service and stream key)", err)
			}
			return nil, err
		}
		go c.readOutputs(cl)
		return map[string]any{"on": on}, nil
	case "lyrics":
		on, ok := p.boolean("on")
		if !ok {
			return nil, errors.New("on: true or false")
		}
		c.mu.Lock()
		src := p.str("source")
		if src == "" {
			src = c.guessLyrics()
		}
		scene := c.program
		if c.studio && c.preview != "" {
			scene = c.preview // Studio Mode: change what goes on air next
		}
		id := -1
		for _, it := range c.items[scene] {
			if it.Source == src {
				id = it.ID
			}
		}
		c.mu.Unlock()
		if src == "" {
			return nil, errors.New("no lyrics source in OBS: add Proclaim (NDI) or press Make the scenes for me")
		}
		if id < 0 {
			return nil, fmt.Errorf("%q isn't in the scene %q", src, scene)
		}
		if err := cl.call(ctx, "SetSceneItemEnabled", map[string]any{"sceneName": scene, "sceneItemId": id, "sceneItemEnabled": on}, nil); err != nil {
			return nil, err
		}
		return map[string]any{"on": on, "scene": scene}, nil
	case "mute":
		in := p.str("input")
		m, ok := p.boolean("muted")
		if in == "" || !ok {
			return nil, errors.New("input and muted are needed")
		}
		return nil, cl.call(ctx, "SetInputMute", map[string]any{"inputName": in, "inputMuted": m}, nil)
	case "volume":
		in := p.str("input")
		db, ok := p["db"].(float64)
		if in == "" || !ok || math.IsNaN(db) {
			return nil, errors.New("input and db are needed")
		}
		db = math.Max(-100, math.Min(26, db))
		return nil, cl.call(ctx, "SetInputVolume", map[string]any{"inputName": in, "inputVolumeDb": db}, nil)
	}
	return nil, addon.ErrUnknownAction
}

func b01(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// makeScenesField answers the connection form's "Make the scenes for me"
// button ({status: true} when the form opens) and the action itself.
func (c *Conn) makeScenesField(ctx context.Context, p params) map[string]any {
	res := func(r addon.ActionFieldResult) map[string]any {
		b, _ := json.Marshal(r)
		m := map[string]any{}
		json.Unmarshal(b, &m)
		return m
	}
	cl, err := c.client()
	if st, _ := p.boolean("status"); st {
		if err != nil {
			return res(addon.ActionFieldResult{Text: "Test the connection first; then this adds a home stream's scenes to OBS.", Button: "Make the scenes for me"})
		}
		c.mu.Lock()
		n := 0
		for _, s := range []string{scStarting, scPastor, scLyrics, scSlide, scBRB, scEnding} {
			if contains(c.scenes, s) {
				n++
			}
		}
		c.mu.Unlock()
		if n == 6 {
			return res(addon.ActionFieldResult{Text: "OBS has the home-stream scenes.", OK: true, Button: "Make the scenes again"})
		}
		return res(addon.ActionFieldResult{Text: "Adds Starting soon, Pastor, Pastor + lyrics, Slide full screen, Be right back and Ending to OBS. Your own scenes are left alone.", Button: "Make the scenes for me"})
	}
	if c.cfg.ReadOnly {
		return res(addon.ActionFieldResult{Error: errReadOnly.Error()})
	}
	if err != nil {
		return res(addon.ActionFieldResult{Error: err.Error()})
	}
	out, err := c.makeScenes(ctx, cl)
	if err != nil {
		return res(addon.ActionFieldResult{Error: err.Error()})
	}
	m := out.(map[string]any)
	text := m["message"].(string)
	if notes, _ := m["notes"].([]string); len(notes) > 0 {
		text += " " + strings.Join(notes, " ")
	}
	r := res(addon.ActionFieldResult{Text: text, OK: true, Button: "Make the scenes again"})
	r["created"] = m["created"]
	return r
}
