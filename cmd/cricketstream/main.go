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
	"cricketstream/internal/obs"
	"cricketstream/internal/server"
	"cricketstream/internal/simulate"
)

const usage = `CricketStream — live-stream graphics for grassroots cricket.

Usage:
  cricketstream                 start (first run opens the setup page)
  cricketstream server          the streaming laptop: overlay, control panel, scoring page
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
	switch cmd {
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
	case "server":
		if err := server.Run(server.Options{}); err != nil {
			console.Die("The stream server stopped", err.Error())
		}
		return 0
	case "help", "-h", "--help":
		fmt.Print(usage)
		return 0
	case "":
		fmt.Print(usage)
		console.Problem("Not built yet", "The streaming server and the in-app setup are still being ported.")
		console.Pause("")
		return 1
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
