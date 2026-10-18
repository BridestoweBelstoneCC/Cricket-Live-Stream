package console

import (
	"bufio"
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

func withInput(t *testing.T, text string) {
	t.Helper()
	saved := stdin
	stdin = bufio.NewReader(strings.NewReader(text))
	t.Cleanup(func() { stdin = saved })
}

// The launcher's two yes/no questions keep their exact console wording and defaults.
func TestConfirmAtTheConsole(t *testing.T) {
	cases := []struct {
		in       string
		def      bool
		yes, ok  bool
		wantHint string
	}{
		{"\n", true, true, true, "[Y/n]: "},
		{"n\n", true, false, true, "[Y/n]: "},
		{"\n", false, false, true, "[y/N]: "},
		{"YES\n", false, true, true, "[y/N]: "},
		{"", false, false, false, "[y/N]: "}, // end of input
		{"", true, true, false, "[Y/n]: "},
	}
	for _, c := range cases {
		withInput(t, c.in)
		var yes, ok bool
		out := captureStdout(t, func() { yes, ok = Confirm("Clear it?", c.def) })
		if yes != c.yes || ok != c.ok {
			t.Errorf("Confirm(%q, def %v) = %v, %v; want %v, %v", c.in, c.def, yes, ok, c.yes, c.ok)
		}
		if out != "  Clear it? "+c.wantHint {
			t.Errorf("prompt %q", out)
		}
	}
}

type fakeUI struct {
	paused, problem string
	lines           []string
}

func (f *fakeUI) Pause(msg string)                   { f.paused = "paused:" + msg }
func (f *fakeUI) Input(prompt string) (string, bool) { return "typed in the window", true }
func (f *fakeUI) Confirm(q string, def bool) (bool, bool) {
	return !def, true
}
func (f *fakeUI) Problem(title string, lines []string) { f.problem, f.lines = title, lines }

// With a window, every prompt goes there — stdin is never read — and the text still
// reaches stdout (the window's activity log).
func TestPromptsGoToTheWindowWhenThereIsOne(t *testing.T) {
	t.Setenv("CRICKETSTREAM_NO_PAUSE", "")
	withInput(t, "") // a read would get end of input
	u := &fakeUI{}
	SetUI(u)
	t.Cleanup(func() { SetUI(nil) })
	out := captureStdout(t, func() {
		if v, ok := Input("  Scoreboard output folder: "); v != "typed in the window" || !ok {
			t.Errorf("Input = %q, %v", v, ok)
		}
		if yes, ok := Confirm("Generate a report?", false); !yes || !ok {
			t.Errorf("Confirm = %v, %v", yes, ok)
		}
		Problem("Port in use", "first", "second")
		Pause("")
	})
	if u.problem != "Port in use" || len(u.lines) != 2 || u.paused != "paused:" {
		t.Errorf("window saw %+v", u)
	}
	mustHave := []string{"Scoreboard output folder:", "Generate a report?", "PROBLEM: Port in use"}
	for _, w := range mustHave {
		if !strings.Contains(out, w) {
			t.Errorf("activity log missing %q:\n%s", w, out)
		}
	}
}
