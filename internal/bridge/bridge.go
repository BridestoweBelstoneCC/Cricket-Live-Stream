// Package bridge is the NV Play bridge: run on the machine that has NV Play / PCS Pro
// open, when that machine is NOT the one streaming (e.g. a dedicated Windows box, so a
// scorer's VM isn't sharing the streaming machine's CPU/heat budget with OBS and the
// server).
//
// It serves NV Play's scoreboard output file over HTTP so the streaming server can
// mirror it in near real time and feed it through the normal PCS pipeline, unchanged.
// Settings live in bridge_config.ini in the app folder (folder, port, random token).
// Reaching it over Tailscale is the recommended way to point the streaming machine at
// this one — see BRIDGE.md.
package bridge

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
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

	"cricketstream/internal/assets"
	"cricketstream/internal/console"
	"cricketstream/internal/ini"
	"cricketstream/internal/port"
	"cricketstream/internal/scorerfile"
)

const DefaultPort = 5050

// ConfigPath is bridge_config.ini in the app folder.
func ConfigPath() string { return assets.Join("bridge_config.ini") }

// NewToken is a random 32-hex-digit token.
func NewToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Config is the bridge's settings.
type Config struct {
	Folder string
	Port   int
	Token  string
}

// Load reads bridge_config.ini; ok is false when there's no usable [bridge] section.
func Load() (Config, bool) {
	f, err := ini.Load(ConfigPath())
	if err != nil || !f.HasSection("bridge") {
		return Config{}, false
	}
	return Config{
		Folder: strings.TrimSpace(f.Get("bridge", "folder", "")),
		Port:   f.GetInt("bridge", "port", DefaultPort),
		Token:  f.Get("bridge", "token", ""),
	}, true
}

// Save writes bridge_config.ini (the in-app setup page calls this too).
func Save(c Config) error {
	f := ini.New()
	f.Set("bridge", "folder", c.Folder)
	f.Set("bridge", "port", strconv.Itoa(c.Port))
	f.Set("bridge", "token", c.Token)
	return f.Save(ConfigPath())
}

// loadOrCreate asks the one-time questions at the console when there's no config.
func loadOrCreate() Config {
	if c, ok := Load(); ok {
		return c
	}
	fmt.Println("No bridge_config.ini found — one-time setup.")
	fmt.Println()
	folder, _ := console.Input("Full path to the NV Play / PCS Pro output folder: ")
	folder = strings.Trim(strings.TrimSpace(folder), `"`)
	portText, _ := console.Input(fmt.Sprintf("Port to serve on [%d]: ", DefaultPort))
	port, err := strconv.Atoi(strings.TrimSpace(portText))
	if err != nil {
		port = DefaultPort
	}
	c := Config{Folder: folder, Port: port, Token: NewToken()}
	if err := Save(c); err != nil {
		console.Die("Couldn't save bridge_config.ini", err.Error())
	}
	fmt.Printf("\nSaved %s — edit it by hand to change these later.\n\n", ConfigPath())
	return c
}

func writeJSON(w http.ResponseWriter, obj any, status int) {
	body, _ := json.Marshal(obj)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	w.Write(body)
}

// Handler serves /pcs/ping (open) and /pcs/latest (token-gated).
func Handler(folder, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Unsupported method", http.StatusNotImplemented)
			return
		}
		path := r.URL.RequestURI()
		if strings.HasPrefix(path, "/pcs/ping") {
			// No token needed — just "is this machine up", no match data in the response.
			writeJSON(w, map[string]any{"ok": true}, 200)
			return
		}
		if !strings.HasPrefix(path, "/pcs/latest") {
			writeJSON(w, map[string]any{"ok": false, "error": "not found"}, 404)
			return
		}
		// A blank configured token must never authenticate a blank/missing header — that
		// would turn "token accidentally cleared" into "anyone on the network can read
		// the live score", silently.
		if token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Bridge-Token")), []byte(token)) != 1 {
			writeJSON(w, map[string]any{"ok": false, "error": "bad token"}, 401)
			return
		}
		p := scorerfile.Find(folder, 10*time.Minute)
		if p == "" {
			writeJSON(w, map[string]any{"ok": false, "error": "no PCS output file found"}, 200)
			return
		}
		content, err := scorerfile.ReadText(p)
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": err.Error()}, 200)
			return
		}
		st, err := os.Stat(p)
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": err.Error()}, 200)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "filename": filepath.Base(p),
			"mtime": float64(st.ModTime().UnixNano()) / 1e9, "content": content}, 200)
	})
}

// Running is a bridge that's serving (the app's window shows it).
type Running struct {
	Config Config

	srv         *http.Server
	mu          sync.Mutex
	fetches     int
	lastFetch   time.Time
	lastFetcher string
}

// Fetches is how many times the streaming machine has collected the file, and when it
// last did.
func (r *Running) Fetches() (n int, last time.Time, from string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fetches, r.lastFetch, r.lastFetcher
}

// Start serves c without asking anything; c must have a token.
func Start(c Config) (*Running, error) {
	if c.Token == "" {
		return nil, errors.New("no token set in bridge_config.ini — a blank token would let anyone on the network read the live score")
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", port.NetworkHost(), c.Port))
	if err != nil {
		return nil, fmt.Errorf("couldn't start on port %d (is the bridge already running?): %w", c.Port, err)
	}
	r := &Running{Config: c}
	h := Handler(c.Folder, c.Token)
	r.srv = &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasPrefix(req.URL.Path, "/pcs/latest") && subtle.ConstantTimeCompare([]byte(req.Header.Get("X-Bridge-Token")), []byte(c.Token)) == 1 {
			host, _, _ := net.SplitHostPort(req.RemoteAddr)
			r.mu.Lock()
			r.fetches, r.lastFetch, r.lastFetcher = r.fetches+1, time.Now(), host
			r.mu.Unlock()
		}
		h.ServeHTTP(w, req)
	})}
	go r.srv.Serve(ln)
	return r, nil
}

// Stop stops serving.
func (r *Running) Stop() { r.srv.Close() }

// Run serves until stopped.
func Run() int {
	c := loadOrCreate()
	if st, err := os.Stat(c.Folder); c.Folder == "" || err != nil || !st.IsDir() {
		fmt.Printf("WARNING: folder not found: %q\n", c.Folder)
		fmt.Printf("Edit %s and fix the 'folder' path, then rerun.\n\n", ConfigPath())
	}
	if c.Token == "" {
		console.Die("No token set in bridge_config.ini — refusing to start",
			"A blank token would let anyone on the network read the live score.",
			"Set one by hand, or delete bridge_config.ini and rerun to generate one.")
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", port.NetworkHost(), c.Port))
	if err != nil {
		console.Die(fmt.Sprintf("Couldn't start on port %d", c.Port), err.Error(), "",
			"Is the bridge already running in another window?")
	}
	fmt.Printf("NV Play bridge — serving '%s' on port %d\n", c.Folder, c.Port)
	fmt.Printf("Token: %s\n", c.Token)
	fmt.Println("On the streaming machine's control panel (Match Setup), set:")
	fmt.Printf("  NV Play Bridge URL:   http://<this machine's Tailscale IP>:%d\n", c.Port)
	fmt.Printf("  NV Play Bridge Token: %s\n", c.Token)
	fmt.Println("\nWindows may prompt to allow network access the first time — allow it.")
	fmt.Println("Keep this window open while streaming. Ctrl+C to stop.")
	fmt.Println()
	srv := &http.Server{Handler: Handler(c.Folder, c.Token), ReadHeaderTimeout: 10 * time.Second}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Println("Bridge stopped:", err)
		return 1
	}
	return 0
}
