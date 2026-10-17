package voice

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cricketstream/internal/proc"
)

func fake(t *testing.T, fn proc.Runner) {
	oldRun, oldLook := run, lookPath
	run, lookPath = fn, func(string) (string, error) { return "espeak", nil }
	t.Cleanup(func() { run, lookPath = oldRun, oldLook })
}

func TestTextGoesThroughAFileNotTheCommandLine(t *testing.T) {
	var captured []string
	fake(t, func(name string, args []string, o proc.Options) (proc.Result, error) {
		captured = append([]string{name}, args...)
		for i, a := range args {
			if a == "-OutFile" || a == "-o" || a == "-w" {
				os.WriteFile(args[i+1], append([]byte("RIFF"), make([]byte, 200)...), 0o644)
			}
		}
		return proc.Result{}, nil
	})
	line := `North hits a "six"; $(rm -rf /) & more`
	ok, msg := Synthesize(line, filepath.Join(t.TempDir(), "a.wav"), "", 0)
	if !ok {
		t.Fatal(msg)
	}
	for _, part := range captured {
		if strings.Contains(part, line) {
			t.Fatal("the line reached the command line")
		}
	}
}

func TestEmptyTextAndFailuresNeverPanic(t *testing.T) {
	if ok, _ := Synthesize("  ", "x.wav", "", 0); ok {
		t.Fatal("empty text should fail")
	}
	fake(t, func(string, []string, proc.Options) (proc.Result, error) {
		return proc.Result{}, errors.New("no engine")
	})
	if ok, _ := Synthesize("hello", filepath.Join(t.TempDir(), "v.wav"), "", 0); ok {
		t.Fatal("expected failure")
	}
}
