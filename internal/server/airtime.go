package server

// Sponsor airtime: how long the weekend sponsor's strap was actually on screen, for the
// club to pass on to the sponsor. The overlay reports each show/hide (it's the only thing
// that knows: the strap rides on whichever end-of-over panels the rotation picks). The
// server times the gap on its own clock, so a slow overlay can't stretch it, and takes
// the sponsor's name from state, not the request. Only time while the stream was LIVE is
// ever presented as airtime; time before going live (a rehearsal) is kept separately.

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cricketstream/internal/pyfmt"
	"cricketstream/internal/pyval"
)

// SponsorShowMaxSec caps one appearance. The panels it rides on last 5-9s (plus a fade).
// A missed "hide" (overlay reloaded mid-panel, a failed beacon) is only closed by the
// next "show" minutes later, so whatever this cap is gets added as phantom airtime —
// keep it near the longest panel. It was 60, which over-reported to the sponsor.
const SponsorShowMaxSec = 12

type airOpen struct {
	sponsor string
	start   float64
	onAir   bool
	day     string
}

var (
	sponsorAirOpen *airOpen
	sponsorAirMu   sync.Mutex
)

func loadAirtime() M {
	b, err := os.ReadFile(SponsorAirtimeFile)
	if err != nil {
		return M{}
	}
	var d M
	if json.Unmarshal(b, &d) != nil || d == nil {
		return M{}
	}
	return d
}

func saveAirtime(data M) error {
	b, err := json.MarshalIndent(data, "", " ")
	if err != nil {
		return err
	}
	tmp := SponsorAirtimeFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, SponsorAirtimeFile)
}

func streamingNow() bool {
	streamMonMu.Lock()
	defer streamMonMu.Unlock()
	return gb(streamMon, "streaming", false)
}

// SponsorAirtimeEvent records the strap appearing ("show") or going ("hide"). Never
// fails: it sits behind an overlay beacon and must never disturb the graphics.
func SponsorAirtimeEvent(event string, t float64) {
	defer func() {
		if r := recover(); r != nil {
			say("  ✗  Sponsor airtime not recorded: %v", r)
		}
	}()
	if t == 0 {
		t = now()
	}
	sponsor := trim(gsOr(LoadState(), "sponsor_name", ""))
	live := streamingNow()
	sponsorAirMu.Lock()
	defer sponsorAirMu.Unlock()
	var data M
	changed := false
	if op := sponsorAirOpen; op != nil {
		// Closes on "hide", and also on a second "show" with no hide in between.
		secs := math.Min(math.Max(t-op.start, 0), SponsorShowMaxSec)
		data = loadAirtime()
		day := gm(data, op.day)
		data[op.day] = day
		rec, ok := day[op.sponsor].(map[string]any)
		if !ok {
			rec = M{"on_air_sec": 0.0, "on_air_shows": 0, "off_air_sec": 0.0, "off_air_shows": 0}
			day[op.sponsor] = rec
		}
		// Live at either end counts: the stream monitor polls every 15s, so a strap shown
		// just as the stream went live shouldn't fall through.
		key := "off_air"
		if op.onAir || live {
			key = "on_air"
		}
		rec[key+"_sec"] = pyfmt.Round(gf(rec, key+"_sec", 0)+secs, 1)
		rec[key+"_shows"] = gi(rec, key+"_shows", 0) + 1
		if key == "on_air" {
			if v, ok := liveViewers(t); ok {
				// Seconds on screen x people watching. Kept with the seconds it covers, so
				// the report never stretches a partial sample.
				rec["viewer_min"] = pyfmt.Round(gf(rec, "viewer_min", 0)+secs*float64(v)/60, 1)
				rec["viewer_covered_sec"] = pyfmt.Round(gf(rec, "viewer_covered_sec", 0)+secs, 1)
			}
		}
		rec["last"] = t
		sponsorAirOpen = nil
		changed = true
	}
	if event == "show" && sponsor != "" {
		sponsorAirOpen = &airOpen{sponsor: truncRunes(sponsor, 100), start: t, onAir: live,
			day: time.Unix(int64(t), 0).Format("2006-01-02")}
	}
	if changed {
		if err := saveAirtime(data); err != nil {
			say("  ✗  Sponsor airtime not recorded: %v", err)
		}
	}
}

func fmtDuration(secs float64) string {
	s := int(math.RoundToEven(secs))
	m, s := s/60, s%60
	h, m := m/60, m%60
	if h > 0 {
		return fmt.Sprintf("%d hr %d min", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%d min %d s", m, s)
	}
	return fmt.Sprintf("%d s", s)
}

// thousands is Python's f"{n:,}".
func thousands(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// SponsorAirtimeSummary is one day's totals (today by default), plus a ready-to-send
// line per sponsor.
func SponsorAirtimeSummary(day string) M {
	if day == "" {
		day = time.Now().Format("2006-01-02")
	}
	data := loadAirtime()
	days := sortedKeys(data)
	sort.Sort(sort.Reverse(sort.StringSlice(days)))
	if len(days) > 30 {
		days = days[:30]
	}
	daysAny := make([]any, len(days))
	for i, d := range days {
		daysAny[i] = d
	}
	sponsors := []any{}
	dayRecs := gm(data, day)
	for _, name := range sortedKeys(dayRecs) {
		rec, _ := dayRecs[name].(map[string]any)
		on, shows := gf(rec, "on_air_sec", 0), gi(rec, "on_air_shows", 0)
		vmin, covered := gf(rec, "viewer_min", 0), gf(rec, "viewer_covered_sec", 0)
		line := fmt.Sprintf("%s wasn't on screen while the stream was live.", name)
		if shows > 0 {
			line = fmt.Sprintf("%s was on screen for %s of the live stream, across %d appearance%s.",
				name, fmtDuration(on), shows, map[bool]string{true: "s", false: ""}[shows != 1])
		}
		if shows > 0 && covered != 0 {
			// An estimate, and worded as one: YouTube's live viewer count (sampled each
			// minute) x the seconds on screen. Only the time a fresh count covered is used;
			// if that's not all of it, say so rather than scale it up.
			vm := thousands(int(math.RoundToEven(vmin)))
			if covered >= on-1 {
				line = line[:len(line)-1] + fmt.Sprintf(" — an estimated %s viewer-minutes (YouTube's live viewer count × time on screen).", vm)
			} else {
				line = line[:len(line)-1] + fmt.Sprintf(" — an estimated %s viewer-minutes over the %s of it that had a YouTube viewer count.", vm, fmtDuration(covered))
			}
		}
		sponsors = append(sponsors, M{"sponsor": name, "on_air_sec": on, "on_air_shows": shows,
			"off_air_sec": gf(rec, "off_air_sec", 0), "off_air_shows": gi(rec, "off_air_shows", 0),
			"viewer_minutes": int(math.RoundToEven(vmin)), "viewer_covered_sec": covered, "line": line})
	}
	var lv any
	if v, ok := liveViewers(0); ok {
		lv = v
	}
	return M{"date": day, "days": daysAny, "sponsors": sponsors, "live_viewers": lv}
}

// ── Live viewer count (for sponsor viewer-minutes) ──
// While live, and only if YouTube is already authorised here, YouTube's concurrent
// viewers for the ACTIVE broadcast, once a minute (~1 quota unit a call). Never
// authorises interactively — no browser popping up mid-match.
const (
	viewerPollSec  = 60
	viewerFreshSec = 180 // a count older than this isn't applied to an appearance
)

var (
	viewers = struct {
		count   int
		has     bool
		at      float64
		video   string
		yt      *YTClient
		err     string
		started bool
	}{}
	viewersMu sync.Mutex
)

// liveViewers is the latest concurrent-viewer count if it's fresh.
func liveViewers(t float64) (int, bool) {
	if t == 0 {
		t = now()
	}
	viewersMu.Lock()
	defer viewersMu.Unlock()
	if viewers.has && t-viewers.at <= viewerFreshSec {
		return viewers.count, true
	}
	return 0, false
}

func viewerTick(t float64) {
	if t == 0 {
		t = now()
	}
	if !streamingNow() {
		viewersMu.Lock()
		viewers.video = "" // the next stream may be a different broadcast
		viewersMu.Unlock()
		return
	}
	if !fileExists(YTCredsFile) || !fileExists(YTTokenFile) {
		return
	}
	viewersMu.Lock()
	yt, video := viewers.yt, viewers.video
	viewersMu.Unlock()
	if yt == nil {
		var msg string
		if yt, msg = ytService(false); yt == nil {
			viewersMu.Lock()
			viewers.err = msg
			viewersMu.Unlock()
			return
		}
	}
	if video == "" {
		// Only an ACTIVE broadcast: unlike the title updater, never guess at the newest
		// one, or a sponsor could be credited with a past stream's audience.
		r, err := yt.Get("liveBroadcasts", url.Values{"part": {"id"}, "broadcastStatus": {"active"},
			"broadcastType": {"all"}, "maxResults": {"1"}})
		if err != nil {
			viewersMu.Lock()
			viewers.err = truncRunes(err.Error(), 200)
			viewersMu.Unlock()
			return
		}
		it := items(r)
		if len(it) == 0 {
			return
		}
		video = gs(it[0], "id", "")
	}
	r, err := yt.Get("videos", url.Values{"part": {"liveStreamingDetails"}, "id": {video}})
	if err != nil {
		viewersMu.Lock()
		viewers.err = truncRunes(err.Error(), 200)
		viewersMu.Unlock()
		return
	}
	found := items(r)
	det := M{}
	if len(found) > 0 {
		det = gm(found[0], "liveStreamingDetails")
	}
	viewersMu.Lock()
	defer viewersMu.Unlock()
	viewers.yt, viewers.err = yt, ""
	viewers.video = video
	if len(found) == 0 || pyval.Truthy(det["actualEndTime"]) {
		viewers.video = ""
	}
	if c, ok := det["concurrentViewers"]; ok && c != nil {
		if n, err := pyval.Int(c); err == nil {
			viewers.count, viewers.has, viewers.at = n, true, t
		}
	}
}

func StartViewerSampler() {
	viewersMu.Lock()
	if viewers.started {
		viewersMu.Unlock()
		return
	}
	viewers.started = true
	viewersMu.Unlock()
	loop("Viewer sampler", viewerPollSec*time.Second, func() { viewerTick(0) })
}
