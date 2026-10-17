package logobg

// Synthetic images stand in for the two kinds of real logo this was built against (a
// club's wordmark and round badge, both on white): a ring whose middle is a "letter
// hole", and a badge whose white middle must survive.

import (
	"image"
	"image/color"
	"math"
	"strings"
	"testing"

	xdraw "golang.org/x/image/draw"
)

var (
	white = color.NRGBA{255, 255, 255, 255}
	gold  = color.NRGBA{230, 180, 40, 255}
	black = color.NRGBA{0, 0, 0, 255}
)

func canvas(w, h int, c color.NRGBA) *image.NRGBA {
	im := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(im.Pix); i += 4 {
		im.Pix[i], im.Pix[i+1], im.Pix[i+2], im.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	return im
}

// ellipse paints an ellipse inside the box: filled, or a ring `width` pixels thick.
func ellipse(im *image.NRGBA, x0, y0, x1, y1, width float64, c color.NRGBA) {
	cx, cy, rx, ry := (x0+x1)/2, (y0+y1)/2, (x1-x0)/2, (y1-y0)/2
	for y := int(y0); y <= int(y1); y++ {
		for x := int(x0); x <= int(x1); x++ {
			dx, dy := (float64(x)+0.5-cx)/rx, (float64(y)+0.5-cy)/ry
			if dx*dx+dy*dy > 1 {
				continue
			}
			if width > 0 {
				ix, iy := (float64(x)+0.5-cx)/(rx-width), (float64(y)+0.5-cy)/(ry-width)
				if ix*ix+iy*iy < 1 {
					continue
				}
			}
			im.SetNRGBA(x, y, c)
		}
	}
}

func ring(size int) *image.NRGBA {
	im := canvas(size, size, white)
	s := float64(size) / 200
	ellipse(im, 40*s, 40*s, 160*s, 160*s, math.Max(1, math.Round(18*s)), gold)
	return im
}

func badge() *image.NRGBA {
	im := canvas(200, 200, white)
	ellipse(im, 10, 10, 190, 190, 4, black)
	for y := 90; y <= 110; y++ {
		for x := 70; x <= 130; x++ {
			im.SetNRGBA(x, y, black)
		}
	}
	return im
}

func alphaAt(im *image.NRGBA, x, y int) uint8 { return im.NRGBAAt(x, y).A }

func TestOutsideKeepsWhiteInsideTheLogo(t *testing.T) {
	out, _, why := RemoveBackground(ring(200), "outside")
	if why != "" {
		t.Fatal(why)
	}
	if alphaAt(out, 5, 5) != 0 || alphaAt(out, 100, 100) != 255 || alphaAt(out, 100, 49) != 255 {
		t.Fatal(alphaAt(out, 5, 5), alphaAt(out, 100, 100), alphaAt(out, 100, 49))
	}
}

func TestEverywhereClearsLetterHoles(t *testing.T) {
	out, _, _ := RemoveBackground(ring(200), "everywhere")
	if alphaAt(out, 5, 5) != 0 || alphaAt(out, 100, 100) != 0 || alphaAt(out, 100, 49) != 255 {
		t.Fatal(alphaAt(out, 5, 5), alphaAt(out, 100, 100), alphaAt(out, 100, 49))
	}
}

func TestBadgeSurvivesOutsideMode(t *testing.T) {
	out, _, _ := RemoveBackground(badge(), "outside")
	if alphaAt(out, 3, 3) != 0 || alphaAt(out, 100, 60) != 255 || out.NRGBAAt(100, 60) != white {
		t.Fatal("badge middle lost")
	}
}

// A soft gold edge on white: the half-transparent edge pixels must come out gold, not
// whitish — that's the halo that would show on the dark strap.
func TestNoPaleFringe(t *testing.T) {
	big := canvas(120, 120, white)
	ellipse(big, 20, 20, 100, 100, 0, gold)
	im := image.NewNRGBA(image.Rect(0, 0, 60, 60))
	xdraw.CatmullRom.Scale(im, im.Rect, big, big.Rect, xdraw.Src, nil) // anti-aliased edges
	out, _, _ := RemoveBackground(im, "outside")
	edges := 0
	for i := 0; i < len(out.Pix); i += 4 {
		if a := out.Pix[i+3]; a > 0 && a < 255 {
			edges++
			if out.Pix[i+2] >= 150 {
				t.Fatalf("edge pixel kept the white background's tint: %v", out.Pix[i:i+4])
			}
		}
	}
	if edges == 0 {
		t.Fatal("no soft edge")
	}
}

func TestOriginalAlphaRespected(t *testing.T) {
	im := ring(200)
	im.SetNRGBA(100, 49, color.NRGBA{230, 180, 40, 100}) // already part-transparent
	out, _, _ := RemoveBackground(im, "outside")
	if alphaAt(out, 100, 49) > 100 {
		t.Fatal(alphaAt(out, 100, 49))
	}
}

func TestLargeImagesAreScaledDown(t *testing.T) {
	out, _, _ := RemoveBackground(ring(2400), "outside")
	if b := out.Rect; max(b.Dx(), b.Dy()) > MaxSide {
		t.Fatal(b)
	}
}

func TestAlreadyTransparent(t *testing.T) {
	im := canvas(100, 100, color.NRGBA{})
	for y := 30; y <= 70; y++ {
		for x := 30; x <= 70; x++ {
			im.SetNRGBA(x, y, gold)
		}
	}
	if out, _, why := RemoveBackground(im, "outside"); out != nil || !strings.Contains(why, "already transparent") {
		t.Fatal(why)
	}
}

func TestPhotoOrGradient(t *testing.T) {
	im := image.NewGray(image.Rect(0, 0, 256, 256)) // Pillow's linear_gradient("L")
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			im.SetGray(x, y, color.Gray{uint8(y)})
		}
	}
	if out, _, why := RemoveBackground(im, "outside"); out != nil || !strings.Contains(why, "no plain background") {
		t.Fatal(why)
	}
}

func TestLogoSameColourAsItsBackground(t *testing.T) {
	if out, _, why := RemoveBackground(canvas(100, 100, white), "everywhere"); out != nil || !strings.Contains(why, "almost the whole image") {
		t.Fatal(why)
	}
}
