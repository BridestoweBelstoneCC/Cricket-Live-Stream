// Package agent is the scorer agent: run on the SCORING laptop (the one with PCS Pro /
// NV Play open) when that machine is NOT the one streaming.
//
// Two-laptop mode: the person running the stream gets their own machine, so they can
// restart the server, re-scene OBS and fix graphics mid-match without reaching across to
// the scorer. See TWO_LAPTOP_SETUP.md.
//
// Unlike the NV Play bridge (a manual URL + token, recommended over Tailscale for reaching
// a scorer's machine off the club network entirely), the agent is built for two laptops
// that ARE already on the same club wifi: it answers a UDP "who's out there?" broadcast so
// the streaming laptop's control panel can find it with a button press instead of someone
// typing an IP address. There is no token — anyone on the same wifi segment can read the
// live score (never any secrets) from this machine while it runs. Use the bridge instead
// if the two machines are NOT on the same local network, or if that open-LAN-read
// tradeoff is unwanted.
//
// Read-only: it never touches PCS Pro or the scoreboard file, only serves a copy of it.
package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"cricketstream/internal/scorerfile"
)

const (
	Version     = "2.12"
	ServiceName = "cricketstream-scorer-agent"

	DefaultHTTPPort      = 8788
	DefaultDiscoveryPort = 8787

	// MaxFileBytes: anything bigger than this is not a scoreboard file — refuse to serve it.
	MaxFileBytes = 2 * 1024 * 1024
)

// DiscoveryMagic starts every discovery broadcast from the streaming laptop.
var DiscoveryMagic = []byte("CRICKETSTREAM-DISCOVER")

// State is shared between the HTTP server, the discovery responder and the console.
type State struct {
	Folder   string
	HTTPPort int
	started  time.Time

	mu          sync.Mutex
	requests    int
	lastRequest time.Time
	lastClient  string
}

func NewState(folder string, port int) *State {
	return &State{Folder: folder, HTTPPort: port, started: time.Now()}
}

func (s *State) noteRequest(client string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests++
	s.lastRequest = time.Now()
	s.lastClient = client
}

func (s *State) snapshot() (int, time.Time, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests, s.lastRequest, s.lastClient
}

func epoch(t time.Time) float64 { return float64(t.UnixNano()) / 1e9 }

// LocalIP is a best guess at this machine's LAN address (no traffic is actually sent).
func LocalIP() string {
	if c, err := net.Dial("udp", "8.8.8.8:80"); err == nil {
		defer c.Close()
		return c.LocalAddr().(*net.UDPAddr).IP.String()
	}
	if h, err := os.Hostname(); err == nil {
		if addrs, err := net.LookupHost(h); err == nil {
			for _, a := range addrs {
				if ip := net.ParseIP(a); ip != nil && ip.To4() != nil {
					return a
				}
			}
		}
	}
	return "127.0.0.1"
}

// Describe is the payload sent in discovery replies and returned by /ping.
func (s *State) Describe() map[string]any {
	host, _ := os.Hostname()
	d := map[string]any{
		"service": ServiceName, "version": Version, "hostname": host,
		"port": s.HTTPPort, "folder": s.Folder,
		"file": nil, "file_mtime": nil, "file_age": nil,
		"uptime": time.Since(s.started).Seconds(),
	}
	if p := scorerfile.Find(s.Folder, 10*time.Minute); p != "" {
		m := scorerfile.MTime(p)
		d["file"] = filepath.Base(p)
		d["file_mtime"] = m
		d["file_age"] = epoch(time.Now()) - m
	}
	return d
}

func writeJSON(w http.ResponseWriter, obj any, code int) {
	body, _ := json.Marshal(obj)
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	// The streaming laptop is a different origin; let it read us.
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	w.Write(body) // the stream laptop going away mid-response is harmless
}

// Handler serves /ping and /pcs.
func (s *State) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "CricketStreamScorerAgent/"+Version)
		if r.Method != http.MethodGet {
			http.Error(w, "Unsupported method", http.StatusNotImplemented)
			return
		}
		client, _, _ := net.SplitHostPort(r.RemoteAddr)
		switch r.URL.Path {
		case "/ping", "/":
			s.noteRequest(client)
			writeJSON(w, s.Describe(), 200)
		case "/pcs":
			s.noteRequest(client)
			s.serveScoreboard(w, r)
		default:
			writeJSON(w, map[string]any{"error": "not found", "paths": []string{"/ping", "/pcs"}}, 404)
		}
	})
}

func (s *State) serveScoreboard(w http.ResponseWriter, r *http.Request) {
	path := scorerfile.Find(s.Folder, 10*time.Minute)
	if path == "" {
		writeJSON(w, map[string]any{"ok": false, "error": "no_file",
			"detail": "No scoreboard file found in " + s.Folder, "folder": s.Folder}, 200)
		return
	}
	st, err := os.Stat(path)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "stat_failed", "detail": err.Error()}, 200)
		return
	}
	mtime := epoch(st.ModTime())
	if st.Size() > MaxFileBytes {
		writeJSON(w, map[string]any{"ok": false, "error": "too_large",
			"detail": fmt.Sprintf("%s is %d bytes", filepath.Base(path), st.Size())}, 200)
		return
	}
	// The streaming laptop tells us the mtime it already has, so an unchanged file costs
	// a few bytes instead of the whole scoreboard.
	since, err := strconv.ParseFloat(strings.TrimSpace(r.URL.Query().Get("since")), 64)
	if err != nil {
		since = 0
	}
	if since != 0 && abs(since-mtime) < 0.001 {
		writeJSON(w, map[string]any{"ok": true, "unchanged": true, "mtime": mtime}, 200)
		return
	}
	// PCS Pro rewrites this file on every ball. If we catch it mid-write we get a
	// truncated read, so retry briefly rather than serving a stub.
	raw := ""
	for attempt := 0; attempt < 3; attempt++ {
		text, err := scorerfile.ReadText(path)
		if err != nil {
			if attempt == 2 {
				writeJSON(w, map[string]any{"ok": false, "error": "read_failed", "detail": err.Error()}, 200)
				return
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		raw = text
		if strings.TrimSpace(raw) != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if strings.TrimSpace(raw) == "" {
		writeJSON(w, map[string]any{"ok": false, "error": "empty", "detail": "File is empty"}, 200)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "unchanged": false, "name": filepath.Base(path),
		"mtime": mtime, "age": epoch(time.Now()) - mtime, "content": raw}, 200)
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// RespondToDiscovery answers "where are you?" broadcasts from the streaming laptop until
// the connection is closed.
func (s *State) RespondToDiscovery(conn net.PacketConn) {
	buf := make([]byte, 2048)
	for {
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if n < len(DiscoveryMagic) || string(buf[:len(DiscoveryMagic)]) != string(DiscoveryMagic) {
			continue
		}
		body, _ := json.Marshal(s.Describe())
		conn.WriteTo(body, addr)
	}
}

// StatusLine is the one-line console summary, printed when it changes.
func (s *State) StatusLine() string {
	path := scorerfile.Find(s.Folder, 10*time.Minute)
	if path == "" {
		return "waiting for the scoreboard file — is PCS Pro scoreboard output switched on?"
	}
	age := epoch(time.Now()) - scorerfile.MTime(path)
	var health string
	switch {
	case age < 30:
		health = "live"
	case age < 300:
		health = fmt.Sprintf("last updated %ds ago", int(age))
	default:
		health = fmt.Sprintf("last updated %d min ago", int(age)/60)
	}
	n, last, client := s.snapshot()
	served := "stream laptop has not connected yet"
	if n > 0 {
		served = fmt.Sprintf("stream laptop %s collected it %ds ago", client, int(time.Since(last).Seconds()))
	}
	return fmt.Sprintf("%s - %s - %s", filepath.Base(path), health, served)
}
