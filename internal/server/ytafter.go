package server

// After the stream, on YouTube: the match video gets a thumbnail (both badges, the
// teams and the result — resultcard.Thumbnail), and the match's replays go up as a
// separate highlights video with its own chapters, linked to the full match. Both run
// from afterStreamYouTube (the stream stopped and stayed off), and from the panel's
// YouTube card.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"cricketstream/internal/resultcard"
)

var ytUpload = "https://www.googleapis.com/upload/youtube/v3/"

// highlightsMinClips: fewer replays than this isn't a highlights video.
const highlightsMinClips = 3

// uploadClient is the API client with room for a big upload: the 20s limit on API
// calls would cut a highlights video off on a ground's upload speed.
func (y *YTClient) uploadClient(limit time.Duration) *http.Client {
	c := *y.http
	c.Timeout = limit
	return &c
}

// SetThumbnail uploads a JPEG as the video's thumbnail.
func (y *YTClient) SetThumbnail(videoID, path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	req, _ := http.NewRequest("POST", ytUpload+"thumbnails/set?videoId="+url.QueryEscape(videoID), bytes.NewReader(b))
	req.Header.Set("Content-Type", "image/jpeg")
	resp, err := y.uploadClient(2 * time.Minute).Do(req)
	if err != nil {
		return err
	}
	_, err = ytDecode(resp)
	return err
}

// UploadVideo uploads a video file (YouTube's resumable upload, in one go) and returns
// the new video's id.
func (y *YTClient) UploadVideo(path string, meta M) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(meta)
	req, _ := http.NewRequest("POST", ytUpload+"videos?uploadType=resumable&part=snippet,status", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("X-Upload-Content-Type", "video/mp4")
	req.Header.Set("X-Upload-Content-Length", strconv.FormatInt(st.Size(), 10))
	resp, err := y.http.Do(req)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		_, err := ytDecode(resp)
		return "", err
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", errors.New("YouTube didn't start the upload")
	}
	put, _ := http.NewRequest("PUT", loc, f)
	put.ContentLength = st.Size()
	put.Header.Set("Content-Type", "video/mp4")
	resp, err = y.uploadClient(90 * time.Minute).Do(put)
	if err != nil {
		return "", err
	}
	out, err := ytDecode(resp)
	if err != nil {
		return "", err
	}
	id := gs(out, "id", "")
	if id == "" {
		return "", errors.New("YouTube didn't say the video's id")
	}
	return id, nil
}

// ytThumbHint turns YouTube's refusal of a custom thumbnail into what to do about it.
func ytThumbHint(err error) string {
	s := err.Error()
	if strings.Contains(s, "403") && strings.Contains(strings.ToLower(s), "thumbnail") {
		return "YouTube only allows custom thumbnails on a verified channel — verify it once at youtube.com/verify, then press Set the thumbnail now"
	}
	return "YouTube: " + s
}

// thumbnailFacts is what the thumbnail shows: the result card's facts when there are
// some (the result once it's known), else just today's two teams.
func thumbnailFacts(kicker string) resultcard.Facts {
	cfg := LoadState()
	var f resultcard.Facts
	if facts := GenerateSocialGraphicFacts(); gb(facts, "ok", false) {
		f = factsToCard(facts)
	} else {
		f.Sides = []resultcard.Side{
			{Club: gsOr(cfg, "home_team", "Home"), ClubID: trim(gsOr(cfg, "home_club_id", "")), Ours: true},
			{Club: gsOr(cfg, "away_team", "Opposition"), ClubID: trim(gsOr(cfg, "away_club_id", ""))},
		}
	}
	f.Kicker = kicker
	if f.Competition == "" {
		f.Competition = strings.Split(gs(cfg, "competition_name", ""), " — ")[0]
	}
	if f.Date == "" {
		f.Date = time.Now().Format("2 Jan 2006")
	}
	return f
}

// buildThumbnail renders the thumbnail beside the app (one per day and kind).
func buildThumbnail(kicker, name string) (string, error) {
	cfg := LoadState()
	out := filepath.Join(appDir, fmt.Sprintf("youtube_%s_%s.jpg", name, time.Now().Format("2006-01-02")))
	return resultcard.SaveThumbnail(thumbnailFacts(kicker), resultcard.Look{ClubColour: gs(cfg, "home_colour", "#1a3a5c"),
		LogosDir: logosFolder(cfg), ClubName: gsOr(cfg, "home_team", "Home CC")}, out)
}

// SetMatchThumbnail gives this match's video its thumbnail.
func SetMatchThumbnail(matchID string) (bool, string) {
	yt, msg := ytService(false)
	if yt == nil {
		return false, msg
	}
	id, _, _, err := matchVideo(yt, MatchMoments(matchID))
	if err != nil {
		return false, "YouTube: " + err.Error()
	}
	if id == "" {
		return false, "No YouTube broadcast on this account covers this match — was it streamed from this channel?"
	}
	path, err := buildThumbnail("Live cricket", "thumbnail")
	if err != nil {
		return false, "Couldn't draw the thumbnail: " + err.Error()
	}
	if err := yt.SetThumbnail(id, path); err != nil {
		return false, ytThumbHint(err)
	}
	return true, "Thumbnail set on the match video."
}

// ── Uploads already made (so a highlights video goes up once per match) ──

func ytUploaded(matchID, kind string) string {
	id := ""
	withDB(func(tx *sql.Tx) error {
		return tx.QueryRow("SELECT video_id FROM yt_uploads WHERE match_id=? AND kind=?", matchID, kind).Scan(&id)
	})
	return id
}

func ytRecordUpload(matchID, kind, videoID string) {
	withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT OR REPLACE INTO yt_uploads (match_id, kind, video_id, ts) VALUES (?,?,?,?)",
			matchID, kind, videoID, time.Now().Format("2006-01-02T15:04:05"))
		return err
	})
}

// matchClipFiles is this match's replays still on disk, in the order they were saved.
func matchClipFiles(folder, matchID string) []string {
	var out []string
	withDB(func(tx *sql.Tx) error {
		q, err := tx.Query("SELECT file FROM clips WHERE match_id=? ORDER BY ts", matchID)
		if err != nil {
			return err
		}
		defer q.Close()
		for q.Next() {
			var f sql.NullString
			if q.Scan(&f) == nil {
				if p := filepath.Join(folder, filepath.Base(f.String)); fileExists(p) {
					out = append(out, p)
				}
			}
		}
		return q.Err()
	})
	return out
}

// Seams for tests: building the video (FFmpeg) and finding today's result.
var (
	compileMatchHighlights = compileHighlights
	matchFinished          = func() bool {
		facts := GenerateSocialGraphicFacts()
		return gb(facts, "ok", false) && gs(facts, "outcome", "live") != "live"
	}
)

var (
	ytHighlightsMu     sync.Mutex
	ytHighlightsStatus = M{"running": false, "ok": nil, "message": "", "url": ""}
)

// YTHighlightsStatus is the last highlights upload, for the panel.
func YTHighlightsStatus() M {
	ytHighlightsMu.Lock()
	defer ytHighlightsMu.Unlock()
	return shallow(ytHighlightsStatus)
}

// StartHighlightsUpload runs UploadHighlights in the background; false if one is running.
func StartHighlightsUpload(matchID string, force bool) bool {
	ytHighlightsMu.Lock()
	if gb(ytHighlightsStatus, "running", false) {
		ytHighlightsMu.Unlock()
		return false
	}
	ytHighlightsStatus = M{"running": true, "ok": nil, "message": "Building the highlights…", "url": ""}
	ytHighlightsMu.Unlock()
	safeGo("highlights upload", func() {
		ok, msg := UploadHighlights(matchID, force)
		say("  %s  YouTube highlights: %s %s", tick(ok), msg, gs(YTHighlightsStatus(), "url", ""))
	})
	return true
}

func setHighlightsStatus(ok bool, msg, link string) (bool, string) {
	ytHighlightsMu.Lock()
	ytHighlightsStatus = M{"running": false, "ok": ok, "message": msg, "url": link}
	ytHighlightsMu.Unlock()
	return ok, msg
}

// UploadHighlights builds this match's highlights video from its replays and uploads it
// to YouTube: the same privacy as the match video, chapters for each replay, a link to
// the full match, and the highlights thumbnail. Once per match unless forced (the
// panel's button, after a first try went wrong).
func UploadHighlights(matchID string, force bool) (bool, string) {
	if id := ytUploaded(matchID, "highlights"); id != "" && !force {
		return setHighlightsStatus(true, "The highlights are already on YouTube.", "https://youtu.be/"+id)
	}
	st := LoadState()
	folder := replayFolder(st)
	clips := matchClipFiles(folder, matchID)
	plan, _ := taggedPlan(clips)
	if len(plan) < highlightsMinClips {
		return setHighlightsStatus(false, fmt.Sprintf("Only %d replay%s saved for this match — a highlights video needs %d.",
			len(plan), map[bool]string{true: "", false: "s"}[len(plan) == 1], highlightsMinClips), "")
	}
	yt, msg := ytService(false)
	if yt == nil {
		return setHighlightsStatus(false, msg, "")
	}
	setHighlightsStatus2("Building the highlights video from " + strconv.Itoa(len(plan)) + " replays…")
	// Its own folder: a video in the replay folder would count as a replay (listClips).
	outDir := filepath.Join(folder, "highlights")
	os.MkdirAll(outDir, 0o755)
	output := filepath.Join(outDir, "highlights_"+slugNonAlnum.ReplaceAllString(strings.ToLower(matchID), "_")+".mp4")
	if ok, msg := compileMatchHighlights(clips, output); !ok {
		return setHighlightsStatus(false, msg, "")
	}

	// The match video: its privacy, and a link to it.
	privacy, full := "unlisted", ""
	if id, _, _, err := matchVideo(yt, MatchMoments(matchID)); err == nil && id != "" {
		full = "https://youtu.be/" + id
		if r, err := yt.Get("videos", url.Values{"part": {"status"}, "id": {id}}); err == nil {
			if it := items(r); len(it) > 0 {
				privacy = gsOr(gm(it[0], "status"), "privacyStatus", privacy)
			}
		}
	}
	title := fmt.Sprintf("%s v %s — highlights, %s", gsOr(st, "home_team", "Home"), gsOr(st, "away_team", "Opposition"),
		time.Now().Format("2 Jan 2006"))
	if r := []rune(title); len(r) > 100 {
		title = string(r[:100])
	}
	desc := ""
	if b, err := os.ReadFile(strings.TrimSuffix(output, filepath.Ext(output)) + "_description.txt"); err == nil {
		desc = string(b) // the chapters, from compileHighlights
	}
	if full != "" {
		desc += "\nThe whole match: " + full + "\n"
	}
	// YouTube refuses < and > anywhere in a title or description.
	strip := strings.NewReplacer("<", "", ">", "")
	setHighlightsStatus2("Uploading the highlights to YouTube…")
	id, err := yt.UploadVideo(output, M{
		"snippet": M{"title": strip.Replace(title), "description": strip.Replace(desc), "categoryId": gsOr(st, "youtube_category", "17")},
		"status":  M{"privacyStatus": privacy, "selfDeclaredMadeForKids": false},
	})
	if err != nil {
		return setHighlightsStatus(false, "YouTube: "+err.Error(), "")
	}
	ytRecordUpload(matchID, "highlights", id)
	note := ""
	if path, err := buildThumbnail("Highlights", "highlights"); err == nil {
		if err := yt.SetThumbnail(id, path); err != nil {
			note = " (no thumbnail: " + ytThumbHint(err) + ")"
		}
	}
	return setHighlightsStatus(true, fmt.Sprintf("Highlights uploaded (%s, %d replays)%s.", privacy, len(plan), note), "https://youtu.be/"+id)
}

// setHighlightsStatus2 says what a running upload is doing now.
func setHighlightsStatus2(msg string) {
	ytHighlightsMu.Lock()
	ytHighlightsStatus["running"], ytHighlightsStatus["message"] = true, msg
	ytHighlightsMu.Unlock()
}

// afterStreamYouTube: the stream stopped a minute and a half ago and is still off, so
// the match video is complete — its chapters and thumbnail, then (once the match has a
// result) the highlights video. A stream that only dropped mid-match gets chapters and a
// thumbnail now and again at the end; the highlights wait for the result. Never raises
// (a background timer).
func afterStreamYouTube() {
	defer func() { recover() }()
	if streamingNow() || !fileExists(YTTokenFile) {
		return
	}
	chaptersAfterStreamEnd()
	st, mid := LoadState(), CurrentMatchID()
	if gb(st, "youtube_thumbnail", true) {
		ok, msg := SetMatchThumbnail(mid)
		say("  %s  YouTube thumbnail: %s", tick(ok), msg)
	}
	if gb(st, "youtube_highlights", true) && matchFinished() && ytUploaded(mid, "highlights") == "" {
		StartHighlightsUpload(mid, false)
	}
}
