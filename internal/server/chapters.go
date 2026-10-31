package server

// Chapters on the match video: after the stream, the YouTube video's description gets a
// timestamp for each innings, wicket, fifty, hundred and five-for, so someone watching
// later jumps straight to them. (The highlights reel has its own chapters, clips.go.)
//
// The moments are logged as they happen, from the /live poll (LogMoments), into the
// match DB — they must survive a server restart, and the in-memory match log doesn't.
// Each has a key, so a moment seen twice (a restart, a scorer's correction) is one row.

import (
	"database/sql"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Moment is one thing worth a chapter.
type Moment struct {
	Key  string
	TS   float64 // unix seconds, when the server saw it
	Kind string  // innings, wicket, milestone, fivefor
	Text string
}

type momentsPrevT struct {
	mid      string
	innings  int
	set      bool
	started  bool
	wkts     int
	score    int
	batters  map[string]int // name → runs
	bowlerWk map[string]int
}

var momentsPrev momentsPrevT

// momentNow is time.Now (a test seam).
var momentNow = time.Now

func cleanName(s string) string {
	s = trim(s)
	if s == "—" || s == "-" {
		return ""
	}
	return s
}

// LogMoments notes the moments in this /live frame. Never raises: it sits in the
// match-day loop.
func LogMoments(state M) {
	defer func() { recover() }()
	innings := gi(state, "innings", 1)
	if innings == 0 {
		innings = 1
	}
	score, wkts := gi(state, "score", 0), gi(state, "wickets", 0)
	balls := oversToBalls(state["overs"])
	mid := CurrentMatchID()
	ov := ballsToOvers(balls)
	batters := map[string]int{}
	for _, k := range []string{"batter1", "batter2"} {
		b := gm(state, k)
		if n := cleanName(gs(b, "name", "")); n != "" {
			batters[n] = gi(b, "runs", 0)
		}
	}
	bw := gm(state, "bowler")
	bowler, bowlerWk := cleanName(gs(bw, "name", "")), gi(bw, "wickets", 0)
	now := float64(momentNow().UnixNano()) / 1e9

	var out []Moment
	add := func(key, kind, text string) { out = append(out, Moment{key, now, kind, text}) }
	p := &momentsPrev
	fresh := !p.set || p.mid != mid || p.innings != innings
	if fresh {
		*p = momentsPrevT{mid: mid, innings: innings, set: true, batters: map[string]int{}, bowlerWk: map[string]int{}}
	}
	if !p.started && (balls > 0 || score > 0) {
		// The innings' first ball (or the first frame seen of one already going).
		p.started = true
		team := orStr(trim(gs(state, "battingTeamName", "")), "Innings "+fmt.Sprint(innings))
		add(fmt.Sprintf("i%d", innings), "innings", team+" batting")
	}
	if !fresh && wkts > p.wkts {
		var gone []string
		for n := range p.batters {
			if _, still := batters[n]; !still {
				gone = append(gone, n)
			}
		}
		sort.Strings(gone)
		for i, w := 0, p.wkts+1; w <= wkts; i, w = i+1, w+1 {
			text := fmt.Sprintf("Wicket: %d/%d (%s ov)", score, w, ov)
			if i < len(gone) {
				text = fmt.Sprintf("Wicket: %s %d, %d/%d (%s ov)", gone[i], p.batters[gone[i]], score, w, ov)
			}
			add(fmt.Sprintf("w%d-%d", innings, w), "wicket", text)
		}
	}
	if !fresh {
		for n, r := range batters {
			before, seen := p.batters[n]
			for _, m := range []int{50, 100, 150, 200} {
				if r >= m && seen && before < m {
					add(fmt.Sprintf("m%d-%s-%d", innings, n, m), "milestone", fmt.Sprintf("%s %d", n, m))
				}
			}
		}
		if bowler != "" && bowlerWk >= 5 && p.bowlerWk[bowler] < 5 {
			if _, seen := p.bowlerWk[bowler]; seen {
				add(fmt.Sprintf("f%d-%s", innings, bowler), "fivefor", fmt.Sprintf("%s takes 5 wickets", bowler))
			}
		}
	}
	p.wkts, p.score, p.batters = wkts, score, batters
	if bowler != "" {
		p.bowlerWk[bowler] = bowlerWk
	}
	if len(out) == 0 {
		return
	}
	withDB(func(tx *sql.Tx) error {
		for _, m := range out {
			if _, err := tx.Exec("INSERT OR IGNORE INTO moments(match_id,key,ts,kind,text) VALUES(?,?,?,?,?)",
				mid, m.Key, m.TS, m.Kind, m.Text); err != nil {
				return err
			}
		}
		return nil
	})
}

// MatchMoments are one match's moments, in time order.
func MatchMoments(matchID string) []Moment {
	var out []Moment
	withDB(func(tx *sql.Tx) error {
		rows, err := tx.Query("SELECT key,ts,kind,text FROM moments WHERE match_id=? ORDER BY ts, rowid", matchID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m Moment
			if rows.Scan(&m.Key, &m.TS, &m.Kind, &m.Text) == nil {
				out = append(out, m)
			}
		}
		return rows.Err()
	})
	return out
}

// chapterLead: a moment is logged when the scorer enters it, some seconds after the
// ball; the chapter starts this much earlier, so it opens on the delivery itself.
const chapterLead = 25 * time.Second

// chapterGap: YouTube ignores chapters shorter than 10 seconds (and then all of them).
const chapterGap = 10 * time.Second

const chaptersHeader = "Chapters (added by CricketStream):"

// BuildChapters is the description block for a video that started at start (and ended
// at end, zero if unknown): "0:00 Start" then one line per moment inside the video. ""
// when there's too little for YouTube to show chapters (it needs three).
func BuildChapters(moments []Moment, start, end time.Time) string {
	lines := []string{"0:00 Start"}
	last := time.Duration(0)
	for _, m := range moments {
		at := time.Unix(0, int64(m.TS*1e9)).Sub(start) - chapterLead
		if !end.IsZero() && time.Unix(0, int64(m.TS*1e9)).After(end) {
			continue
		}
		if at < chapterGap {
			at = chapterGap // too near the start: just after "Start"
		}
		if at < last+chapterGap {
			at = last + chapterGap // close moments (two wickets in a ball) stay in order
		}
		if !end.IsZero() && at > end.Sub(start)-chapterGap {
			continue
		}
		lines = append(lines, clock(at)+" "+m.Text)
		last = at
	}
	if len(lines) < 3 {
		return ""
	}
	return chaptersHeader + "\n" + strings.Join(lines, "\n")
}

func clock(d time.Duration) string {
	s := int(d.Seconds())
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// withChapters puts the block at the end of desc, replacing one added before. YouTube
// allows 5000 bytes: the club's own words are kept, and moments are dropped from the
// end of the block (never the club's text) until it fits.
func withChapters(desc, block string) string {
	if i := strings.Index(desc, chaptersHeader); i >= 0 {
		desc = strings.TrimRight(desc[:i], " \n")
	}
	if block == "" {
		return desc
	}
	for {
		out := block
		if desc != "" {
			out = desc + "\n\n" + block
		}
		if len(out) <= 5000 {
			return out
		}
		lines := strings.Split(block, "\n")
		if len(lines) <= 4 { // header, 0:00 and two: YouTube's minimum
			return desc
		}
		block = strings.Join(lines[:len(lines)-1], "\n")
	}
}

// ytTime parses YouTube's RFC 3339 times.
func ytTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// matchVideo finds this match's video: the broadcast that started before its first
// moment and holds most of them.
func matchVideo(yt *YTClient, moments []Moment) (id string, start, end time.Time, err error) {
	r, err := yt.Get("liveBroadcasts", url.Values{"part": {"id,snippet,status"}, "mine": {"true"},
		"broadcastType": {"all"}, "maxResults": {"50"}})
	if err != nil {
		return "", time.Time{}, time.Time{}, err
	}
	best := 0
	for _, b := range items(r) {
		sn := gm(b, "snippet")
		s, e := ytTime(gs(sn, "actualStartTime", "")), ytTime(gs(sn, "actualEndTime", ""))
		if s.IsZero() {
			continue
		}
		n := 0
		for _, m := range moments {
			t := time.Unix(0, int64(m.TS*1e9))
			if !t.Before(s) && (e.IsZero() || !t.After(e)) {
				n++
			}
		}
		if n > best {
			best, id, start, end = n, gs(b, "id", ""), s, e
		}
	}
	return id, start, end, nil
}

// AddChaptersToYouTube writes this match's chapters into its video's description.
// preview: say what would be written, change nothing. The text is returned either way.
func AddChaptersToYouTube(matchID string, preview bool) (bool, string, string) {
	moments := MatchMoments(matchID)
	if len(moments) < 2 {
		return false, "No wickets or milestones logged for this match yet — chapters need at least two.", ""
	}
	yt, msg := ytService(false)
	if yt == nil {
		return false, msg, ""
	}
	id, start, end, err := matchVideo(yt, moments)
	if err != nil {
		return false, "YouTube: " + err.Error(), ""
	}
	if id == "" {
		return false, "No YouTube broadcast on this account covers this match's moments — was it streamed from this channel?", ""
	}
	block := BuildChapters(moments, start, end)
	if block == "" {
		return false, "Too few moments inside the video for YouTube to show chapters (it needs three).", ""
	}
	if preview {
		return true, "Chapters for video " + id + " (not written yet).", block
	}
	r, err := yt.Get("videos", url.Values{"part": {"snippet"}, "id": {id}})
	if err != nil {
		return false, "YouTube: " + err.Error(), block
	}
	it := items(r)
	if len(it) == 0 {
		return false, "YouTube didn't return video " + id + ".", block
	}
	sn := gm(it[0], "snippet")
	desc := withChapters(gs(sn, "description", ""), block)
	if desc == gs(sn, "description", "") {
		return true, "The video's chapters are already up to date.", block
	}
	// videos.update replaces the whole snippet: send back what's there, with the new
	// description (title and category are required).
	up := M{"title": gs(sn, "title", ""), "categoryId": gsOr(sn, "categoryId", "17"), "description": desc}
	for _, k := range []string{"tags", "defaultLanguage", "defaultAudioLanguage"} {
		if v, ok := sn[k]; ok {
			up[k] = v
		}
	}
	if _, err := yt.Put("videos", "snippet", M{"id": id, "snippet": up}); err != nil {
		return false, "YouTube: " + err.Error(), block
	}
	return true, fmt.Sprintf("Added %d chapters to the match video.", strings.Count(block, "\n")-1), block
}

// chaptersAfterStreamEnd: the stream stopped a minute ago and is still off — the match
// video is complete, so give it its chapters. Never raises (a background timer).
func chaptersAfterStreamEnd() {
	defer func() { recover() }()
	if streamingNow() || !gb(LoadState(), "youtube_chapters", true) || !fileExists(YTTokenFile) {
		return
	}
	ok, msg, _ := AddChaptersToYouTube(CurrentMatchID(), false)
	mark := "✓"
	if !ok {
		mark = "⚠"
	}
	say("  %s  YouTube chapters: %s", mark, msg)
}
