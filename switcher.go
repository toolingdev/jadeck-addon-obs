package main

// OBS as a jadeck switcher: its scenes are the inputs (numbered in OBS's
// order, top first), so jadeck's own Cut / Fade, Input bank, Program and
// the stage screen's "We are live" work with OBS too. In Studio Mode the
// preview is OBS's; otherwise it is kept here until Cut or Fade.

import (
	"context"
	"errors"
	"fmt"

	"github.com/toolingdev/jadeck/sdk/addon"
)

func (c *Conn) State(ctx context.Context) (addon.SwitcherState, error) {
	if _, err := c.client(); err != nil {
		return addon.SwitcherState{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	st := addon.SwitcherState{Version: c.version, Edition: "OBS Studio", Streaming: c.stream.Active, Recording: c.record.Active}
	pv := c.preview
	if !c.studio {
		pv = c.next
	}
	for i, s := range c.scenes {
		n := i + 1
		st.Inputs = append(st.Inputs, addon.Input{Number: n, Key: s, Title: s, Type: "Scene", State: "Running"})
		if s == c.program {
			st.Active = n
		}
		if s == pv {
			st.Preview = n
		}
	}
	return st, nil
}

func (c *Conn) sceneN(n int) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n < 1 || n > len(c.scenes) {
		return "", fmt.Errorf("OBS has no scene %d", n)
	}
	return c.scenes[n-1], nil
}

func (c *Conn) Preview(ctx context.Context, n int) error {
	if c.cfg.ReadOnly {
		return errReadOnly
	}
	cl, err := c.client()
	if err != nil {
		return err
	}
	s, err := c.sceneN(n)
	if err != nil {
		return err
	}
	c.mu.Lock()
	studio := c.studio
	if !studio {
		c.next = s
	}
	c.mu.Unlock()
	if studio {
		return cl.call(ctx, "SetCurrentPreviewScene", map[string]any{"sceneName": s}, nil)
	}
	c.changed()
	return nil
}

func (c *Conn) Cut(ctx context.Context) error { return c.take(ctx, "Cut", 0) }

func (c *Conn) Fade(ctx context.Context, ms int) error { return c.take(ctx, "Fade", ms) }

// take puts the preview on air with OBS's Cut or Fade transition.
func (c *Conn) take(ctx context.Context, transition string, ms int) error {
	if c.cfg.ReadOnly {
		return errReadOnly
	}
	cl, err := c.client()
	if err != nil {
		return err
	}
	c.mu.Lock()
	studio, next := c.studio, c.next
	c.mu.Unlock()
	if !studio && next == "" {
		return errors.New("pick the next scene first")
	}
	// OBS's own Cut / Fade transitions (a failure leaves the current one)
	if cl.call(ctx, "SetCurrentSceneTransition", map[string]any{"transitionName": transition}, nil) == nil && ms > 0 {
		cl.call(ctx, "SetCurrentSceneTransitionDuration", map[string]any{"transitionDuration": max(50, min(ms, 20000))}, nil)
	}
	if studio {
		return cl.call(ctx, "TriggerStudioModeTransition", nil, nil)
	}
	if err := cl.call(ctx, "SetCurrentProgramScene", map[string]any{"sceneName": next}, nil); err != nil {
		return err
	}
	c.mu.Lock()
	if c.next == next {
		c.next = ""
	}
	c.mu.Unlock()
	return nil
}
