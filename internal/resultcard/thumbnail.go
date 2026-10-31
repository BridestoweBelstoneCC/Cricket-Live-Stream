package resultcard

import (
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
)

// The YouTube thumbnail for a match video or its highlights (1280x720, YouTube's size):
//
//	kicker ("LIVE CRICKET" / "HIGHLIGHTS")            date
//	competition
//	   [badge]        V        [badge]
//	  HOME CC                 RIVALS CC
//	  187/7                      153
//	the result (WIN BY 34 RUNS), when there is one
//
// Same palette, type and badges as the result card, so a club's videos and posts look
// like one set. Everything comes in Facts and Look; missing pieces are left out.
const TW, TH = 1280, 720

// Thumbnail draws the thumbnail.
func Thumbnail(facts Facts, look Look) *image.RGBA {
	if look.ClubColour == "" {
		look.ClubColour = "#1a3a5c"
	}
	P := NewPalette(look.ClubColour)
	img := newRGB(TW, TH, P.BG)
	white := col(P.White)

	// The club colour across the left, with the card's faint diagonal lines.
	fill(img, rgb(P.Club).nrgba(170), func(z rasterizer) {
		poly(z, [][2]float64{{0, 0}, {TW * 0.62, 0}, {TW * 0.46, TH}, {0, TH}})
	})
	for x := -TH; x < TW; x += 34 {
		line(img, float64(x), TH, float64(x)+TH, 0, 2, color.NRGBA{255, 255, 255, 8})
	}

	// ── Header ──
	kicker := strings.ToUpper(facts.Kicker)
	if kicker == "" {
		kicker = "LIVE CRICKET"
	}
	rect(img, 64, 54, 110, 62, col(P.Hi))
	tracked(img, 124, 74, kicker, face(40, "bold"), 40, white, 0.12, "ls")
	if date := strings.ToUpper(facts.Date); date != "" {
		text(img, TW-64, 74, date, face(40, "xbold"), white, "rs")
	}
	if comp := strings.ToUpper(facts.Competition); comp != "" {
		c, cs := fit(comp, "semi", 32, 22, TW-128)
		tracked(img, 64, 124, c, face(cs, "semi"), cs, col(P.Muted), 0.04, "ls")
	}

	// ── The two sides ──
	sides := facts.Sides
	if len(sides) > 2 {
		sides = sides[:2]
	}
	for len(sides) < 2 {
		sides = append(sides, Side{Club: "?"})
	}
	decided := facts.Outcome == "win" || facts.Outcome == "loss"
	const badgeSize = 200
	centres := []float64{TW * 0.25, TW * 0.75}
	anyScore := false
	for i, side := range sides {
		cx := centres[i]
		b := clubBadge(side, look, badgeSize, P)
		paste(img, b, int(cx)-b.Rect.Dx()/2, 170+(badgeSize-b.Rect.Dy())/2, nil)
		name, ns := fit(strings.ToUpper(side.Club), "black", 64, 34, TW*0.44)
		c := white
		if decided && !side.Won {
			c = col(P.Muted)
		}
		text(img, cx, 452, name, face(ns, "black"), c, "ms")
		if side.Score != "" {
			anyScore = true
			text(img, cx, 548, side.Score, face(88, "black"), c, "ms")
		}
	}
	text(img, TW/2, 300, "V", face(72, "black"), col(P.Hi), "mm")

	// ── The result ──
	if word, ok := outcomeWord[facts.Outcome]; ok && facts.Outcome != "live" {
		line := word
		if facts.Margin != "" {
			line += " " + strings.ToUpper(facts.Margin)
		}
		y := 640.0
		if !anyScore {
			y = 560
		}
		l, ls := fit(line, "black", 60, 36, TW-128)
		text(img, TW/2, y, l, face(ls, "black"), col(P.Hi), "ms")
	}
	return img
}

// SaveThumbnail writes the thumbnail as a JPEG (YouTube takes up to 2 MB; a PNG of this
// can pass that, a JPEG at this quality is a few hundred KB).
func SaveThumbnail(facts Facts, look Look, outPath string) (string, error) {
	img := Thumbnail(facts, look)
	if dir := filepath.Dir(outPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	}
	f, err := os.Create(outPath)
	if err != nil {
		return "", err
	}
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 90}); err != nil {
		f.Close()
		return "", err
	}
	return outPath, f.Close()
}
