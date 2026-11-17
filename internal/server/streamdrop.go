package server

// When the internet drops out. Two things, both every few seconds while OBS is open:
//
//  1. The way back on air. OBS reconnects by itself, but each wait is half as long again as
//     the last (libobs: 2s × 1.5ⁿ, capped at 15 minutes; not a setting), so after a
//     ten-minute outage its next try can be four or five minutes away with the line long
//     since back. So once OBS has been reconnecting for a while, and the stream's own server
//     ANSWERS (a TCP connect to the host in OBS's stream settings), the stream is stopped and
//     started again at once. Never on a guess: a start with the line still down fails, OBS
//     shows an error box and stops trying, which is worse than waiting. Never with OBS
//     signed in to YouTube ("Connect account"): there, stopping the stream ends the broadcast.
//
//  2. A recording of the whole match on this laptop (`backup_recording`, on by default), so
//     what the internet lost still exists. It starts when the stream does and stops when the
//     stream's been stopped (by someone, not by the line) for two minutes, or at the end of
//     match day. It shares the stream's encoder in OBS's default settings (Simple mode,
//     recording quality "Same as stream"), so it costs disk, not processing. It goes to
//     <replay folder>/match-recordings, NEVER the replay folder itself: every video there
//     counts as a replay — the newest is played, the oldest deleted. OBS has one folder for
//     recordings and replays alike, so the folder is switched for the moment the recording
//     starts and put back once it has (OBS reads it as the recording starts; the replay
//     buffer already running keeps the folder it started with).

import (
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"cricketstream/internal/obsws"
	"cricketstream/internal/pyfmt"
)

const (
	dropWatchPollSec = 5
	// OBS's own first tries (2, 3, 4.5, 6.75s) get their turn: most blips are over by then.
	reconnectKickAfter = 20.0
	reconnectKickEvery = 60.0
	// Stopped by a person (not reconnecting) for this long: the match is over.
	backupStopAfterOff = 120.0
	// Below this much free space, old backup recordings (never today's) make room.
	backupKeepFreeGB = 20.0
	backupDirName    = "match-recordings"
)

type dropState struct {
	reconnSince   float64 // when OBS was first seen reconnecting (0: it isn't)
	lastKick      float64
	kicks         int
	kickNote      string  // why there's no restart yet, for the panel
	offSince      float64 // the stream stopped cleanly at (0: on, or never on)
	wasLive       bool
	recording     bool
	recordingNote string  // why the backup recording isn't running, for the panel
	recSaid       string  // the last recordingNote said in the log
	nextRecTry    float64 // after a start that failed, not again before this
}

var (
	drop   dropState
	dropMu sync.Mutex
)

// Test seams.
var (
	dialStreamServer = func(addr string) bool {
		c, err := net.DialTimeout("tcp", addr, 3*time.Second)
		if err != nil {
			return false
		}
		c.Close()
		return true
	}
	obsSignedIn  = obsSignedInToYouTube
	dropNow      = now
	dropDiskFree = diskFreeBytes
)

func backupRecordingOn(st M) bool { return gb(st, "backup_recording", true) }

func backupRecordingDir(st M) string { return filepath.Join(replayFolder(st), backupDirName) }

func dropWatchTick() {
	st := LoadState()
	r := obsCall(st, []obsws.Req{{Type: "GetStreamStatus"}, {Type: "GetRecordStatus"}}, 5*time.Second)
	t := dropNow()
	if len(r) != 2 || r[0] == nil {
		dropMu.Lock()
		drop.reconnSince, drop.recording = 0, false
		dropMu.Unlock()
		return // OBS closed or crashed: the OBS guard's business
	}
	live, reconn := gb(r[0], "outputActive", false), gb(r[0], "outputReconnecting", false)
	recKnown := r[1] != nil
	recording := recKnown && gb(r[1], "outputActive", false)

	dropMu.Lock()
	drop.recording = recording
	// ── 1. reconnecting ──
	kick := false
	switch {
	case live && reconn:
		if drop.reconnSince == 0 {
			drop.reconnSince, drop.kicks, drop.kickNote = t, 0, ""
			say("  📶  The stream has lost its connection — OBS is reconnecting%s", recordingSafe(st, recording))
		}
		kick = t-drop.reconnSince >= reconnectKickAfter && t-drop.lastKick >= reconnectKickEvery
	case drop.reconnSince != 0:
		if live {
			say("  📶  Back on air after %s without a connection", pyfmt.Fixed(t-drop.reconnSince, 0)+"s")
		}
		drop.reconnSince, drop.kickNote = 0, ""
	}
	// ── 2. when the stream stopped (by a person: reconnecting stays "live") ──
	if live {
		drop.offSince = 0
	} else if drop.wasLive && drop.offSince == 0 {
		drop.offSince = t
	}
	drop.wasLive = live
	offFor := 0.0
	if drop.offSince != 0 {
		offFor = t - drop.offSince
	}
	dropMu.Unlock()

	if kick {
		kickReconnect(st, t)
	}
	if !recKnown || !backupRecordingOn(st) {
		setRecordingNote("")
		return
	}
	switch {
	case live && !recording:
		dropMu.Lock()
		wait := t < drop.nextRecTry
		dropMu.Unlock()
		if !wait && !startBackupRecording(st) {
			dropMu.Lock()
			drop.nextRecTry = t + 60
			dropMu.Unlock()
		}
	case !live && recording && offFor >= backupStopAfterOff:
		// With the setting on, a recording running is taken to be this one, also after a
		// server restart (the crash loop's new server finds it running).
		stopBackupRecording(st, "the stream has been stopped for two minutes")
		dropMu.Lock()
		drop.offSince = 0
		dropMu.Unlock()
	}
}

func recordingSafe(st M, recording bool) string {
	if recording && backupRecordingOn(st) {
		return " (the backup recording on this laptop carries on)"
	}
	return ""
}

func setRecordingNote(n string) {
	dropMu.Lock()
	defer dropMu.Unlock()
	drop.recordingNote = n
	if n != "" && n != drop.recSaid {
		say("  ⚠  Backup recording: %s", n)
	}
	drop.recSaid = n
}

// streamServerAddr is host:port of the server OBS streams to, from its stream settings.
func streamServerAddr(st M) string {
	r := obsCall(st, []obsws.Req{{Type: "GetStreamServiceSettings"}}, 5*time.Second)
	if len(r) == 0 || r[0] == nil {
		return ""
	}
	s, _ := r[0]["streamServiceSettings"].(map[string]any)
	server := strings.TrimSpace(gs(s, "server", ""))
	u, err := url.Parse(server)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	p := u.Port()
	switch {
	case !strings.EqualFold(u.Scheme, "rtmps") && !strings.EqualFold(u.Scheme, "rtmp"):
		return "" // SRT, WHIP...: not a TCP server to knock on
	case p != "":
	case strings.EqualFold(u.Scheme, "rtmps"):
		p = "443"
	default:
		p = "1935"
	}
	return net.JoinHostPort(u.Hostname(), p)
}

func setKickNote(n string) {
	dropMu.Lock()
	drop.kickNote = n
	dropMu.Unlock()
}

// kickReconnect restarts a stream that's been reconnecting a while, once the line's back.
func kickReconnect(st M, t float64) {
	if obsSignedIn() {
		setKickNote("OBS is signed in to YouTube, where restarting the stream would end the broadcast — OBS keeps trying by itself")
		return
	}
	addr := streamServerAddr(st)
	if addr == "" {
		setKickNote("OBS's stream server isn't one this can check — OBS keeps trying by itself")
		return
	}
	if !dialStreamServer(addr) {
		setKickNote("No internet yet — OBS keeps trying, and the stream restarts the moment the line's back")
		return
	}
	// The quality ladder stops and starts the stream too: never both at once.
	if !qualityShift.TryLock() {
		return
	}
	defer qualityShift.Unlock()
	shifting.Store(true)
	defer shifting.Store(false)
	dropMu.Lock()
	drop.lastKick = t
	drop.kicks++
	dropMu.Unlock()
	say("  📶  The internet's back but OBS is still waiting to reconnect — restarting the stream now")
	obsCall(st, []obsws.Req{{Type: "StopStream"}}, 8*time.Second)
	if stopped, _ := waitStopped(st, 20); !stopped {
		setKickNote("OBS didn't stop the stream to restart it — it keeps trying by itself")
		return
	}
	if started, stayed := backOnAir(st); !started || !stayed {
		watchdogLogFix("The stream didn't come back after the internet did — press Start Streaming in OBS now")
		setKickNote("The restart didn't hold — press Start Streaming in OBS")
		return
	}
	setKickNote("")
}

// profileParam reads one OBS profile setting ("" when OBS doesn't answer).
func profileParam(st M, cat, name string) (string, bool) {
	r := obsCall(st, []obsws.Req{{Type: "GetProfileParameter",
		Data: M{"parameterCategory": cat, "parameterName": name}}}, 5*time.Second)
	if len(r) == 0 || r[0] == nil {
		return "", false
	}
	if v := gs(r[0], "parameterValue", ""); v != "" {
		return v, true
	}
	return gs(r[0], "defaultParameterValue", ""), true
}

func setProfileParam(st M, cat, name, value string) bool {
	r := obsCall(st, []obsws.Req{{Type: "SetProfileParameter",
		Data: M{"parameterCategory": cat, "parameterName": name, "parameterValue": value}}}, 5*time.Second)
	return len(r) > 0 && r[0] != nil
}

// recordFolderParam is the OBS setting behind the recording (and replay) folder, or why
// a recording can't safely be pointed elsewhere.
func recordFolderParam(st M) (cat, name, why string) {
	mode, ok := profileParam(st, "Output", "Mode")
	if !ok {
		return "", "", "OBS didn't answer"
	}
	if mode != "Advanced" {
		return "SimpleOutput", "FilePath", ""
	}
	if rt, _ := profileParam(st, "AdvOut", "RecType"); rt != "" && rt != "Standard" {
		return "", "", "OBS's recording is set to a custom output (Settings → Output → Recording → Type), so it's left alone"
	}
	return "AdvOut", "RecFilePath", ""
}

var recordStartWait = 250 * time.Millisecond

func startBackupRecording(st M) bool {
	// Serialised with the ladder, which stops everything sharing the encoder.
	if !qualityShift.TryLock() {
		return true // not a failure: try again next tick
	}
	defer qualityShift.Unlock()
	cat, name, why := recordFolderParam(st)
	if why != "" {
		setRecordingNote(why)
		return false
	}
	old, ok := profileParam(st, cat, name)
	if !ok || old == "" {
		setRecordingNote("couldn't read OBS's recording folder")
		return false
	}
	dir := backupRecordingDir(st)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		setRecordingNote("couldn't make the folder " + dir)
		return false
	}
	pruneBackupRecordings(dir)
	if !setProfileParam(st, cat, name, dir) {
		setRecordingNote("OBS wouldn't take the recording folder")
		return false
	}
	// OBS starts the recording on its own thread after answering, and reads the folder
	// then: the folder is put back only once it's running (or after 10s, whatever happened).
	started := false
	if r := obsCall(st, []obsws.Req{{Type: "StartRecord"}}, 8*time.Second); len(r) > 0 && r[0] != nil {
		for i := 0; i < 40 && !started; i++ {
			time.Sleep(recordStartWait)
			s := obsCall(st, []obsws.Req{{Type: "GetRecordStatus"}}, 5*time.Second)
			started = len(s) > 0 && s[0] != nil && gb(s[0], "outputActive", false)
		}
	}
	if !setProfileParam(st, cat, name, old) {
		// Replays would be saved into the recordings folder next time the buffer starts.
		watchdogLogFix("Couldn't put OBS's replay folder back after starting the backup recording — check OBS Settings → Output: it should be " + old)
	}
	if !started {
		setRecordingNote("OBS didn't start recording")
		return false
	}
	dropMu.Lock()
	drop.recording = true
	dropMu.Unlock()
	setRecordingNote("")
	say("  ⏺  Backup recording of the match started, in %s", dir)
	return true
}

// stopBackupRecording stops the recording and says where it is.
func stopBackupRecording(st M, why string) {
	r := obsCall(st, []obsws.Req{{Type: "StopRecord"}}, 10*time.Second)
	if len(r) == 0 || r[0] == nil {
		say("  ⚠  Couldn't stop the backup recording (%s) — stop it in OBS", why)
		return
	}
	if p := gs(r[0], "outputPath", ""); p != "" {
		say("  ⏺  Backup recording stopped (%s) — saved as %s", why, p)
	} else {
		say("  ⏺  Backup recording stopped (%s)", why)
	}
}

// StopBackupRecordingAtEnd is the clean end of match day: the recording stops, unless the
// stream is still live (someone's still broadcasting; they'll stop both).
func StopBackupRecordingAtEnd() {
	defer func() { recover() }()
	st := LoadState()
	if !backupRecordingOn(st) {
		return
	}
	r := obsCall(st, []obsws.Req{{Type: "GetStreamStatus"}, {Type: "GetRecordStatus"}}, 3*time.Second)
	if len(r) != 2 || r[0] == nil || r[1] == nil || gb(r[0], "outputActive", false) || !gb(r[1], "outputActive", false) {
		return
	}
	stopBackupRecording(st, "match day stopped")
}

// pruneBackupRecordings makes room: below backupKeepFreeGB free, the oldest recordings
// from before today go, one at a time. Only this folder, only videos, never today's.
func pruneBackupRecordings(dir string) {
	type rec struct {
		path string
		mod  time.Time
	}
	var recs []rec
	today := time.Now().Format("2006-01-02")
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".mkv" && ext != ".mp4" && ext != ".mov" && ext != ".flv" && ext != ".ts" {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().Format("2006-01-02") == today {
			continue
		}
		recs = append(recs, rec{filepath.Join(dir, e.Name()), info.ModTime()})
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].mod.Before(recs[j].mod) })
	for _, r := range recs {
		free, err := dropDiskFree(dir)
		if err != nil || float64(free)/1e9 >= backupKeepFreeGB {
			return
		}
		if os.Remove(r.path) == nil {
			say("  🗑  Deleted an old backup recording to make room: %s", filepath.Base(r.path))
		}
	}
}

// dropStatus is the stream monitor's view of the above (lock-free callers: streamMonitorStatus).
func dropStatus() M {
	dropMu.Lock()
	defer dropMu.Unlock()
	out := M{"reconnecting": drop.reconnSince != 0, "reconnecting_secs": 0, "reconnect_note": drop.kickNote,
		"restarts": drop.kicks, "recording": drop.recording, "recording_note": drop.recordingNote}
	if drop.reconnSince != 0 {
		out["reconnecting_secs"] = int(dropNow() - drop.reconnSince)
	}
	return out
}

func StartDropWatch() { loop("Stream dropout watch", dropWatchPollSec*time.Second, dropWatchTick) }
