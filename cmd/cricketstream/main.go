// CricketStream is the whole app in one program. Double-clicked, it opens its own window
// (internal/app): on first run that asks which computer this is and runs setup, and from
// then on it does this computer's job in the window. Subcommands run one part directly at
// a console, for automation and troubleshooting.
package main

// The Windows icon, version and manifest live in winres/ and are compiled into the
// rsrc_windows_*.syso files beside this one (the release workflow restamps the version).
//go:generate go run github.com/tc-hib/go-winres@v0.3.3 make --in winres/winres.json --out rsrc --arch amd64,arm64

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"cricketstream/internal/agent"
	csapp "cricketstream/internal/app"
	"cricketstream/internal/assets"
	"cricketstream/internal/bridge"
	"cricketstream/internal/clubhouse"
	"cricketstream/internal/console"
	"cricketstream/internal/launcher"
	"cricketstream/internal/obs"
	"cricketstream/internal/onvif"
	"cricketstream/internal/server"
	"cricketstream/internal/setup"
	"cricketstream/internal/simulate"
	"cricketstream/internal/tools"
)

// version is set at build time (-ldflags "-X main.version=2.11"); "" for a dev build.
var version = ""

const usage = `CricketStream — live-stream graphics for grassroots cricket.

Usage:
  cricketstream                 open CricketStream's window (first run: setup)
  cricketstream setup           the setup page in the browser (change the details or role)
  cricketstream start           match day on the streaming laptop
        --remote / --local    answer "how do you want to run today?" up front
        --manual / --nvplay   answer "how is today's match being scored?" up front
  cricketstream server          just the server: overlay, control panel, scoring page
  cricketstream agent [folder]  scorer's laptop on the same wifi: serve the scoreboard file
                                (or drop the scoreboard folder onto the app)
        --port N              HTTP port (default 8788)
        --discovery-port N    UDP discovery port (default 8787)
        --check-template      report whether the NV Play scoreboard template is built in
  cricketstream bridge          scorer's machine elsewhere: token-gated scoreboard over HTTP
  cricketstream clubhouse       the clubhouse touch screen (a Raspberry Pi): switches the
                                streaming laptop on, runs match day, shows the score
        --kiosk               open Chromium full screen on it (the Pi's autostart)
        --port N              the screen page's port (default 5097, this computer only)
        --laptop ADDR         pair with this address instead of searching the network
  cricketstream simulate        rehearse with a simulated match (--list for scenarios)
  cricketstream obs-setup       build OBS's scenes and sources (OBS must be open)

Match-day diagnostics:
  cricketstream telemetry       log bandwidth, drops and match context to diagnostics/
        --interval S          seconds between samples (default 10)
        --probe-every S       seconds between headroom probes, 0 = off (default 300)
        --summary CSV         summarise a telemetry CSV and exit
  cricketstream cameras         list the IP cameras on this network (ONVIF)
  cricketstream camera-encoder  read the Reolink camera's own encoder settings
        --source Cam / --host IP --user NAME / --channel N
  cricketstream refresh-cam     reload the OBS camera source on a timer (RTSP drift)
        --source Cam / --interval 60 / --once / --host H / --port P
  cricketstream quality-test    drive the quality ladder down and back while live

  cricketstream version         which version this is
  cricketstream licences        the licence and every third-party notice
  cricketstream help            this text
`

func main() {
	console.Init()
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	os.Exit(guard(func() int { return run(cmd, args) }))
}

// guard turns a crash into the framed explanation and pause every other fatal path
// gives, rather than a stack trace in a double-clicked window that closes at once (the
// trace is printed first, so the explanation is what's left on screen).
func guard(f func() int) (code int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "\n%v\n%s", r, debug.Stack())
			console.Problem("CricketStream hit an unexpected error", fmt.Sprint(r), "",
				"The details are printed above this box. Please report them:",
				"  https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream/issues")
			console.Pause("")
			code = 1
		}
	}()
	return f()
}

func run(cmd string, args []string) int {
	server.Version = version
	switch cmd {
	case "setup":
		res, err := setup.Run(setup.Options{OpenBrowser: true})
		if err != nil {
			fmt.Println("\n  " + err.Error())
			return 1
		}
		fmt.Printf("\n  Saved. This machine is set up as: %s\n", roleName(res.Values.Role))
		console.Pause("")
		return 0
	case "start":
		return startMatch(args)
	case "server":
		if err := server.Run(server.Options{Stop: stopOnEOF()}); err != nil {
			console.Die("The stream server stopped", err.Error())
		}
		return 0
	case "agent":
		fs := flag.NewFlagSet("agent", flag.ExitOnError)
		port := fs.Int("port", agent.DefaultHTTPPort, "HTTP port to serve on")
		disc := fs.Int("discovery-port", agent.DefaultDiscoveryPort, "UDP port to answer discovery on")
		check := fs.Bool("check-template", false, "report whether the NV Play scoreboard template is bundled, then exit")
		fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
		fs.Parse(reorder(args))
		return agent.Run(agent.Options{Folder: fs.Arg(0), Port: *port, DiscoveryPort: *disc, CheckTemplate: *check})
	case "bridge":
		return bridge.Run()
	case "clubhouse":
		fs := flag.NewFlagSet("clubhouse", flag.ExitOnError)
		o := clubhouse.ScreenOptions{Version: version}
		fs.BoolVar(&o.Kiosk, "kiosk", false, "open Chromium full screen on the screen's page")
		fs.IntVar(&o.Port, "port", clubhouse.DefaultScreenPort, "the screen page's port (loopback only)")
		fs.StringVar(&o.Laptop, "laptop", "", "the streaming laptop's address, if the network blocks the search")
		fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
		fs.Parse(args)
		return clubhouse.RunScreen(o)
	case "simulate":
		fs := flag.NewFlagSet("simulate", flag.ExitOnError)
		o := simulate.Options{}
		fs.StringVar(&o.Scenario, "scenario", "full", "full | chase | century | collapse")
		fs.StringVar(&o.Folder, "folder", "", "where to write the scoreboard file (default: sim_pcs_output in the app folder)")
		fs.Float64Var(&o.BallSeconds, "ball-seconds", 3.0, "pace: seconds between deliveries")
		fs.IntVar(&o.Overs, "overs", 20, "overs per innings")
		seed := fs.Int64("seed", -1, "reproducible match")
		fs.BoolVar(&o.Configure, "configure", false, "point the RUNNING server's PCS folder at the sim folder")
		fs.BoolVar(&o.Chaos, "chaos", false, "inject realistic failures: mid-write empty files, feed stalls")
		fs.BoolVar(&o.List, "list", false, "describe scenarios and exit")
		fs.BoolVar(&o.ClearingTicker, "clearing-ticker", false, "clear the ball ticker when an over completes")
		fs.Parse(args)
		if *seed >= 0 {
			o.Seed = seed
		}
		return simulate.Run(o)
	case "obs-setup":
		fmt.Println()
		fmt.Println("OBS Auto-Setup — CricketStream")
		fmt.Println("──────────────────────────────")
		ok, _ := obs.SetupFromConfig(assets.Join("config.ini"), assets.Join("match_state.json"), true, false)
		fmt.Println()
		if ok {
			fmt.Println("  OBS is configured and ready.")
		} else {
			fmt.Println("  Setup incomplete — see messages above.")
		}
		fmt.Println()
		if !ok {
			return 1
		}
		return 0
	case "telemetry":
		fs := flag.NewFlagSet("telemetry", flag.ExitOnError)
		o := tools.TelemetryOptions{}
		fs.Float64Var(&o.Interval, "interval", 10, "seconds between samples")
		fs.StringVar(&o.Summary, "summary", "", "summarise an existing CSV and exit")
		fs.Float64Var(&o.ProbeEvery, "probe-every", 300, "seconds between headroom probes; 0 disables")
		fs.Parse(args)
		return tools.Telemetry(o)
	case "cameras":
		fmt.Println("\n  Looking for IP cameras on this network (ONVIF)...")
		cams, err := onvif.Discover(3 * time.Second)
		if err != nil {
			fmt.Println("  " + err.Error())
			return 1
		}
		if len(cams) == 0 {
			fmt.Println("  None answered. Is ONVIF switched on in the camera's own settings, and is it on this network?")
			return 0
		}
		for _, c := range cams {
			maker := ""
			if c.Make != "" && !strings.Contains(c.Name, c.Make) {
				maker = "  (" + c.Make + ")"
			}
			fmt.Printf("  %-15s  %s%s\n", c.Address, c.Name, maker)
		}
		return 0
	case "camera-encoder":
		fs := flag.NewFlagSet("camera-encoder", flag.ExitOnError)
		o := tools.CameraOptions{}
		fs.StringVar(&o.Source, "source", "Cam", "OBS source to read the RTSP URL from")
		fs.StringVar(&o.Host, "host", "", "camera IP (skips OBS lookup)")
		fs.StringVar(&o.User, "user", "", "camera username")
		fs.IntVar(&o.Channel, "channel", 0, "camera channel")
		fs.Parse(args)
		return tools.CameraEncoder(o)
	case "refresh-cam":
		fs := flag.NewFlagSet("refresh-cam", flag.ExitOnError)
		o := tools.RefreshOptions{}
		fs.StringVar(&o.Source, "source", "Cam", "media source name")
		fs.Float64Var(&o.Interval, "interval", 60, "seconds between reloads")
		fs.BoolVar(&o.Once, "once", false, "reload once and exit")
		fs.StringVar(&o.Host, "host", "localhost", "OBS host")
		fs.IntVar(&o.Port, "port", 4455, "OBS WebSocket port")
		fs.Parse(args)
		return tools.RefreshCam(o)
	case "quality-test":
		return tools.QualityTest()
	case "version", "--version":
		if version == "" {
			fmt.Println("CricketStream (development build)")
		} else {
			fmt.Println("CricketStream v" + strings.TrimPrefix(version, "v"))
		}
		return 0
	case "licences", "licenses":
		// The MIT/BSD/Apache notices of everything built in travel with every copy — and
		// a release is just this file. (Generated by scripts/notices.)
		for _, name := range []string{"THIRD_PARTY_NOTICES.txt", "LICENSE"} {
			if b, err := assets.Embedded(name); err == nil {
				os.Stdout.Write(b)
				fmt.Println()
			}
		}
		return 0
	case "help", "-h", "--help":
		fmt.Print(usage)
		return 0
	case "":
		return openApp()
	}
	// A folder dropped onto the exe is the scorer's laptop pointing the agent at NV Play's
	// output, as the old scorer-agent exe allowed: remembered, and the window opens on it.
	if st, err := os.Stat(cmd); err == nil && st.IsDir() {
		if abs, err := filepath.Abs(cmd); err == nil {
			agent.SaveFolder(abs)
		}
		return openApp()
	}
	fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
	return 2
}

// reorder lets flags follow the positional folder ("agent C:\x --port 9"), as argparse
// allowed; Go's flag package stops at the first non-flag.
func reorder(args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && a != "--check-template" && a != "-check-template" && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		pos = append(pos, a)
	}
	return append(flags, pos...)
}

func roleName(r string) string {
	switch r {
	case setup.RoleScorer:
		return "the scorer's laptop (same wifi as the streaming laptop)"
	case setup.RoleBridge:
		return "the scorer's machine (reached over Tailscale)"
	}
	return "the streaming laptop"
}

// openApp is the double-click: CricketStream's own window.
func openApp() int {
	return csapp.Run(csapp.Options{Version: version, Headless: os.Getenv("CRICKETSTREAM_NO_WINDOW") == "1"})
}

// stopOnEOF is how the launcher stops the server it supervises: by closing its stdin
// (a clean stop on every platform, where a Windows TerminateProcess can't be caught).
func stopOnEOF() <-chan struct{} {
	if os.Getenv(launcher.StopOnEOFEnv) != "1" {
		return nil
	}
	ch := make(chan struct{})
	go func() {
		io.Copy(io.Discard, os.Stdin)
		close(ch)
	}()
	return ch
}

func startMatch(args []string) int {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	remote := fs.Bool("remote", false, "allow Tailscale / LAN access to the control panel")
	local := fs.Bool("local", false, "control panel on this laptop only")
	manual := fs.Bool("manual", false, "score manually at /scoring today")
	nvplay := fs.Bool("nvplay", false, "the scorer's software writes the feed")
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	fs.Parse(args)
	o := launcher.Options{}
	if *remote || *local {
		o.Remote = remote
	}
	if *manual || *nvplay {
		o.Manual = manual
	}
	return launcher.Run(o)
}
