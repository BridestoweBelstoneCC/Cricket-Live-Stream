//go:build !windows

package server

import "os/exec"

func hideWindow(*exec.Cmd) {}
