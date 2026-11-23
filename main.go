// OBS Studio: a jadeck add-on. OBS does the video (capture, layout,
// streaming); jadeck is the remote: go live, scenes, lyrics on / off,
// audio and a picture of what's on air, from any phone, tablet or PC.
// It talks to OBS over obs-websocket (built into OBS 28+). No OBS code
// is included.
package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/toolingdev/jadeck/sdk/addon"
)

// Version is set by the release build.
var Version = "0.1.1"

func main() { addon.Serve(spec()) }

const changelog = `0.1.1: a tapped scene shows ON AIR right away (no second tap). 0.1.0: first release.
- Connects to OBS Studio 28 or later over obs-websocket (password supported); reconnects by itself when OBS restarts.
- Widgets: Program (a live picture of what's on air), Stream (go live / stop and record, pressed and held, with bitrate, dropped frames and CPU, and what to do when the upload struggles), Scenes, Lyrics (show / hide Proclaim's lyrics, lower third or full screen) and Audio (faders, meters, mute).
- OBS also works as a switcher in jadeck: Cut / Fade, Input bank, Program and the stage screen's "We are live".
- "Make the scenes for me": Starting soon, Pastor, Pastor + lyrics, Slide full screen, Be right back and Ending, from the Zoom capture and Proclaim (NDI).`

func open(raw json.RawMessage) (*Conn, error) {
	cfg := config{Host: "127.0.0.1", Port: 4455}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	cfg.Host = strings.Trim(strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(cfg.Host, "ws://"), "http://"), "/")), "[]")
	if h, _, ok := strings.Cut(cfg.Host, ":"); ok && !strings.Contains(h, ":") && strings.Count(cfg.Host, ":") == 1 {
		cfg.Host = h // "192.168.1.5:4455" typed into the address
	}
	if cfg.Host == "" {
		return nil, fmt.Errorf("enter OBS's computer: %w", addon.ErrNotConfigured)
	}
	if cfg.Port == 0 {
		cfg.Port = 4455
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return nil, fmt.Errorf("the port must be 1-65535")
	}
	return newConn(cfg), nil
}

// ----------------------------------------------------------- samples

var sampleAt = time.Date(2026, 11, 28, 18, 42, 13, 0, time.UTC)

// sampleConn is an OBS in the middle of a home stream (never connects).
func sampleConn() *Conn {
	c := newConn(config{Host: "127.0.0.1", Port: 4455})
	c.sample = true
	c.now = func() time.Time { return sampleAt }
	c.version = "31.1.2"
	c.scenes = []string{scStarting, scPastor, scLyrics, scSlide, scBRB, scEnding}
	c.program = scLyrics
	c.items = map[string][]sceneItem{scLyrics: {{ID: 1, Source: "Zoom", Enabled: true}, {ID: 2, Source: "Proclaim", Enabled: true}}}
	c.inputs = map[string]string{"Zoom": "screen_capture", "Proclaim": "ndi_source", "Music": "ffmpeg_source", "Mic/Aux": "coreaudio_input_capture"}
	c.audio = map[string]*audioInput{
		"Zoom":    {Name: "Zoom", Kind: "screen_capture", Db: 0, Levels: []float64{0.42, 0.4}},
		"Music":   {Name: "Music", Kind: "ffmpeg_source", Db: -12, Levels: []float64{0.08, 0.07}},
		"Mic/Aux": {Name: "Mic/Aux", Kind: "coreaudio_input_capture", Muted: true, Db: -3},
	}
	c.stream = outStatus{Active: true, Duration: (42*60 + 13) * 1000, Total: 76000, Skipped: 12}
	c.record = outStatus{Active: true, Duration: (42*60 + 10) * 1000}
	c.stats = obsStats{CPU: 18.4, FPS: 30, RenderTotal: 76000}
	c.kbps = 6020
	return c
}

func sampleState(settings map[string]any) map[string]any {
	st, err := sampleConn().AddonStateFor(nil, settings)
	if err != nil {
		panic(err)
	}
	return st
}

var coreSample = map[string]any{"/api/switcher/status": map[string]any{
	"label": "OBS — home Mac", "connected": true, "version": "31.1.2", "edition": "OBS Studio",
	"inputs": []map[string]any{
		{"number": 1, "key": scStarting, "title": scStarting, "type": "Scene", "state": "Running"},
		{"number": 2, "key": scPastor, "title": scPastor, "type": "Scene", "state": "Running"},
		{"number": 3, "key": scLyrics, "title": scLyrics, "type": "Scene", "state": "Running"},
		{"number": 4, "key": scSlide, "title": scSlide, "type": "Scene", "state": "Running"},
	},
	"preview": 2, "active": 3, "streaming": true, "recording": true,
}}

// ----------------------------------------------------------- widgets

var (
	fHold = addon.Field{Key: "holdSeconds", Label: "Hold to confirm (seconds)", Type: "number", Default: 5, Min: addon.Num(3), Max: addon.Num(10),
		Help: "Going live, stopping, and recording need the button held this long."}
	sz = func(w, h int) *addon.Size { return &addon.Size{W: w, H: h} }
)

func widgets() []addon.WidgetDef {
	return []addon.WidgetDef{
		{Kind: "core", Type: "switcher.cutfade", Label: "Cut / Fade", Sample: coreSample},
		{Kind: "core", Type: "switcher.inputs", Label: "Input bank", Sample: coreSample},
		{Kind: "core", Type: "stage.live", Label: "Live status", Sample: coreSample, Settings: map[string]any{"mode": "stream"}},

		{Kind: addon.KindFrame, Type: "program", Label: "Program", Description: "A live picture of what's on air in OBS (what YouTube sees), with LIVE and the scene's name.",
			Entry: "ui/program.html", Size: sz(8, 6), MinPx: &addon.Size{W: 200, H: 130},
			Fields: []addon.Field{{Key: "fps", Label: "Pictures a second", Type: "select", Default: "1", Options: []addon.Option{
				{Value: "0.5", Label: "One every 2 seconds"}, {Value: "1", Label: "1"}, {Value: "2", Label: "2"}},
				Help: "More is smoother but works OBS harder. 1 is plenty to check the picture."}},
			Sample: sampleState(nil)},
		{Kind: addon.KindFrame, Type: "stream", Label: "Stream", Description: "Go live and stop (press and hold), recording, how long you've been live, bitrate, dropped frames and CPU, and what to do when the upload struggles.",
			Entry: "ui/stream.html", Size: sz(5, 4), MinPx: &addon.Size{W: 200, H: 120},
			Fields: []addon.Field{fHold, {Key: "record", Label: "Record button", Type: "bool", Default: true}, {Key: "stats", Label: "Numbers (bitrate, dropped, CPU)", Type: "bool", Default: true}},
			Sample: sampleState(nil)},
		{Kind: addon.KindButtons, Type: "scenes", Label: "Scenes", Description: "OBS's scenes as buttons: red is on air (green is next, in Studio Mode). Tap to switch.",
			Bind: map[string]any{"items": "sceneButtons"}, Size: sz(6, 3), MinPx: &addon.Size{W: 160, H: 64},
			Fields: []addon.Field{{Key: "scenes", Label: "Scenes", Type: addon.FieldOrder, OptionsFrom: "obs.scenes", Help: "Pick and order them. None picked: all of OBS's scenes, in OBS's order."},
				{Key: "awayScene", Label: "Show time away on", Type: "select", OptionsFrom: "obs.scenes", Help: "While this scene is on air its button counts the time (\"away 1:42\"). Blank: \"" + scBRB + "\"."}},
			Sample: sampleState(nil)},
		{Kind: addon.KindButtons, Type: "lyrics", Label: "Lyrics", Description: "Show or hide Proclaim's lyrics on the stream, and switch between lyrics along the bottom and the slide full screen.",
			Bind: map[string]any{"items": "lyricsButtons", "columns": 3}, Size: sz(5, 2), MinPx: &addon.Size{W: 180, H: 64},
			Fields: []addon.Field{
				{Key: "source", Label: "Lyrics source", Type: "select", OptionsFrom: "obs.sources", Help: "Blank: the NDI source (or the one named Proclaim)."},
				{Key: "lowerScene", Label: "Lower-third scene", Type: "select", OptionsFrom: "obs.scenes", Help: "Blank: \"" + scLyrics + "\"."},
				{Key: "fullScene", Label: "Full-screen slide scene", Type: "select", OptionsFrom: "obs.scenes", Help: "Blank: \"" + scSlide + "\"."},
			},
			Sample: sampleState(nil)},
		{Kind: addon.KindFrame, Type: "audio", Label: "Audio", Description: "OBS's audio: a fader, live meter and mute for each source (Zoom, music, your mic).",
			Entry: "ui/audio.html", Size: sz(5, 7), MinPx: &addon.Size{W: 120, H: 200},
			Fields: []addon.Field{
				{Key: "strips", Label: "Sources", Type: addon.FieldOrder, OptionsFrom: "obs.audio", Help: "Pick and order them. None picked: every source with sound."},
				{Key: "meters", Label: "Meters", Type: "bool", Default: true},
				{Key: "faders", Label: "Faders", Type: "bool", Default: true, Help: "Off: meters and mutes only (nothing to nudge by accident)."},
			},
			Sample: sampleState(nil)},
	}
}

func spec() addon.Spec {
	return addon.Spec{
		Manifest: addon.Manifest{
			ID: "obs", Name: "OBS Studio", Version: Version, Publisher: "jadeck", APIVersion: addon.APIVersion, Repo: "toolingdev/jadeck-addon-obs",
			Description: "Go live, switch scenes, show lyrics and mix audio in OBS Studio, from any screen.",
			Reaches:     "The OBS you add as a connection (obs-websocket, port 4455)",
			ConnectionTypes: []addon.ConnectionType{{
				ID: "obs", Label: "OBS Studio", Capabilities: []string{addon.CapSwitcher},
				Description: "OBS Studio 28 or later, on this computer or another one on the network.",
				Needs:       "OBS 28+ · WebSocket password",
				Guide: &addon.Guide{Minutes: 10, Steps: []addon.GuideStep{
					{Text: "Install OBS Studio (free).", Link: &addon.GuideLink{Label: "obsproject.com", URL: "https://obsproject.com/download"}},
					{Text: "In OBS: Tools → WebSocket Server Settings → tick Enable WebSocket server. Click Show Connect Info and copy the password into the box below."},
					{Text: "For lyrics on top: install the DistroAV plugin for OBS, then in Proclaim turn on NDI output with a transparent background.",
						Link: &addon.GuideLink{Label: "DistroAV", URL: "https://github.com/DistroAV/DistroAV/releases"}},
					{Text: "For a Zoom call: join it, pin the speaker, and hide your own video and people without video. On a Mac, OBS captures Zoom's sound too (macOS 13 or later)."},
					{Text: "In OBS: Settings → Stream → YouTube, and sign in or paste the stream key. Then Test here, and press Make the scenes for me."},
				}, Note: "OBS is separate free software. jadeck only remote-controls it, the way it does vMix."},
				Fields: []addon.Field{
					{Key: "host", Label: "OBS computer", Type: "text", Default: "127.0.0.1", Placeholder: "127.0.0.1", Required: true,
						Help: "127.0.0.1 when OBS runs on this computer; otherwise that computer's address (over Tailscale or the network)."},
					{Key: "port", Label: "Port", Type: "number", Default: 4455, Min: addon.Num(1), Max: addon.Num(65535), Advanced: true},
					{Key: "password", Label: "WebSocket password", Type: "password", Help: "From OBS → Tools → WebSocket Server Settings → Show Connect Info."},
					{Key: "readOnly", Label: "Read-only", Type: "bool", Default: false, Advanced: true, Help: "Show what OBS is doing, but never change anything."},
					{Key: "makeScenes", Label: "Scenes", Type: addon.FieldAction, Action: "make_scenes"},
				},
				Sample:     map[string]any{"host": "127.0.0.1", "port": 4455, "password": ""},
				SampleTest: "Connected — OBS 31.1.2, 6 scenes, streaming",
			}},
			Actions:   actionDefs(),
			Widgets:   widgets(),
			Changelog: changelog,
		},
		Open: map[string]addon.OpenFunc{"obs": func(raw json.RawMessage) (any, error) {
			c, err := open(raw)
			if err != nil {
				return nil, err
			}
			c.start()
			return c, nil
		}},
	}
}
