package resultcard

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestVisualCompare renders the same card with the Go port and the Python original
// into $CARD_COMPARE_DIR for checking by eye. Only runs when that variable is set.
func TestVisualCompare(t *testing.T) {
	dir := os.Getenv("CARD_COMPARE_DIR")
	if dir == "" {
		t.Skip("set CARD_COMPARE_DIR to render the comparison")
	}
	root, _ := filepath.Abs("../..")
	facts := Facts{Outcome: "win", Margin: "by 46 runs", Kicker: "", Competition: "Devon League · A Division",
		Date: "25 Jul 2026", Ground: "The Ground",
		Sides: []Side{{Club: "Home & District CC", Team: "1st XI", Score: "231-7", Overs: "40 overs", ClubID: "111", Won: true, Ours: true},
			{Club: "Rivals CC", Team: "1st XI", Score: "185", Overs: "All out · 36.2 overs", ClubID: "222"}},
		Stars: []Star{{Label: "Top score", Big: "78", Small: "(96)", Name: "P SMITH"}, {Label: "Best bowling", Big: "3-25", Small: "10 ov", Name: "S QUICK"}}}
	for _, variant := range []struct {
		name  string
		photo string
	}{{"photo", filepath.Join(root, "docs", "trailer-poster.jpg")}, {"nophoto", ""}} {
		look := Look{ClubColour: "#1a3a5c", Crest: filepath.Join(root, "brand", "icon-512.png"), Photo: variant.photo,
			ClubName: "Home & District CC", Sponsors: []string{filepath.Join(root, "brand", "logo-light-bg.png")}}
		if _, err := Render(facts, look, filepath.Join(dir, variant.name+"_go.png")); err != nil {
			t.Fatal(err)
		}
		script := `import sys; sys.path.insert(0, sys.argv[1]); import result_card as rc
facts = {"outcome": "win", "margin": "by 46 runs", "competition": "Devon League · A Division",
         "date": "25 Jul 2026", "ground": "The Ground",
         "sides": [{"club": "Home & District CC", "team": "1st XI", "score": "231-7", "overs": "40 overs", "club_id": "111", "won": True, "ours": True},
                   {"club": "Rivals CC", "team": "1st XI", "score": "185", "overs": "All out · 36.2 overs", "club_id": "222"}],
         "stars": [{"label": "Top score", "big": "78", "small": "(96)", "name": "P SMITH"},
                   {"label": "Best bowling", "big": "3-25", "small": "10 ov", "name": "S QUICK"}]}
look = {"club_colour": "#1a3a5c", "crest": sys.argv[2], "photo": sys.argv[3] or None,
        "club_name": "Home & District CC", "sponsors": [sys.argv[4]]}
rc.render(facts, look, sys.argv[5])`
		cmd := exec.Command("python", "-I", "-c", script, filepath.Join(root, ".upstream"), look.Crest, variant.photo,
			look.Sponsors[0], filepath.Join(dir, variant.name+"_py.png"))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	}
}
