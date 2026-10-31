package server

// Side-on replays: a wicket replays from camera 2 too. OBS's replay buffer keeps only
// what was on air, so camera 2's view of the wicket was never kept; this keeps it. While
// "replay_side_on" is on and camera 2 is set up, FFmpeg copies camera 2's RTSP stream
// (no re-encode: next to nothing for the laptop) into a ring of short segments, the last
// ~40s; a wicket cuts the newest of them into a clip (SideOnClip), which the replay
// plays after the main one (ObsTriggerReplay).
//
// The recorder must never outlive the server: it's started tied to it (proc.StartTied —
// a job object on Windows), runs in a folder of this server's own (a stray one can't
// write over it), and stops itself after sideCamRun anyway (macOS has no tie), restarted
// at once.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cricketstream/internal/proc"
)

const (
	sideCamSegment = 2                // seconds a segment aims at (it splits on keyframes)
	sideCamWrap    = 20               // segments in the ring
	sideCamRun     = 30 * time.Minute // one FFmpeg run, then a fresh one
	sideClipKeep   = 30               // side-on clips kept in <replays>/side-on
)

var (
	sideMu      sync.Mutex
	sideRunning bool   // FFmpeg is recording
	sideStatus  string // what the panel and log say
	sideStop    = make(chan struct{})
	sideOnce    sync.Once
)

// sideCamDir is this server's ring of segments.
func sideCamDir() string { return filepath.Join(appDir, ".sidecam", strconv.Itoa(os.Getpid())) }

// sideOnEnabled: switched on, with a camera 2 to record.
func sideOnEnabled(st M) bool {
	return gb(st, "replay_side_on", false) && trim(gs(st, "camera2_rtsp_url", "")) != ""
}

// sideCamArgs records camera 2 into the ring: video only (a camera's own audio is often
// a codec MPEG-TS won't carry, and the main replay has the sound).
func sideCamArgs(rtsp, dir string) []string {
	return []string{"-nostdin", "-hide_banner", "-loglevel", "error",
		"-rtsp_transport", "tcp", "-timeout", "10000000", "-i", rtsp,
		"-map", "0:v:0", "-c", "copy", "-t", strconv.Itoa(int(sideCamRun.Seconds())),
		"-f", "segment", "-segment_time", strconv.Itoa(sideCamSegment), "-segment_wrap", strconv.Itoa(sideCamWrap),
		"-reset_timestamps", "1", "-segment_format", "mpegts", filepath.Join(dir, "s%02d.ts")}
}

// startSideCam is the recorder's start (a test seam): FFmpeg, tied to this process.
var startSideCam = func(args []string) (*exec.Cmd, error) {
	cmd := exec.Command("ffmpeg", args...)
	return cmd, proc.StartTied(cmd)
}

var sideSleep = func(d time.Duration) bool {
	select {
	case <-sideStop:
		return false
	case <-time.After(d):
		return true
	}
}

func setSideStatus(running bool, status string) {
	sideMu.Lock()
	sideRunning, sideStatus = running, status
	sideMu.Unlock()
}

// SideCamStatus is the recorder's state, for the panel.
func SideCamStatus() M {
	sideMu.Lock()
	defer sideMu.Unlock()
	return M{"running": sideRunning, "status": sideStatus}
}

// StartSideCam runs the recorder loop for the server's life: it records while side-on
// replays are on, and stops when they're switched off or camera 2's address changes.
func StartSideCam() {
	sideOnce.Do(func() {
		cleanOldSideCam()
		background.Add(1)
		go func() {
			defer background.Done()
			defer func() { recover() }()
			sideCamLoop()
		}()
	})
}

// StopSideCam ends the loop and the recording (server shutdown).
func StopSideCam() {
	sideMu.Lock()
	select {
	case <-sideStop:
	default:
		close(sideStop)
	}
	sideMu.Unlock()
}

// cleanOldSideCam removes rings left by earlier runs (their recorders are gone: tied).
func cleanOldSideCam() {
	ents, _ := os.ReadDir(filepath.Join(appDir, ".sidecam"))
	for _, e := range ents {
		if e.Name() != strconv.Itoa(os.Getpid()) {
			os.RemoveAll(filepath.Join(appDir, ".sidecam", e.Name()))
		}
	}
}

func sideCamLoop() {
	fails := 0
	for {
		st := LoadState()
		if !sideOnEnabled(st) {
			setSideStatus(false, "off")
			if !sideSleep(10 * time.Second) {
				return
			}
			continue
		}
		if !ffmpegFound() {
			setSideStatus(false, "FFmpeg isn't installed — side-on replays need it")
			if !sideSleep(time.Minute) {
				return
			}
			continue
		}
		rtsp, dir := trim(gs(st, "camera2_rtsp_url", "")), sideCamDir()
		os.RemoveAll(dir)
		os.MkdirAll(dir, 0o755)
		cmd, err := startSideCam(sideCamArgs(rtsp, dir))
		if err != nil {
			setSideStatus(false, "couldn't start FFmpeg: "+err.Error())
			if !sideSleep(time.Minute) {
				return
			}
			continue
		}
		setSideStatus(true, "recording camera 2")
		started := time.Now()
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		stopped := false
	watch:
		for {
			select {
			case err = <-done:
				break watch
			case <-sideStop:
				cmd.Process.Kill()
				<-done
				stopped = true
				break watch
			case <-time.After(5 * time.Second):
				// Switched off, or camera 2 changed: stop, and the loop starts again.
				now := LoadState()
				if !sideOnEnabled(now) || trim(gs(now, "camera2_rtsp_url", "")) != rtsp {
					cmd.Process.Kill()
					<-done
					break watch
				}
			}
		}
		if stopped {
			setSideStatus(false, "stopped")
			return
		}
		if time.Since(started) > sideCamRun-time.Minute {
			fails = 0 // a full run: start the next at once
			continue
		}
		// It stopped early: the camera's unreachable, most likely. Wait, longer each time.
		fails++
		wait := min(time.Duration(5<<min(fails, 5))*time.Second, 2*time.Minute)
		msg := "camera 2's stream stopped"
		if err != nil {
			msg += " (" + err.Error() + ")"
		}
		setSideStatus(false, msg+fmt.Sprintf(" — trying again in %ds", int(wait.Seconds())))
		if fails == 1 || fails%10 == 0 {
			say("  ⚠  Side-on replays: %s", msg) // never the URL: it holds the camera's password
		}
		if !sideSleep(wait) {
			return
		}
	}
}

// sideSegments is the ring's finished segments, oldest first: the newest is still being
// written, so it's left out.
func sideSegments(dir string) []string {
	ents, _ := os.ReadDir(dir)
	type seg struct {
		path string
		t    time.Time
	}
	var segs []seg
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".ts") {
			continue
		}
		if info, err := e.Info(); err == nil && info.Size() > 0 {
			segs = append(segs, seg{filepath.Join(dir, e.Name()), info.ModTime()})
		}
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].t.Before(segs[j].t) })
	if len(segs) > 0 {
		segs = segs[:len(segs)-1]
	}
	out := make([]string, len(segs))
	for i, s := range segs {
		out[i] = s.path
	}
	return out
}

// sideWindow picks the segments covering the last `secs` seconds: each finished
// segment's time is when it ended, so it's those that ended inside the window, and the
// one before (it holds the window's start).
func sideWindow(segs []string, secs float64, now time.Time) []string {
	from := len(segs)
	for i := len(segs) - 1; i >= 0; i-- {
		info, err := os.Stat(segs[i])
		if err != nil {
			break
		}
		from = i
		if now.Sub(info.ModTime()).Seconds() > secs {
			break
		}
	}
	return segs[from:]
}

// sideOnClipFor is SideOnClip (a test seam).
var sideOnClipFor = SideOnClip

// SideOnClip cuts camera 2's last `secs` seconds into a clip in <replays>/side-on and
// returns its path ("" and why, when there's no recording to cut from).
func SideOnClip(st M, secs float64) (string, string) {
	segs := sideWindow(sideSegments(sideCamDir()), secs, time.Now())
	if len(segs) == 0 {
		return "", "camera 2 isn't being recorded (" + gsOr(SideCamStatus(), "status", "off") + ")"
	}
	if t, err := os.Stat(segs[len(segs)-1]); err != nil || time.Since(t.ModTime()) > 30*time.Second {
		return "", "camera 2's recording has stopped"
	}
	out := filepath.Join(replayFolder(st), "side-on")
	os.MkdirAll(out, 0o755)
	list := filepath.Join(out, "_concat.txt")
	var b strings.Builder
	for _, s := range segs {
		fmt.Fprintf(&b, "file '%s'\n", strings.ReplaceAll(s, `\`, "/"))
	}
	os.WriteFile(list, []byte(b.String()), 0o644)
	clip := filepath.Join(out, "side_"+time.Now().Format("2006-01-02_15-04-05")+".mp4")
	r, err := proc.Run("ffmpeg", []string{"-nostdin", "-y", "-loglevel", "error", "-f", "concat", "-safe", "0", "-i", list,
		"-c", "copy", "-movflags", "+faststart", clip}, proc.Options{Timeout: 20 * time.Second})
	os.Remove(list)
	if err != nil || r.Code != 0 {
		return "", "FFmpeg couldn't cut the side-on clip"
	}
	keepNewest(out, sideClipKeep)
	return clip, ""
}

// keepNewest deletes all but the newest n clips in dir.
func keepNewest(dir string, n int) {
	m, _ := filepath.Glob(filepath.Join(dir, "side_*.mp4"))
	if len(m) <= n {
		return
	}
	sort.Slice(m, func(i, j int) bool { return mtime(m[i]) < mtime(m[j]) })
	for _, f := range m[:len(m)-n] {
		os.Remove(f)
	}
}
