// Package matchpage renders a shareable match page: one self-contained HTML file per
// match (images inlined, no external requests), for the club website or straight into
// a WhatsApp group.
//
// Pure rendering — Render(data) takes everything as plain values (the server's
// buildMatchPage gathers them) and every piece of text is HTML-escaped, so a player's
// name or an AI-written report can never break or inject into the page.
package matchpage

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"cricketstream/internal/htmlesc"
	"cricketstream/internal/pyfmt"
)

var E = htmlesc.E

type Batter struct {
	Name  string `json:"name"`
	Runs  int    `json:"runs"`
	Balls int    `json:"balls"`
	Out   bool   `json:"out"`
}

type Bowler struct {
	Name string `json:"name"`
	O    string `json:"o"`
	R    int    `json:"r"`
	W    int    `json:"w"`
}

type FOW struct {
	Score  string `json:"score"`
	Batter string `json:"batter"`
}

type Innings struct {
	Team    string
	Total   string
	Batters []Batter
	Bowlers []Bowler
	FOW     []FOW
}

// Point is (over, cumulative runs).
type Point [2]int

// Data is everything the page shows; missing parts are left out.
type Data struct {
	Club, Title, Date, Competition, Result, Report string
	Innings                                        []Innings
	Worm                                           map[string][]Point // innings key -> points
	Colours                                        []string
	CardPNGB64, CrestB64                           string
	SponsorsB64                                    []string
	VideoURL                                       string
	Accent                                         string
}

func f0(x float64) string { return pyfmt.Fixed(x, 0) }
func f1(x float64) string { return pyfmt.Fixed(x, 1) }

// wormSVG is the run-rate "worm": cumulative runs by over for each innings, as inline
// SVG, with a legend (labels[i] names the i-th innings) and over numbers along the
// bottom.
func wormSVG(worm map[string][]Point, colours, labels []string) string {
	const width, height = 760, 320
	keys := make([]string, 0, len(worm))
	for k, pts := range worm {
		if len(pts) > 0 {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	maxOver, maxRuns := 0, 0
	for _, k := range keys {
		for _, p := range worm[k] {
			maxOver, maxRuns = max(maxOver, p[0]), max(maxRuns, p[1])
		}
	}
	if maxOver == 0 {
		maxOver = 1
	}
	if maxRuns == 0 {
		maxRuns = 1
	}
	const padL, padB, padT, padR = 48, 34, 16, 16
	w, h := float64(width-padL-padR), float64(height-padT-padB)
	xy := func(over, runs int) (float64, float64) {
		return padL + w*float64(over)/float64(maxOver), padT + h - h*float64(runs)/float64(maxRuns)
	}
	var out strings.Builder
	fmt.Fprintf(&out, `<svg viewBox="0 0 %d %d" role="img" aria-label="Runs by over" style="width:100%%;height:auto">`, width, height)
	for i := 0; i < 5; i++ {
		y := padT + h*float64(i)/4
		fmt.Fprintf(&out, `<line x1="%d" y1="%s" x2="%d" y2="%s" stroke="currentColor" stroke-opacity="0.12"/>`,
			padL, f0(y), width-padR, f0(y))
		fmt.Fprintf(&out, `<text x="%d" y="%s" text-anchor="end" font-size="12" fill="currentColor" fill-opacity="0.6">%d</text>`,
			padL-8, f0(y+4), int(math.RoundToEven(float64(maxRuns)*float64(4-i)/4)))
	}
	step := max(1, (maxOver+9)/10) // at most ~10 over numbers, whatever the format
	for o := step; o <= maxOver; o += step {
		x, _ := xy(o, 0)
		fmt.Fprintf(&out, `<text x="%s" y="%s" text-anchor="middle" font-size="12" fill="currentColor" fill-opacity="0.6">%d</text>`,
			f0(x), f0(padT+h+16), o)
	}
	fmt.Fprintf(&out, `<text x="%s" y="%d" text-anchor="middle" font-size="12" fill="currentColor" fill-opacity="0.6">Overs</text>`,
		f0(padL+w/2), height-4)
	for idx, k := range keys {
		col := colours[idx%len(colours)]
		if idx < len(labels) && labels[idx] != "" {
			ly := padT + 4 + idx*20
			fmt.Fprintf(&out, `<rect x="%d" y="%d" width="14" height="4" rx="2" fill="%s"/><text x="%d" y="%d" font-size="13" fill="currentColor">%s</text>`,
				padL+12, ly, E(col), padL+32, ly+6, E(labels[idx]))
		}
		x0, y0 := xy(0, 0)
		pts := []string{f1(x0) + "," + f1(y0)}
		for _, p := range worm[k] {
			x, y := xy(p[0], p[1])
			pts = append(pts, f1(x)+","+f1(y))
		}
		fmt.Fprintf(&out, `<polyline points="%s" fill="none" stroke="%s" stroke-width="3" stroke-linejoin="round" stroke-linecap="round"/>`,
			strings.Join(pts, " "), E(col))
	}
	out.WriteString("</svg>")
	return out.String()
}

func inningsTable(inn Innings) string {
	var rows, bowl strings.Builder
	for _, b := range inn.Batters {
		star := "*"
		if b.Out {
			star = ""
		}
		fmt.Fprintf(&rows, "<tr><td>%s</td><td class=num>%d%s</td><td class=num>%d</td></tr>", E(b.Name), b.Runs, star, b.Balls)
	}
	for _, b := range inn.Bowlers {
		fmt.Fprintf(&bowl, "<tr><td>%s</td><td class=num>%s</td><td class=num>%d</td><td class=num>%d</td></tr>", E(b.Name), E(b.O), b.R, b.W)
	}
	var fow []string
	for _, f := range inn.FOW {
		fow = append(fow, fmt.Sprintf("%s (%s)", E(f.Score), E(f.Batter)))
	}
	s := fmt.Sprintf(`<section class=card><h3>%s <span class=total>%s</span></h3>`+
		`<table><thead><tr><th>Batting</th><th class=num>R</th><th class=num>B</th></tr></thead>`+
		`<tbody>%s</tbody></table>`, E(inn.Team), E(inn.Total), rows.String())
	if bowl.Len() > 0 {
		s += `<table><thead><tr><th>Bowling</th><th class=num>O</th><th class=num>R</th>` +
			`<th class=num>W</th></tr></thead><tbody>` + bowl.String() + `</tbody></table>`
	}
	if len(fow) > 0 {
		s += `<p class=muted>Fall of wickets: ` + strings.Join(fow, ", ") + `</p>`
	}
	return s + "</section>"
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// Render builds the page.
func Render(d Data) string {
	accent := or(d.Accent, "#2f6db3")
	var report strings.Builder
	for _, p := range strings.Split(d.Report, "\n") {
		if p = strings.TrimSpace(p); p != "" && !strings.HasPrefix(p, "#") {
			report.WriteString("<p>" + E(p) + "</p>")
		}
	}
	crest, card, video := "", "", ""
	if d.CrestB64 != "" {
		crest = `<img class=crest src="data:image/png;base64,` + d.CrestB64 + `" alt="">`
	}
	if d.CardPNGB64 != "" {
		card = `<img class=card-img src="data:image/png;base64,` + d.CardPNGB64 + `" alt="Result card">`
	}
	if d.VideoURL != "" {
		video = `<a class=watch href="` + E(d.VideoURL) + `">&#9654; Watch the full stream</a>`
	}
	var sponsors strings.Builder
	for _, s := range d.SponsorsB64 {
		sponsors.WriteString(`<img src="data:image/png;base64,` + s + `" alt="">`)
	}
	colours := d.Colours
	if len(colours) == 0 {
		colours = []string{accent, "#d9534f"}
	}
	var labels []string
	var innings strings.Builder
	for _, i := range d.Innings {
		labels = append(labels, i.Team)
		innings.WriteString(inningsTable(i))
	}
	worm := wormSVG(d.Worm, colours, labels)
	var meta []string
	for _, x := range []string{d.Competition, d.Date} {
		if x != "" {
			meta = append(meta, x)
		}
	}
	section := func(cond bool, s string) string {
		if cond {
			return s
		}
		return ""
	}
	return `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>` + E(or(d.Title, "Match report")) + `</title>
<style>
:root { --bg:#f6f7f9; --fg:#14181f; --muted:#5b6472; --card:#fff; --line:#e3e6eb; --accent:` + E(accent) + `; }
@media (prefers-color-scheme: dark) { :root { --bg:#0f141b; --fg:#e8ecf1; --muted:#98a2b3; --card:#171e27; --line:#263241; } }
* { box-sizing:border-box }
body { margin:0; background:var(--bg); color:var(--fg); font:16px/1.55 system-ui,-apple-system,"Segoe UI",sans-serif }
main { max-width:820px; margin:0 auto; padding:24px 16px 48px }
header { display:flex; gap:16px; align-items:center; margin-bottom:18px }
.crest { width:72px; height:72px; object-fit:contain }
h1 { font-size:26px; margin:0; line-height:1.2 }
.muted { color:var(--muted); font-size:14px }
.result { font-size:20px; font-weight:700; color:var(--accent); margin:6px 0 0 }
.card { background:var(--card); border:1px solid var(--line); border-radius:12px; padding:18px; margin:16px 0 }
.card-img { width:100%; max-width:540px; display:block; margin:16px auto; border-radius:12px }
h2 { font-size:18px; margin:0 0 10px }
h3 { font-size:17px; margin:0 0 10px; display:flex; justify-content:space-between; gap:8px }
.total { color:var(--accent) }
table { width:100%; border-collapse:collapse; margin:6px 0 12px; font-size:15px }
th, td { text-align:left; padding:6px 4px; border-bottom:1px solid var(--line) }
th { font-size:12px; text-transform:uppercase; letter-spacing:.06em; color:var(--muted) }
.num { text-align:right; width:56px; font-variant-numeric:tabular-nums }
.watch { display:inline-block; margin:6px 0 0; padding:10px 16px; border-radius:8px;
         background:var(--accent); color:#fff; text-decoration:none; font-weight:700 }
.sponsors { display:flex; flex-wrap:wrap; gap:18px; justify-content:center; align-items:center;
            background:#fff; border-radius:12px; padding:16px; margin-top:24px }
.sponsors img { max-height:52px; max-width:150px; object-fit:contain }
footer { text-align:center; margin-top:18px }
</style></head>
<body><main>
<header>` + crest + `<div><h1>` + E(d.Title) + `</h1>
<div class=muted>` + E(strings.Join(meta, " · ")) + `</div>
<div class=result>` + E(d.Result) + `</div>` + video + `</div></header>
` + card + `
` + section(report.Len() > 0, `<section class=card><h2>Match report</h2>`+report.String()+`</section>`) + `
` + section(worm != "", `<section class=card><h2>Runs by over</h2>`+worm+`</section>`) + `
` + innings.String() + `
` + section(sponsors.Len() > 0, `<div class=sponsors>`+sponsors.String()+`</div>`) + `
<footer class=muted>` + E(d.Club) + ` · streamed live with CricketStream</footer>
</main></body></html>
`
}
