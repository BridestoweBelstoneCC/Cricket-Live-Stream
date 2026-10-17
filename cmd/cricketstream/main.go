// CricketStream is the whole app in one program. Double-clicked, it works out what this
// machine is for (from config.ini, or the in-app setup on first run) and does it.
// Subcommands run one part directly, for automation and troubleshooting.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"cricketstream/internal/agent"
	"cricketstream/internal/assets"
	"cricketstream/internal/bridge"
	"cricketstream/internal/console"
	"cricketstream/internal/ini"
	"cricketstream/internal/launcher"
	"cricketstream/internal/obs"
	"cricketstream/internal/server"
	"cricketstream/internal/setup"
	"cricketstream/internal/simulate"
)

// version is set at build time (-ldflags "-X main.version=2.11"); "" for a dev build.
var version = ""

const usage = `CricketStream — live-stream graphics for grassroots cricket.

Usage:
  cricketstream                 what this machine is set up for (first run opens setup)
  cricketstream setup           open the setup page (change the club's details or role)
  cricketstream start           match day on the streaming laptop
        --remote / --local    answer "how do you want to run today?" up front
        --manual / --nvplay   answer "how is today's match being scored?" up front
  cricketstream server          just the server: overlay, control panel, scoring page
  cricketstream agent [folder]  scorer's laptop on the same wifi: serve the scoreboard file
        --port N              HTTP port (default 8788)
        --discovery-port N    UDP discovery port (default 8787)
        --check-template      report whether the NV Play scoreboard template is built in
  cricketstream bridge          scorer's machine elsewhere: token-gated scoreboard over HTTP
  cricketstream simulate        rehearse with a simulated match (--list for scenarios)
  cricketstream obs-setup       build OBS's scenes and sources (OBS must be open)
  cricketstream help            this text
`

func main() {
	console.Init()
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	os.Exit(run(cmd, args))
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
		if err := server.Run(server.Options{}); err != nil {
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
	case "help", "-h", "--help":
		fmt.Print(usage)
		return 0
	case "":
		return byRole()
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

// byRole is the double-click: first run opens the setup page, then every run goes
// straight into the job this machine was set up for.
func byRole() int {
	role := setup.Role()
	if role == "" {
		fmt.Println()
		fmt.Println("  Welcome to CricketStream — no config.ini yet, so first a one-time setup.")
		fmt.Println()
		res, err := setup.Run(setup.Options{OpenBrowser: true})
		if err != nil {
			fmt.Println("\n  " + err.Error())
			console.Pause("")
			return 1
		}
		role = res.Values.Role
		fmt.Printf("\n  Saved. This machine is %s.\n\n", roleName(role))
	}
	switch role {
	case setup.RoleScorer:
		folder := ""
		if f, err := ini.Load(setup.ConfigFile()); err == nil {
			folder = f.Get("Scoring", "pcs_output_folder", "")
		}
		return agent.Run(agent.Options{Folder: folder})
	case setup.RoleBridge:
		return bridge.Run()
	}
	return launcher.Run(launcher.Options{})
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
