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
	"cricketstream/internal/bridge"
	"cricketstream/internal/console"
)

const usage = `CricketStream — live-stream graphics for grassroots cricket.

Usage:
  cricketstream                 start (first run opens the setup page)
  cricketstream agent [folder]  scorer's laptop on the same wifi: serve the scoreboard file
        --port N              HTTP port (default 8788)
        --discovery-port N    UDP discovery port (default 8787)
        --check-template      report whether the NV Play scoreboard template is built in
  cricketstream bridge          scorer's machine elsewhere: token-gated scoreboard over HTTP
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
