// Package logobg makes a sponsor logo's background transparent.
//
// Most sponsor logos arrive as a picture on a plain white (or other solid) background,
// which shows as a hard box on the stream's dark sponsor strap. This removes that
// background — not with any clever image recognition, but the way a person would with a
// "magic wand" from the edges:
//
//  1. Work out the background colour from the image's border.
//  2. Find every pixel of roughly that colour that's CONNECTED to the border, and make it
//     transparent. White inside the logo (the hole in an "o", a badge's white middle)
//     stays, because it isn't reachable from the edge without crossing the logo itself.
//  3. Soften the cut edge by a pixel and take the old background's tint out of the edge
//     pixels, so a white logo background doesn't leave a pale halo on a dark strap.
//
// It refuses, rather than guess, when there's no plain background to find (a photo, a
// gradient, a logo that's already transparent) or when it would remove nearly
// everything. It never edits the original: the caller saves the result as a new image.
package logobg

import (
	"image"
	"image/color"
	"math"
	"sort"

	xdraw "golang.org/x/image/draw"
)

const (
	MaxSide     = 1200 // bigger is pointless (the strap shows logos at most 480x170) and slow
	Tolerance   = 48   // how far from the background colour still counts as background
	PlainBorder = 0.80 // share of the border that must be background-coloured
	MaxRemoved  = 0.97 // refusing beyond this: it's eaten the logo, not just its background
)

// Modes are the two ways to choose what counts as background (see RemoveBackground).
var Modes = []string{"outside", "everywhere"}

// ToNRGBA converts any image to non-premultiplied RGBA at the origin (Pillow's "RGBA").
func ToNRGBA(src image.Image) *image.NRGBA {
	if n, ok := src.(*image.NRGBA); ok && n.Rect.Min == (image.Point{}) {
		return n
	}
	b := src.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			out.Set(x, y, color.NRGBAModel.Convert(src.At(b.Min.X+x, b.Min.Y+y)))
		}
	}
	return out
}

// Thumbnail shrinks an image to fit within max×max, keeping its aspect (Pillow's
// thumbnail()); a smaller image is returned as is.
func Thumbnail(im *image.NRGBA, maxW, maxH int) *image.NRGBA {
	w, h := im.Rect.Dx(), im.Rect.Dy()
	if w <= maxW && h <= maxH {
		return im
	}
	scale := math.Min(float64(maxW)/float64(w), float64(maxH)/float64(h))
	nw, nh := max(1, int(math.Round(float64(w)*scale))), max(1, int(math.Round(float64(h)*scale)))
	out := image.NewNRGBA(image.Rect(0, 0, nw, nh))
	xdraw.CatmullRom.Scale(out, out.Rect, im, im.Rect, xdraw.Src, nil)
	return out
}

func borderPoints(w, h int) []image.Point {
	var pts []image.Point
	for x := 0; x < w; x++ {
		pts = append(pts, image.Pt(x, 0))
	}
	for x := 0; x < w; x++ {
		pts = append(pts, image.Pt(x, h-1))
	}
	for y := 1; y < h-1; y++ {
		pts = append(pts, image.Pt(0, y))
	}
	for y := 1; y < h-1; y++ {
		pts = append(pts, image.Pt(w-1, y))
	}
	return pts
}

func absDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

func dist(c color.NRGBA, bg [3]uint8) int {
	return max(absDiff(c.R, bg[0]), absDiff(c.G, bg[1]), absDiff(c.B, bg[2]))
}

// BackgroundColour returns the background colour for an image whose border is a plain
// colour, else ok=false and the reason.
func BackgroundColour(im *image.NRGBA) (bg [3]uint8, reason string, ok bool) {
	w, h := im.Rect.Dx(), im.Rect.Dy()
	pts := borderPoints(w, h)
	var opaque []color.NRGBA
	for _, p := range pts {
		if c := im.NRGBAAt(p.X, p.Y); c.A > 200 {
			opaque = append(opaque, c)
		}
	}
	if float64(len(opaque)) < 0.5*float64(len(pts)) {
		return bg, "This logo's background is already transparent.", false
	}
	// Per-channel median of the border: robust to a few pixels of logo touching the edge.
	ch := make([]int, len(opaque))
	for c := 0; c < 3; c++ {
		for i, p := range opaque {
			ch[i] = int([]uint8{p.R, p.G, p.B}[c])
		}
		sort.Ints(ch)
		bg[c] = uint8(ch[len(ch)/2])
	}
	near := 0
	for _, p := range opaque {
		if dist(p, bg) <= Tolerance {
			near++
		}
	}
	if float64(near)/float64(len(opaque)) < PlainBorder {
		return bg, "There's no plain background around the edge to remove (a photo or a " +
			"gradient needs a proper image editor).", false
	}
	return bg, "", true
}

// maxFilter5 is Pillow's ImageFilter.MaxFilter(5) on a single channel.
func maxFilter5(src []uint8, w, h int) []uint8 {
	// Separable: rows then columns.
	tmp := make([]uint8, len(src))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m := uint8(0)
			for dx := -2; dx <= 2; dx++ {
				if xx := x + dx; xx >= 0 && xx < w && src[y*w+xx] > m {
					m = src[y*w+xx]
				}
			}
			tmp[y*w+x] = m
		}
	}
	out := make([]uint8, len(src))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m := uint8(0)
			for dy := -2; dy <= 2; dy++ {
				if yy := y + dy; yy >= 0 && yy < h && tmp[yy*w+x] > m {
					m = tmp[yy*w+x]
				}
			}
			out[y*w+x] = m
		}
	}
	return out
}

// mulDiv255 is Pillow's rounding multiply (ImageChops.multiply).
func mulDiv255(a, b uint8) uint8 {
	t := int(a)*int(b) + 128
	return uint8(((t >> 8) + t) >> 8)
}

// RemoveBackground returns the new image and the share removed, or nil and the reason.
//
// mode "outside": only background connected to the image's edge. Keeps white INSIDE the
// logo — right for a badge with a white middle (verified on a real sponsor's round
// badge), wrong for a wordmark, whose letter holes ("o", "B", "A") stay white.
// mode "everywhere": every background-coloured pixel, letter holes included — right for
// a wordmark, wrong for a badge (it would clear the badge's middle too). No single rule
// gets both, so the panel shows both and the operator picks by eye.
func RemoveBackground(src image.Image, mode string) (*image.NRGBA, float64, string) {
	im := Thumbnail(ToNRGBA(src), MaxSide, MaxSide)
	bg, why, ok := BackgroundColour(im)
	if !ok {
		return nil, 0, why
	}
	w, h := im.Rect.Dx(), im.Rect.Dy()

	// Distance from the background colour, per pixel: the largest channel difference.
	d := make([]uint8, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			d[y*w+x] = uint8(dist(im.NRGBAAt(x, y), bg))
		}
	}
	// Candidates are background-coloured; only those connected to the border are removed.
	region := make([]uint8, w*h)
	for i, v := range d {
		if v <= Tolerance {
			region[i] = 255
		}
	}
	if mode != "everywhere" {
		filled := make([]bool, w*h)
		var stack []int
		for _, p := range borderPoints(w, h) {
			if i := p.Y*w + p.X; region[i] == 255 && !filled[i] {
				filled[i] = true
				stack = append(stack, i)
			}
		}
		for len(stack) > 0 { // 4-connected, like ImageDraw.floodfill
			i := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			x, y := i%w, i/w
			for _, n := range [4][2]int{{x + 1, y}, {x - 1, y}, {x, y + 1}, {x, y - 1}} {
				if n[0] < 0 || n[0] >= w || n[1] < 0 || n[1] >= h {
					continue
				}
				if j := n[1]*w + n[0]; region[j] == 255 && !filled[j] {
					filled[j] = true
					stack = append(stack, j)
				}
			}
		}
		for i := range region {
			region[i] = 0
			if filled[i] {
				region[i] = 255
			}
		}
	}
	count := 0
	for _, v := range region {
		if v == 255 {
			count++
		}
	}
	removed := float64(count) / float64(w*h)
	if removed > MaxRemoved {
		return nil, removed, "That would remove almost the whole image — the logo seems to " +
			"be the same colour as its background."
	}
	if removed == 0 {
		return nil, 0, "Couldn't find any background to remove."
	}

	// Soft edge. The removed region is fully transparent. In a thin band of logo pixels
	// around it, an anti-aliased pixel is part logo, part background: its transparency is
	// judged from how close its colour is to the background (just past the tolerance:
	// nearly clear; twice the tolerance or more: solid). Not a blur of the cut-out: that
	// also gave the pure background just OUTSIDE the edge some opacity — a faint white
	// halo on the dark strap. The band is only a couple of pixels wide, so white well
	// inside a logo (a badge's middle) is never touched.
	//
	// "How close to the background" is measured against the logo colour right beside the
	// pixel (the strongest colour within 2px), not a fixed scale: a pixel halfway from
	// white to that local colour is half transparent. A fixed scale counted strong
	// colours (gold on white) as solid too early and left a tint on their edges.
	grown := maxFilter5(region, w, h)
	local := maxFilter5(d, w, h)
	out := image.NewNRGBA(im.Rect)
	copy(out.Pix, im.Pix)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			keep := 255 - region[i]
			if grown[i] > region[i] { // in the band
				// Un-matte the part-transparent edge as each pixel's opacity is set: take
				// the background's share back out of its colour, so half-gold-half-white
				// becomes half-transparent gold.
				f := 1.0
				if reach := local[i]; reach > Tolerance {
					f = math.Min(1, float64(d[i])/float64(reach))
				}
				keep = uint8(math.RoundToEven(f * 255))
				if f > 0 && f < 1 {
					c := im.NRGBAAt(x, y)
					un := func(v, b uint8) uint8 {
						r := math.RoundToEven((float64(v) - (1-f)*float64(b)) / f)
						return uint8(math.Max(0, math.Min(255, r)))
					}
					out.SetNRGBA(x, y, color.NRGBA{un(c.R, bg[0]), un(c.G, bg[1]), un(c.B, bg[2]), c.A})
				}
			}
			o := out.Pix[out.PixOffset(x, y):]
			o[3] = mulDiv255(im.Pix[im.PixOffset(x, y)+3], keep)
		}
	}
	return out, removed, ""
}

// HasPlainBackground is the quick check for the panel's "make it transparent?"
// suggestion after an upload.
func HasPlainBackground(im image.Image) bool {
	defer func() { recover() }()
	_, _, ok := BackgroundColour(ToNRGBA(im))
	return ok
}
