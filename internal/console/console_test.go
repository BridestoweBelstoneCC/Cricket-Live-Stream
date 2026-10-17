package console

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	f()
	w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	return string(b)
}

func TestProblemIsFramedAndReadable(t *testing.T) {
	out := captureStdout(t, func() { Problem("Something broke", "first line", "", "second line") })
	for _, want := range []string{strings.Repeat("=", 60), "PROBLEM: Something broke", "   first line\n\n   second line"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// With CRICKETSTREAM_NO_PAUSE=1 a pause returns at once (automation); otherwise it
// waits for a line on stdin.
func TestPauseCanBeSwitchedOff(t *testing.T) {
	t.Setenv("CRICKETSTREAM_NO_PAUSE", "1")
	if !NoPause() {
		t.Fatal("NoPause not honoured")
	}
	done := make(chan struct{})
	go func() { Pause(""); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("paused despite CRICKETSTREAM_NO_PAUSE=1")
	}
	t.Setenv("CRICKETSTREAM_NO_PAUSE", "")
	if NoPause() {
		t.Fatal("NoPause on without the variable")
	}
}
