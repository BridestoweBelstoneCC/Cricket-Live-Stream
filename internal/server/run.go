package server

// Entry point: `cricketstream server`, and what the streaming-laptop role runs.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

// Options for Run.
type Options struct {
	// Ready, if set, is called once the server is listening (the launcher opens the
	// browser from it).
	Ready func(addr string)
	// Stop, if set, shuts the server down when closed (otherwise Ctrl+C does).
	Stop <-chan struct{}
}

// Start does everything server.py's import and __main__ block did before
// serve_forever: config, state, the control token and every background loop.
func Start() {
	LoadAuthConfig()
	if !fileExists(StateFile) {
		if err := SaveState(DefaultState()); err == nil {
			say("  Created %s\n", StateFile)
		}
	}
	SeedStateFromConfig()
	RecoverInterruptedRehearsal()
	EnsureControlToken()
	StartWatchdog()
	StartStreamMonitor()
	StartViewerSampler()
	StartPrecheckLoop()
	StartCameraSpotter()
	StartOBSGuard()
	StartPCSBridgeSync()
	if CloudflareTunnel {
		if ClubPassword == "" {
			say("  ✗  Cloudflare Tunnel is enabled in config.ini but club_password is blank.")
			say("     Refusing to start it — that would expose the control panel with no")
			say("     login. Set [Auth] club_password first, then restart.")
		} else {
			startCloudflareTunnel()
		}
	}
	DBInit() // ensure the ball-by-ball database exists
}

var remoteLabels = map[string]string{"tailscale": "Tailscale remote",
	"cloudflare": "Cloudflare remote (public)", "lan": "Same-network remote"}

func banner() {
	targets := remoteTargets()
	var info strings.Builder
	for _, t := range targets {
		fmt.Fprintf(&info, "\n  %s → %s", remoteLabels[gs(t, "via", "")], gs(t, "url", ""))
	}
	if len(targets) == 0 && BindHost != "127.0.0.1" {
		fmt.Fprintf(&info, "\n  Listening on %s:%d — use your device IP to connect remotely", BindHost, Port)
	}
	hasCF := false
	for _, t := range targets {
		hasCF = hasCF || gs(t, "via", "") == "cloudflare"
	}
	if CloudflareTunnel && ClubPassword != "" && !hasCF {
		info.WriteString("\n  Cloudflare Tunnel starting up — its URL will appear in the control panel shortly")
	}
	if len(targets) > 0 {
		top := targets[0]
		if q, err := qrcode.New(gs(top, "url", ""), qrcode.Low); err == nil {
			say("\n  Scan to open the control panel on another device (%s):\n", gs(top, "via", ""))
			say("%s", q.ToSmallString(true))
		}
	}
	st := LoadState()
	say(`
  ╔══════════════════════════════════════════════════════╗
  ║        CricketStream Overlay — Stream Server         ║
  ╠══════════════════════════════════════════════════════╣
  ║   Control panel  →  http://localhost:%d/control     ║
  ║   OBS overlay    →  http://localhost:%d/overlay     ║
  ╚══════════════════════════════════════════════════════╝
%s
  Replay clips capped at %d — oldest auto-deleted.

  Keep this window open while streaming. Ctrl+C to stop.
`, Port, Port, info.String(), gi(st, "max_clips", MaxClips))
}

// Listen opens the server's socket. Every request used to be a fresh HTTP/1.0
// connection, so a burst of near-simultaneous pollers (overlay + panel + /scoring + a
// reconnect after a wifi blip) queues real TCP connects; Python's default backlog of 5
// reset connections at concurrency >= 10 and had to be raised to 64. Go's listener asks
// the OS for its maximum backlog (SOMAXCONN), comfortably above that, and net/http also
// keeps connections alive, so the burst is smaller in the first place.
func Listen() (net.Listener, error) {
	return net.Listen("tcp", net.JoinHostPort(BindHost, fmt.Sprint(Port)))
}

// NewHTTPServer is the server with this package's handler and sane timeouts. No write
// timeout: /commentary/test and the AI report wait for Claude.
func NewHTTPServer() *http.Server {
	return &http.Server{Handler: Handler{}, ReadHeaderTimeout: 20 * time.Second, IdleTimeout: 120 * time.Second,
		ErrorLog: nil}
}

// Run starts everything and serves until Ctrl+C (or opts.Stop).
func Run(opts Options) error {
	Start()
	banner()
	ln, err := Listen()
	if err != nil {
		stopCloudflareTunnel()
		return fmt.Errorf("could not listen on %s:%d — is CricketStream already running? (%w)", BindHost, Port, err)
	}
	srv := NewHTTPServer()
	if opts.Ready != nil {
		opts.Ready(ln.Addr().String())
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	go func() {
		select {
		case <-sig:
		case <-opts.Stop:
		}
		StopRehearsal() // puts the score source back
		say("\n  Server stopped.")
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()
	err = srv.Serve(ln)
	stopCloudflareTunnel()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
