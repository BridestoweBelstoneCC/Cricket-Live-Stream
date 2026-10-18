package resultcard

// The handful of Pillow operations the card is built from — anti-aliased shapes, TTF
// text with Pillow's anchors, masked pastes, a Gaussian blur and the two ImageEnhance
// filters — on top of golang.org/x/image, so the app needs no image library beyond it.

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
	"sync"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
	_ "golang.org/x/image/webp"

	"cricketstream/internal/assets"
)

type rgb [3]uint8

func (c rgb) nrgba(a uint8) color.NRGBA { return color.NRGBA{c[0], c[1], c[2], a} }

// ── Fonts ───────────────────────────────────────────────────────────────────

var weights = map[string]string{"black": "Black", "xbold": "ExtraBold", "bold": "Bold",
	"semi": "SemiBold", "medium": "Medium"}

var (
	fontMu    sync.Mutex
	parsed    = map[string]*opentype.Font{}
	faceCache = map[[2]any]font.Face{}
)

// face is Barlow Condensed at a pixel size (Pillow's truetype(path, size)). The fonts
// are built into the app, so the card looks the same on every club's laptop.
func face(size int, weight string) font.Face {
	fontMu.Lock()
	defer fontMu.Unlock()
	key := [2]any{size, weight}
	if f, ok := faceCache[key]; ok {
		return f
	}
	name := "fonts/BarlowCondensed-" + weights[weight] + ".ttf"
	f, ok := parsed[name]
	if !ok {
		data, err := assets.Read(name)
		if err == nil {
			f, err = opentype.Parse(data)
		}
		if err != nil {
			panic("result card font " + name + ": " + err.Error()) // built in; can't be missing
		}
		parsed[name] = f
	}
	fc, err := opentype.NewFace(f, &opentype.FaceOptions{Size: float64(size), DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		panic(err)
	}
	faceCache[key] = fc
	return fc
}

// textlength is Pillow's ImageDraw.textlength: the advance width in pixels.
func textlength(s string, f font.Face) float64 {
	return float64(font.MeasureString(f, s)) / 64
}

// text draws s with a Pillow anchor: first letter l/m/r (horizontal), second s/m
// (baseline / middle of ascender..descender).
func text(dst draw.Image, x, y float64, s string, f font.Face, c color.Color, anchor string) {
	if s == "" {
		return
	}
	switch anchor[0] {
	case 'r':
		x -= textlength(s, f)
	case 'm':
		x -= textlength(s, f) / 2
	}
	if anchor[1] == 'm' {
		m := f.Metrics()
		y += float64(m.Ascent-m.Descent) / 64 / 2
	}
	d := font.Drawer{Dst: dst, Src: image.NewUniform(c), Face: f,
		Dot: fixed.Point26_6{X: fixed.Int26_6(math.Round(x * 64)), Y: fixed.Int26_6(math.Round(y * 64))}}
	d.DrawString(s)
}

// tracked is letter-spaced text (Pillow has no tracking). tracking is in ems; size is
// the font's pixel size. Returns the width.
func tracked(dst draw.Image, x, y float64, s string, f font.Face, size int, c color.Color, tracking float64, anchor string) float64 {
	runes := []rune(s)
	widths := make([]float64, len(runes))
	total := 0.0
	for i, r := range runes {
		widths[i] = textlength(string(r), f)
		total += widths[i]
	}
	extra := float64(size) * tracking
	total += extra * float64(max(len(runes)-1, 0))
	switch anchor[0] {
	case 'r':
		x -= total
	case 'm':
		x -= total / 2
	}
	a := "l" + anchor[1:]
	for i, r := range runes {
		text(dst, x, y, string(r), f, c, a)
		x += widths[i] + extra
	}
	return total
}

// ── Shapes ──────────────────────────────────────────────────────────────────

type rasterizer = *vector.Rasterizer

func fill(dst draw.Image, c color.Color, build func(z rasterizer)) {
	b := dst.Bounds()
	z := vector.NewRasterizer(b.Dx(), b.Dy())
	build(z)
	z.Draw(dst, b, image.NewUniform(c), image.Point{})
}

func poly(z *vector.Rasterizer, pts [][2]float64) {
	z.MoveTo(float32(pts[0][0]), float32(pts[0][1]))
	for _, p := range pts[1:] {
		z.LineTo(float32(p[0]), float32(p[1]))
	}
	z.ClosePath()
}

// rect is Pillow's rectangle([x0, y0, x1, y1]): both corners inclusive.
func rect(dst draw.Image, x0, y0, x1, y1 float64, c color.Color) {
	fill(dst, c, func(z *vector.Rasterizer) {
		poly(z, [][2]float64{{x0, y0}, {x1 + 1, y0}, {x1 + 1, y1 + 1}, {x0, y1 + 1}})
	})
}

func roundedRect(dst draw.Image, x0, y0, x1, y1, r float64, c color.Color) {
	x1, y1 = x1+1, y1+1
	r = math.Min(r, math.Min((x1-x0)/2, (y1-y0)/2))
	var pts [][2]float64
	corner := func(cx, cy, a0 float64) {
		for i := 0; i <= 8; i++ {
			a := a0 + float64(i)/8*math.Pi/2
			pts = append(pts, [2]float64{cx + r*math.Cos(a), cy + r*math.Sin(a)})
		}
	}
	corner(x1-r, y0+r, -math.Pi/2)
	corner(x1-r, y1-r, 0)
	corner(x0+r, y1-r, math.Pi/2)
	corner(x0+r, y0+r, math.Pi)
	fill(dst, c, func(z *vector.Rasterizer) { poly(z, pts) })
}

func ellipsePts(x0, y0, x1, y1 float64, reverse bool) [][2]float64 {
	cx, cy, rx, ry := (x0+x1)/2, (y0+y1)/2, (x1-x0)/2, (y1-y0)/2
	const n = 96
	pts := make([][2]float64, n)
	for i := range pts {
		a := float64(i) / n * 2 * math.Pi
		if reverse {
			a = -a
		}
		pts[i] = [2]float64{cx + rx*math.Cos(a), cy + ry*math.Sin(a)}
	}
	return pts
}

// ellipse fills the box, then strokes an outline `width` pixels thick inside its edge.
func ellipse(dst draw.Image, x0, y0, x1, y1 float64, fillC color.Color, outline color.Color, width float64) {
	x1, y1 = x1+1, y1+1
	if fillC != nil {
		fill(dst, fillC, func(z *vector.Rasterizer) { poly(z, ellipsePts(x0, y0, x1, y1, false)) })
	}
	if outline != nil && width > 0 {
		fill(dst, outline, func(z *vector.Rasterizer) {
			poly(z, ellipsePts(x0, y0, x1, y1, false))
			poly(z, ellipsePts(x0+width, y0+width, x1-width, y1-width, true))
		})
	}
}

func line(dst draw.Image, xa, ya, xb, yb, width float64, c color.Color) {
	dx, dy := xb-xa, yb-ya
	l := math.Hypot(dx, dy)
	if l == 0 {
		return
	}
	nx, ny := -dy/l*width/2, dx/l*width/2
	fill(dst, c, func(z *vector.Rasterizer) {
		poly(z, [][2]float64{{xa + nx, ya + ny}, {xb + nx, yb + ny}, {xb - nx, yb - ny}, {xa - nx, ya - ny}})
	})
}

// ── Images ──────────────────────────────────────────────────────────────────

func newRGB(w, h int, c rgb) *image.RGBA {
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(im, im.Rect, image.NewUniform(c.nrgba(255)), image.Point{}, draw.Src)
	return im
}

func toNRGBA(src image.Image) *image.NRGBA {
	b := src.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Rect, src, b.Min, draw.Src)
	return out
}

func resize(src image.Image, w, h int) *image.NRGBA {
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(out, out.Rect, src, src.Bounds(), xdraw.Src, nil)
	return out
}

// thumbnail is Pillow's thumbnail(): shrink to fit the box, never enlarge. Pillow
// rounds each side so the aspect ratio is kept as closely as possible.
func thumbnail(im *image.NRGBA, bw, bh int) *image.NRGBA {
	w, h := im.Rect.Dx(), im.Rect.Dy()
	if w <= bw && h <= bh {
		return im
	}
	s := math.Min(float64(bw)/float64(w), float64(bh)/float64(h))
	return resize(im, max(1, int(math.Round(float64(w)*s))), max(1, int(math.Round(float64(h)*s))))
}

func decodeFile(path string) (image.Image, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	im, _, err := image.Decode(bytes.NewReader(data))
	return im, err
}

// loadRGBA opens an image as RGBA, shrunk to fit the box; nil if it can't be read.
func loadRGBA(path string, bw, bh int) *image.NRGBA {
	if path == "" {
		return nil
	}
	im, err := decodeFile(path)
	if err != nil {
		return nil
	}
	return thumbnail(toNRGBA(im), bw, bh)
}

// paste puts src at (x, y) over dst, using src's own alpha (Pillow's
// paste(im, xy, im)), optionally scaled by an extra mask.
func paste(dst draw.Image, src image.Image, x, y int, mask image.Image) {
	r := src.Bounds().Sub(src.Bounds().Min).Add(image.Pt(x, y))
	if mask == nil {
		draw.Draw(dst, r, src, src.Bounds().Min, draw.Over)
		return
	}
	draw.DrawMask(dst, r, src, src.Bounds().Min, mask, mask.Bounds().Min, draw.Over)
}

// pasteAt fills the rectangle r of dst with src through mask (aligned to r.Min).
func pasteAt(dst draw.Image, src image.Image, r image.Rectangle, mask image.Image) {
	draw.DrawMask(dst, r, src, image.Point{}, mask, mask.Bounds().Min, draw.Over)
}

// scaleAlpha multiplies an image's alpha by k (Pillow's point(lambda v: int(v * k))).
func scaleAlpha(im *image.NRGBA, k float64) {
	for i := 3; i < len(im.Pix); i += 4 {
		im.Pix[i] = uint8(float64(im.Pix[i]) * k)
	}
}

// verticalAlpha is a w×h mask ramping through (position 0..1, alpha 0..255) stops,
// smoothstepped — every column the same.
func verticalAlpha(w, h int, stops [][2]float64) *image.Alpha {
	m := image.NewAlpha(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		t := float64(y) / float64(max(h-1, 1))
		var a uint8
		for i := 0; i+1 < len(stops); i++ {
			p0, a0, p1, a1 := stops[i][0], stops[i][1], stops[i+1][0], stops[i+1][1]
			if p0 <= t && t <= p1 {
				u := (t - p0) / math.Max(p1-p0, 1e-6)
				u = u * u * (3 - 2*u)
				a = uint8(int(a0 + (a1-a0)*u))
				break
			}
		}
		row := m.Pix[y*m.Stride : y*m.Stride+w]
		for x := range row {
			row[x] = a
		}
	}
	return m
}

// gaussianBlur blurs an alpha mask with standard deviation sigma (Pillow's
// GaussianBlur(radius)), approximated by three box blurs like Pillow itself.
func gaussianBlur(m *image.Alpha, sigma float64) *image.Alpha {
	w, h := m.Rect.Dx(), m.Rect.Dy()
	// Box sizes for 3 passes (Kovesi's method).
	n := 3.0
	wIdeal := math.Sqrt(12*sigma*sigma/n + 1)
	wl := int(math.Floor(wIdeal))
	if wl%2 == 0 {
		wl--
	}
	wu := wl + 2
	mIdeal := (12*sigma*sigma - n*float64(wl*wl) - 4*n*float64(wl) - 3*n) / (-4*float64(wl) - 4)
	mm := int(math.Round(mIdeal))
	src := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			src[y*w+x] = float64(m.Pix[y*m.Stride+x])
		}
	}
	tmp := make([]float64, w*h)
	box := func(in, out []float64, r int, horizontal bool) {
		lines, length := h, w
		if !horizontal {
			lines, length = w, h
		}
		idx := func(line, i int) int {
			if horizontal {
				return line*w + i
			}
			return i*w + line
		}
		for l := 0; l < lines; l++ {
			sum := 0.0
			at := func(i int) float64 {
				if i < 0 || i >= length {
					return 0 // Pillow pads with the edge colour; the shadow mask's edges are 0
				}
				return in[idx(l, i)]
			}
			for i := -r; i <= r; i++ {
				sum += at(i)
			}
			for i := 0; i < length; i++ {
				out[idx(l, i)] = sum / float64(2*r+1)
				sum += at(i+r+1) - at(i-r)
			}
		}
	}
	for i := 0; i < 3; i++ {
		size := wl
		if i >= mm {
			size = wu
		}
		r := (size - 1) / 2
		box(src, tmp, r, true)
		box(tmp, src, r, false)
	}
	out := image.NewAlpha(m.Rect)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			out.Pix[y*out.Stride+x] = uint8(math.Max(0, math.Min(255, math.Round(src[y*w+x]))))
		}
	}
	return out
}

func clip8(v float64) uint8 { return uint8(math.Max(0, math.Min(255, v))) }

// luma is Pillow's RGB -> L conversion.
func luma(r, g, b uint8) uint8 {
	return uint8((uint32(r)*19595 + uint32(g)*38470 + uint32(b)*7471 + 0x8000) >> 16)
}

// enhanceContrast is ImageEnhance.Contrast(im).enhance(f): blend from the mean grey.
func enhanceContrast(im *image.NRGBA, f float64) {
	var sum float64
	n := 0
	for i := 0; i < len(im.Pix); i += 4 {
		sum += float64(luma(im.Pix[i], im.Pix[i+1], im.Pix[i+2]))
		n++
	}
	mean := float64(int(sum/float64(max(n, 1)) + 0.5))
	for i := 0; i < len(im.Pix); i += 4 {
		for c := 0; c < 3; c++ {
			im.Pix[i+c] = clip8(mean + f*(float64(im.Pix[i+c])-mean))
		}
	}
}

// enhanceColor is ImageEnhance.Color(im).enhance(f): blend from each pixel's grey.
func enhanceColor(im *image.NRGBA, f float64) {
	for i := 0; i < len(im.Pix); i += 4 {
		g := float64(luma(im.Pix[i], im.Pix[i+1], im.Pix[i+2]))
		for c := 0; c < 3; c++ {
			im.Pix[i+c] = clip8(g + f*(float64(im.Pix[i+c])-g))
		}
	}
}
