package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

// A crash anywhere under main is turned into the framed explanation (and the pause), not
// a bare stack trace in a window that then closes.
func TestGuardTurnsAPanicIntoAnExplainedExit(t *testing.T) {
	t.Setenv("CRICKETSTREAM_NO_PAUSE", "1")
	r, w, _ := os.Pipe()
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w
	code := guard(func() int { panic("boom") })
	w.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	out, _ := io.ReadAll(r)
	if code != 1 {
		t.Errorf("exit %d", code)
	}
	for _, want := range []string{"boom", "PROBLEM: CricketStream hit an unexpected error", "issues"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if guard(func() int { return 7 }) != 7 {
		t.Error("guard changed a normal exit code")
	}
}

// Flags may follow the folder, as argparse allowed ("agent C:\x --port 9").
func TestReorderPutsFlagsFirst(t *testing.T) {
	got := strings.Join(reorder([]string{`C:\x`, "--port", "9", "--check-template"}), " ")
	if got != `--port 9 --check-template C:\x` {
		t.Errorf("got %q", got)
	}
}
