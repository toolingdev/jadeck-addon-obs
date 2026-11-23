package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toolingdev/jadeck/sdk/addon"
)

func TestManifest(t *testing.T) {
	m := spec().Manifest
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, w := range m.Widgets {
		if w.Kind != addon.KindFrame {
			continue
		}
		for _, f := range []string{w.Entry, strings.Replace(w.Entry, ".html", ".js", 1), strings.Replace(w.Entry, ".html", ".css", 1), "ui/bridge.js"} {
			if _, err := os.Stat(filepath.FromSlash(f)); err != nil {
				t.Errorf("widget %s: %v", w.Type, err)
			}
		}
		page, _ := os.ReadFile(filepath.FromSlash(w.Entry))
		for _, want := range []string{`href="/api/kit/v1/kit.css"`, `src="/api/kit/v1/kit.js"`, `src="bridge.js"`} {
			if !strings.Contains(string(page), want) {
				t.Errorf("widget %s doesn't load %s", w.Type, want)
			}
		}
	}
}

func dialFake(t *testing.T, f *fakeOBS, password string) *Conn {
	t.Helper()
	h, p := f.hostPort()
	raw, _ := json.Marshal(map[string]any{"host": h, "port": p, "password": password})
	c, err := open(raw)
	if err != nil {
		t.Fatal(err)
	}
	c.start()
	t.Cleanup(func() { c.Close() })
	return c
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func state(t *testing.T, c *Conn, settings map[string]any) map[string]any {
	t.Helper()
	st, err := c.AddonStateFor(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func connected(t *testing.T, c *Conn) {
	waitFor(t, "connected with scenes", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.cl != nil && len(c.scenes) > 0 && len(c.audio) > 0
	})
}

func TestStateFromOBS(t *testing.T) {
	f := newFakeOBS(t, "s3cret")
	c := dialFake(t, f, "s3cret")
	connected(t, c)
	st := state(t, c, nil)
	if st["program"] != "Pastor" || st["version"] != "31.1.2" || st["streamOn"] != false {
		t.Fatalf("state: %v", st)
	}
	sb := st["sceneButtons"].([]map[string]any)
	if len(sb) != 2 || sb[0]["label"] != "Pastor" || sb[0]["tone"] != "pgm" || sb[1]["label"] != "Pastor + lyrics" {
		t.Fatalf("scene buttons (top of OBS's list first): %v", sb)
	}
	// Proclaim isn't in the scene on air: the lyrics button says so
	lb := st["lyricsButtons"].([]any)[0].(map[string]any)
	if st["lyricsSource"] != "Proclaim" || lb["disabled"] != true || lb["sub"] != "NOT IN THIS SCENE" {
		t.Fatalf("lyrics: %v %v", st["lyricsSource"], lb)
	}
	strips := st["mixerStrips"].([]map[string]any)
	if len(strips) != 2 || strips[0]["name"] != "Zoom" || strips[1]["name"] != "Mic/Aux" || strips[1]["muted"] != true {
		t.Fatalf("strips (app sound first, then mics; Proclaim has none): %v", strips)
	}
}

func TestWrongPassword(t *testing.T) {
	f := newFakeOBS(t, "right")
	c := dialFake(t, f, "wrong")
	waitFor(t, "an error", func() bool { _, err := c.client(); return err != nil && !strings.Contains(err.Error(), "connecting") })
	if _, err := c.AddonState(context.Background()); err == nil || !strings.Contains(err.Error(), "refused the password") {
		t.Fatalf("got %v", err)
	}
	c2 := dialFake(t, f, "")
	waitFor(t, "an error", func() bool {
		_, err := c2.client()
		return err != nil && strings.Contains(err.Error(), "asks for a password")
	})
}

func TestNotRunning(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"host": "127.0.0.1", "port": 1})
	c, _ := open(raw)
	c.start()
	defer c.Close()
	waitFor(t, "an error", func() bool { _, err := c.client(); return err != nil && strings.Contains(err.Error(), "open OBS") })
}

func act(t *testing.T, c *Conn, name string, p map[string]any) (any, error) {
	t.Helper()
	raw, _ := json.Marshal(p)
	return c.AddonAction(context.Background(), name, raw)
}

func TestActions(t *testing.T) {
	f := newFakeOBS(t, "")
	c := dialFake(t, f, "")
	connected(t, c)
	if _, err := act(t, c, "scene", map[string]any{"scene": "Pastor + lyrics"}); err != nil {
		t.Fatal(err)
	}
	// on air the moment OBS takes it, not after OBS's own event
	c.mu.Lock()
	got := c.program
	c.mu.Unlock()
	if got != "Pastor + lyrics" {
		t.Fatalf("program right after the switch = %q", got)
	}
	waitFor(t, "program from the event", func() bool { return state(t, c, nil)["program"] == "Pastor + lyrics" })
	if _, err := act(t, c, "scene", map[string]any{"scene": "Nope"}); err == nil {
		t.Fatal("unknown scene accepted")
	}

	// lyrics: on in this scene; hide it
	lb := state(t, c, nil)["lyricsButtons"].([]any)[0].(map[string]any)
	if lb["label"] != "On screen" || lb["tone"] != "pvw" {
		t.Fatalf("lyrics button: %v", lb)
	}
	if _, err := act(t, c, "lyrics", map[string]any{"on": false}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "lyrics hidden", func() bool { return state(t, c, nil)["lyricsOn"] == false })

	// stream: needs the hold's confirmation
	if _, err := act(t, c, "stream", map[string]any{"on": true}); err == nil || !strings.Contains(err.Error(), "confirmed") {
		t.Fatalf("unconfirmed start: %v", err)
	}
	if _, err := act(t, c, "stream", map[string]any{"on": true, "confirmed": true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "live", func() bool { return state(t, c, nil)["streamOn"] == true })
	st := state(t, c, nil)
	if st["streamSince"] == nil || st["dropped"].(float64) != 0.2 {
		t.Fatalf("stream state: %v %v", st["streamSince"], st["dropped"])
	}
	// already live: not an error
	if r, err := act(t, c, "stream", map[string]any{"on": true, "confirmed": true}); err != nil || !strings.Contains(r.(map[string]any)["note"].(string), "already") {
		t.Fatalf("second start: %v %v", r, err)
	}

	// mute
	if _, err := act(t, c, "mute", map[string]any{"input": "Zoom", "muted": true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "muted", func() bool { return state(t, c, nil)["mixerStrips"].([]map[string]any)[0]["muted"] == true })
	if _, err := act(t, c, "volume", map[string]any{"input": "Zoom", "db": -6.0}); err != nil {
		t.Fatal(err)
	}
}

func TestSwitcher(t *testing.T) {
	f := newFakeOBS(t, "")
	c := dialFake(t, f, "")
	connected(t, c)
	ctx := context.Background()
	st, err := c.State(ctx)
	if err != nil || len(st.Inputs) != 2 || st.Active != 1 || st.Preview != 0 {
		t.Fatalf("%+v %v", st, err)
	}
	if err := c.Cut(ctx); err == nil {
		t.Fatal("cut with nothing picked")
	}
	c.Preview(ctx, 2)
	if st, _ := c.State(ctx); st.Preview != 2 {
		t.Fatalf("preview %d", st.Preview)
	}
	if err := c.Fade(ctx, 500); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "faded", func() bool { st, _ := c.State(ctx); return st.Active == 2 && st.Preview == 0 })
	if !f.did("SetCurrentSceneTransition") {
		t.Fatal("transition not set")
	}
}

func TestMakeScenes(t *testing.T) {
	f := newFakeOBS(t, "")
	c := dialFake(t, f, "")
	connected(t, c)
	st, _ := act(t, c, "make_scenes", map[string]any{"status": true})
	if !strings.Contains(st.(map[string]any)["text"].(string), "Adds") {
		t.Fatalf("status: %v", st)
	}
	r, err := act(t, c, "make_scenes", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	m := r.(map[string]any)
	created := m["created"].([]string)
	if strings.Join(created, ",") != "Starting soon,Slide full screen,Be right back,Ending" || m["ok"] != true {
		t.Fatalf("created %v: %v", created, m)
	}
	f.mu.Lock()
	slide := f.items["Slide full screen"]
	brb := f.items["Be right back"]
	f.mu.Unlock()
	// the full-screen verse and Be right back keep the sermon's sound:
	// the call under an opaque background, the slide / card on top
	src := func(l []*fakeItem) (out []string) {
		for _, x := range l {
			out = append(out, x.Source)
		}
		return
	}
	if got := strings.Join(src(slide), ","); got != "Zoom,Slide full screen · background,Proclaim" {
		t.Fatalf("slide: %s", got)
	}
	if got := strings.Join(src(brb), ","); got != "Zoom,Be right back · background,Be right back · text" {
		t.Fatalf("brb: %s", got)
	}
	// again: nothing new
	r, _ = act(t, c, "make_scenes", map[string]any{})
	if len(r.(map[string]any)["created"].([]string)) != 0 {
		t.Fatalf("made again: %v", r)
	}
}

func TestMakeScenesFromScratch(t *testing.T) {
	f := newFakeOBS(t, "")
	f.scenes, f.program, f.items = []string{"Scene"}, "Scene", map[string][]*fakeItem{"Scene": nil}
	f.inputs = map[string]string{"Mic/Aux": "coreaudio_input_capture"}
	c := dialFake(t, f, "")
	connected(t, c)
	r, err := act(t, c, "make_scenes", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.(map[string]any)["created"].([]string)) != 6 {
		t.Fatalf("%v", r)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inputs["Zoom"] != "screen_capture" || f.inputs["Proclaim"] != "ndi_source" || f.inputs["Zoom sound"] != "" || f.inputs["Starting soon · text"] != "text_ft2_source_v2" {
		t.Fatalf("inputs %v", f.inputs)
	}
	if !strings.Contains(r.(map[string]any)["text"].(string), "double-click") {
		t.Fatalf("no notes on what to pick: %v", r)
	}
}

// A source OBS can't make leaves no half-made scene behind.
func TestMakeScenesRollsBack(t *testing.T) {
	f := newFakeOBS(t, "")
	f.kinds = []string{"screen_capture", "color_source"} // unversioned: OBS refuses it
	c := dialFake(t, f, "")
	connected(t, c)
	r, _ := act(t, c, "make_scenes", map[string]any{})
	if !strings.Contains(r.(map[string]any)["error"].(string), "Ending") {
		t.Fatalf("%v", r)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if contains(f.scenes, "Ending") {
		t.Fatalf("half-made scene left: %v", f.scenes)
	}
}

func TestResources(t *testing.T) {
	f := newFakeOBS(t, "")
	c := dialFake(t, f, "")
	connected(t, c)
	r, err := c.ResourceResult(context.Background(), "program.jpg?w=640")
	if err != nil || r.ContentType != "image/jpeg" || !strings.HasPrefix(string(r.Data), "\xff\xd8") {
		t.Fatalf("%v %v", r, err)
	}
	r, err = c.ResourceResult(context.Background(), "meters?ids=Zoom")
	if err != nil || !strings.Contains(string(r.Data), `"Zoom"`) || strings.Contains(string(r.Data), "Mic") {
		t.Fatalf("%s %v", r.Data, err)
	}
	if _, err := c.ResourceResult(context.Background(), "nope"); err != addon.ErrNotFound {
		t.Fatal(err)
	}
}

func TestOptions(t *testing.T) {
	f := newFakeOBS(t, "")
	c := dialFake(t, f, "")
	connected(t, c)
	o, _ := c.AddonOptions(context.Background(), "obs.audio")
	if len(o) != 2 || o[0].Value != "Zoom" {
		t.Fatalf("%v", o)
	}
}

func TestReadOnly(t *testing.T) {
	f := newFakeOBS(t, "")
	h, p := f.hostPort()
	raw, _ := json.Marshal(map[string]any{"host": h, "port": p, "readOnly": true})
	c, _ := open(raw)
	c.start()
	defer c.Close()
	connected(t, c)
	if _, err := act(t, c, "scene", map[string]any{"scene": "Pastor + lyrics"}); err != errReadOnly {
		t.Fatal(err)
	}
	if st := state(t, c, nil); st["sceneButtons"].([]map[string]any)[0]["disabled"] != true {
		t.Fatal("buttons not disabled")
	}
}

func TestSample(t *testing.T) {
	st := sampleState(nil)
	if st["streamOn"] != true || st["lyricsOn"] != true || st["kbps"] != 6020 {
		t.Fatalf("%v", st)
	}
}

func TestOpen(t *testing.T) {
	for in, want := range map[string]string{"ws://192.168.1.9:4455/": "192.168.1.9", " obs-mac ": "obs-mac", "[::1]": "::1"} {
		raw, _ := json.Marshal(map[string]any{"host": in})
		c, err := open(raw)
		if err != nil || c.cfg.Host != want || c.cfg.Port != 4455 {
			t.Errorf("%q: %v %v", in, c, err)
		}
	}
	if _, err := open([]byte(`{"host":""}`)); err == nil {
		t.Error("empty host accepted")
	}
}
