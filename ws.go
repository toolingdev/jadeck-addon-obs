package main

// A minimal RFC 6455 websocket client, standard library only: text
// messages in and out, ping → pong, close. Enough for obs-websocket (OBS Studio 28+)
// on this computer or the network. Errors never carry
// the URL.

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// wsMaxMessage is the largest message accepted.
const wsMaxMessage = 4 << 20

var errWSClosed = errors.New("OBS closed the connection")

// wsCloseError is a close frame from OBS: its code and reason
// (obs-websocket: 4009 wrong password, 4010 too old, …).
type wsCloseError struct {
	Code   int
	Reason string
}

func (e *wsCloseError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("OBS closed the connection (%d: %s)", e.Code, e.Reason)
	}
	return "OBS closed the connection"
}
func (e *wsCloseError) Unwrap() error { return errWSClosed }

// scrub drops the address from a URL error (the message is enough).
func scrub(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}

type wsConn struct {
	c    net.Conn
	br   *bufio.Reader
	wmu  sync.Mutex
	stop chan struct{}
	once sync.Once
}

// wsDial opens a websocket to rawURL (ws:// or wss://). The connection is
// closed when ctx ends.
func wsDial(ctx context.Context, rawURL string, hdr http.Header) (*wsConn, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Hostname() == "" {
		return nil, errors.New("bad OBS address")
	}
	host := u.Host
	if u.Port() == "" {
		port := "80"
		if u.Scheme == "wss" {
			port = "443"
		}
		host = net.JoinHostPort(u.Hostname(), port)
	}
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	nd := &net.Dialer{}
	var c net.Conn
	if u.Scheme == "wss" {
		td := &tls.Dialer{NetDialer: nd, Config: &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12}}
		c, err = td.DialContext(dctx, "tcp", host)
	} else {
		c, err = nd.DialContext(dctx, "tcp", host)
	}
	if err != nil {
		return nil, scrubNet(err)
	}
	if dl, ok := dctx.Deadline(); ok {
		c.SetDeadline(dl)
	}
	var kb [16]byte
	rand.Read(kb[:])
	key := base64.StdEncoding.EncodeToString(kb[:])
	var b strings.Builder
	fmt.Fprintf(&b, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n", u.RequestURI(), u.Host, key)
	for k, vs := range hdr {
		for _, v := range vs {
			fmt.Fprintf(&b, "%s: %s\r\n", k, v)
		}
	}
	b.WriteString("\r\n")
	if _, err := io.WriteString(c, b.String()); err != nil {
		c.Close()
		return nil, scrubNet(err)
	}
	br := bufio.NewReader(c)
	res, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		c.Close()
		return nil, scrubNet(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusSwitchingProtocols {
		c.Close()
		return nil, fmt.Errorf("OBS refused the connection (%s)", res.Status)
	}
	sum := sha1.Sum([]byte(key + wsGUID))
	if res.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(sum[:]) {
		c.Close()
		return nil, errors.New("OBS answered oddly (not a websocket)")
	}
	c.SetDeadline(time.Time{})
	w := &wsConn{c: c, br: br, stop: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			w.Close()
		case <-w.stop:
		}
	}()
	return w, nil
}

// scrubNet drops addresses from a network error.
func scrubNet(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return scrub(err)
	}
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Err != nil {
		return errors.New(oe.Err.Error())
	}
	return scrub(err)
}

// read returns the next text (or binary) message.
func (w *wsConn) read() (string, error) {
	var msg []byte
	var hdr [8]byte
	for {
		if _, err := io.ReadFull(w.br, hdr[:2]); err != nil {
			return "", w.readErr(err)
		}
		fin, op := hdr[0]&0x80 != 0, hdr[0]&0x0f
		masked, n := hdr[1]&0x80 != 0, uint64(hdr[1]&0x7f)
		switch n {
		case 126:
			if _, err := io.ReadFull(w.br, hdr[:2]); err != nil {
				return "", w.readErr(err)
			}
			n = uint64(binary.BigEndian.Uint16(hdr[:2]))
		case 127:
			if _, err := io.ReadFull(w.br, hdr[:8]); err != nil {
				return "", w.readErr(err)
			}
			n = binary.BigEndian.Uint64(hdr[:8])
		}
		if n > wsMaxMessage || uint64(len(msg))+n > wsMaxMessage {
			return "", errors.New("OBS sent a message too large")
		}
		var mask [4]byte
		if masked {
			if _, err := io.ReadFull(w.br, mask[:]); err != nil {
				return "", w.readErr(err)
			}
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(w.br, payload); err != nil {
			return "", w.readErr(err)
		}
		if masked {
			for i := range payload {
				payload[i] ^= mask[i%4]
			}
		}
		switch op {
		case 0x8: // close: obs-websocket says why in the code and reason
			w.writeFrame(0x8, payload[:min(len(payload), 2)])
			ce := &wsCloseError{}
			if len(payload) >= 2 {
				ce.Code = int(binary.BigEndian.Uint16(payload[:2]))
				ce.Reason = string(payload[2:])
			}
			return "", ce
		case 0x9: // ping
			if err := w.writeFrame(0xA, payload); err != nil {
				return "", err
			}
			continue
		case 0xA: // pong
			continue
		case 0x1, 0x2:
			msg = payload
		case 0x0:
			msg = append(msg, payload...)
		default:
			return "", fmt.Errorf("OBS sent an unknown frame (%d)", op)
		}
		if fin {
			return string(msg), nil
		}
	}
}

func (w *wsConn) readErr(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return errWSClosed
	}
	return scrubNet(err)
}

// send writes one text message.
func (w *wsConn) send(text string) error { return w.writeFrame(0x1, []byte(text)) }

// writeFrame writes one masked frame (clients always mask).
func (w *wsConn) writeFrame(op byte, payload []byte) error {
	return w.writeFrameWithin(op, payload, 10*time.Second)
}

func (w *wsConn) writeFrameWithin(op byte, payload []byte, d time.Duration) error {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	n := len(payload)
	buf := make([]byte, 0, 14+n)
	buf = append(buf, 0x80|op)
	switch {
	case n < 126:
		buf = append(buf, 0x80|byte(n))
	case n <= 0xffff:
		buf = append(buf, 0x80|126, byte(n>>8), byte(n))
	default:
		buf = append(buf, 0x80|127)
		buf = binary.BigEndian.AppendUint64(buf, uint64(n))
	}
	var mask [4]byte
	rand.Read(mask[:])
	buf = append(buf, mask[:]...)
	for i, b := range payload {
		buf = append(buf, b^mask[i%4])
	}
	w.c.SetWriteDeadline(time.Now().Add(d))
	_, err := w.c.Write(buf)
	if err != nil {
		return w.readErr(err)
	}
	return nil
}

// Close says goodbye (best effort) and closes the connection.
func (w *wsConn) Close() error {
	w.once.Do(func() {
		close(w.stop)
		w.writeFrameWithin(0x8, []byte{0x03, 0xe8}, time.Second)
		w.c.Close()
	})
	return nil
}
