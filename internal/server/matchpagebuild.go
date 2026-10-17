package server

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cricketstream/internal/matchpage"
	"cricketstream/internal/resultcard"
	"cricketstream/internal/winpredict"
)

// ── Match page ──
// One self-contained HTML page per match: result, result card, AI report, scorecard from
// the scorer's own figures, runs-by-over chart, a link to the stream, sponsors. Built
// about a minute after the stream ends, or from the panel. Every part is optional: a
// failed AI report or card just leaves that section out.
var (
	matchPageStatus = M{"running": false, "ok": nil, "message": "", "file": nil}
	matchPageMu     sync.Mutex
)

func b64File(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// todaysStreamURL is the club's YouTube video for today's stream, best-effort.
func todaysStreamURL() string {
	if !fileExists(YTCredsFile) || !fileExists(YTTokenFile) {
		return ""
	}
	yt, _ := ytService(false)
	if yt == nil {
		return ""
	}
	today := time.Now().Format("2006-01-02")
	for _, status := range []string{"active", "completed"} {
		r, err := yt.Get("liveBroadcasts", url.Values{"part": {"id,snippet"}, "broadcastStatus": {status},
			"broadcastType": {"all"}, "maxResults": {"10"}})
		if err != nil {
			return ""
		}
		for _, b := range items(r) {
			if strings.HasPrefix(gs(gm(b, "snippet"), "actualStartTime", ""), today) {
				return "https://youtu.be/" + gs(b, "id", "")
			}
		}
	}
	return ""
}

func resultSentence(facts M) string {
	switch o := gs(facts, "outcome", ""); o {
	case "tie":
		return "Match tied"
	case "draw":
		return "Match drawn"
	case "abandoned", "cancelled":
		return "Match " + o
	}
	for _, s := range toMs(gl(facts, "sides")) {
		if gb(s, "won", false) {
			return strings.TrimSpace(fmt.Sprintf("%s won %s", gs(s, "club", ""), gs(facts, "margin", "")))
		}
	}
	return "Match in progress"
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// BuildMatchPage gathers everything and writes the page: (ok, message, path).
func BuildMatchPage() (bool, string, string) {
	cfg := LoadState()
	facts := GenerateSocialGraphicFacts()
	if !gb(facts, "ok", false) {
		return false, gsOr(facts, "error", "No match data yet"), ""
	}
	mid := CurrentMatchID()
	db := MatchFactsFromDB(mid)
	sides := toMs(gl(facts, "sides"))
	dbInn := gm(db, "innings")
	var innings []matchpage.Innings
	for i, key := range sortedKeys(dbInn) {
		rec := dbInn[key].(M)
		team := gs(rec, "batting_team", "")
		if i < len(sides) && gs(sides[i], "club", "") != "" {
			team = gs(sides[i], "club", "")
		}
		inn := matchpage.Innings{Team: team,
			Total: fmt.Sprintf("%s-%s (%s ov)", anyStr(rec, "score", "0"), anyStr(rec, "wickets", "0"), anyStr(rec, "overs", "None"))}
		for _, b := range toMs(gl(gm(db, "batters"), key)) {
			inn.Batters = append(inn.Batters, matchpage.Batter{Name: gs(b, "name", ""), Runs: gi(b, "runs", 0), Balls: gi(b, "balls", 0), Out: gb(b, "out", false)})
		}
		for _, b := range toMs(gl(gm(db, "bowlers"), key)) {
			inn.Bowlers = append(inn.Bowlers, matchpage.Bowler{Name: gs(b, "name", ""), O: gs(b, "o", ""), R: gi(b, "r", 0), W: gi(b, "w", 0)})
		}
		for _, f := range toMs(gl(db, "fall_of_wickets")) {
			if gs(f, "innings", "") == key {
				inn.FOW = append(inn.FOW, matchpage.FOW{Score: gs(f, "score", ""), Batter: gs(f, "batter", "")})
			}
		}
		innings = append(innings, inn)
	}
	// The worm — never past the innings' own overs: a re-scored or replayed innings can
	// leave balls logged beyond where it finished.
	worm := map[string][]matchpage.Point{}
	withDB(func(tx *sql.Tx) error {
		rows, err := tx.Query("SELECT innings, over, MAX(cum_runs) FROM balls WHERE match_id=? GROUP BY innings, over ORDER BY innings, over", mid)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var inn, over int
			var runs sql.NullInt64
			rows.Scan(&inn, &over, &runs)
			key := strconv.Itoa(inn)
			if rec, ok := dbInn[key].(M); ok {
				if f, err := strconv.ParseFloat(gs(rec, "overs", ""), 64); err == nil && float64(over+1) > math.Ceil(f) {
					continue
				}
			}
			worm[key] = append(worm[key], matchpage.Point{over + 1, int(runs.Int64)})
		}
		return nil
	})
	report := ""
	if r := GenerateMatchReport("report"); gb(r, "ok", false) {
		report = gs(r, "text", "")
	}
	card := ""
	func() {
		defer func() { recover() }()
		photos := listSocialPhotos("")
		photo := ""
		if len(photos) > 0 {
			dir := socialsDir("")
			best := -1.0
			for _, p := range photos {
				if t := mtime(filepath.Join(dir, p)); t > best {
					best, photo = t, filepath.Join(dir, p)
				}
			}
		}
		tmp := filepath.Join(os.TempDir(), "match_page_card.png")
		if _, err := BuildInstagramImage(facts, photo, tmp); err == nil {
			card, _ = b64File(tmp)
		}
	}()
	crest := ""
	if p := badgeFile(logosFolder(cfg), trim(gsOr(cfg, "home_club_id", ""))); p != "" {
		crest, _ = b64File(p)
	}
	var sponsors []string
	for _, p := range sponsorLogosForCard(sponsorsFolder(cfg), gs(cfg, "sponsor_id", "")) {
		if b, err := b64File(p); err == nil {
			sponsors = append(sponsors, b)
		}
	}
	hi := resultcard.NewPalette(gs(cfg, "home_colour", "#1a3a5c")).Hi
	accent := fmt.Sprintf("#%02x%02x%02x", hi[0], hi[1], hi[2])
	var names []string
	for i, s := range sides {
		if i < 2 {
			names = append(names, gs(s, "club", ""))
		}
	}
	title := strings.Join(names, " v ")
	if title == "" {
		title = gsOr(cfg, "home_team", "Match")
	}
	date := gs(facts, "date", "")
	if date == "" {
		date = time.Now().Format("02 Jan 2006")
	}
	html := matchpage.Render(matchpage.Data{Club: gs(cfg, "home_team", ""), Title: title, Date: date,
		Competition: gs(facts, "competition", ""), Result: resultSentence(facts), Report: report,
		Innings: innings, Worm: worm, Colours: []string{accent, "#d9534f"}, CardPNGB64: card,
		CrestB64: crest, SponsorsB64: sponsors, VideoURL: todaysStreamURL(), Accent: accent})
	if err := os.MkdirAll(MatchPagesDir, 0o755); err != nil {
		return false, err.Error(), ""
	}
	slug := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(title), "-"), "-")
	slug = truncRunes(slug, 60)
	if slug == "" {
		slug = "match"
	}
	path := filepath.Join(MatchPagesDir, fmt.Sprintf("%s_%s.html", time.Now().Format("2006-01-02"), slug))
	if err := os.WriteFile(path, []byte(html), 0o644); err != nil {
		return false, err.Error(), ""
	}
	return true, "Match page saved: " + path, path
}

// StartMatchPageBuild builds in the background unless a build is already running
// (false = busy). Locked: the stream-end timer and the panel's button can arrive
// together.
func StartMatchPageBuild() bool {
	matchPageMu.Lock()
	if gb(matchPageStatus, "running", false) {
		matchPageMu.Unlock()
		return false
	}
	matchPageStatus["running"], matchPageStatus["ok"], matchPageStatus["message"] = true, nil, ""
	matchPageMu.Unlock()
	go func() {
		ok, msg, path := false, "", ""
		func() {
			defer func() {
				if r := recover(); r != nil {
					msg = truncRunes(fmt.Sprint(r), 200)
				}
			}()
			ok, msg, path = BuildMatchPage()
		}()
		matchPageMu.Lock()
		var file any
		if path != "" {
			file = path
		}
		matchPageStatus = M{"running": false, "ok": ok, "message": msg, "file": file}
		matchPageMu.Unlock()
		icon := "✗"
		if ok {
			icon = "✓"
		}
		say("  %s  Match page: %s", icon, msg)
	}()
	return true
}

func matchPageAfterStreamEnd() {
	if !streamingNow() {
		StartMatchPageBuild()
	}
}

func latestMatchPage() string {
	pages, _ := filepath.Glob(filepath.Join(MatchPagesDir, "*.html"))
	sort.SliceStable(pages, func(i, j int) bool { return mtime(pages[i]) < mtime(pages[j]) })
	if len(pages) == 0 {
		return ""
	}
	return pages[len(pages)-1]
}

// ── Win predictor ──
// The league model is built from the match's own format (40-over, 50-over…) when there
// are at least winModelFormatMin games of it, else from all formats pooled.
const winModelFormatMin = 10

var (
	winHistoryMtime float64
	winHistoryRows  []winpredict.Innings
	winHistoryMu    sync.Mutex
)

func toInnings(rows []any) []winpredict.Innings {
	b, _ := json.Marshal(rows)
	var out []winpredict.Innings
	json.Unmarshal(b, &out)
	return out
}

// seasonInningsHistory is this season's innings summaries: from the in-memory season
// stats, else straight from their cache file — which survives a restart, so the
// predictor doesn't go quiet until someone refreshes the season stats.
func seasonInningsHistory() []winpredict.Innings {
	seasonStatsMu.Lock()
	rows := gl(seasonStats, "innings_history")
	seasonStatsMu.Unlock()
	if len(rows) > 0 {
		return toInnings(rows)
	}
	mt := mtime(SeasonStatsCache)
	if mt == 0 {
		return nil
	}
	winHistoryMu.Lock()
	defer winHistoryMu.Unlock()
	if winHistoryMtime != mt {
		b, err := os.ReadFile(SeasonStatsCache)
		if err != nil {
			return nil
		}
		var d M
		if json.Unmarshal(b, &d) != nil {
			return nil
		}
		winHistoryMtime, winHistoryRows = mt, toInnings(gl(d, "innings_history"))
	}
	return append([]winpredict.Innings(nil), winHistoryRows...)
}

// ResetWinHistoryCache forgets the cached history file (tests).
func ResetWinHistoryCache() {
	winHistoryMu.Lock()
	winHistoryMtime, winHistoryRows = 0, nil
	winHistoryMu.Unlock()
}

// WinPrediction is the predictor's result for a /live state, or nil.
func WinPrediction(state, cfg M) *winpredict.Prediction {
	if cfg == nil {
		cfg = LoadState()
	}
	if !gb(cfg, "graphics_win_predictor", true) {
		return nil
	}
	innings := gi(state, "innings", 1)
	if innings == 0 {
		innings = 1
	}
	score, wkts := gi(state, "score", 0), gi(state, "wickets", 0)
	overs := valOr(state, "overs", 0)
	if score == 0 && wkts == 0 && winpredict.Balls(overs) == 0 {
		return nil // a pre-match frame
	}
	limit := 0.0
	if rem := gi(state, "ballsRemaining", 0); rem > 0 {
		limit = float64(winpredict.Balls(overs)+rem) / 6
	}
	if limit == 0 {
		limit = float64(gi(cfg, "max_overs", 0))
	}
	history := seasonInningsHistory()
	var same []winpredict.Innings
	for _, h := range history {
		if limit != 0 && float64(h.OversLimit) == math.RoundToEven(limit) {
			same = append(same, h)
		}
	}
	use := history
	if len(same) >= winModelFormatMin {
		use = same
	}
	model := winpredict.LeagueModel(use)
	target := gi(state, "targetRuns", 0)
	if innings >= 2 && target == 0 && gi(state, "runsRequired", 0) > 0 {
		target = score + gi(state, "runsRequired", 0)
	}
	return winpredict.Predict(innings, score, wkts, overs, limit, model, target)
}
