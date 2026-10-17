// Command makebrand renders the CricketStream brand assets to brand/ (and the website's
// copies to docs/). Bundled fonts only, the same rule as the result card, so anyone can
// regenerate them after a tweak:
//
//	go run ./scripts/makebrand          (from the repository root)
//
// The mark is a cricket ball sending out a broadcast signal: the two things the project
// is about. Everything is drawn at 4x and scaled down, as the original did for Pillow
// (whose shapes aren't anti-aliased); here it also keeps the seam stitches crisp.
//
// The banners carry a scorebar strip from examples/scorebar-modern.png, which
// `go run ./scripts/renderscorebar modern` makes.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

type rgb [3]uint8

func (c rgb) a(alpha uint8) color.NRGBA { return color.NRGBA{c[0], c[1], c[2], alpha} }
func (c rgb) c() color.NRGBA            { return c.a(255) }

// The project website's greens (docs/index.html) plus a cricket-ball red.
var (
	greenDark   = rgb{16, 52, 30}
	green       = rgb{29, 82, 48}
	greenMid    = rgb{42, 110, 63}
	greenBright = rgb{126, 214, 150}
	ball        = rgb{196, 22, 44}
	ballShade   = rgb{138, 10, 28}
	seam        = rgb{250, 244, 236}
	seamLine    = rgb{112, 6, 22}
	white       = rgb{255, 255, 255}
	ink         = rgb{26, 26, 26}
)

const ss = 4 // supersampling factor

var fonts = map[string]*opentype.Font{}

func face(weight string, size float64) font.Face {
	f, ok := fonts[weight]
	if !ok {
		data, err := os.ReadFile(filepath.Join("fonts", "BarlowCondensed-"+weight+".ttf"))
		if err != nil {
			die("%v (run this from the repository root)", err)
		}
		if f, err = opentype.Parse(data); err != nil {
			die("%v", err)
		}
		fonts[weight] = f
	}
	fc, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		die("%v", err)
	}
	return fc
}

func length(f font.Face, s string) float64 { return float64(font.MeasureString(f, s)) / 64 }

// text draws s with its left (anchor "l") or middle ("m") at x and its baseline at y.
func text(dst draw.Image, x, y float64, s string, f font.Face, c color.Color, anchor byte) {
	if anchor == 'm' {
		x -= length(f, s) / 2
	}
	d := font.Drawer{Dst: dst, Src: image.NewUniform(c), Face: f,
		Dot: fixed.Point26_6{X: fixed.Int26_6(x * 64), Y: fixed.Int26_6(y * 64)}}
	d.DrawString(s)
}

// ── shapes ──

type path [][2]float64

// fillPaths fills the paths (non-zero winding) in one colour. The rasterizer covers only
// the paths' bounding box: the seam alone is hundreds of tiny stitches on a 4096px canvas.
func fillPaths(dst draw.Image, c color.Color, paths ...path) {
	x0, y0, x1, y1 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, p := range paths {
		for _, q := range p {
			x0, y0 = math.Min(x0, q[0]), math.Min(y0, q[1])
			x1, y1 = math.Max(x1, q[0]), math.Max(y1, q[1])
		}
	}
	box := image.Rect(int(math.Floor(x0)), int(math.Floor(y0)), int(math.Ceil(x1))+1, int(math.Ceil(y1))+1).Intersect(dst.Bounds())
	if box.Empty() {
		return
	}
	ox, oy := float64(box.Min.X), float64(box.Min.Y)
	z := vector.NewRasterizer(box.Dx(), box.Dy())
	for _, p := range paths {
		z.MoveTo(float32(p[0][0]-ox), float32(p[0][1]-oy))
		for _, q := range p[1:] {
			z.LineTo(float32(q[0]-ox), float32(q[1]-oy))
		}
		z.ClosePath()
	}
	z.Draw(dst, box, image.NewUniform(c), image.Point{})
}

func circle(cx, cy, r float64, reverse bool) path {
	n := max(48, int(r))
	p := make(path, n)
	for i := range p {
		a := float64(i) / float64(n) * 2 * math.Pi
		if reverse {
			a = -a
		}
		p[i] = [2]float64{cx + r*math.Cos(a), cy + r*math.Sin(a)}
	}
	return p
}

// arc is a ring segment `width` thick inside radius r, from a0 to a1 degrees (clockwise
// from three o'clock, y down), like Pillow's arc().
func arc(cx, cy, r, width, a0, a1 float64) path {
	const n = 64
	var p path
	for i := 0; i <= n; i++ {
		a := (a0 + (a1-a0)*float64(i)/n) * math.Pi / 180
		p = append(p, [2]float64{cx + r*math.Cos(a), cy + r*math.Sin(a)})
	}
	for i := n; i >= 0; i-- {
		a := (a0 + (a1-a0)*float64(i)/n) * math.Pi / 180
		p = append(p, [2]float64{cx + (r-width)*math.Cos(a), cy + (r-width)*math.Sin(a)})
	}
	return p
}

func line(xa, ya, xb, yb, width float64) path {
	dx, dy := xb-xa, yb-ya
	l := math.Hypot(dx, dy)
	nx, ny := -dy/l*width/2, dx/l*width/2
	return path{{xa + nx, ya + ny}, {xb + nx, yb + ny}, {xb - nx, yb - ny}, {xa - nx, ya - ny}}
}

func roundedRect(x0, y0, x1, y1, r float64) path {
	var p path
	corner := func(cx, cy, a0 float64) {
		for i := 0; i <= 16; i++ {
			a := a0 + float64(i)/16*math.Pi/2
			p = append(p, [2]float64{cx + r*math.Cos(a), cy + r*math.Sin(a)})
		}
	}
	corner(x1-r, y0+r, -math.Pi/2)
	corner(x1-r, y1-r, 0)
	corner(x0+r, y1-r, math.Pi/2)
	corner(x0+r, y0+r, math.Pi)
	return p
}

func vgradient(w, h int, top, bottom rgb) *image.NRGBA {
	im := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		t := float64(y) / float64(max(h-1, 1))
		var c rgb
		for i := range c {
			c[i] = uint8(math.RoundToEven(float64(top[i]) + (float64(bottom[i])-float64(top[i]))*t))
		}
		draw.Draw(im, image.Rect(0, y, w, y+1), image.NewUniform(c.c()), image.Point{}, draw.Src)
	}
	return im
}

func resize(src image.Image, w, h int) *image.NRGBA {
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(out, out.Rect, src, src.Bounds(), xdraw.Src, nil)
	return out
}

// ── the mark ──

// drawMark is the square icon: rounded tile, ball, three signal arcs.
func drawMark(size int, transparent bool) *image.NRGBA {
	S := float64(size * ss)
	n := size * ss
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	if !transparent {
		mask := image.NewAlpha(img.Rect)
		fillPaths(mask, color.Opaque, roundedRect(0, 0, S, S, float64(int(S*0.22))))
		draw.DrawMask(img, img.Rect, vgradient(n, n, greenMid, greenDark), image.Point{}, mask, image.Point{}, draw.Src)
	}

	cx, cy, r := S*0.40, S*0.60, S*0.235
	// Flat-style ball, drawn on its own layer and clipped to the circle: a lighter face
	// offset up-left leaves a crescent of shade at the lower right.
	layer := image.NewNRGBA(img.Rect)
	fillPaths(layer, ballShade.c(), circle(cx, cy, r, false))
	fr, fx, fy := r*0.89, cx-r*0.12, cy-r*0.12
	fillPaths(layer, ball.c(), circle(fx, fy, fr, false))

	// Seam: one thin darker line, a gentle arc of a much larger circle, with two rows of
	// slanted round-ended stitches either side of it.
	big := r * 2.4
	phi := -12 * math.Pi / 180 // direction to the arc's centre: leans with the flight
	scx, scy := cx+math.Cos(phi)*(big-r*0.10), cy+math.Sin(phi)*(big-r*0.10)
	sw := float64(int(r * 0.042))
	fillPaths(layer, seamLine.c(), circle(scx, scy, big, false), circle(scx, scy, big-sw, true))
	dash, dashW := r*0.085, float64(int(r*0.032))
	step := r * 0.125 / big
	for k := -12; k <= 12; k++ { // runs off the ball's edge; the clip trims it
		a := phi + math.Pi + float64(k)*step
		slant := a + 60*math.Pi/180
		for _, off := range []float64{-0.17, -0.075, 0.075, 0.17} {
			px := scx + math.Cos(a)*(big+r*off)
			py := scy + math.Sin(a)*(big+r*off)
			hx, hy := math.Cos(slant)*dash/2, math.Sin(slant)*dash/2
			fillPaths(layer, seam.c(), line(px-hx, py-hy, px+hx, py+hy, dashW))
			fillPaths(layer, seam.c(), circle(px-hx, py-hy, dashW/2, false))
			fillPaths(layer, seam.c(), circle(px+hx, py+hy, dashW/2, false))
		}
	}
	clip := image.NewAlpha(img.Rect)
	fillPaths(clip, color.Opaque, circle(cx, cy, r, false))
	draw.DrawMask(img, img.Rect, layer, image.Point{}, clip, image.Point{}, draw.Over)

	// Broadcast arcs, round-capped, fanning up and right from the ball.
	width := float64(int(S * 0.058))
	a0, a1 := -62.0, -8.0
	for i, rad := range []float64{r * 1.45, r * 1.95, r * 2.45} {
		col := white
		if i == 2 {
			col = greenBright
		}
		fillPaths(img, col.c(), arc(cx, cy, rad, width, a0, a1))
		for _, a := range []float64{a0, a1} {
			mid := rad - width/2
			px, py := cx+math.Cos(a*math.Pi/180)*mid, cy+math.Sin(a*math.Pi/180)*mid
			fillPaths(img, col.c(), circle(px, py, width/2, false))
		}
	}
	return resize(img, size, size)
}

func wordmarkWidth(h float64) float64 {
	f := face("Black", h)
	return length(f, "CRICKET") + length(f, "STREAM")
}

// drawWordmark: CRICKET in white (or ink), STREAM in green — one word, two colours.
func drawWordmark(dst draw.Image, x, y, h float64, darkBg bool) {
	f := face("Black", h)
	first, second := white, greenBright
	if !darkBg {
		first, second = ink, greenMid
	}
	text(dst, x, y, "CRICKET", f, first.c(), 'l')
	text(dst, x+length(f, "CRICKET"), y, "STREAM", f, second.c(), 'l')
}

// lockup is icon + wordmark on one line, for README headers and the website.
func lockup(darkBg bool) *image.NRGBA {
	H := 240
	fH := float64(H)
	icon := drawMark(H, false)
	textH := float64(int(fH * 0.62))
	w := H + int(fH*0.18) + int(wordmarkWidth(textH)) + 10
	img := image.NewNRGBA(image.Rect(0, 0, w, H))
	draw.Draw(img, icon.Rect, icon, image.Point{}, draw.Over)
	drawWordmark(img, fH+float64(int(fH*0.18)), float64(int(fH*0.5+textH*0.36)), textH, darkBg)
	return img
}

// banner is a wide card: gradient, mark, wordmark, tagline, optional scorebar strip.
func banner(w, h int, tagline bool, strip image.Image) *image.NRGBA {
	img := vgradient(w, h, green, greenDark)
	// A faint pitch: two crease lines, so the field isn't flat colour.
	for _, fx := range []float64{0.08, 0.92} {
		lw := float64(max(2, w/400))
		x := float64(w) * fx
		fillPaths(img, color.NRGBA{255, 255, 255, 22}, path{{x - lw/2, 0}, {x + lw/2, 0}, {x + lw/2, float64(h)}, {x - lw/2, float64(h)}})
	}
	fh := float64(h)
	k := 0.42
	if strip != nil {
		k = 0.34
	}
	iconS := int(fh * k)
	textH := float64(int(float64(iconS) * 0.62))
	// Shrink the whole lockup if it wouldn't fit inside the crease lines.
	for float64(iconS+int(float64(iconS)*0.18))+wordmarkWidth(textH) > float64(w)*0.76 {
		iconS -= 2
		textH = float64(int(float64(iconS) * 0.62))
	}
	blockW := iconS + int(float64(iconS)*0.18) + int(wordmarkWidth(textH))
	topK := 0.22
	if strip != nil {
		topK = 0.16
	}
	top := int(fh * topK)
	x := (w - blockW) / 2
	icon := drawMark(iconS, false)
	draw.Draw(img, icon.Rect.Add(image.Pt(x, top)), icon, image.Point{}, draw.Over)
	drawWordmark(img, float64(x+iconS+int(float64(iconS)*0.18)), float64(int(float64(top)+float64(iconS)*0.5+textH*0.36)), textH, true)
	if tagline {
		f := face("SemiBold", float64(int(math.Min(fh*0.075, float64(w)*0.032))))
		text(img, float64(w)/2, float64(top+iconS)+fh*0.13, "FREE BROADCAST GRAPHICS FOR GRASSROOTS CRICKET", f, color.NRGBA{225, 240, 230, 255}, 'm')
	}
	if strip != nil {
		bw := int(float64(w) * 0.92)
		sb := strip.Bounds()
		bar := resize(strip, bw, int(float64(sb.Dy())*float64(bw)/float64(sb.Dx())))
		y := h - bar.Rect.Dy() - int(fh*0.09)
		// The original painted this frame translucent on an RGBA canvas and then dropped
		// the alpha channel, which leaves it solid black; drawn solid here for the same look.
		fillPaths(img, color.Black, roundedRect(float64((w-bw)/2-6), float64(y-6), float64((w+bw)/2+6)+1, float64(y+bar.Rect.Dy()+6)+1, 8))
		draw.Draw(img, bar.Rect.Add(image.Pt((w-bw)/2, y)), bar, image.Point{}, draw.Over)
	}
	// Opaque, like the original's convert("RGB").
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	return img
}

// ── output ──

func save(path string, im image.Image) {
	f, err := os.Create(path)
	if err != nil {
		die("%v", err)
	}
	defer f.Close()
	if err := png.Encode(f, im); err != nil {
		die("%v", err)
	}
}

// saveICO writes a Windows icon with one PNG-compressed image per size (Vista and later
// read these, and so does every browser).
func saveICO(path string, src image.Image, sizes []int) {
	var imgs [][]byte
	for _, s := range sizes {
		var b bytes.Buffer
		png.Encode(&b, resize(src, s, s))
		imgs = append(imgs, b.Bytes())
	}
	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, [3]uint16{0, 1, uint16(len(sizes))})
	offset := 6 + 16*len(sizes)
	for i, s := range sizes {
		dim := uint8(s)
		if s >= 256 {
			dim = 0
		}
		out.Write([]byte{dim, dim, 0, 0})
		binary.Write(&out, binary.LittleEndian, [2]uint16{1, 32})
		binary.Write(&out, binary.LittleEndian, [2]uint32{uint32(len(imgs[i])), uint32(offset)})
		offset += len(imgs[i])
	}
	for _, b := range imgs {
		out.Write(b)
	}
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		die("%v", err)
	}
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "makebrand: "+format+"\n", a...)
	os.Exit(1)
}

func main() {
	out, docs := "brand", "docs"
	os.MkdirAll(out, 0o755)
	stripPath := filepath.Join("examples", "scorebar-modern.png")
	sf, err := os.Open(stripPath)
	if err != nil {
		die("%s is missing — render it first: go run ./scripts/renderscorebar modern", stripPath)
	}
	strip, _, err := image.Decode(sf)
	sf.Close()
	if err != nil {
		die("%s: %v", stripPath, err)
	}

	save(filepath.Join(out, "icon-1024.png"), drawMark(1024, false))
	save(filepath.Join(out, "icon-512.png"), drawMark(512, false))
	// The browser-tab icon the server serves at /favicon.ico, and the exe's icon.
	save(filepath.Join(out, "favicon-64.png"), drawMark(64, false))
	saveICO(filepath.Join(out, "icon.ico"), drawMark(256, false), []int{16, 24, 32, 48, 64, 128, 256})
	saveICO(filepath.Join("cmd", "cricketstream", "winres", "icon.ico"), drawMark(256, false), []int{16, 24, 32, 48, 64, 128, 256})
	save(filepath.Join(out, "mark-transparent.png"), drawMark(1024, true))
	save(filepath.Join(out, "logo-dark-bg.png"), lockup(true))
	save(filepath.Join(out, "logo-light-bg.png"), lockup(false))
	save(filepath.Join(out, "social-card-1200x630.png"), banner(1200, 630, true, strip))
	save(filepath.Join(out, "discord-banner-960x540.png"), banner(960, 540, true, strip))
	save(filepath.Join(out, "youtube-banner-2560x1440.png"), banner(2560, 1440, true, strip))
	save(filepath.Join(out, "header-1500x500.png"), banner(1500, 500, true, nil))
	// GitHub Pages serves only docs/, so the website needs its own copies.
	save(filepath.Join(docs, "favicon-32.png"), drawMark(32, false))
	save(filepath.Join(docs, "apple-touch-icon.png"), drawMark(180, false))
	save(filepath.Join(docs, "icon-96.png"), drawMark(96, false))
	save(filepath.Join(docs, "social-card.png"), banner(1200, 630, true, strip))
	fmt.Println("Wrote brand assets to", out, "and the website's copies to", docs)
	fmt.Println("If the icon changed, rebuild the exe's resources too: go generate ./cmd/cricketstream")
}
