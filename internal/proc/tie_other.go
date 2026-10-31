//go:build !windows && !linux

package proc

import "os/exec"

func beforeStart(*exec.Cmd) {}

func afterStart(*exec.Cmd) {}
