package socialclips

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"cricketstream/internal/proc"
)

func TestHeadlines(t *testing.T) {
	for in, want := range map[string]string{"Wicket": "WICKET!", "Century - Smith": "CENTURY!",
		"Boundary": "FOUR!", "": "HIGHLIGHT", "Hat-trick ball": "HAT-TRICK!", "Lovely shot by Jones": "LOVELY SHOT BY J"} {
		if got := HeadlineFor(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

func TestLinesWrapAndCap(t *testing.T) {
	lines := WrapLines(strings.Repeat("word ", 40), 30, 3)
	if len(lines) != 3 || !strings.HasSuffix(lines[2], "…") {
		t.Fatalf("%q", lines)
	}
	for _, l := range lines {
		if utf8.RuneCountInString(l) > 31 {
			t.Fatalf("too long: %q", l)
		}
	}
}

// A caption with quotes/colons can't break the filter graph: text goes via files.
func TestFilterUsesTextfilesNotInlineText(t *testing.T) {
	graph, last := BuildFilter(TextFiles{Headline: `C:\tmp\h.txt`, Lines: []string{`C:\tmp\l0.txt`}, Handle: `C:\tmp\x.txt`}, "", true)
	for _, want := range []string{`textfile='C\:/tmp/h.txt'`, "[1:v]scale=170:-1[badge]", "1080:1920"} {
		if !strings.Contains(graph, want) {
			t.Fatalf("missing %q in %s", want, graph)
		}
	}
	if !strings.HasSuffix(graph, "["+last+"]") {
		t.Fatal("graph doesn't end at its output label")
	}
}

func TestRenderReportsFailureInsteadOfPanicking(t *testing.T) {
	old := run
	run = func(string, []string, proc.Options) (proc.Result, error) {
		return proc.Result{Code: 1, Stderr: "boom\nInvalid data"}, nil
	}
	defer func() { run = old }()
	tmp := t.TempDir()
	ok, msg := Render("in.mp4", filepath.Join(tmp, "o.mp4"), "SIX!", "x", tmp, "", "", "", 0)
	if ok || !strings.Contains(msg, "Invalid data") {
		t.Fatal(ok, msg)
	}
}

func TestWrapMatchesPythonTextwrap(t *testing.T) {
	py, err := exec.LookPath("python")
	if err != nil {
		t.Skip("no python")
	}
	rng := rand.New(rand.NewSource(3))
	words := []string{"SIX", "North", "hits", "a", "huge", "one", "over", "long-on", "hat-trick", "60-1",
		"Smith's", "first", "fifty", "of", "the", "season!", "supercalifragilisticexpialidocious",
		"—", "x-y", "well--known", "a-b-c-d", "café", "…"}
	var cases []string
	for i := 0; i < 300; i++ {
		var b strings.Builder
		for j := rng.Intn(25); j >= 0; j-- {
			b.WriteString(words[rng.Intn(len(words))])
			b.WriteString([]string{" ", " ", "  ", "\n"}[rng.Intn(4)])
		}
		cases = append(cases, b.String())
	}
	widths := []int{30, 12, 5}
	in, _ := json.Marshal(map[string]any{"cases": cases, "widths": widths})
	cmd := exec.Command(py, "-I", "-c", `import json, sys, textwrap
d = json.loads(sys.stdin.buffer.read().decode("utf-8"))
json.dump([[textwrap.wrap(c, width=w) for w in d["widths"]] for c in d["cases"]], sys.stdout)`)
	cmd.Stdin = bytes.NewReader(in)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var want [][][]string
	json.Unmarshal(out, &want)
	for i, c := range cases {
		for wi, w := range widths {
			got := Wrap(c, w)
			if got == nil {
				got = []string{}
			}
			if !reflect.DeepEqual(got, want[i][wi]) {
				t.Fatalf("width %d, %q:\n go %q\n py %q", w, c, got, want[i][wi])
			}
		}
	}
}
