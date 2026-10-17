// Package port is the stream server's HTTP port, in the one place every part of the app
// reads it: the server, the launcher's health checks, OBS's overlay source, the
// simulator and the diagnostic tools must all agree on it.
package port

import (
	"os"
	"strconv"
)

// Default is what every OBS overlay source points at.
const Default = 5000

// Env overrides the port — for running a second copy side by side, or on a Mac whose
// AirPlay Receiver holds 5000 and can't be switched off.
const Env = "CRICKETSTREAM_PORT"

// Server is the port to use: the override if it's a valid port, else Default.
func Server() int {
	if p, err := strconv.Atoi(os.Getenv(Env)); err == nil && p > 0 && p < 65536 {
		return p
	}
	return Default
}

// LoopbackOnlyEnv keeps the scorer's-laptop roles (agent, bridge) on 127.0.0.1 instead
// of every interface. For automated runs: listening on the network makes Windows ask
// about the firewall for every new copy of the binary.
const LoopbackOnlyEnv = "CRICKETSTREAM_LOOPBACK_ONLY"

// NetworkHost is the address the agent and bridge listen on: every interface, unless
// LoopbackOnlyEnv is set.
func NetworkHost() string {
	if os.Getenv(LoopbackOnlyEnv) == "1" {
		return "127.0.0.1"
	}
	return "0.0.0.0"
}
