// Package proc runs external programs (ffmpeg, the OS speech engine, OBS) the way the
// match-day code needs: with a time limit, output captured, and never a stray console
// window or a priority that could starve OBS's encoder.
package proc

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

// ErrTimeout is returned when the program ran past its time limit (and was killed).
var ErrTimeout = errors.New("timed out")

// Result is what a finished program left behind.
type Result struct {
	Code   int
	Stdout string
	Stderr string
}

// Options tweak how a program is started.
type Options struct {
	Timeout     time.Duration
	BelowNormal bool // Windows BELOW_NORMAL_PRIORITY_CLASS; nice 10 elsewhere
	Dir         string
	Env         []string // the whole environment; nil = this process's
}

// Runner is swappable so tests can fake external programs.
type Runner func(name string, args []string, o Options) (Result, error)

// Run is the real Runner. A non-zero exit is reported in Result.Code, not as an error;
// the error is for "couldn't run it at all" and ErrTimeout.
func Run(name string, args []string, o Options) (Result, error) {
	ctx := context.Background()
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = o.Dir
	cmd.Env = o.Env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	configure(cmd, o)
	err := cmd.Run()
	res := Result{Stdout: out.String(), Stderr: errb.String()}
	if ctx.Err() == context.DeadlineExceeded {
		res.Code = -1
		return res, ErrTimeout
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.Code = ee.ExitCode()
		return res, nil
	}
	return res, err
}

// Hide runs cmd without a console window of its own (Windows), for a command whose
// output is read as it runs rather than through Run.
func Hide(cmd *exec.Cmd) { configure(cmd, Options{}) }
