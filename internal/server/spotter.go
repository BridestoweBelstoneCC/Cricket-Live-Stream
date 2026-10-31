package server

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"cricketstream/internal/ai"
	"cricketstream/internal/obsws"
	"cricketstream/internal/voice"
)

// scoreFeedStatus is (fresh, detail) for whichever score source is in use — manual
// scoring outranks the scorer's file, then agent / bridge / local: the same precedence
// /live applies. One shared rule for every feed consumer, so it can't drift.
func scoreFeedStatus(s M, t float64) (bool, string) {
	if t == 0 {
		t = now()
	}
	if ManualSessionActive() {
		return true, "Scoring by hand on the /scoring page."
	}
	if gs(s, "pcs_source", "local") == "agent" {
		agentMu.Lock()
		ok := agentLastOK != 0 && t-agentLastOK < 120
		agentMu.Unlock()
		if ok {
			return true, "Scorer's laptop connected and sending."
		}
		return false, "Can't see the scorer's laptop yet."
	}
	folder := EffectivePCSFolder(s)
	if folder == "" {
		return false, "No scoreboard folder set (Setup → Scoring source)."
	}
	path := FindPCSOutputFile(folder)
	if path == "" {
		return false, "No scoreboard file yet — NV Play writes it after the first ball."
	}
	age := t - mtime(path)
	if age < 120 {
		return true, fmt.Sprintf("Scoreboard updated %ds ago.", int(age))
	}
	return false, fmt.Sprintf("Last scoreboard update %s ago — has the scorer started?", fmtDuration(age))
}

// ── AI camera spotter ──
// While live, every few minutes Claude Haiku looks at a frame from each camera and flags
// what a viewer would notice — fogged or wet lens, camera knocked off the pitch, glare,
// too dark, something blocking it, a black or test picture. A frozen picture is caught
// without AI: two byte-identical frames in a row (real cameras have sensor noise). Off by
// default (it spends the club's AI credit), and never off-air. An API failure is
// "couldn't check", never a camera problem.
const SpotterTickSec = 60

const SpotterPrompt = "This is a frame from a live-streamed grassroots cricket match camera. Would a " +
	"viewer notice a problem with the picture? Look only for: a fogged, wet, dirty or " +
	"rain-spotted lens; the camera knocked or tilted so the pitch isn't properly in " +
	"shot; heavy glare or the picture washed out; far too dark; something close to " +
	"the lens blocking the view; a black, frozen-looking or test-pattern picture. " +
	"Normal things are fine: players, empty field between balls, clouds, shadows, " +
	"spectators in the distance. Reply with ONLY a JSON object: " +
	`{"ok": true or false, "problem": "<at most 8 words, empty if ok>"}`

var (
	spotterCams    = map[string]M{}
	spotterLastRun float64
	spotterRunning bool
	spotterMu      sync.Mutex
)

func spotterCameras(st M) [][2]string {
	cams := [][2]string{{"Wide camera", trim(gs(st, "obs_camera_name", ""))}}
	if trim(gs(st, "camera2_rtsp_url", "")) != "" {
		cams = append(cams, [2]string{"Camera 2", trim(gs(st, "obs_camera2_name", ""))})
	}
	var out [][2]string
	for _, c := range cams {
		if c[1] != "" {
			out = append(out, c)
		}
	}
	return out
}

// obsFrame is a camera source as OBS sees it, as JPEG bytes, or nil. Swappable.
var obsFrame = func(st M, source string) []byte {
	r := obsCall(st, []obsws.Req{{Type: "GetSourceScreenshot", Data: M{"sourceName": source, "imageFormat": "jpg",
		"imageWidth": 768, "imageCompressionQuality": 70}}}, 8*time.Second)
	if len(r) == 0 || r[0] == nil {
		return nil
	}
	data := gs(r[0], "imageData", "")
	_, b64, ok := strings.Cut(data, ",")
	if !ok {
		return nil
	}
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil
	}
	return b
}

// aiLook asks Claude about one frame: (ok, problem), or an error on API trouble.
var aiLook = func(st M, jpeg []byte) (bool, string, error) {
	text, err := ai.Ask(context.Background(), ai.Request{Key: anthropicKey(st), Prompt: SpotterPrompt,
		MaxTokens: 150, ImageJPEG: jpeg, Timeout: 30 * time.Second})
	if err != nil {
		return false, "", err
	}
	raw := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(text, "```json", ""), "```", ""))
	var d M
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return false, "", err
	}
	return gb(d, "ok", true), truncRunes(trim(gsOr(d, "problem", "")), 80), nil
}

// SpotCameras checks every camera once and returns the camera records.
func SpotCameras(st M, useAI bool) M {
	if st == nil {
		st = LoadState()
	}
	spotterMu.Lock()
	if spotterRunning {
		out := M{}
		for k, v := range spotterCams {
			out[k] = v
		}
		spotterMu.Unlock()
		return out
	}
	spotterRunning = true
	spotterMu.Unlock()
	defer func() {
		spotterMu.Lock()
		spotterRunning = false
		spotterMu.Unlock()
	}()
	for _, c := range spotterCameras(st) {
		label, source := c[0], c[1]
		spotterMu.Lock()
		prev := spotterCams[source]
		spotterMu.Unlock()
		rec := M{"label": label, "checked_at": now(), "ok": true, "problem": "", "how": "ai"}
		jpeg := obsFrame(st, source)
		if jpeg == nil {
			rec["ok"], rec["problem"], rec["how"] = false, "No picture from OBS", "obs"
		} else {
			sum := sha1.Sum(jpeg)
			digest := hex.EncodeToString(sum[:])
			rec["digest"] = digest
			if prev != nil && gs(prev, "digest", "") == digest {
				rec["ok"], rec["problem"], rec["how"] = false, "Picture frozen — same frame twice", "frozen"
			} else if useAI {
				ok, problem, err := aiLook(st, jpeg)
				if err != nil {
					rec["how"], rec["problem"] = "error", "Couldn't check: "+truncRunes(err.Error(), 80)
				} else {
					rec["ok"] = ok
					if !ok {
						rec["problem"] = problem
					}
				}
			}
		}
		spotterMu.Lock()
		spotterCams[source] = rec
		spotterMu.Unlock()
	}
	spotterMu.Lock()
	defer spotterMu.Unlock()
	spotterLastRun = now()
	out := M{}
	for k, v := range spotterCams {
		out[k] = v
	}
	return out
}

// spotterDue: only when switched on, live (off-air checks would spend credit for
// nothing), with an AI key, and the interval has passed.
func spotterDue(st M, live bool, t float64) bool {
	if t == 0 {
		t = now()
	}
	every := float64(max(1, gi(st, "camera_spotter_minutes", 5)) * 60)
	if gi(st, "camera_spotter_minutes", 0) == 0 {
		every = 5 * 60
	}
	spotterMu.Lock()
	last := spotterLastRun
	spotterMu.Unlock()
	return gb(st, "camera_spotter", false) && live && trim(gs(st, "anthropic_api_key", "")) != "" && t-last >= every
}

func StartCameraSpotter() {
	loop("Camera spotter", SpotterTickSec*time.Second, func() {
		st := LoadState()
		if spotterDue(st, streamingNow(), 0) {
			SpotCameras(st, true)
		}
	})
}

func spotterStatus() M {
	spotterMu.Lock()
	defer spotterMu.Unlock()
	cams := []any{}
	problems := 0
	for _, k := range sortedKeys(spotterCams) {
		rec := M{}
		for kk, v := range spotterCams[k] {
			if kk != "digest" {
				rec[kk] = v
			}
		}
		if !gb(rec, "ok", true) {
			problems++
		}
		cams = append(cams, rec)
	}
	var last any
	if spotterLastRun != 0 {
		last = spotterLastRun
	}
	return M{"cameras": cams, "last_run": last, "problems": problems}
}

// ── Spoken commentary (optional) ──
// The end-of-over line read aloud by the computer's own voice. The overlay plays it as
// the commentary panel appears; OBS sends the overlay's audio into the stream.
var voiceKeep = 20

// synthesize is swappable for tests.
var synthesize = func(text, out string) (bool, string) { return voice.Synthesize(text, out, "", 0) }

// SpeakOverCommentary synthesizes the line and points the over commentary at it — only
// if that's still the same over (a slow synth must not land on the next one). Never
// fails.
func SpeakOverCommentary(text string, overNum int) {
	defer func() {
		if r := recover(); r != nil {
			say("  ✗  Spoken commentary: %v", r)
		}
	}()
	name := fmt.Sprintf("over_%d.wav", overNum)
	if ok, msg := synthesize(text, filepath.Join(VoiceDir, name)); !ok {
		say("  ✗  Spoken commentary: %s", msg)
		return
	}
	overCommentaryMu.Lock()
	if gi(overCommentary, "over", -1) == overNum {
		c := shallow(overCommentary)
		c["voice"] = fmt.Sprintf("/voice/%s?t=%d", name, time.Now().Unix())
		overCommentary = c
	}
	overCommentaryMu.Unlock()
	old, _ := filepath.Glob(filepath.Join(VoiceDir, "*.wav"))
	sort.SliceStable(old, func(i, j int) bool { return mtime(old[i]) < mtime(old[j]) })
	if len(old) > voiceKeep {
		for _, f := range old[:len(old)-voiceKeep] {
			os.Remove(f)
		}
	}
}
