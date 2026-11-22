package main

// obs-websocket v5 (built into OBS Studio 28 and later): Hello → Identify
// (with the password, if OBS has one) → Identified, then requests and
// events over one websocket. https://github.com/obsproject/obs-websocket
// (protocol.md). Only the protocol is used, no OBS code.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Message opcodes.
const (
	opHello      = 0
	opIdentify   = 1
	opIdentified = 2
	opEvent      = 5
	opRequest    = 6
	opResponse   = 7
)

// Event subscriptions (protocol.md EventSubscription).
const (
	subGeneral       = 1 << 0
	subConfig        = 1 << 1
	subScenes        = 1 << 2
	subInputs        = 1 << 3
	subTransitions   = 1 << 4
	subOutputs       = 1 << 6
	subSceneItems    = 1 << 7
	subUI            = 1 << 10
	subInputMeters   = 1 << 16 // high volume: every 50 ms
	subscribeDefault = subGeneral | subConfig | subScenes | subInputs | subTransitions | subOutputs | subSceneItems | subUI
)

var (
	errWrongPassword = errors.New("OBS refused the password: copy it again from OBS → Tools → WebSocket Server Settings → Show Connect Info")
	errNeedPassword  = errors.New("OBS asks for a password: copy it from OBS → Tools → WebSocket Server Settings → Show Connect Info")
)

type obsMsg struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
}

type obsResp struct {
	data json.RawMessage
	err  error
}

// obsClient is one live obs-websocket session.
type obsClient struct {
	ws      *wsConn
	wmu     sync.Mutex
	mu      sync.Mutex
	pending map[string]chan obsResp
	seq     atomic.Int64
	done    chan struct{}
	err     error
	onEvent func(typ string, data json.RawMessage)

	WSVersion string // obs-websocket version
}

// obsAuth is the Identify authentication string: base64(sha256(
// base64(sha256(password + salt)) + challenge)).
func obsAuth(password, salt, challenge string) string {
	a := sha256.Sum256([]byte(password + salt))
	b := sha256.Sum256([]byte(base64.StdEncoding.EncodeToString(a[:]) + challenge))
	return base64.StdEncoding.EncodeToString(b[:])
}

// dialOBS connects and identifies. onEvent runs on the reading goroutine.
func dialOBS(ctx context.Context, url, password string, subs int, onEvent func(string, json.RawMessage)) (*obsClient, error) {
	ws, err := wsDial(ctx, url, nil)
	if err != nil {
		return nil, err
	}
	c := &obsClient{ws: ws, pending: map[string]chan obsResp{}, done: make(chan struct{}), onEvent: onEvent}
	fail := func(err error) (*obsClient, error) { ws.Close(); return nil, err }

	ws.c.SetDeadline(time.Now().Add(10 * time.Second))
	raw, err := ws.read()
	if err != nil {
		return fail(err)
	}
	var m obsMsg
	if json.Unmarshal([]byte(raw), &m) != nil || m.Op != opHello {
		return fail(errors.New("that isn't OBS (no obs-websocket hello): check the port, OBS uses 4455"))
	}
	var hello struct {
		ObsWebSocketVersion string `json:"obsWebSocketVersion"`
		RPCVersion          int    `json:"rpcVersion"`
		Authentication      *struct {
			Challenge string `json:"challenge"`
			Salt      string `json:"salt"`
		} `json:"authentication"`
	}
	json.Unmarshal(m.D, &hello)
	c.WSVersion = hello.ObsWebSocketVersion
	id := map[string]any{"rpcVersion": 1, "eventSubscriptions": subs}
	if hello.Authentication != nil {
		if password == "" {
			return fail(errNeedPassword)
		}
		id["authentication"] = obsAuth(password, hello.Authentication.Salt, hello.Authentication.Challenge)
	}
	if err := c.send(opIdentify, id); err != nil {
		return fail(err)
	}
	raw, err = ws.read()
	if err != nil {
		var ce *wsCloseError
		if errors.As(err, &ce) && ce.Code == 4009 {
			return fail(errWrongPassword)
		}
		return fail(err)
	}
	if json.Unmarshal([]byte(raw), &m) != nil || m.Op != opIdentified {
		return fail(errors.New("OBS didn't accept the connection"))
	}
	ws.c.SetDeadline(time.Time{})
	go c.readLoop()
	return c, nil
}

func (c *obsClient) send(op int, d any) error {
	b, err := json.Marshal(map[string]any{"op": op, "d": d})
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.ws.writeFrameWithin(0x1, b, 10*time.Second)
}

func (c *obsClient) readLoop() {
	var err error
	defer func() {
		c.mu.Lock()
		c.err = err
		for id, ch := range c.pending {
			ch <- obsResp{err: errors.New("OBS went away")}
			delete(c.pending, id)
		}
		c.mu.Unlock()
		close(c.done)
	}()
	for {
		var raw string
		raw, err = c.ws.read()
		if err != nil {
			return
		}
		var m obsMsg
		if json.Unmarshal([]byte(raw), &m) != nil {
			continue
		}
		switch m.Op {
		case opResponse:
			var r struct {
				RequestID     string `json:"requestId"`
				RequestStatus struct {
					Result  bool   `json:"result"`
					Code    int    `json:"code"`
					Comment string `json:"comment"`
				} `json:"requestStatus"`
				ResponseData json.RawMessage `json:"responseData"`
			}
			if json.Unmarshal(m.D, &r) != nil {
				continue
			}
			c.mu.Lock()
			ch := c.pending[r.RequestID]
			delete(c.pending, r.RequestID)
			c.mu.Unlock()
			if ch == nil {
				continue
			}
			if !r.RequestStatus.Result {
				msg := r.RequestStatus.Comment
				if msg == "" {
					msg = "code " + strconv.Itoa(r.RequestStatus.Code)
				}
				ch <- obsResp{err: &obsError{Code: r.RequestStatus.Code, Msg: msg}}
				continue
			}
			ch <- obsResp{data: r.ResponseData}
		case opEvent:
			var e struct {
				EventType string          `json:"eventType"`
				EventData json.RawMessage `json:"eventData"`
			}
			if json.Unmarshal(m.D, &e) == nil && c.onEvent != nil {
				c.onEvent(e.EventType, e.EventData)
			}
		}
	}
}

// obsError is a request OBS turned down, with its reason.
type obsError struct {
	Code int
	Msg  string
}

func (e *obsError) Error() string { return "OBS: " + e.Msg }

// call runs one request and decodes responseData into out (may be nil).
func (c *obsClient) call(ctx context.Context, typ string, data any, out any) error {
	id := strconv.FormatInt(c.seq.Add(1), 10)
	ch := make(chan obsResp, 1)
	c.mu.Lock()
	if c.err != nil || c.closed() {
		c.mu.Unlock()
		return errors.New("not connected to OBS")
	}
	c.pending[id] = ch
	c.mu.Unlock()
	d := map[string]any{"requestType": typ, "requestId": id}
	if data != nil {
		d["requestData"] = data
	}
	if err := c.send(opRequest, d); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	select {
	case r := <-ch:
		if r.err != nil {
			return r.err
		}
		if out != nil && len(r.data) > 0 {
			if err := json.Unmarshal(r.data, out); err != nil {
				return fmt.Errorf("OBS %s: %w", typ, err)
			}
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("OBS didn't answer %s in time", typ)
	}
}

func (c *obsClient) closed() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

func (c *obsClient) Close() error { return c.ws.Close() }
