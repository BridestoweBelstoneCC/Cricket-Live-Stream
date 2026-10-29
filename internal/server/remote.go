package server

import (
	"bufio"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"cricketstream/internal/port"
)

// Port is the server's HTTP port (CRICKETSTREAM_PORT overrides 5000).
var Port = port.Server()

// ── Remote access helpers (Tailscale / LAN / QR) ──────────────

// lanIP is the best-guess LAN IP for this machine. The UDP "connect" sends no packets —
// it just asks the OS which local interface would be used to reach that address.
func lanIP() string {
	c, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP.String()
}

func tailscaleIP() string {
	cmd := exec.Command("tailscale", "ip", "-4")
	hideWindow(cmd)
	done := make(chan []byte, 1)
	go func() {
		out, err := cmd.Output()
		if err != nil {
			out = nil
		}
		done <- out
	}()
	select {
	case out := <-done:
		return strings.TrimSpace(string(out))
	case <-time.After(2 * time.Second):
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		return ""
	}
}

// Cloudflare Tunnel is a persistent subprocess (unlike the one-shot `tailscale ip`), so
// its URL is discovered once at startup and cached here rather than looked up per request.
var (
	cfProcess      *exec.Cmd
	cfURL          string
	cfMu           sync.Mutex
	cfRestartCount int
)

const CFMaxRestarts = 5 // give up auto-restarting after this many — likely a real problem

var cfURLPattern = regexp.MustCompile(`https://[a-zA-Z0-9\-]+\.trycloudflare\.com`)

// startCloudflareTunnel launches `cloudflared tunnel --url` (a free "quick tunnel", no
// Cloudflare account needed) and captures the https://*.trycloudflare.com URL it
// prints. Safe to call even if cloudflared isn't installed.
func startCloudflareTunnel() {
	cmd := exec.Command("cloudflared", "tunnel", "--url", fmt.Sprintf("http://127.0.0.1:%d", Port))
	hideWindow(cmd)
	out, err := cmd.StdoutPipe()
	if err == nil {
		cmd.Stderr = cmd.Stdout
		err = cmd.Start()
	}
	if err != nil {
		if _, lerr := exec.LookPath("cloudflared"); lerr != nil {
			say("  ✗  Cloudflare Tunnel: 'cloudflared' not installed — see")
			say("     https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/")
		} else {
			say("  ✗  Cloudflare Tunnel failed to start: %v", err)
		}
		return
	}
	cfMu.Lock()
	cfProcess, cfURL = cmd, ""
	cfMu.Unlock()
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			if m := cfURLPattern.FindString(sc.Text()); m != "" {
				cfMu.Lock()
				cfURL = m
				cfMu.Unlock()
				say("  ✓  Cloudflare Tunnel ready → %s/control", m)
				break
			}
		}
		for sc.Scan() { // keep draining so cloudflared never blocks on a full pipe
		}
	}()
	go cmd.Wait()
}

func stopCloudflareTunnel() {
	cfMu.Lock()
	p := cfProcess
	cfMu.Unlock()
	if p != nil && p.Process != nil && p.ProcessState == nil {
		p.Process.Kill()
	}
}

// remoteTargets lists ways to reach the control panel from another device, in priority
// order — tailscale (private), cloudflare (public, opt-in), lan (same network). Empty if
// bound to localhost only and no tunnel is running.
func remoteTargets() []M {
	targets := []M{}
	if BindHost != "127.0.0.1" {
		if ip := tailscaleIP(); ip != "" {
			targets = append(targets, M{"via": "tailscale", "url": fmt.Sprintf("http://%s:%d/control", ip, Port)})
		}
	}
	cfMu.Lock()
	u := cfURL
	cfMu.Unlock()
	if u != "" {
		targets = append(targets, M{"via": "cloudflare", "url": u + "/control"})
	}
	if BindHost != "127.0.0.1" {
		if ip := lanIP(); ip != "" {
			targets = append(targets, M{"via": "lan", "url": fmt.Sprintf("http://%s:%d/control", ip, Port)})
		}
	}
	return targets
}

// qrPNG is a QR code for data as PNG bytes.
func qrPNG(data string) []byte {
	q, err := qrcode.New(data, qrcode.Medium)
	if err != nil {
		return nil
	}
	q.DisableBorder = false
	png, err := q.PNG(-8) // 8px modules, like the original's box_size=8
	if err != nil {
		return nil
	}
	return png
}

// ── Rate limiting (manual AI endpoints only) ─────────────────
// /commentary/over/generate is NOT here — the overlay fires it automatically at the end
// of each over; capping it would break the live experience. Only manual,
// button-triggered endpoints are rate-limited.
var rateLimits = map[string]float64{
	"/commentary/test":       60,  // test button
	"/report/generate":       120, // AI match report
	"/social/image/generate": 120, // AI social graphic
	"/obs/stream_check":      300, // a real upload-speed test uses data
	"/precheck":              20,  // calls YouTube, PlayCricket and Anthropic
	"/camera/spotter":        20,  // one AI call per camera
	// Broadcasts on the LAN — a short cooldown, just enough to stop a script (not a human
	// clicking "Find scorer laptop") from flooding the network.
	"/agent/discover":   3,
	"/cameras/discover": 3,
}

var (
	rateLimitTS = map[string]float64{}
	rateLimitMu sync.Mutex
)

// rateLimited returns the seconds left to wait (0 = go ahead, and the clock restarts).
// key splits one path's cooldown (report vs social post: separate buttons, so making one
// shouldn't lock the other out); the length still comes from the path.
func rateLimited(path, key string) int {
	cool, ok := rateLimits[path]
	if !ok {
		return 0
	}
	if key == "" {
		key = path
	}
	rateLimitMu.Lock()
	defer rateLimitMu.Unlock()
	t := now()
	if wait := cool - (t - rateLimitTS[key]); wait > 0 {
		return int(wait) + 1
	}
	rateLimitTS[key] = t
	return 0
}
