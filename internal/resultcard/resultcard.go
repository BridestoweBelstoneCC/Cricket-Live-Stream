// Package resultcard renders the post-match result card for Instagram (1080x1350, 4:5).
//
// Pure rendering: everything it shows arrives in Facts and Look, so it can be rendered
// and tested without a server (the server's buildInstagramImage gathers both). Layout,
// top down:
//
//	photo (full colour, fading into the club-colour panel)  + crest / competition / date
//	kicker + hero outcome word ("WIN") with the margin beside it
//	two score rows: badge, club, team, big score, overs — the winner bright, the other muted
//	two star performers with big stat numbers
//	sponsor strip
//
// Type is Barlow Condensed (SIL OFL, built into the app) so the card looks the same on
// every club's laptop instead of whatever Arial-alike the OS has.
package resultcard

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	W, H = 1080, 1350
	PAD  = 60
)

var outcomeWord = map[string]string{"win": "WIN", "loss": "DEFEAT", "tie": "TIED", "draw": "DRAW",
	"abandoned": "ABANDONED", "cancelled": "CANCELLED", "live": "IN PLAY"}

// Side is one team's score row.
type Side struct {
	Club   string `json:"club"`
	Team   string `json:"team"`
	Score  string `json:"score"`
	Overs  string `json:"overs"`
	ClubID string `json:"club_id"`
	Won    bool   `json:"won"`
	Ours   bool   `json:"ours"`
}

// Star is one star performer ("78" "(96)" "P SMITH").
type Star struct {
	Label string `json:"label"`
	Big   string `json:"big"`
	Small string `json:"small"`
	Name  string `json:"name"`
}

type Facts struct {
	Outcome, Margin, Kicker, Competition, Date, Ground string
	Sides                                              []Side
	Stars                                              []Star
}

type Look struct {
	ClubColour, Crest, Photo, LogosDir, ClubName string
	Sponsors                                     []string
}

// ── Colour ──────────────────────────────────────────────────────────────────

// HexRGB parses "#rrggbb"; anything else gives the fallback.
func HexRGB(h string, fallback [3]uint8) [3]uint8 {
	h = strings.TrimLeft(strings.TrimSpace(h), "#")
	var out [3]uint8
	for i := 0; i < 3; i++ {
		// Python's h[i:i+2]: a short string gives a 1-char or empty slice; "" fails.
		if len(h) <= 2*i {
			return fallback
		}
		v, err := strconv.ParseUint(h[2*i:min(2*i+2, len(h))], 16, 8)
		if err != nil {
			return fallback
		}
		out[i] = uint8(v)
	}
	return out
}

func lum(c [3]uint8) float64 {
	ch := func(v uint8) float64 {
		x := float64(v) / 255
		if x <= 0.03928 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(c[0]) + 0.7152*ch(c[1]) + 0.0722*ch(c[2])
}

// Contrast is the WCAG contrast ratio of two colours.
func Contrast(a, b [3]uint8) float64 {
	la, lb := lum(a), lum(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// Mix: t=0 -> a, t=1 -> b.
func Mix(a, b [3]uint8, t float64) [3]uint8 {
	var out [3]uint8
	for i := range out {
		out[i] = uint8(int(math.RoundToEven(float64(a[i]) + (float64(b[i])-float64(a[i]))*t)))
	}
	return out
}

func pymod1(x float64) float64 {
	m := math.Mod(x, 1)
	if m < 0 {
		m += 1
	}
	return m
}

// rgbToHLS and hlsToRGB are Python's colorsys, operation for operation.
func rgbToHLS(r, g, b float64) (float64, float64, float64) {
	maxc, minc := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
	sumc, rangec := maxc+minc, maxc-minc
	l := sumc / 2.0
	if minc == maxc {
		return 0, l, 0
	}
	var s float64
	if l <= 0.5 {
		s = rangec / sumc
	} else {
		s = rangec / (2.0 - maxc - minc)
	}
	rc, gc, bc := (maxc-r)/rangec, (maxc-g)/rangec, (maxc-b)/rangec
	var h float64
	switch {
	case r == maxc:
		h = bc - gc
	case g == maxc:
		h = 2.0 + rc - bc
	default:
		h = 4.0 + gc - rc
	}
	return pymod1(h / 6.0), l, s
}

func hlsV(m1, m2, hue float64) float64 {
	hue = pymod1(hue)
	switch {
	case hue < 1.0/6.0:
		return m1 + (m2-m1)*hue*6.0
	case hue < 0.5:
		return m2
	case hue < 2.0/3.0:
		return m1 + (m2-m1)*(2.0/3.0-hue)*6.0
	}
	return m1
}

func hlsToRGB(h, l, s float64) (float64, float64, float64) {
	if s == 0 {
		return l, l, l
	}
	var m2 float64
	if l <= 0.5 {
		m2 = l * (1.0 + s)
	} else {
		m2 = l + s - (l * s)
	}
	m1 := 2.0*l - m2
	return hlsV(m1, m2, h+1.0/3.0), hlsV(m1, m2, h), hlsV(m1, m2, h-1.0/3.0)
}

// Palette is the card's colours, all derived from one club colour.
type Palette struct {
	Club, BG, Hi, White, Muted, Faint [3]uint8
}

// NewPalette derives the panel background, highlight and muted text from one club
// colour. The panel is always dark (white type sits on it); the highlight is the club
// colour lifted until it reads on that panel — a near-black navy becomes a clear
// mid-blue rather than vanishing, the same problem the overlay's wormColour() solves for
// the scorebar.
func NewPalette(clubHex string) Palette {
	club := HexRGB(clubHex, [3]uint8{26, 58, 92})
	bg := Mix(club, [3]uint8{6, 10, 18}, 0.72)
	// Lighten in HSL, keeping the hue and a decent saturation: mixing towards white
	// turned a navy into steel grey, losing the club's colour exactly where it should show.
	h, l, sat := rgbToHLS(float64(club[0])/255, float64(club[1])/255, float64(club[2])/255)
	if sat > 0.08 { // greys/black/white stay neutral
		sat = math.Max(sat, 0.55)
	}
	hi := club
	for Contrast(hi, bg) < 4.5 && l < 0.97 {
		l = math.Min(l+0.03, 0.97)
		r, g, b := hlsToRGB(h, l, sat)
		hi = [3]uint8{uint8(math.RoundToEven(r * 255)), uint8(math.RoundToEven(g * 255)), uint8(math.RoundToEven(b * 255))}
	}
	white := [3]uint8{255, 255, 255}
	return Palette{Club: club, BG: bg, Hi: hi, White: white,
		Muted: Mix(white, bg, 0.38), Faint: Mix(white, bg, 0.62)}
}

func col(c [3]uint8) color.NRGBA { return color.NRGBA{c[0], c[1], c[2], 255} }

// ── Layout helpers ──────────────────────────────────────────────────────────

// fit picks the largest font (down to minSize) that fits text in maxW; ellipsis below
// that. Returns the text, face and its size.
func fit(text, weight string, size, minSize int, maxW float64) (string, int) {
	for size > minSize && textlength(text, face(size, weight)) > maxW {
		size -= 2
	}
	f := face(size, weight)
	if textlength(text, f) > maxW {
		r := []rune(text)
		for len(r) > 0 && textlength(string(r)+"…", f) > maxW {
			r = r[:len(r)-1]
		}
		text = strings.TrimRight(string(r), " \t\n\r\v\f") + "…"
	}
	return text, size
}

// cover scales and crops to fill w x h. Crops keep a point ~38% down the photo in frame
// — where heads usually are in a cricket shot — rather than the dead centre.
func cover(img image.Image, w, h int) *image.NRGBA {
	const focusY = 0.38
	b := img.Bounds()
	s := math.Max(float64(w)/float64(b.Dx()), float64(h)/float64(b.Dy()))
	rw := max(w, int(math.RoundToEven(float64(b.Dx())*s)))
	rh := max(h, int(math.RoundToEven(float64(b.Dy())*s)))
	big := resize(img, rw, rh)
	left := (rw - w) / 2
	top := int(math.Min(math.Max(float64(rh)*focusY-float64(h)*0.38, 0), float64(rh-h)))
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		copy(out.Pix[y*out.Stride:y*out.Stride+w*4], big.Pix[(top+y)*big.Stride+left*4:])
	}
	return out
}

// initialsBadge stands in for a club with no badge in logos/: a disc with its initials.
func initialsBadge(name string, size int, c [3]uint8) *image.NRGBA {
	im := image.NewNRGBA(image.Rect(0, 0, size*2, size*2)) // 2x, then downsampled
	ellipse(im, 4, 4, float64(size*2-4), float64(size*2-4), col(c), color.NRGBA{255, 255, 255, 200}, 6)
	skip := map[string]bool{"CC": true, "AND": true, "&": true, "THE": true, "CRICKET": true, "CLUB": true, "OF": true}
	var ini strings.Builder
	n := 0
	for _, w := range strings.Fields(strings.ReplaceAll(name, "&", " ")) {
		if skip[strings.ToUpper(w)] {
			continue
		}
		if n < 2 {
			ini.WriteRune([]rune(w)[0])
		}
		n++
	}
	s := strings.ToUpper(ini.String())
	if s == "" {
		s = "?"
	}
	text(im, float64(size), float64(size+4), s, face(int(float64(size)*0.85), "black"), color.White, "mm")
	return resize(im, size, size)
}

func splitMargin(margin string) []string {
	words := strings.Fields(margin)
	if len(words) == 0 {
		return nil
	}
	if len(words) <= 2 {
		return []string{margin}
	}
	return []string{strings.Join(words[:len(words)-1], " "), words[len(words)-1]}
}

// shadowed draws hero type with a soft shadow so it holds over any part of a photo.
func shadowed(img *image.RGBA, x, y float64, s string, size int, fill color.Color) {
	f := face(size, "black")
	sh := image.NewAlpha(img.Rect)
	text(sh, x+4, y+8, s, f, color.Alpha{150}, "ls")
	sh = gaussianBlur(sh, 14)
	paste(img, image.NewUniform(color.Black), 0, 0, sh)
	text(img, x, y, s, f, fill, "ls")
}

// ── The card ────────────────────────────────────────────────────────────────

// Render draws the card to outPath (PNG). Missing pieces are skipped, never fatal.
func Render(facts Facts, look Look, outPath string) (string, error) {
	img := RenderImage(facts, look)
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil && filepath.Dir(outPath) != "" {
		return "", err
	}
	f, err := os.Create(outPath)
	if err != nil {
		return "", err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return "", err
	}
	return outPath, f.Close()
}

// RenderImage draws the card.
func RenderImage(facts Facts, look Look) *image.RGBA {
	if look.ClubColour == "" {
		look.ClubColour = "#1a3a5c"
	}
	P := NewPalette(look.ClubColour)
	img := newRGB(W, H, P.BG)
	const photoH = 820

	// Backdrop: the photo in full colour, fading into the panel; else the club colour
	// with the crest as a large watermark, so the top never sits empty.
	var photo image.Image
	if look.Photo != "" {
		photo, _ = decodeFile(look.Photo)
	}
	if photo != nil {
		ph := cover(photo, W, photoH)
		enhanceContrast(ph, 1.08)
		enhanceColor(ph, 1.06)
		for i := 3; i < len(ph.Pix); i += 4 {
			ph.Pix[i] = 255
		}
		paste(img, ph, 0, 0, verticalAlpha(W, photoH, [][2]float64{{0, 255}, {0.55, 255}, {1, 0}}))
	} else {
		poly4 := [][2]float64{{W * 0.30, 0}, {W, 0}, {W, H * 0.48}, {W * 0.62, H * 0.62}}
		fill(img, rgb(P.Club).nrgba(150), func(z rasterizer) { poly(z, poly4) })
		for x := -H; x < W; x += 34 {
			line(img, float64(x), H*0.62, float64(x)+H*0.62, 0, 2, color.NRGBA{255, 255, 255, 9})
		}
		if wm := loadRGBA(look.Crest, 640, 640); wm != nil {
			// Badges in logos/ are ~180px and thumbnail() never enlarges; soft is fine at
			// 16%. Ends above y=760 so the hero scrim fades it out instead of cutting it off.
			k := 640 / float64(max(wm.Rect.Dx(), wm.Rect.Dy()))
			wm = resize(wm, int(math.RoundToEven(float64(wm.Rect.Dx())*k)), int(math.RoundToEven(float64(wm.Rect.Dy())*k)))
			scaleAlpha(wm, 0.16)
			paste(img, wm, W-wm.Rect.Dx()+100, 110, nil)
		}
	}

	// Header scrim so white type holds over a bright sky.
	paste(img, image.NewUniform(color.Black), 0, 0, verticalAlpha(W, 260, [][2]float64{{0, 150}, {1, 0}}))
	// Bottom scrim under the hero word, over the photo's fade.
	heroMask := verticalAlpha(W, 360, [][2]float64{{0, 0}, {1, 170}})
	pasteAt(img, image.NewUniform(col(P.BG)), image.Rect(0, photoH-360, W, photoH), heroMask)

	white := col(P.White)

	// ── Header: crest + club + competition | date + ground ──
	x := float64(PAD)
	if crest := loadRGBA(look.Crest, 104, 104); crest != nil {
		paste(img, crest, PAD, 44, nil)
		x = float64(PAD + crest.Rect.Dx() + 22)
	}
	date := strings.ToUpper(facts.Date)
	dateW := 0.0
	if date != "" {
		dateW = textlength(date, face(34, "xbold"))
	}
	room := float64(W-PAD) - x
	if date != "" {
		room -= dateW + 40
	}
	club, clubSize := fit(strings.ToUpper(look.ClubName), "xbold", 38, 26, room)
	text(img, x, 92, club, face(clubSize, "xbold"), white, "ls")
	if comp := strings.ToUpper(facts.Competition); comp != "" {
		comp, compSize := fit(comp, "semi", 28, 22, room)
		c := white
		if photo == nil {
			c = col(P.Hi)
		}
		tracked(img, x, 130, comp, face(compSize, "semi"), compSize, c, 0.04, "ls")
	}
	if date != "" {
		text(img, W-PAD, 92, date, face(34, "xbold"), white, "rs")
		if ground := strings.ToUpper(facts.Ground); ground != "" {
			g, gs := fit(ground, "medium", 26, 20, 360)
			text(img, W-PAD, 128, g, face(gs, "medium"), color.NRGBA{235, 238, 245, 255}, "rs")
		}
	}

	// ── Hero: kicker, outcome word, margin ──
	word, ok := outcomeWord[facts.Outcome]
	if !ok {
		word = "RESULT"
	}
	mLines := splitMargin(strings.ToUpper(facts.Margin))
	fM := face(78, "xbold")
	mW := 0.0
	for _, l := range mLines {
		mW = math.Max(mW, textlength(l, fM))
	}
	heroSize := 270
	for heroSize > 120 {
		extra := 0.0
		if len(mLines) > 0 {
			extra = mW + 34
		}
		if textlength(word, face(heroSize, "black"))+extra <= W-2*PAD {
			break
		}
		heroSize -= 6
	}
	const base = 790.0 // hero baseline
	kicker := strings.ToUpper(facts.Kicker)
	if kicker == "" {
		kicker = "MATCH RESULT"
	}
	kx := float64(PAD)
	hs := float64(heroSize)
	rect(img, kx, base-hs*0.74-52, kx+46, base-hs*0.74-44, col(P.Hi))
	ky := base - hs*0.74 - 34
	tracked(img, kx+62, ky+2, kicker, face(30, "bold"), 30, color.Black, 0.14, "ls") // over a photo
	tracked(img, kx+60, ky, kicker, face(30, "bold"), 30, white, 0.14, "ls")
	shadowed(img, PAD-6, base, word, heroSize, white)
	if len(mLines) > 0 {
		mx := PAD - 6 + textlength(word, face(heroSize, "black")) + 34
		ly := base - float64(len(mLines)-1)*72
		for _, l := range mLines {
			text(img, mx, ly, l, fM, col(P.Hi), "ls")
			ly += 72
		}
	}

	// ── Score rows ──
	y := 834
	sides := facts.Sides
	if len(sides) > 2 {
		sides = sides[:2]
	}
	const rowH, gap = 118, 14
	decided := facts.Outcome == "win" || facts.Outcome == "loss"
	for _, side := range sides {
		scoreRow(img, P, side, y, rowH, look, decided && !side.Won)
		y += rowH + gap
	}

	// ── Stars ──
	var stars []Star
	for _, s := range facts.Stars {
		if s.Big != "" && len(stars) < 2 {
			stars = append(stars, s)
		}
	}
	var sponsors []string
	for _, p := range look.Sponsors {
		if p != "" {
			sponsors = append(sponsors, p)
		}
	}
	stripH := 0
	if len(sponsors) > 0 {
		stripH = 104
	}
	if len(stars) > 0 {
		top := float64(y + 12)
		colW := float64(W-2*PAD) / float64(len(stars))
		for i, s := range stars {
			sx := PAD + float64(i)*colW
			if i > 0 {
				sx += 34
				lx := PAD + float64(i)*colW
				line(img, lx, top+8, lx, top+128, 2, col(P.Faint))
			}
			tracked(img, sx, top+26, strings.ToUpper(s.Label), face(25, "bold"), 25, col(P.Hi), 0.12, "ls")
			fBig := face(92, "black")
			text(img, sx-2, top+112, s.Big, fBig, white, "ls")
			bx := sx + textlength(s.Big, fBig) + 12
			if s.Small != "" {
				text(img, bx, top+112, strings.ToUpper(s.Small), face(36, "bold"), col(P.Muted), "ls")
			}
			// Name to the right of the number, detail beneath it:  87  P SMITH
			//                                                          (94)
			name, ns := fit(strings.ToUpper(s.Name), "bold", 36, 24, PAD+float64(i+1)*colW-bx-24)
			text(img, bx, top+72, name, face(ns, "bold"), white, "ls")
		}
	}

	// ── Sponsor strip ──
	if len(sponsors) > 0 {
		sy := float64(H - stripH)
		rect(img, 0, sy, W, H, color.NRGBA{246, 247, 249, 255})
		rect(img, 0, sy, W, sy+6, col(P.Club))
		var logos []*image.NRGBA
		for _, p := range sponsors {
			if l := loadRGBA(p, 400, stripH-40); l != nil {
				logos = append(logos, l)
			}
		}
		if len(logos) > 0 {
			const gapX = 40
			avail := float64(W - 2*PAD)
			sumW := 0
			for _, l := range logos {
				sumW += l.Rect.Dx()
			}
			scale := math.Min(1, (avail-float64(gapX*(len(logos)-1)))/float64(sumW))
			if scale < 1 {
				for i, l := range logos {
					logos[i] = resize(l, max(1, int(float64(l.Rect.Dx())*scale)), max(1, int(float64(l.Rect.Dy())*scale)))
				}
			}
			total := gapX * (len(logos) - 1)
			for _, l := range logos {
				total += l.Rect.Dx()
			}
			lx := (W - total) / 2
			for _, l := range logos {
				paste(img, l, lx, int(sy)+3+(stripH-l.Rect.Dy())/2, nil)
				lx += l.Rect.Dx() + gapX
			}
		}
	}
	return img
}

func scoreRow(img *image.RGBA, P Palette, side Side, y, rowH int, look Look, dim bool) {
	a := uint8(26)
	if dim {
		a = 14
	}
	roundedRect(img, PAD-20, float64(y), W-PAD+20, float64(y+rowH), 14, color.NRGBA{255, 255, 255, a})
	if side.Won {
		roundedRect(img, PAD-20, float64(y), PAD-12, float64(y+rowH), 4, col(P.Hi))
	}
	textC, subC := col(P.White), col(P.Muted)
	if dim {
		textC, subC = col(P.Muted), col(P.Faint)
	}

	const badgeSize = 84
	badge := clubBadge(side, look, badgeSize, P)
	if dim {
		scaleAlpha(badge, 0.6)
	}
	bx := PAD + 6
	paste(img, badge, bx+(badgeSize-badge.Rect.Dx())/2, y+(rowH-badge.Rect.Dy())/2, nil)

	// Score on the right, then the name gets whatever width is left.
	fS := face(84, "black")
	sw := textlength(side.Score, fS)
	text(img, W-PAD, float64(y+80), side.Score, fS, textC, "rs")
	if side.Overs != "" {
		text(img, W-PAD, float64(y+108), strings.ToUpper(side.Overs), face(24, "semi"), subC, "rs")
	}
	nx := float64(bx + badgeSize + 26)
	room := W - PAD - sw - 36 - nx
	name, ns := fit(strings.ToUpper(side.Club), "xbold", 46, 30, room)
	hasTeam := side.Team != ""
	ny := y + 76
	if hasTeam {
		ny = y + 64
	}
	text(img, nx, float64(ny), name, face(ns, "xbold"), textC, "ls")
	if hasTeam {
		tracked(img, nx, float64(y+98), strings.ToUpper(side.Team), face(25, "semi"), 25, subC, 0.08, "ls")
	}
}

// clubBadge is the side's badge from logos/ (by PlayCricket club id), else its initials on
// a disc — the club colour for ours, grey for theirs.
func clubBadge(side Side, look Look, size int, P Palette) *image.NRGBA {
	if cid := strings.TrimSpace(side.ClubID); cid != "" && look.LogosDir != "" {
		for _, ext := range []string{".png", ".webp", ".jpg", ".jpeg"} {
			p := filepath.Join(look.LogosDir, cid+ext)
			if _, err := os.Stat(p); err == nil {
				if b := loadRGBA(p, size, size); b != nil {
					return b
				}
				break
			}
		}
	}
	name := side.Club
	if name == "" {
		name = "?"
	}
	c := [3]uint8{90, 98, 112}
	if side.Ours {
		c = P.Club
	}
	return initialsBadge(name, size, c)
}
