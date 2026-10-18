package server

import (
	"net"
	"net/http"
	"strings"
	"testing"

	"cricketstream/internal/agent"
)

// The scorer's laptop's pairing code connects the panel to it — saved only once that
// laptop answers; a typo or a laptop that isn't there changes nothing.
func TestPairingCodeConnectsTheScorersLaptop(t *testing.T) {
	s := newTestServer(t, "")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	st := agent.NewState(t.TempDir(), ln.Addr().(*net.TCPAddr).Port)
	srv := &http.Server{Handler: st.Handler()}
	go srv.Serve(ln)
	defer srv.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	code := agent.PairingCode("127.0.0.1", port)
	status, d := s.postJSON("/agent/pair", M{"code": strings.ToLower(code)})
	if status != 200 || d["ok"] != true || d["address"] != ln.Addr().String() {
		t.Fatalf("pair: %d %v", status, d)
	}
	cur := LoadState()
	if cur["agent_host"] != ln.Addr().String() || cur["pcs_source"] != "agent" {
		t.Errorf("not saved: %v %v", cur["agent_host"], cur["pcs_source"])
	}

	// A typo: refused, nothing changed.
	typo := []byte(code)
	typo[0] = map[byte]byte{'0': '1'}[typo[0]]
	if typo[0] == 0 {
		typo[0] = '0'
	}
	if status, _ := s.postJSON("/agent/pair", M{"code": string(typo)}); status != 400 {
		t.Errorf("typo: %d", status)
	}
	// A code for a laptop that isn't there: says so, keeps the working one.
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	gone := closed.Addr().(*net.TCPAddr).Port
	closed.Close()
	if _, d := s.postJSON("/agent/pair", M{"code": agent.PairingCode("127.0.0.1", gone)}); d["ok"] != false {
		t.Errorf("a missing laptop paired: %v", d)
	}
	if LoadState()["agent_host"] != ln.Addr().String() {
		t.Error("a failed pairing replaced the working address")
	}
}

func TestPairingNeedsTheLogin(t *testing.T) {
	s := newTestServer(t, "pw")
	if status, _ := s.postJSON("/agent/pair", M{"code": "0000-0000"}); status != 401 {
		t.Errorf("without the login: %d", status)
	}
}
