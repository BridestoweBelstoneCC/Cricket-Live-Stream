package resultcard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/png"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"cricketstream/internal/assets"
)

func TestHighlightReadsOnThePanelForAnyClubColour(t *testing.T) {
	for _, hex := range []string{"#1a3a5c", "#000000", "#ffffff", "#ffd700", "#b52d6e", "#0b6623"} {
		p := NewPalette(hex)
		if Contrast(p.Hi, p.BG) < 4.5 || Contrast(p.White, p.BG) < 7 {
			t.Fatalf("%s: hi %.2f white %.2f", hex, Contrast(p.Hi, p.BG), Contrast(p.White, p.BG))
		}
	}
}

func TestNavyStaysBlue(t *testing.T) {
	hi := NewPalette("#1a3a5c").Hi
	if int(hi[2]) <= int(hi[0])+60 { // not lifted to steel grey
		t.Fatal(hi)
	}
}

func decodeSize(t *testing.T, path string) (int, int) {
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	im, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return im.Bounds().Dx(), im.Bounds().Dy()
}

func TestRendersWithNothingOptional(t *testing.T) {
	out := filepath.Join(t.TempDir(), "c.png")
	if _, err := Render(Facts{Outcome: "win", Margin: "by 1 run"},
		Look{ClubColour: "not a colour", Crest: "/nope.png", Photo: "/nope.jpg", Sponsors: []string{"/nope.png"}}, out); err != nil {
		t.Fatal(err)
	}
	if w, h := decodeSize(t, out); w != 1080 || h != 1350 {
		t.Fatal(w, h)
	}
}

func TestLongNamesAndUnknownBadges(t *testing.T) {
	tmp := t.TempDir()
	side := Side{Club: "The Extraordinarily Long Named Cricket And Social Club Of Somewhere",
		Team: "Sunday Development XI", Score: "999-9", Overs: "All out · 49.5 overs", ClubID: "nope", Won: true}
	lost := side
	lost.Won = false
	out := filepath.Join(tmp, "c.png")
	if _, err := Render(Facts{Outcome: "loss", Margin: "by 999 wickets", Sides: []Side{side, lost},
		Stars: []Star{{Label: "Top score", Big: "150*", Small: "(99)", Name: "A VERY-LONG-SURNAME-INDEED"}}},
		Look{ClubColour: "#ffd700", LogosDir: tmp}, out); err != nil {
		t.Fatal(err)
	}
	if w, h := decodeSize(t, out); w != 1080 || h != 1350 {
		t.Fatal(w, h)
	}
}

func TestFontsAreBuiltIn(t *testing.T) {
	for _, w := range []string{"Black", "ExtraBold", "Bold", "SemiBold", "Medium"} {
		if _, err := assets.Embedded("fonts/BarlowCondensed-" + w + ".ttf"); err != nil {
			t.Fatal(w, err)
		}
	}
	if _, err := assets.Embedded("fonts/OFL.txt"); err != nil {
		t.Fatal(err)
	}
}

func TestFitShrinksThenEllipsises(t *testing.T) {
	s, size := fit("SHORT", "xbold", 46, 30, 400)
	if s != "SHORT" || size != 46 {
		t.Fatal(s, size)
	}
	s, size = fit("THE EXTRAORDINARILY LONG NAMED CRICKET CLUB", "xbold", 46, 30, 200)
	if size != 30 || []rune(s)[len([]rune(s))-1] != '…' || textlength(s, face(30, "xbold")) > 200 {
		t.Fatal(s, size)
	}
}

// The palette also sets the match page's accent, so it must equal Python's exactly.
func TestPaletteMatchesPython(t *testing.T) {
	upstream, _ := filepath.Abs("../../.upstream")
	if _, err := os.Stat(filepath.Join(upstream, "result_card.py")); err != nil {
		t.Skip("no upstream clone")
	}
	py, err := exec.LookPath("python")
	if err != nil || exec.Command(py, "-c", "import PIL").Run() != nil {
		t.Skip("no python with Pillow")
	}
	rng := rand.New(rand.NewSource(11))
	hexes := []string{"#1a3a5c", "#000000", "#ffffff", "#ffd700", "zz", "#abc", "#12345", ""}
	for i := 0; i < 300; i++ {
		hexes = append(hexes, fmt.Sprintf("#%06x", rng.Intn(1<<24)))
	}
	in, _ := json.Marshal(hexes)
	cmd := exec.Command(py, "-I", "-c", `import json, sys; sys.path.insert(0, sys.argv[1]); import result_card as rc
out = []
for h in json.loads(sys.stdin.buffer.read().decode()):
    p = rc.palette(h)
    out.append([list(p[k]) for k in ("club", "bg", "hi", "muted", "faint")])
json.dump(out, sys.stdout)`, upstream)
	cmd.Stdin = bytes.NewReader(in)
	raw, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var want [][][3]uint8
	json.Unmarshal(raw, &want)
	for i, h := range hexes {
		p := NewPalette(h)
		got := [][3]uint8{p.Club, p.BG, p.Hi, p.Muted, p.Faint}
		for k := range got {
			if got[k] != want[i][k] {
				t.Fatalf("%q field %d: go %v, python %v", h, k, got[k], want[i][k])
			}
		}
	}
}
