package app

import (
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func serveForClose(t *testing.T, h http.HandlerFunc) (*http.Server, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return srv, "http://" + ln.Addr().String()
}

// The Close button replies and then quits. The reply is still in the handler when the app
// goes, and it must get out: this is the EOF that turned CI red on a close that had worked.
func TestClosingTheAppLetsTheReplyOut(t *testing.T) {
	inHandler := make(chan struct{})
	srv, url := serveForClose(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
		close(inHandler)
		time.Sleep(100 * time.Millisecond) // the quit gets ahead of the handler finishing
	})
	type answer struct {
		body string
		err  error
	}
	got := make(chan answer, 1)
	go func() {
		r, err := http.Post(url+"/api/close", "application/json", nil)
		if err != nil {
			got <- answer{err: err}
			return
		}
		defer r.Body.Close()
		b, err := io.ReadAll(r.Body)
		got <- answer{string(b), err}
	}()
	<-inHandler
	closeServer(srv)
	a := <-got
	if a.err != nil || a.body != `{"ok":true}` {
		t.Fatalf("the reply didn't get out: %q %v", a.body, a.err)
	}
}

// Nothing can hold the app open for long: a request that never finishes is cut off.
func TestClosingTheAppDoesntWaitForever(t *testing.T) {
	saved := closeWait
	t.Cleanup(func() { closeWait = saved })
	closeWait = 100 * time.Millisecond
	inHandler, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv, url := serveForClose(t, func(w http.ResponseWriter, r *http.Request) {
		close(inHandler)
		<-release
	})
	go func() {
		if r, err := http.Get(url); err == nil {
			r.Body.Close()
		}
	}()
	<-inHandler
	start := time.Now()
	closeServer(srv)
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("a stuck request held the app open for %v", d)
	}
}

// The app itself goes through it.
func TestTheAppClosesGracefully(t *testing.T) {
	src, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "defer closeServer(srv)") || strings.Contains(string(src), "defer srv.Close()") {
		t.Error("the app's server must stop through closeServer")
	}
}
