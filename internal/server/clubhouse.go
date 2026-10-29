package server

// What the optional clubhouse screen (internal/clubhouse) needs from the server beyond
// the control panel's own routes: a small picture of what's on air, and which YouTube
// video is live so the screen can play it full size. Both are login-gated like the rest
// of the operator's reads, and both are cached, so a screen (or several) polling them
// costs OBS one screenshot every couple of seconds and YouTube one lookup a minute or two.

import (
	"encoding/base64"
	"net/url"
	"strings"
	"sync"
	"time"

	"cricketstream/internal/obsws"
)

// previewTTL: the screen asks every ~2s; a second viewer inside that is served the same
// frame. The streaming laptop has overheated before — one small JPEG a couple of times a
// second is what it costs, and only while someone is looking.
const previewTTL = 1500 * time.Millisecond

var previewCache struct {
	sync.Mutex
	at   time.Time
	jpeg []byte
}

// ProgramPreview is what's on air (OBS's program scene, replays and all) as a small
// JPEG, or nil when OBS can't be reached.
func ProgramPreview() []byte {
	previewCache.Lock()
	defer previewCache.Unlock() // held while fetching: concurrent callers share one shot
	if time.Since(previewCache.at) < previewTTL && previewCache.jpeg != nil {
		return previewCache.jpeg
	}
	st := LoadState()
	scene := ""
	if r := obsCall(st, []obsws.Req{{Type: "GetCurrentProgramScene"}}, 3*time.Second); len(r) > 0 && r[0] != nil {
		scene = gsOr(r[0], "currentProgramSceneName", gs(r[0], "sceneName", ""))
	}
	if scene == "" {
		previewCache.jpeg, previewCache.at = nil, time.Time{}
		return nil
	}
	r := obsCall(st, []obsws.Req{{Type: "GetSourceScreenshot", Data: M{"sourceName": scene, "imageFormat": "jpg",
		"imageWidth": 640, "imageCompressionQuality": 60}}}, 4*time.Second)
	var jpeg []byte
	if len(r) > 0 && r[0] != nil {
		if _, b64, ok := strings.Cut(gs(r[0], "imageData", ""), ","); ok {
			jpeg, _ = base64.StdEncoding.DecodeString(b64)
		}
	}
	previewCache.jpeg, previewCache.at = jpeg, time.Now()
	return jpeg
}

const liveVideoTTL = 90 * time.Second

var liveVideoCache struct {
	sync.Mutex
	at  time.Time
	out M
}

// liveVideoLookup finds the channel's live broadcast (active first, then one that's
// ready to go). Swappable for tests.
var liveVideoLookup = func() M {
	if !fileExists(YTCredsFile) || !fileExists(YTTokenFile) {
		return M{"ok": false, "video_id": "", "error": "YouTube isn't connected on the streaming laptop."}
	}
	yt, why := ytService(false)
	if yt == nil {
		return M{"ok": false, "video_id": "", "error": orStr(why, "YouTube isn't connected.")}
	}
	for _, status := range []string{"active", "upcoming"} {
		r, err := yt.Get("liveBroadcasts", url.Values{"part": {"id,snippet"}, "broadcastStatus": {status},
			"broadcastType": {"all"}, "maxResults": {"5"}})
		if err != nil {
			return M{"ok": false, "video_id": "", "error": err.Error()}
		}
		if it := items(r); len(it) > 0 {
			return M{"ok": true, "video_id": gs(it[0], "id", ""), "status": status,
				"title": gs(gm(it[0], "snippet"), "title", "")}
		}
	}
	return M{"ok": true, "video_id": "", "status": "none"}
}

// LiveVideo is GET /youtube/live: {ok, video_id, status, title}, cached.
func LiveVideo(force bool) M {
	liveVideoCache.Lock()
	defer liveVideoCache.Unlock()
	if !force && liveVideoCache.out != nil && time.Since(liveVideoCache.at) < liveVideoTTL {
		return liveVideoCache.out
	}
	out := liveVideoLookup()
	liveVideoCache.out, liveVideoCache.at = out, time.Now()
	return out
}
