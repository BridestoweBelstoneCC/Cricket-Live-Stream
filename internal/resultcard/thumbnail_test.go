package resultcard

import (
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
)

func TestThumbnailIsYouTubeSizedJPEG(t *testing.T) {
	out := filepath.Join(t.TempDir(), "sub", "t.jpg")
	facts := Facts{Outcome: "win", Margin: "by 34 runs", Kicker: "Highlights", Competition: "Devon League A",
		Date: "31 Oct 2026", Sides: []Side{{Club: "Home CC", Score: "187/7", Ours: true, Won: true}, {Club: "Rivals CC", Score: "153"}}}
	if _, err := SaveThumbnail(facts, Look{ClubColour: "#1a5c2a"}, out); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	im, err := jpeg.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	if b := im.Bounds(); b.Dx() != TW || b.Dy() != TH {
		t.Fatal(b)
	}
	if st, _ := os.Stat(out); st.Size() > 2<<20 {
		t.Fatalf("%d bytes — over YouTube's 2 MB", st.Size())
	}
}

// Before a result (or with no facts at all) it still draws: two named sides, no result line.
func TestThumbnailWithNothingOptional(t *testing.T) {
	for _, f := range []Facts{{}, {Sides: []Side{{Club: "The Extraordinarily Long Named Cricket And Social Club Of Somewhere", ClubID: "nope"}}}} {
		if im := Thumbnail(f, Look{ClubColour: "nonsense", LogosDir: "/nope"}); im.Bounds().Dx() != TW {
			t.Fatal(im.Bounds())
		}
	}
}
