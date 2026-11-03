package matchpage

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var innings = []Innings{
	{Team: "Home CC", Total: "33-1 (5 ov)",
		Batters: []Batter{{Name: "Smith", Runs: 19, Balls: 13}},
		Bowlers: []Bowler{{Name: "Frost", O: "3", R: 22}},
		FOW:     []FOW{{Score: "7-1", Batter: "P Smith"}}},
	{Team: "Rival CC", Total: "73-5 (10 ov)"},
}

func TestEveryPieceOfTextIsEscaped(t *testing.T) {
	inn := innings[0]
	inn.Batters = []Batter{{Name: "O'Neil <i>", Runs: 1, Balls: 2, Out: true}}
	page := Render(Data{Title: "<script>alert(1)</script>", Result: "A & B won",
		Report:   "He hit <b>six</b>\n\n<img src=x onerror=alert(1)>",
		Innings:  []Innings{inn},
		VideoURL: `https://youtu.be/x" onclick="evil`})
	for _, bad := range []string{"<script>alert", "<img src=x", "<i>", `" onclick="`} {
		if strings.Contains(page, bad) {
			t.Fatalf("page contains %q", bad)
		}
	}
	if !strings.Contains(page, "A &amp; B won") {
		t.Fatal("result not escaped")
	}
}

func TestMissingPartsAreLeftOut(t *testing.T) {
	page := Render(Data{Title: "Home v Away"})
	for _, absent := range []string{"Watch the full stream", "Match report", "Runs by over", "class=sponsors", "Result card"} {
		if strings.Contains(page, absent) {
			t.Fatalf("page contains %q", absent)
		}
	}
}

func TestMomentsLinkIntoTheVideo(t *testing.T) {
	inn := Innings{Team: "Home CC", Total: "33-1 (5 ov)",
		FOW:     []FOW{{Score: "7-1", Batter: "P Smith", URL: "https://youtu.be/abc?t=2705"}, {Score: "9-2", Batter: "Jones"}},
		Moments: []Moment{{Clock: "45:05", Text: "Wicket: P Smith 3, 7/1 (2.1 ov)", URL: "https://youtu.be/abc?t=2705"}}}
	page := Render(Data{Innings: []Innings{inn}})
	for _, want := range []string{
		`<a href="https://youtu.be/abc?t=2705">7-1 (P Smith)</a>`,
		"9-2 (Jones)",
		`<a href="https://youtu.be/abc?t=2705"><span class=clock>&#9654; 45:05</span> Wicket: P Smith 3, 7/1 (2.1 ov)</a>`,
		"Watch it back", ".moments {"} {
		if !strings.Contains(page, want) {
			t.Errorf("page has no %q", want)
		}
	}
	if strings.Contains(page, `9-2 (Jones)</a>`) {
		t.Error("a wicket with no moment shouldn't be a link")
	}
}

// What's typed by a scorer, and an address from an API, can't break out of the link.
func TestMomentsAreEscaped(t *testing.T) {
	inn := Innings{FOW: []FOW{{Score: "1-1", Batter: "A", URL: `https://youtu.be/x" onclick="evil`}},
		Moments: []Moment{{Clock: "<b>", Text: "<script>alert(1)</script>", URL: `https://youtu.be/x" onmouseover="evil`}}}
	page := Render(Data{Innings: []Innings{inn}})
	for _, bad := range []string{`" onclick="`, `" onmouseover="`, "<script>alert", "<b>"} {
		if strings.Contains(page, bad) {
			t.Errorf("page contains %q", bad)
		}
	}
}

// No video, or no moments: nothing of this on the page, styles included. That's what keeps
// such a page byte for byte the Python original's (TestDifferentialAgainstPython).
func TestNoMomentsLeavesNoTrace(t *testing.T) {
	page := Render(Data{Title: "Home v Away", Innings: innings})
	for _, absent := range []string{"Watch it back", ".moments", "class=clock", "<a href"} {
		if strings.Contains(page, absent) {
			t.Errorf("page contains %q", absent)
		}
	}
}

func TestReportMarkdownHeadingsAreDropped(t *testing.T) {
	page := Render(Data{Report: "# Headline\nFirst para\n\nSecond para"})
	if strings.Contains(page, "Headline") || !strings.Contains(page, "<p>First para</p>") {
		t.Fatal(page)
	}
}

func TestWormHasALegendAndOverNumbers(t *testing.T) {
	svg := wormSVG(map[string][]Point{"1": {{1, 5}, {2, 12}}, "2": {{1, 3}, {2, 20}}},
		[]string{"#111111", "#222222"}, []string{"Home CC", "Rival <CC>"})
	if strings.Count(svg, "<polyline") != 2 || !strings.Contains(svg, ">Home CC<") ||
		!strings.Contains(svg, "Rival &lt;CC&gt;") || !strings.Contains(svg, ">2</text>") {
		t.Fatal(svg)
	}
	if wormSVG(nil, []string{"#000"}, nil) != "" {
		t.Fatal("empty worm should render nothing")
	}
}

func TestOverLabelsThinOutForLongFormats(t *testing.T) {
	var pts []Point
	for o := 1; o <= 50; o++ {
		pts = append(pts, Point{o, o * 4})
	}
	svg := wormSVG(map[string][]Point{"1": pts}, []string{"#000"}, nil)
	if !strings.Contains(svg, ">50</text>") || strings.Contains(svg, ">3</text>") {
		t.Fatal(svg)
	}
}

// The full page must be byte-identical to the Python original's.
func TestDifferentialAgainstPython(t *testing.T) {
	upstream, _ := filepath.Abs("../../.upstream")
	if _, err := os.Stat(filepath.Join(upstream, "match_page.py")); err != nil {
		t.Skip("no upstream clone")
	}
	py, err := exec.LookPath("python")
	if err != nil {
		t.Skip("no python")
	}
	d := Data{Club: "Home CC", Title: "Home CC v Rival's <CC>", Date: "02 Oct 2026", Competition: "Div 1",
		Result: "Rival CC won by 5 wickets", Report: "# Head\nA fine chase & more.\n\nSecond 'para'",
		Innings: innings, Worm: map[string][]Point{"1": {{1, 5}, {2, 12}, {3, 33}}, "2": {{1, 7}, {2, 19}, {10, 73}}},
		Colours: []string{"#2f6db3", "#d9534f"}, CardPNGB64: "AAAA", CrestB64: "BBBB",
		SponsorsB64: []string{"CC", "DD"}, VideoURL: "https://youtu.be/x?a=1&b=2", Accent: "#2f6db3"}
	type pyInn struct {
		Team    string   `json:"team"`
		Total   string   `json:"total"`
		Batters []Batter `json:"batters"`
		Bowlers []Bowler `json:"bowlers"`
		FOW     []FOW    `json:"fow"`
	}
	var inns []pyInn
	for _, i := range d.Innings {
		inns = append(inns, pyInn{i.Team, i.Total, append([]Batter{}, i.Batters...), append([]Bowler{}, i.Bowlers...), append([]FOW{}, i.FOW...)})
	}
	in, _ := json.Marshal(map[string]any{"club": d.Club, "title": d.Title, "date": d.Date,
		"competition": d.Competition, "result": d.Result, "report": d.Report, "innings": inns,
		"worm": d.Worm, "colours": d.Colours, "card_png_b64": d.CardPNGB64, "crest_b64": d.CrestB64,
		"sponsors_b64": d.SponsorsB64, "video_url": d.VideoURL, "accent": d.Accent})
	cmd := exec.Command(py, "-I", "-c", `import sys, json; sys.path.insert(0, sys.argv[1]); import match_page
sys.stdout.buffer.write(match_page.render(json.loads(sys.stdin.buffer.read().decode("utf-8"))).encode("utf-8"))`, upstream)
	cmd.Stdin = bytes.NewReader(in)
	want, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := Render(d); got != string(want) {
		os.WriteFile(filepath.Join(t.TempDir(), "go.html"), []byte(got), 0o644)
		for i := range got {
			if i >= len(want) || got[i] != want[i] {
				t.Fatalf("differs at byte %d:\ngo: %q\npy: %q", i, got[max(0, i-80):min(len(got), i+80)], string(want[max(0, i-80):min(len(want), i+80)]))
			}
		}
		t.Fatal("python output is longer")
	}
}
