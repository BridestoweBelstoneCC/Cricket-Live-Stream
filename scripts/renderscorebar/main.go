// Command renderscorebar renders every scorebar style to a PNG, headlessly, with fixed
// mock data.
//
// Why this exists: the scorebar styles are pure CSS over a shared DOM, and the failure
// mode is always visual, never a syntax error — a team colour that vanishes into the
// background, an inline colour from applyColours() winning over the stylesheet, one
// style's full-width accent covering another element's. None of that shows up in go vet
// or the test suite. Two real bugs found by rendering during the 2026-09-24 style pass:
// Minimal's bowler name was white-on-white (applyColours sets color:#fff inline), and
// Impact's footer rule painted straight over the striker underline.
//
// It stubs fetch() rather than talking to a server, so every style renders the IDENTICAL
// match state — what changes between two shots is the design, not the score. The state
// is a deliberately busy one (2nd-innings chase, full ball ticker, target block visible,
// bowler figures), because that's the most crowded the bar ever gets.
//
//	go run ./scripts/renderscorebar                  # all styles -> examples/
//	go run ./scripts/renderscorebar modern impact    # just these
//	go run ./scripts/renderscorebar -out-dir /tmp    # somewhere else
//
// Needs Chrome or Edge installed; no running server.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

var styles = []string{"classic", "modern", "impact", "minimal", "franchise", "neon", "block"}

var browsers = []string{
	`C:\Program Files\Google\Chrome\Application\chrome.exe`,
	`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
	filepath.Join(os.Getenv("LOCALAPPDATA"), `Google\Chrome\Application\chrome.exe`),
	`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
	`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
	"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
}

func findBrowser() string {
	for _, name := range []string{"google-chrome", "chromium", "chromium-browser", "microsoft-edge"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	for _, p := range browsers {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// Club-agnostic sample data — no real club names here, same rule as DEFAULT_STATE and
// config.example.ini (CLAUDE.md's "no hardcoded club identity" gotcha).
func state(style string) map[string]any {
	s := map[string]any{
		"home_team": "Home CC", "away_team": "Rival CC",
		"home_abbrev": "HOME", "away_abbrev": "RIV",
		"home_colour": "#1a3a5c", "away_colour": "#7b2d2d",
		"max_overs": 20, "scorebar_style": style,
		"home_club_id": "", "away_club_id": "",
		"sponsor_name": "", "sponsor_id": "", "anthropic_key_set": false,
		"camera2_configured": false, "drinks_over": 0, "replay_enabled": false,
	}
	for _, g := range []string{"fow", "partnership", "lineup", "boundary_flash", "milestones",
		"innings_summary", "commentary", "commentary_over", "over_summary", "partnership_display",
		"runrate_trend", "league_table", "player_card", "camera_auto_cut"} {
		s["graphics_"+g] = false
	}
	return s
}

var live = map[string]any{
	"source": "pcs", "feed": "file", "club_id": 0, "events": []any{},
	"state": map[string]any{
		"battingTeamName": "Rival CC", "bowlingTeamName": "Home CC",
		"innings": 2, "score": 137, "wickets": 4, "overs": 15.3,
		"batter1": map[string]any{"name": "J Hatton", "number": "21", "runs": 48, "balls": 31,
			"onStrike": true, "sr": 154.8, "fours": 5, "sixes": 2, "howOut": ""},
		"batter2": map[string]any{"name": "T Gostling", "number": "7", "runs": 23, "balls": 19,
			"onStrike": false, "sr": 121.0, "fours": 2, "sixes": 0, "howOut": ""},
		"bowler":     map[string]any{"name": "S Ewen", "overs": "3.3", "runs": 28, "wickets": 2, "maidens": 0},
		"statusText": "Needs 31 off 27", "run_rate": 8.84,
		"targetRuns": 168, "runsRequired": 31, "ballsRemaining": 27,
		"partnership_runs": 44, "partnership_balls": 28,
		"lastWicketHowOut": "", "lastWicketBatter": "", "lastWicketBowler": "",
		"lastWicketFielder": "", "lastWicketType": "",
		"last_ball": "1 4 . 6 W 2", "last_over_runs": 13, "over_history": "",
		"pcs_overs": 15.3, "card": map[string]any{"batters": []any{}, "bowlers": []any{}},
		"home_abbrev": "HOME", "away_abbrev": "RIV",
	},
}

const stub = `
<script>
(function(){
  const STATE = __STATE__, LIVE = __LIVE__;
  const j = (o) => Promise.resolve({ ok: true, status: 200,
      json: () => Promise.resolve(o), text: () => Promise.resolve(JSON.stringify(o)) });
  window.fetch = function(url) {
    url = String(url);
    if (url.includes('/state'))    return j(STATE);
    if (url.includes('/live'))     return j(LIVE);
    if (url.includes('/commands')) return j({commands: []});
    return j({});
  };
  // Hide everything that isn't the scorebar — the rotating panels would otherwise cover it
  // at whatever point in their cycle the screenshot lands.
  window.addEventListener('load', function(){
    setTimeout(function(){
      var sel = '.graphic-panel, #partnership-display, #runrate-display,'
              + '#over-commentary-display, #commentary-lower, #weather-widget, #vs-badge,'
              + '#innings-pill, #target-banner, #replay-banner, #event-flash, #sponsor-strap';
      document.querySelectorAll(sel).forEach(function(e){ e.style.display = 'none'; });
    }, 900);
  });
})();
</script>
`

func render(style, outPNG, browser string) bool {
	b, err := os.ReadFile("overlay.html")
	if err != nil {
		fmt.Printf("  ✗  %v (run this from the repository root)\n", err)
		return false
	}
	html := string(b)
	st, _ := json.Marshal(state(style))
	lv, _ := json.Marshal(live)
	s := strings.Replace(strings.Replace(stub, "__STATE__", string(st), 1), "__LIVE__", string(lv), 1)
	// Inject ahead of the overlay's own first <script> so the stub is installed before any
	// of its code runs and tries a real fetch.
	idx := strings.Index(html, "<script>")
	if idx < 0 {
		fmt.Println("  ✗  overlay.html has no <script> block")
		return false
	}
	html = html[:idx] + s + html[idx:]
	tmp := filepath.Join(os.TempDir(), "_scorebar_render_"+style+".html")
	if err := os.WriteFile(tmp, []byte(html), 0o644); err != nil {
		fmt.Printf("  ✗  %v\n", err)
		return false
	}
	defer os.Remove(tmp)
	os.Remove(outPNG)
	abs, _ := filepath.Abs(outPNG)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	exec.CommandContext(ctx, browser, "--headless=new", "--disable-gpu", "--hide-scrollbars",
		"--force-device-scale-factor=1", "--window-size=1920,1080",
		"--virtual-time-budget=4000", "--screenshot="+abs,
		"file:///"+strings.ReplaceAll(tmp, `\`, "/")).Run()
	_, err = os.Stat(abs)
	return err == nil
}

// cropToBar keeps the bottom 72px: the bar itself.
func cropToBar(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	im, err := png.Decode(f)
	f.Close()
	if err != nil {
		return err
	}
	b := im.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx(), 72))
	draw.Draw(out, out.Rect, im, image.Pt(b.Min.X, b.Max.Y-72), draw.Src)
	w, err := os.Create(path)
	if err != nil {
		return err
	}
	defer w.Close()
	return png.Encode(w, out)
}

func main() {
	outDir := flag.String("out-dir", "examples", "where to write the PNGs")
	full := flag.Bool("full", false, "keep the whole 1920x1080 frame instead of cropping to the bar")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: go run ./scripts/renderscorebar [-out-dir DIR] [-full] [style ...]\n  styles: %s\n", strings.Join(styles, ", "))
	}
	flag.Parse()

	browser := findBrowser()
	if browser == "" {
		fmt.Println("  ✗  No Chrome/Edge found — install one, or add its path to browsers.")
		os.Exit(1)
	}
	want := flag.Args()
	if len(want) == 0 {
		want = styles
	}
	var unknown []string
	for _, s := range want {
		if !slices.Contains(styles, s) {
			unknown = append(unknown, s)
		}
	}
	if len(unknown) > 0 {
		fmt.Printf("  ✗  Unknown style(s): %s (known: %s)\n", strings.Join(unknown, ", "), strings.Join(styles, ", "))
		os.Exit(1)
	}

	os.MkdirAll(*outDir, 0o755)
	fmt.Printf("  Browser: %s\n", browser)
	failed := 0
	for _, style := range want {
		out := filepath.Join(*outDir, "scorebar-"+style+".png")
		if !render(style, out, browser) {
			fmt.Printf("  ✗  %s: no screenshot produced\n", style)
			failed++
			continue
		}
		if !*full {
			if err := cropToBar(out); err != nil {
				fmt.Printf("  ✗  %s: %v\n", style, err)
				failed++
				continue
			}
		}
		fmt.Printf("  OK   %s  ->  %s\n", style, out)
	}
	if failed > 0 {
		os.Exit(1)
	}
}
