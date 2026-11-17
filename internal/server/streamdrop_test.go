package server

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cricketstream/internal/obsws"
)

// dropOBS is OBS as the dropout watch sees it. A recording starts the way OBS's does: the
// request is answered at once, and the folder is read when it really starts (here, at the
// next status read), so putting the folder back too soon is caught.
type dropOBS struct {
	mu                      sync.Mutex
	live, reconn, recording bool
	recPending              bool
	params                  map[string]string
	recFolder               string
	server                  string
	calls                   []string
	refuseStart             bool
}

func (o *dropOBS) called(typ string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := 0
	for _, c := range o.calls {
		if c == typ {
			n++
		}
	}
	return n
}

type dropRig struct {
	obs     *dropOBS
	clock   *float64
	reach   *bool
	signed  *bool
	replays string
	said    func() string
}

func newDropRig(t *testing.T) *dropRig {
	t.Helper()
	sandbox(t, "")
	replays := t.TempDir()
	setState(t, M{"obs_host": "localhost", "obs_port": 4455, "replay_folder": replays})
	o := &dropOBS{params: map[string]string{"Output/Mode": "Simple", "SimpleOutput/FilePath": replays},
		server: "rtmps://a.rtmps.youtube.com:443/live2"}
	clock, reach, signed := 1000.0, true, false
	saved := []any{obsCall, dialStreamServer, obsSignedIn, dropNow, shiftSleep, recordStartWait, dropDiskFree}
	t.Cleanup(func() {
		obsCall = saved[0].(func(M, []obsws.Req, time.Duration) []map[string]any)
		dialStreamServer = saved[1].(func(string) bool)
		obsSignedIn = saved[2].(func() bool)
		dropNow = saved[3].(func() float64)
		shiftSleep = saved[4].(func(time.Duration))
		recordStartWait = saved[5].(time.Duration)
		dropDiskFree = saved[6].(func(string) (uint64, error))
		dropMu.Lock()
		drop = dropState{}
		dropMu.Unlock()
	})
	dropMu.Lock()
	drop = dropState{}
	dropMu.Unlock()
	dialStreamServer = func(string) bool { return reach }
	obsSignedIn = func() bool { return signed }
	dropNow = func() float64 { return clock }
	shiftSleep = func(time.Duration) {}
	recordStartWait = 0
	dropDiskFree = func(string) (uint64, error) { return 500e9, nil }
	obsCall = func(_ M, reqs []obsws.Req, _ time.Duration) []map[string]any {
		o.mu.Lock()
		defer o.mu.Unlock()
		var out []map[string]any
		for _, r := range reqs {
			o.calls = append(o.calls, r.Type)
			d, _ := r.Data.(M)
			key := func() string { return gs(d, "parameterCategory", "") + "/" + gs(d, "parameterName", "") }
			switch r.Type {
			case "GetStreamStatus":
				out = append(out, map[string]any{"outputActive": o.live, "outputReconnecting": o.reconn})
			case "GetRecordStatus":
				if o.recPending {
					o.recPending, o.recording = false, true
					folderKey := "SimpleOutput/FilePath"
					if o.params["Output/Mode"] == "Advanced" {
						folderKey = "AdvOut/RecFilePath"
					}
					o.recFolder = o.params[folderKey]
				}
				out = append(out, map[string]any{"outputActive": o.recording})
			case "StartRecord":
				o.recPending = true
				out = append(out, map[string]any{})
			case "StopRecord":
				o.recording = false
				out = append(out, map[string]any{"outputPath": filepath.Join(o.recFolder, "match.mkv")})
			case "StopStream":
				o.live, o.reconn = false, false
				out = append(out, map[string]any{})
			case "StartStream":
				if o.refuseStart {
					out = append(out, nil)
					continue
				}
				o.live = true
				out = append(out, map[string]any{})
			case "GetProfileParameter":
				out = append(out, map[string]any{"parameterValue": o.params[key()]})
			case "SetProfileParameter":
				o.params[key()] = gs(d, "parameterValue", "")
				out = append(out, map[string]any{})
			case "GetStreamServiceSettings":
				out = append(out, map[string]any{"streamServiceSettings": map[string]any{"server": o.server}})
			default:
				out = append(out, map[string]any{})
			}
		}
		return out
	}
	return &dropRig{obs: o, clock: &clock, reach: &reach, signed: &signed, replays: replays, said: captureSay(t)}
}

func (r *dropRig) at(secs float64) { *r.clock = 1000 + secs; dropWatchTick() }

func (r *dropRig) set(live, reconn bool) {
	r.obs.mu.Lock()
	r.obs.live, r.obs.reconn = live, reconn
	r.obs.mu.Unlock()
}

// The line's been down long enough for OBS's waits to grow: once the stream's server
// answers, the stream's restarted rather than left to OBS's next try minutes away.
func TestReconnectingStreamIsRestartedOnceTheLineIsBack(t *testing.T) {
	r := newDropRig(t)
	r.set(true, true)
	*r.reach = false
	r.at(0)
	r.at(15)
	if r.obs.called("StopStream") != 0 {
		t.Fatal("restarted before OBS's own first tries had their turn")
	}
	r.at(25)
	if r.obs.called("StopStream") != 0 {
		t.Fatal("restarted with the line still down: OBS would fail to start and stop trying")
	}
	if n := gs(dropStatus(), "reconnect_note", ""); !strings.Contains(n, "No internet yet") {
		t.Fatalf("note: %q", n)
	}
	// One answer between silences is a flicker, not the line back.
	*r.reach = true
	r.at(30)
	*r.reach = false
	r.at(35)
	*r.reach = true
	r.at(40)
	if r.obs.called("StopStream") != 0 {
		t.Fatal("restarted on a single answer: a flickering line would get a start into nothing")
	}
	// Twice running: back.
	r.at(45)
	if r.obs.called("StopStream") != 1 || r.obs.called("StartStream") != 1 {
		t.Fatalf("calls: %v", r.obs.calls)
	}
	r.at(50)
	if gb(dropStatus(), "reconnecting", true) {
		t.Fatal("still reported as reconnecting once back")
	}
	if s := r.said(); !strings.Contains(s, "lost its connection") || !strings.Contains(s, "restarting the stream now") || !strings.Contains(s, "Back on air") {
		t.Fatal(s)
	}
}

// Not more than once a minute, however long OBS keeps reconnecting.
func TestReconnectRestartIsRateLimited(t *testing.T) {
	r := newDropRig(t)
	r.set(true, true)
	r.at(0)
	r.at(20)
	r.at(25) // the second answer running: restarted
	if n := r.obs.called("StopStream"); n != 1 {
		t.Fatalf("%d restarts", n)
	}
	r.set(true, true) // the restart didn't take: OBS is reconnecting again
	r.at(30)
	r.at(70)
	r.at(84)
	if n := r.obs.called("StopStream"); n != 1 {
		t.Fatalf("%d restarts inside a minute", n)
	}
	r.at(86)
	r.at(91)
	if n := r.obs.called("StopStream"); n != 2 {
		t.Fatalf("%d restarts after a minute", n)
	}
}

// Signed in to YouTube, a stop ends the broadcast: OBS is left to it.
func TestNoRestartWhenOBSIsSignedInToYouTube(t *testing.T) {
	r := newDropRig(t)
	*r.signed = true
	r.set(true, true)
	r.at(0)
	r.at(60)
	if r.obs.called("StopStream") != 0 {
		t.Fatal("stopped a signed-in stream, which ends the broadcast")
	}
	if n := gs(dropStatus(), "reconnect_note", ""); !strings.Contains(n, "signed in") {
		t.Fatalf("note: %q", n)
	}
}

func TestStreamServerAddress(t *testing.T) {
	r := newDropRig(t)
	for server, want := range map[string]string{
		"rtmps://a.rtmps.youtube.com:443/live2": "a.rtmps.youtube.com:443",
		"rtmps://a.rtmps.youtube.com/live2":     "a.rtmps.youtube.com:443",
		"rtmp://a.rtmp.youtube.com/live2":       "a.rtmp.youtube.com:1935",
		"rtmp://127.0.0.1:19350/live":           "127.0.0.1:19350",
		"srt://example.com:9000":                "",
		"":                                      "",
	} {
		r.obs.server = server
		if got := streamServerAddr(LoadState()); got != want {
			t.Errorf("%q: got %q, want %q", server, got, want)
		}
	}
}

// The recording goes to its own folder — never the replay folder, where the newest video
// is played as a replay and the oldest deleted — and OBS's folder is put back for replays,
// but only once the recording has really started.
func TestBackupRecordingStartsWithTheStreamInItsOwnFolder(t *testing.T) {
	r := newDropRig(t)
	r.at(0)
	if r.obs.called("StartRecord") != 0 {
		t.Fatal("recording before the stream started")
	}
	r.set(true, false)
	r.at(5)
	want := filepath.Join(r.replays, backupDirName)
	if r.obs.recFolder != want {
		t.Fatalf("recorded into %q, want %q", r.obs.recFolder, want)
	}
	if got := r.obs.params["SimpleOutput/FilePath"]; got != r.replays {
		t.Fatalf("OBS's folder left at %q: replays would go there", got)
	}
	if !isDir(want) || !gb(dropStatus(), "recording", false) {
		t.Fatal("no folder, or not reported as recording")
	}
	r.at(10)
	if n := r.obs.called("StartRecord"); n != 1 {
		t.Fatalf("started %d times", n)
	}
}

func TestBackupRecordingInAdvancedMode(t *testing.T) {
	r := newDropRig(t)
	r.obs.params["Output/Mode"] = "Advanced"
	r.obs.params["AdvOut/RecFilePath"] = r.replays
	r.set(true, false)
	r.at(0)
	if want := filepath.Join(r.replays, backupDirName); r.obs.recFolder != want || r.obs.params["AdvOut/RecFilePath"] != r.replays {
		t.Fatalf("recorded into %q, folder now %q", r.obs.recFolder, r.obs.params["AdvOut/RecFilePath"])
	}
}

// A custom recording output has a folder setting of its own: left alone, and said why.
func TestBackupRecordingLeavesACustomOutputAlone(t *testing.T) {
	r := newDropRig(t)
	r.obs.params["Output/Mode"] = "Advanced"
	r.obs.params["AdvOut/RecType"] = "FFmpeg"
	r.set(true, false)
	r.at(0)
	r.at(5)
	if r.obs.called("StartRecord") != 0 {
		t.Fatal("started a custom output")
	}
	if n := gs(dropStatus(), "recording_note", ""); !strings.Contains(n, "custom output") {
		t.Fatalf("note %q", n)
	}
	if c := strings.Count(r.said(), "custom output"); c != 1 {
		t.Fatalf("said %d times", c)
	}
}

// The internet dropping out keeps the stream "live" in OBS: the recording carries on.
// Stopped by a person, it stops two minutes later; the end of match day stops it too.
func TestBackupRecordingStopsTwoMinutesAfterTheStream(t *testing.T) {
	r := newDropRig(t)
	r.set(true, false)
	r.at(0)
	r.set(true, true)
	r.at(30)
	r.at(400)
	if r.obs.called("StopRecord") != 0 {
		t.Fatal("stopped while OBS was only reconnecting")
	}
	*r.reach = false // nothing restarts it either
	r.set(false, false)
	r.at(410)
	r.at(500)
	if r.obs.called("StopRecord") != 0 {
		t.Fatal("stopped before two minutes")
	}
	r.at(531)
	if r.obs.called("StopRecord") != 1 {
		t.Fatal("not stopped after two minutes off")
	}
	if !strings.Contains(r.said(), "match.mkv") {
		t.Fatal("didn't say where it is")
	}
}

func TestBackupRecordingStopsAtTheEndOfMatchDay(t *testing.T) {
	r := newDropRig(t)
	r.set(true, false)
	r.at(0)
	StopBackupRecordingAtEnd()
	if r.obs.called("StopRecord") != 0 {
		t.Fatal("stopped while still streaming")
	}
	r.set(false, false)
	StopBackupRecordingAtEnd()
	if r.obs.called("StopRecord") != 1 {
		t.Fatal("not stopped at the end of match day")
	}
}

// OBS on another computer: the folder is this laptop's and the YouTube sign-in is read from
// this laptop's OBS, so nothing is recorded or restarted there; only reported.
func TestOBSOnAnotherComputerIsOnlyReported(t *testing.T) {
	r := newDropRig(t)
	setState(t, M{"obs_host": "192.168.1.50"})
	r.set(true, true)
	r.at(0)
	r.at(60)
	if r.obs.called("StartRecord") != 0 || r.obs.called("StopStream") != 0 || r.obs.called("SetProfileParameter") != 0 {
		t.Fatalf("acted on another computer's OBS: %v", r.obs.calls)
	}
	if d := dropStatus(); d["reconnecting"] != true || !strings.Contains(gs(d, "reconnect_note", ""), "another computer") {
		t.Fatalf("status: %v", d)
	}
	r.set(false, false)
	r.obs.recording = true
	StopBackupRecordingAtEnd()
	if r.obs.called("StopRecord") != 0 {
		t.Fatal("stopped another computer's recording")
	}
}

func TestBackupRecordingOff(t *testing.T) {
	r := newDropRig(t)
	setState(t, M{"backup_recording": false})
	r.set(true, false)
	r.at(0)
	if r.obs.called("StartRecord") != 0 || r.obs.called("SetProfileParameter") != 0 {
		t.Fatal("recorded with the setting off")
	}
}

// A start that fails isn't retried every five seconds (each try moves OBS's folder).
func TestFailedRecordingStartWaitsAMinute(t *testing.T) {
	r := newDropRig(t)
	r.obs.params["SimpleOutput/FilePath"] = ""
	r.set(true, false)
	r.at(0)
	r.at(5)
	r.at(30)
	if n := r.obs.called("GetProfileParameter"); n != 2 { // Mode, FilePath: once
		t.Fatalf("tried again too soon (%d reads)", n)
	}
	r.at(61)
	if n := r.obs.called("GetProfileParameter"); n != 4 {
		t.Fatalf("didn't try again after a minute (%d reads)", n)
	}
}

// Low on space, old recordings make room — the oldest first, never today's, nothing but
// videos, and only while space is short.
func TestOldBackupRecordingsMakeRoom(t *testing.T) {
	r := newDropRig(t)
	dir := filepath.Join(r.replays, backupDirName)
	os.MkdirAll(dir, 0o755)
	mk := func(name string, age time.Duration) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte("x"), 0o644)
		tm := time.Now().Add(-age)
		os.Chtimes(p, tm, tm)
		return p
	}
	oldest, older, today, notes := mk("a.mkv", 30*24*time.Hour), mk("b.mp4", 7*24*time.Hour), mk("c.mkv", 0), mk("d.txt", 60*24*time.Hour)
	free := uint64(19e9)
	dropDiskFree = func(string) (uint64, error) { return free, nil }
	removed := 0
	pruneOnce := func() {
		pruneBackupRecordings(dir)
	}
	// Each deletion frees 2 GB.
	dropDiskFree = func(string) (uint64, error) {
		n := 0
		for _, p := range []string{oldest, older} {
			if _, err := os.Stat(p); err != nil {
				n++
			}
		}
		removed = n
		return free + uint64(n)*2e9, nil
	}
	pruneOnce()
	if _, err := os.Stat(oldest); err == nil {
		t.Fatal("oldest kept")
	}
	if _, err := os.Stat(older); err != nil || removed != 1 {
		t.Fatal("deleted more than it needed")
	}
	free = 1e9
	pruneOnce()
	for _, p := range []string{today, notes} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("deleted %s", filepath.Base(p))
		}
	}
}

// The quality ladder needs every output off the encoder: the backup recording is split,
// not a reason to refuse. Someone's own recording (setting off) still is.
func TestQualityStepSplitsTheBackupRecording(t *testing.T) {
	r := newDropRig(t)
	r.obs.recording = true
	streamMonMu.Lock()
	savedMon := streamMon
	streamMon = newStreamMon()
	streamMon["baseline_kbps"] = 6000
	streamMonMu.Unlock()
	t.Cleanup(func() { streamMonMu.Lock(); streamMon = savedMon; streamMonMu.Unlock() })
	setState(t, M{"backup_recording": false})
	if ok, msg := ApplyStreamQualityStep(1, "test", "manual"); ok || !strings.Contains(msg, "OBS is recording") {
		t.Fatal(ok, msg)
	}
	setState(t, M{"backup_recording": true})
	ok, msg := ApplyStreamQualityStep(1, "test", "manual")
	if !ok || r.obs.called("StopRecord") != 1 || !strings.Contains(msg, "new file") {
		t.Fatal(ok, msg, r.obs.calls)
	}
}
