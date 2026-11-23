package main

// "Make the scenes for me": a home stream's scenes, built from the Zoom
// capture and Proclaim's lyrics (NDI). Sources that already exist are
// reused (found by name or kind), missing ones are made; scenes that
// already exist are never touched.

import (
	"context"
	"fmt"
	"strings"
)

type sceneKit struct {
	zoom, lyrics string // source names ("" = none)
	sound        string // Zoom's sound when it's its own source (Mac)
	textKind     string
	colorKind    string
	w, h         int
	notes        []string
	pending      map[string]pendingInput // sources still to make (with their first scene)
}

const (
	scStarting = "Starting soon"
	scPastor   = "Pastor"
	scLyrics   = "Pastor + lyrics"
	scSlide    = "Slide full screen"
	scBRB      = "Be right back"
	scEnding   = "Ending"
)

func (c *Conn) makeScenes(ctx context.Context, cl *obsClient) (any, error) {
	// read OBS as it is now (a second press must not make the scenes twice)
	c.refreshInputs(cl)
	c.refreshScenes(cl)
	var kinds struct {
		Kinds []string `json:"inputKinds"`
	}
	if err := cl.call(ctx, "GetInputKindList", map[string]any{"unversioned": false}, &kinds); err != nil {
		return nil, err
	}
	// OBS makes sources by their versioned kind ("color_source_v3"):
	// kind("color_source") is the one this OBS has, "" if none.
	kind := func(base string) string {
		for _, k := range kinds.Kinds {
			if k == base || strings.HasPrefix(k, base+"_v") {
				return k
			}
		}
		return ""
	}
	has := func(k string) bool { return kind(k) != "" }
	var vs struct {
		W int `json:"baseWidth"`
		H int `json:"baseHeight"`
	}
	cl.call(ctx, "GetVideoSettings", nil, &vs)
	k := &sceneKit{w: vs.W, h: vs.H, pending: map[string]pendingInput{}}
	if k.w == 0 {
		k.w, k.h = 1920, 1080
	}
	for _, t := range []string{"text_ft2_source", "text_gdiplus"} {
		if has(t) {
			k.textKind = kind(t)
			break
		}
	}
	k.colorKind = kind("color_source")

	c.mu.Lock()
	inputs := map[string]string{}
	for n, kd := range c.inputs {
		inputs[n] = kd
	}
	existing := append([]string(nil), c.scenes...)
	c.mu.Unlock()

	// Zoom: a capture source named Zoom, else a new one
	for n, kd := range inputs {
		if strings.Contains(strings.ToLower(n), "zoom") && isCapture(kd) {
			k.zoom = n
		}
	}
	var created []string
	if k.zoom == "" {
		ck, settings := "", map[string]any{}
		switch {
		case has("screen_capture"): // macOS 13+: capture the Zoom app
			ck, settings = kind("screen_capture"), map[string]any{"type": 2, "application": "us.zoom.xos", "show_cursor": false}
		case has("window_capture"): // Windows
			ck, settings = kind("window_capture"), map[string]any{"window": "Zoom Meeting:ConfMultiTabContentWndClass:Zoom.exe", "cursor": false}
		case has("xcomposite_input"):
			ck = kind("xcomposite_input")
		}
		if ck == "" {
			k.notes = append(k.notes, "This OBS can't capture windows: add the Zoom window yourself as a source named Zoom.")
		} else {
			k.zoom = uniqueName(inputs, "Zoom")
			inputs[k.zoom] = ck
			k.notes = append(k.notes, "Made the source \""+k.zoom+"\": in OBS, double-click it and check it shows the Zoom call (pick the Zoom meeting window if not).")
			k.pending[k.zoom] = pendingInput{kind: ck, settings: settings}
		}
	}
	// Zoom's sound: the capture carries it (OBS 30+ on a Mac, window
	// capture on Windows). Someone's own separate Zoom audio source is used
	// too; none is ever made (the sermon would play twice).
	for n, kd := range inputs {
		if strings.Contains(strings.ToLower(n), "zoom") && strings.Contains(kd, "audio") {
			k.sound = n
		}
	}
	// Lyrics: Proclaim's NDI output
	k.lyrics = c.guessLyricsIn(inputs)
	if k.lyrics == "" {
		if has("ndi_source") {
			k.lyrics = uniqueName(inputs, "Proclaim")
			inputs[k.lyrics] = kind("ndi_source")
			k.pending[k.lyrics] = pendingInput{kind: kind("ndi_source"), settings: map[string]any{}}
			k.notes = append(k.notes, "Made the source \""+k.lyrics+"\": in OBS, double-click it and pick Proclaim's NDI output (turn it on in Proclaim first, with a transparent background).")
		} else {
			k.notes = append(k.notes, "No NDI in this OBS: install the DistroAV plugin, then press Make the scenes again to add the lyrics.")
		}
	}

	plan := []struct {
		name  string
		parts []string // "zoom", "lyrics", "card:<text>"
	}{
		{scStarting, []string{"card:We'll begin shortly"}},
		{scPastor, []string{"zoom", "sound"}},
		{scLyrics, []string{"zoom", "sound", "lyrics"}},
		// the sermon is still heard under a full-screen verse and Be right
		// back: the call sits under an opaque background
		{scSlide, []string{"zoom", "sound", "bg", "lyrics"}},
		{scBRB, []string{"zoom", "sound", "card:We'll be right back"}},
		{scEnding, []string{"card:Thank you for joining us"}},
	}
	// OBS puts each new scene just under the current one: make them last
	// first so its list reads Starting soon … Ending, top to bottom.
	made := map[string]bool{}
	for i := len(plan) - 1; i >= 0; i-- {
		sc := plan[i]
		if contains(existing, sc.name) {
			continue
		}
		need := false
		for _, p := range sc.parts {
			if (p == "zoom" && k.zoom == "") || (p == "lyrics" && k.lyrics == "") {
				need = true
			}
		}
		if need {
			continue // a scene with its main source missing is no use
		}
		if err := cl.call(ctx, "CreateScene", map[string]any{"sceneName": sc.name}, nil); err != nil {
			return nil, fmt.Errorf("making %q: %w", sc.name, err)
		}
		for _, p := range sc.parts {
			var err error
			switch {
			case p == "sound":
				if k.sound != "" {
					err = k.place(ctx, cl, sc.name, k.sound)
				}
			case p == "zoom":
				err = k.place(ctx, cl, sc.name, k.zoom)
			case p == "lyrics":
				err = k.place(ctx, cl, sc.name, k.lyrics)
			case p == "bg":
				err = k.card(ctx, cl, sc.name, "", inputs)
			case strings.HasPrefix(p, "card:"):
				err = k.card(ctx, cl, sc.name, strings.TrimPrefix(p, "card:"), inputs)
			}
			if err != nil {
				cl.call(ctx, "RemoveScene", map[string]any{"sceneName": sc.name}, nil) // no half-made scene left behind
				return nil, fmt.Errorf("making %q: %w", sc.name, err)
			}
		}
		made[sc.name] = true
	}
	for _, sc := range plan {
		if made[sc.name] {
			created = append(created, sc.name)
		}
	}
	go func() { c.refreshInputs(cl); c.refreshScenes(cl); c.changed() }()
	msg := "Nothing to add: those scenes are already in OBS."
	if len(created) > 0 {
		msg = "Added " + strings.Join(created, ", ") + "."
	}
	return map[string]any{"created": created, "message": msg, "notes": k.notes}, nil
}

type pendingInput struct {
	kind     string
	settings map[string]any
}

// place adds a source to a scene, fitted to the canvas (making the source
// the first time).
func (k *sceneKit) place(ctx context.Context, cl *obsClient, scene, source string) error {
	var r struct {
		ID int `json:"sceneItemId"`
	}
	if p, ok := k.pending[source]; ok {
		if err := cl.call(ctx, "CreateInput", map[string]any{"sceneName": scene, "inputName": source, "inputKind": p.kind, "inputSettings": p.settings, "sceneItemEnabled": true}, &r); err != nil {
			return err
		}
		delete(k.pending, source)
		if source == k.sound {
			return nil
		}
	} else if err := cl.call(ctx, "CreateSceneItem", map[string]any{"sceneName": scene, "sourceName": source, "sceneItemEnabled": true}, &r); err != nil {
		return err
	}
	if source == k.sound {
		return nil // sound only: nothing to fit
	}
	return cl.call(ctx, "SetSceneItemTransform", map[string]any{"sceneName": scene, "sceneItemId": r.ID, "sceneItemTransform": map[string]any{
		"positionX": 0, "positionY": 0, "alignment": 5, "boundsType": "OBS_BOUNDS_SCALE_INNER", "boundsAlignment": 0,
		"boundsWidth": float64(k.w), "boundsHeight": float64(k.h)}}, nil)
}

// card is a dark background with one line of big text, centred.
func (k *sceneKit) card(ctx context.Context, cl *obsClient, scene, text string, inputs map[string]string) error {
	if k.colorKind != "" {
		bg := uniqueName(inputs, scene+" · background")
		inputs[bg] = k.colorKind
		if err := cl.call(ctx, "CreateInput", map[string]any{"sceneName": scene, "inputName": bg, "inputKind": k.colorKind,
			"inputSettings": map[string]any{"color": 0xFF1A1511, "width": k.w, "height": k.h}, "sceneItemEnabled": true}, nil); err != nil {
			return err
		}
	}
	if k.textKind == "" || text == "" {
		return nil
	}
	name := uniqueName(inputs, scene+" · text")
	inputs[name] = k.textKind
	settings := map[string]any{"text": text, "font": map[string]any{"face": "Helvetica", "size": k.h / 12, "style": "Bold"}}
	if strings.HasPrefix(k.textKind, "text_gdiplus") {
		settings["font"] = map[string]any{"face": "Segoe UI", "size": k.h / 12, "style": "Bold"}
	}
	var r struct {
		ID int `json:"sceneItemId"`
	}
	if err := cl.call(ctx, "CreateInput", map[string]any{"sceneName": scene, "inputName": name, "inputKind": k.textKind, "inputSettings": settings, "sceneItemEnabled": true}, &r); err != nil {
		return err
	}
	return cl.call(ctx, "SetSceneItemTransform", map[string]any{"sceneName": scene, "sceneItemId": r.ID, "sceneItemTransform": map[string]any{
		"positionX": float64(k.w) / 2, "positionY": float64(k.h) / 2, "alignment": 0}}, nil)
}

func isCapture(kind string) bool {
	if strings.Contains(kind, "audio") {
		return false
	}
	for _, s := range []string{"screen_capture", "window_capture", "xcomposite", "display_capture", "macos-avcapture", "sck"} {
		if strings.Contains(kind, s) {
			return true
		}
	}
	return false
}

func (c *Conn) guessLyricsIn(inputs map[string]string) string {
	c.mu.Lock()
	save := c.inputs
	c.inputs = inputs
	g := c.guessLyrics()
	c.inputs = save
	c.mu.Unlock()
	return g
}

func uniqueName(inputs map[string]string, base string) string {
	if _, ok := inputs[base]; !ok {
		return base
	}
	for i := 2; ; i++ {
		n := fmt.Sprintf("%s %d", base, i)
		if _, ok := inputs[n]; !ok {
			return n
		}
	}
}
