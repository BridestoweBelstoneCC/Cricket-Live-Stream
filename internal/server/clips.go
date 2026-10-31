package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"cricketstream/internal/ai"
	"cricketstream/internal/assets"
	"cricketstream/internal/proc"
	"cricketstream/internal/resultcard"
	"cricketstream/internal/socialclips"
)

// controlHTML is the panel with the kit-colour presets injected in place of the
// placeholder. Read per request (an app-folder copy wins over the built-in one), so
// panel edits show up on a browser refresh with no restart.
func controlHTML() ([]byte, error) {
	b, err := assets.Read("control.html")
	if err != nil {
		return nil, err
	}
	presets, _ := json.Marshal(KitPresets)
	return []byte(strings.Replace(string(b), "/*__KIT_PRESETS__*/[]", string(presets), 1)), nil
}

// ── Post-match highlights compiler ──
// It runs in the background, so the panel polls GET /highlights/status for the outcome.
var (
	highlightsStatus = M{"running": false, "ok": nil, "message": ""}
	highlightsMu     sync.Mutex
)

// ffFilterPath makes a path safe INSIDE an ffmpeg filter option value: forward
// slashes, and ':' escaped (the parser treats it as a separator — every Windows drive).
func ffFilterPath(p string) string {
	return strings.ReplaceAll(strings.ReplaceAll(p, `\`, "/"), ":", `\:`)
}

// ffmpegFound is swappable for tests.
var ffmpegFound = func() bool { _, err := exec.LookPath("ffmpeg"); return err == nil }

const noFFmpeg = "FFmpeg not found — download from https://ffmpeg.org/download.html and add to PATH"

// ── Social clips (vertical 9:16, for Shorts / Reels / TikTok) ──
// After the match, every tagged replay becomes a vertical clip with a headline, a short
// line and the club badge, plus a ready-to-paste caption. Post-match by design:
// re-encoding video mid-match would compete with OBS's encoder.
var (
	socialClipsStatus = M{"running": false, "ok": nil, "message": "", "done": 0, "total": 0}
	socialClipsMu     sync.Mutex
)

func replayFolder(cfg M) string {
	if f := gs(cfg, "replay_folder", ""); f != "" {
		return f
	}
	return defaultReplayFolder()
}

// socialClipsDir is swappable for tests.
var socialClipsDir = func(folder string) string {
	if folder == "" {
		folder = replayFolder(LoadState())
	}
	return filepath.Join(folder, "social_clips", time.Now().Format("2006-01-02"))
}

// aiClipTexts is (on-screen line, post caption) for one clip. Claude writes both from
// the clip's tag; without a key — or if the call fails — the tag itself and a plain
// caption are used, so clips are still made offline. The prompt must keep saying the tag
// doesn't know which side a player is on: without it, the model wrote "takes us to 60-1"
// about an opposition batter. Swappable for tests.
var aiClipTexts = func(caption, reason string, cfg M) (string, string) {
	club := gsOr(cfg, "home_team", "the club")
	fbLine, fbPost := caption, caption+" 🏏 #cricket #villagecricket"
	key := trim(gs(cfg, "anthropic_api_key", ""))
	if key == "" || caption == "" {
		return fbLine, fbPost
	}
	prompt := fmt.Sprintf("A cricket club (%s) is posting a short vertical video clip of this moment "+
		"from their live stream:\n%s\n\n", club, caption) +
		"That text doesn't say which team the player is on — it could be the club's " +
		"player or the opposition's — so never write \"we\", \"us\" or \"our\" about " +
		"the moment. Name the player and what happened.\n" +
		"Return ONLY a JSON object with two keys:\n" +
		"  \"line\": one punchy line shown on the video, max 70 characters, no hashtags, " +
		"no emojis — use only facts given above\n" +
		"  \"post\": a caption for Instagram/TikTok, max 40 words, at most 1 emoji and 3 " +
		"hashtags, facts from above only"
	text, err := ai.Ask(context.Background(), ai.Request{Key: key, Prompt: prompt, MaxTokens: 300, Timeout: 30 * time.Second})
	if err != nil {
		return fbLine, fbPost
	}
	raw := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(text, "```json", ""), "```", ""))
	var d M
	if json.Unmarshal([]byte(raw), &d) != nil {
		return fbLine, fbPost
	}
	line := truncRunes(trim(gsOr(d, "line", "")), 90)
	if line == "" {
		line = caption
	}
	post := trim(gsOr(d, "post", ""))
	if post == "" {
		post = fbPost
	}
	return line, post
}

// renderClip is swappable for tests.
var renderClip = func(src, out, headline, line, work, badge, handle, accent string) (bool, string) {
	return socialclips.Render(src, out, headline, line, work, badge, handle, accent, 0)
}

func replayClips(folder string) []string {
	var out []string
	for _, c := range listClips(folder) {
		if !strings.Contains(strings.ToLower(filepath.Base(c)), "highlights") {
			out = append(out, c)
		}
	}
	return out
}

// taggedPlan tags every clip (recorded tags, else guesses from the ball log) and plans
// them in match order.
func taggedPlan(clips []string) ([]PlanEntry, map[string]ClipTag) {
	tags := clipTags(clips)
	var untagged []string
	for _, c := range clips {
		if _, ok := tags[filepath.Base(c)]; !ok {
			untagged = append(untagged, c)
		}
	}
	for name, cap := range guessClipTags(untagged, 90) {
		tags[name] = ClipTag{"auto", cap}
	}
	return planHighlights(clips, tags), tags
}

var slugNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// MakeSocialClips renders a vertical clip for every tagged replay (test and untagged
// clips are skipped). Already-made clips are kept, so re-running only adds new ones.
func MakeSocialClips(folder string) (bool, string, []string) {
	if !ffmpegFound() {
		return false, noFFmpeg, nil
	}
	cfg := LoadState()
	if folder == "" {
		folder = replayFolder(cfg)
	}
	all, tags := taggedPlan(replayClips(folder))
	var plan []PlanEntry
	for _, e := range all {
		if e.Caption != "" {
			plan = append(plan, e)
		}
	}
	if len(plan) == 0 {
		return false, "No tagged replays to make clips from yet", nil
	}
	outDir := socialClipsDir(folder)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return false, err.Error(), nil
	}
	badge := ""
	if cid := trim(gsOr(cfg, "home_club_id", "")); cid != "" {
		for _, e := range []string{".png", ".webp", ".jpg"} {
			if p := filepath.Join(logosFolder(cfg), cid+e); fileExists(p) {
				badge = p
				break
			}
		}
	}
	hi := resultcard.NewPalette(gs(cfg, "home_colour", "#1a3a5c")).Hi
	accent := fmt.Sprintf("0x%02X%02X%02X", hi[0], hi[1], hi[2])
	handle := trim(gs(cfg, "social_handle", ""))
	work, err := os.MkdirTemp("", "social_clips_")
	if err != nil {
		return false, err.Error(), nil
	}
	defer os.RemoveAll(work)
	socialClipsMu.Lock()
	socialClipsStatus["total"], socialClipsStatus["done"] = len(plan), 0
	socialClipsMu.Unlock()
	made := []string{}
	failed := 0
	for i, e := range plan {
		reason := tags[filepath.Base(e.File)].Reason
		src := reason
		if reason == "auto" {
			src = e.Caption
		}
		headline := socialclips.HeadlineFor(src)
		slug := strings.Trim(slugNonAlnum.ReplaceAllString(strings.ToLower(headline), "-"), "-")
		if slug == "" {
			slug = "clip"
		}
		out := filepath.Join(outDir, fmt.Sprintf("%02d_%s.mp4", i+1, slug))
		if !(fileExists(out) && mtime(out) >= mtime(e.File)) {
			line, post := aiClipTexts(e.Caption, reason, cfg)
			if ok, msg := renderClip(e.File, out, headline, line, work, badge, handle, accent); !ok {
				failed++
				say("  ✗  Social clip %d: %s", i+1, msg)
				continue
			}
			os.WriteFile(strings.TrimSuffix(out, filepath.Ext(out))+".txt", []byte(post+"\n"), 0o644)
		}
		made = append(made, filepath.Base(out))
		socialClipsMu.Lock()
		socialClipsStatus["done"] = i + 1
		socialClipsMu.Unlock()
	}
	msg := fmt.Sprintf("%d %s ready in %s", len(made), plural(len(made), "clip"), outDir)
	if failed > 0 {
		msg += fmt.Sprintf(" (%d failed — see the server window)", failed)
	}
	return len(made) > 0, msg, made
}

func listSocialClips() []any {
	files, _ := filepath.Glob(filepath.Join(socialClipsDir(""), "*.mp4"))
	sort.Strings(files)
	out := []any{}
	for _, f := range files {
		b, _ := os.ReadFile(strings.TrimSuffix(f, filepath.Ext(f)) + ".txt")
		out = append(out, M{"name": filepath.Base(f), "caption": strings.TrimSpace(string(b))})
	}
	return out
}

// CompileHighlights stitches the replay clips into a captioned highlights reel, in match
// order. Each tagged clip has its caption burned in as a lower-third; replay-test clips
// are excluded. A YouTube-ready description with chapter timestamps goes next to it.
func CompileHighlights(folder, output string) (bool, string) {
	if !ffmpegFound() {
		return false, noFFmpeg
	}
	clips := replayClips(folder)
	if len(clips) == 0 {
		return false, "No replay clips found in " + folder
	}
	return compileHighlights(clips, output)
}

// compileHighlights is CompileHighlights for a given set of clips (the YouTube
// highlights video uses only this match's).
func compileHighlights(clips []string, output string) (bool, string) {
	if !ffmpegFound() {
		return false, noFFmpeg
	}
	plan, _ := taggedPlan(clips)
	if len(plan) == 0 {
		return false, "Only replay-test clips found — nothing worth compiling"
	}
	tagged := 0
	for _, e := range plan {
		if e.Caption != "" {
			tagged++
		}
	}
	say("  📎  Compiling %d clips (%d captioned) into highlights reel...", len(plan), tagged)
	font := fontPath()
	tmp, err := os.MkdirTemp("", "highlights_")
	if err != nil {
		return false, err.Error()
	}
	defer os.RemoveAll(tmp)
	var concat strings.Builder
	var captions []string
	var durations []float64
	allKnown := true
	for i, e := range plan {
		src := e.File
		if e.Caption != "" && font != "" {
			// The caption goes in via a textfile: no escaping minefield for the text itself.
			txt := filepath.Join(tmp, fmt.Sprintf("cap_%03d.txt", i))
			os.WriteFile(txt, []byte(e.Caption), 0o644)
			vf := "drawtext=textfile='" + ffFilterPath(txt) + "':fontfile='" + ffFilterPath(font) + "'" +
				":fontcolor=white:fontsize=38:x=48:y=h-th-48:box=1:boxcolor=black@0.55:boxborderw=16"
			captioned := filepath.Join(tmp, fmt.Sprintf("cap_%03d.mp4", i))
			r, err := proc.Run("ffmpeg", []string{"-y", "-i", src, "-vf", vf, "-c:v", "libx264", "-preset", "veryfast",
				"-crf", "20", "-c:a", "aac", "-b:a", "160k", captioned}, proc.Options{Timeout: 180 * time.Second, BelowNormal: true})
			if err == nil && r.Code == 0 {
				src = captioned // caption failed? fall back to the raw clip
			}
		}
		fmt.Fprintf(&concat, "file '%s'\n", strings.ReplaceAll(src, `\`, "/"))
		d := clipDuration(src)
		allKnown = allKnown && d > 0
		captions, durations = append(captions, e.Caption), append(durations, d)
	}
	cf := filepath.Join(tmp, "_concat.txt")
	os.WriteFile(cf, []byte(concat.String()), 0o644)
	r, err := proc.Run("ffmpeg", []string{"-y", "-f", "concat", "-safe", "0", "-i", cf, "-c:v", "libx264", "-crf", "23",
		"-preset", "fast", "-c:a", "aac", "-b:a", "128k", output}, proc.Options{Timeout: 600 * time.Second, BelowNormal: true})
	if errors.Is(err, proc.ErrTimeout) {
		return false, "FFmpeg timed out — too many clips or slow machine"
	}
	if err != nil {
		return false, err.Error()
	}
	if r.Code != 0 {
		tail := r.Stderr
		if len(tail) > 200 {
			tail = tail[len(tail)-200:]
		}
		return false, "FFmpeg error: " + tail
	}
	// The chapters file — only when every clip's duration is known (ffprobe).
	note := ""
	if allKnown {
		cfg := LoadState()
		title := fmt.Sprintf("%s v %s — highlights, %s", gs(cfg, "home_team", "Home"), gs(cfg, "away_team", "Opposition"),
			time.Now().Format("02 Jan 2006"))
		desc := strings.TrimSuffix(output, filepath.Ext(output)) + "_description.txt"
		if os.WriteFile(desc, []byte(chaptersText(captions, durations, title)), 0o644) == nil {
			note = fmt.Sprintf(" + chapter list (%s)", filepath.Base(desc))
		}
	}
	return true, fmt.Sprintf("Highlights saved to %s (%d clips, %d captioned%s)", output, len(plan), tagged, note)
}
