package server

// The break show: during drinks, the innings break, or a break the operator starts (rain,
// an injury), the stream replays the innings' best moments instead of sitting on an empty
// field. The overlay decides when a break starts and ends (it knows the overs; it ends
// the break at the first ball after), and puts its break graphics up on the main scene;
// this plays the saved replay clips in between, through the Replay scene, captioned.
//
// The clips are the ones replays already saved (the clips table), so nothing is recorded
// for it. No OBS, no clips, or replays switched off: the break graphics still show.

import (
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"cricketstream/internal/obsws"
)

type breakT struct {
	active bool
	kind   string // drinks, innings, rain, break
	since  time.Time
	stop   chan struct{}
	clip   string // the caption of the clip playing, "" between clips
}

var (
	breakMu  sync.Mutex
	breakNow breakT
)

// breakKinds are the breaks there are, as the graphics name them.
var breakKinds = map[string]string{"drinks": "Drinks", "innings": "Innings break", "rain": "Rain delay",
	"break": "Break in play"}

// Timings of the reel, swappable for tests: the graphics have the screen first, then a
// clip, then the graphics again between clips.
var (
	breakLead    = 20 * time.Second
	breakBetween = 25 * time.Second
	breakClipMax = 30 * time.Second
)

// breakClipsMax: the reel is the innings' best few, not every four.
const breakClipsMax = 8

// StartBreak starts (or renames) a break. Starting one already running keeps its reel.
func StartBreak(kind string) {
	if breakKinds[kind] == "" {
		kind = "break"
	}
	breakMu.Lock()
	defer breakMu.Unlock()
	if breakNow.active {
		breakNow.kind = kind
		return
	}
	stop := make(chan struct{})
	breakNow = breakT{active: true, kind: kind, since: time.Now(), stop: stop}
	say("  ☕  %s — the break show is on", breakKinds[kind])
	background.Add(1)
	go func() {
		defer background.Done()
		defer func() { recover() }() // a background reel must never take the server down
		breakReel(stop)
	}()
}

// EndBreak ends the break (play has restarted, or the operator ended it).
func EndBreak() {
	breakMu.Lock()
	defer breakMu.Unlock()
	if !breakNow.active {
		return
	}
	close(breakNow.stop)
	say("  ▶  %s over — back to the cricket", breakKinds[breakNow.kind])
	breakNow = breakT{}
}

// BreakStatus is the panel's view of it.
func BreakStatus() M {
	breakMu.Lock()
	defer breakMu.Unlock()
	if !breakNow.active {
		return M{"active": false}
	}
	return M{"active": true, "kind": breakNow.kind, "label": breakKinds[breakNow.kind],
		"since": int(time.Since(breakNow.since).Seconds()), "clip": breakNow.clip}
}

func setBreakClip(caption string) {
	breakMu.Lock()
	defer breakMu.Unlock()
	if breakNow.active {
		breakNow.clip = caption
	}
}

// breakClip is a saved replay for the reel.
type breakClip struct {
	path, caption, reason string
}

// clipRank: the reel's order of preference when there are more than it plays.
func clipRank(reason string) int {
	r := strings.ToLower(reason)
	switch {
	case r == "wicket":
		return 0
	case strings.HasPrefix(r, "century"), strings.HasPrefix(r, "fifty"):
		return 1
	case r == "six":
		return 2
	}
	return 3
}

// breakClips are this match's best saved replays still on disk, in the order they
// happened.
func breakClips(folder string) []breakClip {
	type row struct {
		breakClip
		ts string
	}
	var rows []row
	withDB(func(tx *sql.Tx) error {
		q, err := tx.Query("SELECT file, ts, reason, caption FROM clips WHERE match_id=? ORDER BY ts", CurrentMatchID())
		if err != nil {
			return err
		}
		defer q.Close()
		for q.Next() {
			var f, ts, r, c sql.NullString
			if q.Scan(&f, &ts, &r, &c) == nil {
				p := filepath.Join(folder, filepath.Base(f.String))
				if _, err := os.Stat(p); err == nil {
					rows = append(rows, row{breakClip{p, c.String, r.String}, ts.String})
				}
			}
		}
		return q.Err()
	})
	if len(rows) > breakClipsMax {
		// The best few by kind (newest first within a kind), then back in match order.
		best := append([]row(nil), rows...)
		sort.SliceStable(best, func(i, j int) bool {
			ri, rj := clipRank(best[i].reason), clipRank(best[j].reason)
			if ri != rj {
				return ri < rj
			}
			return best[i].ts > best[j].ts
		})
		best = best[:breakClipsMax]
		sort.SliceStable(best, func(i, j int) bool { return best[i].ts < best[j].ts })
		rows = best
	}
	out := make([]breakClip, len(rows))
	for i, r := range rows {
		out[i] = r.breakClip
	}
	return out
}

// breakWait waits d, or until the break ends; false when it ended.
var breakWait = func(stop chan struct{}, d time.Duration) bool {
	select {
	case <-stop:
		return false
	case <-time.After(d):
		return true
	}
}

// breakObs is the reel's OBS: set the clip, cut to a scene. Swappable for tests.
var breakObs = func(st M, clip, scene string) bool {
	c, err := obsws.Dial(obsTarget(st), 8*time.Second)
	if err != nil {
		return false
	}
	defer c.Close()
	if clip != "" {
		if r, err := c.Request("SetInputSettings", M{"inputName": "ReplayClip", "inputSettings": M{"local_file": clip}}); err != nil || !r.OK {
			return false
		}
	}
	r, err := c.Request("SetCurrentProgramScene", M{"sceneName": scene})
	return err == nil && r.OK
}

// breakClipLength is how long a clip plays: its own length, else the replay length,
// never longer than breakClipMax. Swappable for tests (ffprobe).
var breakClipLength = func(st M, path string) time.Duration {
	d := time.Duration(clipDuration(path) * float64(time.Second))
	if d <= 0 {
		d = time.Duration(gi(st, "replay_duration", 18)) * time.Second
	}
	return min(d, breakClipMax)
}

// breakReel plays the clips until the break ends: graphics first, then clip, graphics,
// clip... round again. Whenever the break ends it's back on the main scene at once.
func breakReel(stop chan struct{}) {
	st := LoadState()
	main, replay := gs(st, "obs_main_scene", "Main"), gs(st, "obs_replay_scene", "Replay")
	folder := gs(st, "replay_folder", "")
	if folder == "" {
		folder = defaultReplayFolder()
	}
	if !gb(st, "replay_enabled", false) || !gb(st, "break_reel", true) {
		return
	}
	if !breakWait(stop, breakLead) {
		return
	}
	for {
		clips := breakClips(folder) // the match's best so far
		if len(clips) == 0 {
			say("  ☕  No saved replays to show in the break — the graphics carry on")
			return
		}
		for _, c := range clips {
			if shifting.Load() { // a stream quality step: not now
				if !breakWait(stop, breakBetween) {
					return
				}
				continue
			}
			length := breakClipLength(st, c.path)
			noteReplay(length.Seconds() + 5)
			setBreakClip(c.caption)
			setCommand("break_clip", orStr(c.caption, "Replay"))
			if !breakObs(st, c.path, replay) {
				setBreakClip("")
				say("  ⚠  The break show couldn't play a replay — is OBS open? The graphics carry on.")
				return
			}
			played := breakWait(stop, length)
			breakObs(st, "", main)
			setBreakClip("")
			if !played || !breakWait(stop, breakBetween) {
				return
			}
		}
	}
}
