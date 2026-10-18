// Package obsws speaks OBS's WebSocket protocol (v5): Hello → Identify (with the
// password challenge) → Request/RequestResponse, skipping the events OBS pushes down the
// same socket.
//
// Two ways to use it, as in the original:
//   - Call: connect, run a list of requests, disconnect. Callers are all low-frequency
//     (>=15s apart), and never holding a socket means a mid-match OBS restart can't
//     wedge anything.
//   - Fast: one socket kept open, for camera cuts only, which an operator does in quick
//     succession. Measured against a live OBS 32.2.2: 32.8 ms median per connect-per-call
//     request vs 9.3 ms reused. Robustness comes from reconnecting on ANY error.
package obsws

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Opcodes.
const (
	opHello           = 0
	opIdentify        = 1
	opIdentified      = 2
	opRequest         = 6
	opRequestResponse = 7
)

// Target is where OBS is and how to get in.
type Target struct {
	Host     string
	Port     int
	Password string
}

// Resolved is the address actually dialled: the defaults filled in, and OBS's usual
// port moved if a sandbox asked (WSPortEnv).
func (t Target) Resolved() Target {
	if t.Host == "" {
		t.Host = "localhost"
	}
	if t.Port == 0 {
		t.Port = 4455
	}
	if t.Port == 4455 {
		if p, err := strconv.Atoi(os.Getenv(WSPortEnv)); err == nil && p > 0 {
			t.Port = p
		}
	}
	return t
}

func (t Target) url() string {
	r := t.Resolved()
	return "ws://" + net.JoinHostPort(r.Host, strconv.Itoa(r.Port))
}

// WSPortEnv moves OBS's usual WebSocket port (4455) somewhere else, for everything that
// talks to OBS. It's how a sandbox (the end-to-end tests, a trial copy) makes sure it can
// never reach the OBS installed on the machine, which listens on 4455 whenever it's open.
const WSPortEnv = "CRICKETSTREAM_OBS_WS_PORT"

// AuthResponse computes OBS's authentication string.
func AuthResponse(password, salt, challenge string) string {
	s := sha256.Sum256([]byte(password + salt))
	secret := base64.StdEncoding.EncodeToString(s[:])
	r := sha256.Sum256([]byte(secret + challenge))
	return base64.StdEncoding.EncodeToString(r[:])
}

type message struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
}

// Response is one request's outcome.
type Response struct {
	OK      bool
	Code    int
	Comment string
	Data    map[string]any // responseData ({} when OK with none)
}

// Conn is an identified connection.
type Conn struct {
	ws      *websocket.Conn
	timeout time.Duration
	nextID  int
}

// ErrUnreachable means OBS couldn't be reached or wouldn't identify us.
var ErrUnreachable = errors.New("OBS not reachable")

// Guard, when set, may refuse a connection before it's made (the obs package refuses any
// OBS but the portable copy while sandboxed).
var Guard func(Target) error

// Dial connects and identifies.
func Dial(t Target, timeout time.Duration) (*Conn, error) {
	if Guard != nil {
		if err := Guard(t.Resolved()); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
		}
	}
	d := websocket.Dialer{HandshakeTimeout: timeout}
	ws, _, err := d.Dial(t.url(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	c := &Conn{ws: ws, timeout: timeout}
	hello, err := c.waitOp(opHello)
	if err != nil {
		ws.Close()
		return nil, fmt.Errorf("%w: no Hello from OBS (%v)", ErrUnreachable, err)
	}
	var h struct {
		Authentication *struct {
			Challenge string `json:"challenge"`
			Salt      string `json:"salt"`
		} `json:"authentication"`
	}
	json.Unmarshal(hello, &h)
	identify := map[string]any{"rpcVersion": 1}
	if h.Authentication != nil && t.Password != "" {
		identify["authentication"] = AuthResponse(t.Password, h.Authentication.Salt, h.Authentication.Challenge)
	}
	if err := c.send(opIdentify, identify); err != nil {
		ws.Close()
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	if _, err := c.waitOp(opIdentified); err != nil {
		ws.Close()
		return nil, fmt.Errorf("%w: OBS did not identify us — check the WebSocket password", ErrUnreachable)
	}
	return c, nil
}

func (c *Conn) Close() error { return c.ws.Close() }

func (c *Conn) send(op int, d any) error {
	c.ws.SetWriteDeadline(time.Now().Add(c.timeout))
	return c.ws.WriteJSON(map[string]any{"op": op, "d": d})
}

// waitOp reads until a message with the opcode arrives, or the timeout passes.
func (c *Conn) waitOp(op int) (json.RawMessage, error) {
	c.ws.SetReadDeadline(time.Now().Add(c.timeout))
	for {
		var m message
		if err := c.ws.ReadJSON(&m); err != nil {
			return nil, err
		}
		if m.Op == op {
			return m.D, nil
		}
	}
}

// Request runs one request. A transport error (timeout, closed socket) is an error; a
// request OBS refused is a Response with OK false.
func (c *Conn) Request(requestType string, data any) (*Response, error) {
	c.nextID++
	rid := fmt.Sprintf("%d-%d", time.Now().UnixMilli(), c.nextID)
	payload := map[string]any{"requestType": requestType, "requestId": rid}
	if data != nil {
		payload["requestData"] = data
	}
	if err := c.send(opRequest, payload); err != nil {
		return nil, err
	}
	c.ws.SetReadDeadline(time.Now().Add(c.timeout))
	for {
		var m message
		if err := c.ws.ReadJSON(&m); err != nil {
			return nil, err
		}
		if m.Op != opRequestResponse {
			continue // OBS pushes events down the same socket
		}
		var d struct {
			RequestID     string `json:"requestId"`
			RequestStatus struct {
				Result  bool   `json:"result"`
				Code    int    `json:"code"`
				Comment string `json:"comment"`
			} `json:"requestStatus"`
			ResponseData map[string]any `json:"responseData"`
		}
		if json.Unmarshal(m.D, &d) != nil || d.RequestID != rid {
			continue
		}
		r := &Response{OK: d.RequestStatus.Result, Code: d.RequestStatus.Code,
			Comment: d.RequestStatus.Comment, Data: d.ResponseData}
		if r.OK && r.Data == nil {
			r.Data = map[string]any{}
		}
		return r, nil
	}
}

// Req is one request for Call.
type Req struct {
	Type string
	Data any
}

// Call connects, runs the requests in order and disconnects. Returns each request's
// responseData, nil for any OBS refused — check each result, not just the slice, or a
// refusal reads as success — or a nil slice if OBS couldn't be reached at all.
func Call(t Target, reqs []Req, timeout time.Duration) []map[string]any {
	if timeout == 0 {
		timeout = 6 * time.Second
	}
	c, err := Dial(t, timeout)
	if err != nil {
		return nil
	}
	defer c.Close()
	out := make([]map[string]any, 0, len(reqs))
	for _, r := range reqs {
		resp, err := c.Request(r.Type, r.Data)
		if err != nil {
			return nil
		}
		if resp.OK {
			out = append(out, resp.Data)
		} else {
			out = append(out, nil)
		}
	}
	return out
}

// fastDefaultIdle is how long an unused socket is trusted before it's remade.
const fastDefaultIdle = 120 * time.Second

// Fast keeps one socket open between calls (camera cuts).
type Fast struct {
	mu     sync.Mutex
	conn   *Conn
	target Target
	used   time.Time
	// IdleLimit: a socket idle longer than this is dropped and remade.
	IdleLimit time.Duration
}

func (f *Fast) drop() {
	if f.conn != nil {
		f.conn.Close()
		f.conn = nil
	}
}

// Call runs one request over the held socket. Returns (ok, reached): reached is false if
// OBS couldn't be reached at all, so the caller can fall back to Call or report it.
func (f *Fast) Call(t Target, requestType string, data any, timeout time.Duration) (ok, reached bool) {
	if timeout == 0 {
		timeout = 3 * time.Second
	}
	idle := f.IdleLimit
	if idle == 0 {
		idle = fastDefaultIdle
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	// Settings changed under us, or the socket has been idle long enough that it might be
	// half-dead — cheaper to remake it than to discover that mid-cut.
	if f.conn != nil && (f.target != t || time.Since(f.used) > idle) {
		f.drop()
	}
	reusing := f.conn != nil
	for attempt := 1; attempt <= 2; attempt++ {
		if f.conn == nil {
			c, err := Dial(t, timeout)
			if err != nil {
				// A failure while CONNECTING means OBS isn't reachable; retrying only
				// doubles the wait before the caller can fall back.
				return false, false
			}
			f.conn, f.target = c, t
		}
		f.conn.timeout = timeout
		resp, err := f.conn.Request(requestType, data)
		if err == nil {
			f.used = time.Now()
			return resp.OK, true
		}
		f.drop()
		// Retry ONLY when a socket we were reusing turned out to be stale — an OBS
		// restart mid-match, say — because a fresh connection genuinely fixes that.
		if !reusing {
			return false, false
		}
		reusing = false
	}
	return false, false
}

// Close drops the held socket.
func (f *Fast) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.drop()
}
