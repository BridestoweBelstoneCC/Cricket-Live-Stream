package server

// The sponsor-logo "make the background transparent" routes and the plain-background hint
// on upload (the route half of tests/test_logo_bg.py; the image work is in internal/logobg).

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cricketstream/internal/logobg"
)

// ringPNG is a gold "o" on white: its middle is a letter hole, enclosed by the letter.
func ringPNG(t *testing.T) []byte {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, 200, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 200; x++ {
			d := math.Hypot(float64(x)-100, float64(y)-100)
			c := color.RGBA{255, 255, 255, 255}
			if d >= 42 && d <= 60 {
				c = color.RGBA{230, 180, 40, 255}
			}
			im.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func newBgServer(t *testing.T) (*testServer, []byte) {
	s := newTestServer(t, "")
	os.MkdirAll(SponsorDir, 0o755)
	orig := ringPNG(t)
	os.WriteFile(filepath.Join(SponsorDir, "1.png"), orig, 0o644)
	setState(t, M{"sponsor_id": "1"})
	return s, orig
}

func TestHasPlainBackground(t *testing.T) {
	im, _ := png.Decode(bytes.NewReader(ringPNG(t)))
	if !logobg.HasPlainBackground(im) {
		t.Error("a logo on white has a plain background")
	}
	grad := image.NewGray(image.Rect(0, 0, 256, 256))
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			grad.SetGray(x, y, color.Gray{uint8(y)})
		}
	}
	if logobg.HasPlainBackground(grad) {
		t.Error("a gradient has no plain background")
	}
}

func TestBgPreviewIsATransparentPNGAndSavesNothing(t *testing.T) {
	s, _ := newBgServer(t)
	r := s.get("/sponsor/bg_preview?id=1&mode=everywhere")
	mustEq(t, r.status, 200)
	if ct := r.header.Get("Content-Type"); ct != "image/png" {
		t.Fatalf("content type %q", ct)
	}
	im, err := png.Decode(bytes.NewReader(r.body))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, a := im.At(100, 100).RGBA(); a != 0 {
		t.Errorf("letter hole not cleared: alpha %d", a)
	}
	eqv(t, sponsorFiles(), []string{"1.png"})
}

func TestRemoveBackgroundSavesANewImageAndKeepsTheOriginal(t *testing.T) {
	s, orig := newBgServer(t)
	st, d := s.postJSON("/sponsor/remove_background", M{"id": "1", "mode": "outside"}, map[string]string{"Origin": "http://" + s.addr})
	mustEq(t, st, 200)
	mustEq(t, str(d, "sponsor_id", ""), "2")
	mustEq(t, str(d, "previous_id", ""), "1")
	mustEq(t, gs(LoadState(), "sponsor_id", ""), "2")
	b, _ := os.ReadFile(filepath.Join(SponsorDir, "1.png"))
	if !bytes.Equal(b, orig) {
		t.Error("the original changed — Undo would lose it")
	}
	f, err := os.Open(filepath.Join(SponsorDir, "2.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	im, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := im.(*image.NRGBA); !ok {
		if _, ok := im.(*image.RGBA); !ok {
			t.Errorf("saved copy has no alpha channel: %T", im)
		}
	}
}

func TestRemoveBackgroundBadModeAndMissingImage(t *testing.T) {
	s, _ := newBgServer(t)
	h := map[string]string{"Origin": "http://" + s.addr}
	st, _ := s.postJSON("/sponsor/remove_background", M{"id": "1", "mode": "x"}, h)
	mustEq(t, st, 400)
	st, _ = s.postJSON("/sponsor/remove_background", M{"id": "9", "mode": "outside"}, h)
	mustEq(t, st, 400)
}

func TestSponsorIDsCantReachOutsideTheFolder(t *testing.T) {
	newBgServer(t)
	secret := filepath.Join(filepath.Dir(SponsorDir), "secret.png")
	os.WriteFile(secret, ringPNG(t), 0o644)
	for _, bad := range []string{"../secret", `..\secret`, "/secret", "C:secret"} {
		if p := findSponsorImage(bad); p != "" {
			t.Errorf("%q reached %s", bad, p)
		}
	}
}

func TestUploadSaysWhenThereIsAPlainBackground(t *testing.T) {
	s, _ := newBgServer(t)
	st, d := s.upload(ringPNG(t), "", "image/png")
	mustEq(t, st, 200)
	if d["plain_background"] != true {
		t.Errorf("plain_background = %v", d["plain_background"])
	}
}

// The panel's CSP is img-src 'self' data: — a blob: preview is silently refused and the
// operator gets two empty boxes (found in a real browser).
func TestBgChooserPreviewsUseDataURLs(t *testing.T) {
	html := page(t, "control.html")
	i, j := strings.Index(html, "async function openBgChooser("), strings.Index(html, "async function pickBg(")
	if i < 0 || j < i {
		t.Fatal("openBgChooser/pickBg not found")
	}
	fn := html[i:j]
	if strings.Contains(fn, "URL.createObjectURL(") || !strings.Contains(fn, "readAsDataURL") {
		t.Error("previews must be data: URLs")
	}
	s := newTestServer(t, "")
	if csp := s.get("/control").header.Get("Content-Security-Policy"); !strings.Contains(csp, "img-src 'self' data:;") {
		t.Errorf("panel CSP %q", csp)
	}
}

func TestBgPreviewNeedsALogin(t *testing.T) {
	s := newTestServer(t, "secret")
	os.MkdirAll(SponsorDir, 0o755)
	os.WriteFile(filepath.Join(SponsorDir, "1.png"), ringPNG(t), 0o644)
	mustEq(t, s.get("/sponsor/bg_preview?id=1&mode=outside").status, 401)
}
