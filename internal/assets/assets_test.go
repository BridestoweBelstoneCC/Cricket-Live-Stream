package assets

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func withDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	saved := Dir()
	SetDir(d)
	t.Cleanup(func() { SetDir(saved) })
	return d
}

// Everything the app serves or installs is built in: one download is the whole install.
func TestEveryAssetTheAppNeedsIsBuiltIn(t *testing.T) {
	for _, name := range []string{"overlay.html", "control.html", "scoring.html", "scoreboard.template",
		"config.example.ini", "match_state.example.json", "brand/favicon-64.png", "graphics/instant_replay.png",
		"fonts/BarlowCondensed-Black.ttf", "fonts/BarlowCondensed-Bold.ttf", "fonts/BarlowCondensed-ExtraBold.ttf",
		"fonts/BarlowCondensed-Medium.ttf", "fonts/BarlowCondensed-SemiBold.ttf"} {
		b, err := Embedded(name)
		if err != nil || len(b) == 0 {
			t.Errorf("%s not built in: %v", name, err)
		}
	}
}

// The built-in copies are the repository's files, byte for byte (an edit that didn't
// make it into the build would otherwise ship the old page).
func TestBuiltInCopiesMatchTheRepository(t *testing.T) {
	for _, name := range []string{"overlay.html", "control.html", "scoring.html", "scoreboard.template"} {
		want, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Fatal(err)
		}
		got, _ := Embedded(name)
		if !bytes.Equal(got, want) {
			t.Errorf("%s: built-in copy differs from the repository's", name)
		}
	}
}

func TestAFileBesideTheAppWinsAndGoingBackIsInstant(t *testing.T) {
	d := withDir(t)
	builtIn, _ := Embedded("overlay.html")
	if b, _ := Read("overlay.html"); !bytes.Equal(b, builtIn) {
		t.Fatal("no override: should read the built-in copy")
	}
	os.WriteFile(filepath.Join(d, "overlay.html"), []byte("edited"), 0o644)
	if b, _ := Read("overlay.html"); string(b) != "edited" {
		t.Fatalf("override not used: %.40q", b)
	}
	os.MkdirAll(filepath.Join(d, "fonts"), 0o755)
	os.WriteFile(filepath.Join(d, "fonts", "BarlowCondensed-Black.ttf"), []byte("font"), 0o644)
	if b, _ := Read("fonts/BarlowCondensed-Black.ttf"); string(b) != "font" {
		t.Fatal("nested override not used")
	}
	os.Remove(filepath.Join(d, "overlay.html"))
	if b, _ := Read("overlay.html"); !bytes.Equal(b, builtIn) {
		t.Fatal("removing the override didn't go back to the built-in copy")
	}
	if _, err := Read("no-such-file.html"); err == nil {
		t.Fatal("a missing asset read without error")
	}
}

// Path gives external tools (ffmpeg) a real file: the override if there is one, else an
// extract of the built-in copy, keyed by content so an upgrade never uses a stale one.
func TestPathExtractsTheBuiltInCopyOnce(t *testing.T) {
	d := withDir(t)
	t.Setenv("LOCALAPPDATA", t.TempDir()) // os.UserCacheDir on Windows
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	p, err := Path("fonts/BarlowCondensed-Bold.ttf")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	want, _ := Embedded("fonts/BarlowCondensed-Bold.ttf")
	if !bytes.Equal(got, want) {
		t.Fatal("extract differs")
	}
	if p2, _ := Path("fonts/BarlowCondensed-Bold.ttf"); p2 != p {
		t.Fatal("extracted twice")
	}
	over := filepath.Join(d, "fonts", "BarlowCondensed-Bold.ttf")
	os.MkdirAll(filepath.Dir(over), 0o755)
	os.WriteFile(over, []byte("x"), 0o644)
	if p3, _ := Path("fonts/BarlowCondensed-Bold.ttf"); p3 != over {
		t.Fatalf("override not preferred: %s", p3)
	}
}

func TestJoinIsInsideTheAppFolder(t *testing.T) {
	d := withDir(t)
	if Join("config.ini") != filepath.Join(d, "config.ini") {
		t.Fatal(Join("config.ini"))
	}
}
