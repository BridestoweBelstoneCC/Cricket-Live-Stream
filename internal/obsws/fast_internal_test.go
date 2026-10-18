package obsws

// The held-open socket used for camera cuts (tests/test_obs_fast.py). Measured against a
// live OBS: per-call median ~32ms, held socket ~9ms. These cover what's checkable without
// an OBS: the unreachable path stays bounded and reports "not reached" so the caller can
// fall back, and the socket is dropped when it shouldn't be trusted.

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// liveSocket is a real, open WebSocket to a throwaway server.
func liveSocket(t *testing.T) *websocket.Conn {
	t.Helper()
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err == nil {
			defer c.Close()
			c.ReadMessage()
		}
	}))
	t.Cleanup(srv.Close)
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// deadTarget is a loopback port with nothing listening: refused at once, not timed out.
func deadTarget(t *testing.T) Target {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close()
	p, _ := strconv.Atoi(port)
	return Target{Host: "127.0.0.1", Port: p}
}

func TestFastUnreachableReportsNotReachedSoCallerCanFallBack(t *testing.T) {
	var f Fast
	if ok, reached := f.Call(deadTarget(t), "SetCurrentProgramScene", map[string]any{"sceneName": "Main"}, 2*time.Second); ok || reached {
		t.Fatal(ok, reached)
	}
}

func TestFastUnreachableFailsQuicklyNoConnectRetry(t *testing.T) {
	// Retrying a CONNECT failure just doubles the wait before the caller can fall back —
	// an earlier version did, which made "OBS not running" take ~14s.
	var f Fast
	start := time.Now()
	f.Call(deadTarget(t), "SetCurrentProgramScene", nil, 2*time.Second)
	if d := time.Since(start); d > 6*time.Second {
		t.Fatalf("took %v — looks like the connect failure is retried", d)
	}
}

func TestFastNoSocketLeftAfterFailure(t *testing.T) {
	var f Fast
	f.Call(deadTarget(t), "SetCurrentProgramScene", nil, 2*time.Second)
	if f.conn != nil {
		t.Fatal("a failed call left a dead socket cached")
	}
}

func TestFastChangingSettingsInvalidatesHeldSocket(t *testing.T) {
	// A password or host change mid-match must not keep talking down the old socket.
	f := Fast{conn: &Conn{ws: liveSocket(t)}, target: Target{Host: "otherhost", Port: 4455, Password: "old"}, used: time.Now()}
	f.Call(deadTarget(t), "SetCurrentProgramScene", nil, 2*time.Second)
	if f.conn != nil {
		t.Fatal("kept the socket for the old settings")
	}
}

func TestFastIdleSocketNotTrustedIndefinitely(t *testing.T) {
	var f Fast
	if f.IdleLimit != 0 {
		t.Fatal("unexpected default")
	}
	// The default applied in Call: a socket held for very long without use is more
	// likely to be half-dead than useful.
	if fastDefaultIdle > 300*time.Second {
		t.Fatal(fastDefaultIdle)
	}
}

// A sandbox moves OBS's usual port, so nothing in it can reach the OBS installed on the
// machine; any other port (a test's fake OBS) is left alone.
func TestSandboxMovesOnlyTheUsualPort(t *testing.T) {
	t.Setenv(WSPortEnv, "")
	if u := (Target{}).url(); u != "ws://localhost:4455" {
		t.Fatal(u)
	}
	t.Setenv(WSPortEnv, "45999")
	for _, c := range []struct {
		t    Target
		want string
	}{
		{Target{}, "ws://localhost:45999"},
		{Target{Host: "127.0.0.1", Port: 4455}, "ws://127.0.0.1:45999"},
		{Target{Port: 61234}, "ws://localhost:61234"},
	} {
		if u := c.t.url(); u != c.want {
			t.Errorf("%+v: %s, want %s", c.t, u, c.want)
		}
	}
}
