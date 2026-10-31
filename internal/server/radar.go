package server

// The rain radar for the stream: the last hour of rain around the ground, as a loop of
// pictures the overlay plays in a rain delay (and before play or at a break when rain is
// about). Each frame is drawn here — an OpenStreetMap base, darkened so the rain reads,
// the RainViewer radar on top, the ground marked — so the overlay only swaps images.
//
// Both services are free and need no key. RainViewer's radar stops at zoom 7 (in 512px
// tiles, which line up exactly with the map's zoom-8 256px tiles) and has no forecast
// frames, so the "dry by 15:40" line comes from Open-Meteo's 15-minute rain forecast
// (rainOutlook). Map tiles are cached on disk for good (a ground's map never changes; the
// OSM tile policy asks for exactly that), radar tiles in memory for the build.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	radarW, radarH = 720, 540
	radarFrames    = 7 // RainViewer's frames are 10 minutes apart: the last hour
	radarMapZoom   = 8 // OSM base; the radar is zoom 7 in 512px tiles — the same scale
	radarKeep      = 5 * time.Minute
)

var (
	radarAPI = "https://api.rainviewer.com/public/weather-maps.json"
	osmTile  = "https://tile.openstreetmap.org/%d/%d/%d.png"
	// radarGet fetches a tile or the frame list (a test seam). The OSM tile policy wants a
	// User-Agent that says who's asking.
	radarGet = func(u string) ([]byte, error) {
		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "CricketStream/"+Version+" (+https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream)")
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return nil, &httpError{Code: resp.StatusCode}
		}
		return io.ReadAll(resp.Body)
	}
)

func radarCacheDir() string { return filepath.Join(appDir, ".radar_cache") }

var (
	radarMu     sync.Mutex
	radarBuilt  time.Time
	radarResult M
	radarPNG    = map[int][]byte{}
)

// worldPx is a point's pixel position on the zoom-8 world map.
func worldPx(lat, lon float64) (float64, float64) {
	n := 256 * math.Exp2(radarMapZoom)
	x := (lon + 180) / 360 * n
	y := (1 - math.Asinh(math.Tan(lat*math.Pi/180))/math.Pi) / 2 * n
	return x, y
}

func decodePNG(b []byte) image.Image {
	im, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return nil
	}
	return im
}

// radarBase is the darkened street map around the ground (left, top: its world pixel).
func radarBase(x0, y0 int) *image.RGBA {
	base := image.NewRGBA(image.Rect(0, 0, radarW, radarH))
	draw.Draw(base, base.Bounds(), image.NewUniform(color.RGBA{30, 34, 42, 255}), image.Point{}, draw.Src)
	for ty := y0 / 256; ty <= (y0+radarH-1)/256; ty++ {
		for tx := x0 / 256; tx <= (x0+radarW-1)/256; tx++ {
			p := filepath.Join(radarCacheDir(), fmt.Sprintf("osm-%d-%d-%d.png", radarMapZoom, tx, ty))
			b, err := os.ReadFile(p)
			if err != nil {
				if b, err = radarGet(fmt.Sprintf(osmTile, radarMapZoom, tx, ty)); err != nil {
					continue // a missing square stays plain; the rain still shows
				}
				os.MkdirAll(radarCacheDir(), 0o755)
				os.WriteFile(p, b, 0o644)
			}
			if im := decodePNG(b); im != nil {
				at := image.Pt(tx*256-x0, ty*256-y0)
				draw.Draw(base, image.Rectangle{at, at.Add(image.Pt(256, 256))}, im, im.Bounds().Min, draw.Src)
			}
		}
	}
	// Darken it: the map is the background, the rain is the story.
	draw.Draw(base, base.Bounds(), image.NewUniform(color.RGBA{10, 14, 22, 120}), image.Point{}, draw.Over)
	return base
}

// radarFrame is one radar picture over the base, the ground marked.
func radarFrame(base *image.RGBA, host, path string, x0, y0 int, tiles map[string]image.Image) []byte {
	im := image.NewRGBA(base.Bounds())
	draw.Draw(im, im.Bounds(), base, image.Point{}, draw.Src)
	for ty := y0 / 512; ty <= (y0+radarH-1)/512; ty++ {
		for tx := x0 / 512; tx <= (x0+radarW-1)/512; tx++ {
			u := fmt.Sprintf("%s%s/512/%d/%d/%d/2/1_1.png", host, path, radarMapZoom-1, tx, ty)
			t, ok := tiles[u]
			if !ok {
				if b, err := radarGet(u); err == nil {
					t = decodePNG(b)
				}
				tiles[u] = t
			}
			if t != nil {
				at := image.Pt(tx*512-x0, ty*512-y0)
				draw.Draw(im, image.Rectangle{at, at.Add(image.Pt(512, 512))}, t, t.Bounds().Min, draw.Over)
			}
		}
	}
	// The ground: a white dot in a dark ring, in the middle.
	cx, cy := radarW/2, radarH/2
	for y := cy - 10; y <= cy+10; y++ {
		for x := cx - 10; x <= cx+10; x++ {
			d := math.Hypot(float64(x-cx), float64(y-cy))
			switch {
			case d <= 6:
				im.Set(x, y, color.RGBA{255, 255, 255, 255})
			case d <= 9:
				im.Set(x, y, color.RGBA{10, 14, 22, 255})
			}
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, im)
	return buf.Bytes()
}

// RainRadar is the radar loop for the overlay: {"ok", "frames": [{"time", "label",
// "img"}], plus the rain outlook}. Kept 5 minutes (RainViewer adds a frame every 10).
func RainRadar() M {
	radarMu.Lock()
	defer radarMu.Unlock()
	if radarResult != nil && time.Since(radarBuilt) < radarKeep {
		return radarResult
	}
	out := buildRadar()
	if gb(out, "ok", false) {
		radarResult, radarBuilt = out, time.Now()
	}
	return out
}

func buildRadar() M {
	b, err := radarGet(radarAPI)
	if err != nil {
		return M{"ok": false, "error": "RainViewer: " + err.Error()}
	}
	var maps struct {
		Host  string `json:"host"`
		Radar struct {
			Past []struct {
				Time int64  `json:"time"`
				Path string `json:"path"`
			} `json:"past"`
		} `json:"radar"`
	}
	if err := json.Unmarshal(b, &maps); err != nil || maps.Host == "" || len(maps.Radar.Past) == 0 {
		return M{"ok": false, "error": "RainViewer sent no radar frames"}
	}
	past := maps.Radar.Past
	if len(past) > radarFrames {
		past = past[len(past)-radarFrames:]
	}
	lat, lon := groundCoords()
	gx, gy := worldPx(lat, lon)
	x0, y0 := int(math.Round(gx))-radarW/2, int(math.Round(gy))-radarH/2
	base := radarBase(x0, y0)
	tiles := map[string]image.Image{}
	frames := []any{}
	radarPNG = map[int][]byte{}
	for i, f := range past {
		radarPNG[i] = radarFrame(base, maps.Host, f.Path, x0, y0, tiles)
		frames = append(frames, M{"time": f.Time, "label": time.Unix(f.Time, 0).Format("15:04"),
			"img": fmt.Sprintf("/weather/radar/frame/%d.png?t=%d", i, f.Time)})
	}
	out := M{"ok": true, "frames": frames, "credit": "Map © OpenStreetMap contributors · Radar: RainViewer"}
	for k, v := range rainOutlook() {
		out[k] = v
	}
	return out
}

// RadarFramePNG is one built frame, or nil.
func RadarFramePNG(i int) []byte {
	radarMu.Lock()
	defer radarMu.Unlock()
	return radarPNG[i]
}

// ── When it'll rain, and stop ──

// rainWet is rain worth calling rain: 0.1 mm in a quarter of an hour.
const rainWet = 0.1

// weatherGet is httpGetJSON (a test seam).
var weatherGet = httpGetJSON

// rainOutlook is Open-Meteo's next three hours at the ground in 15-minute steps:
// whether it's raining now, when it should start or stop, and a line for the screen.
func rainOutlook() M {
	lat, lon := groundCoords()
	u := fmt.Sprintf("https://api.open-meteo.com/v1/forecast?latitude=%v&longitude=%v&minutely_15=precipitation"+
		"&forecast_minutely_15=13&timezone=Europe%%2FLondon", lat, lon)
	data, err := weatherGet(u, 8*time.Second, nil)
	if err != nil {
		return M{"outlook": ""}
	}
	m := gm(data, "minutely_15")
	return outlookFrom(gl(m, "time"), gl(m, "precipitation"))
}

// outlookFrom reads the forecast: slot 0 is the quarter-hour we're in.
func outlookFrom(times, rain []any) M {
	n := min(len(times), len(rain))
	for i := 0; i < n; i++ {
		if _, ok := rain[i].(float64); !ok {
			n = i // a gap in the forecast: say nothing about it or beyond, least of all "dry"
		}
	}
	if n == 0 {
		return M{"outlook": ""}
	}
	wet := func(i int) bool {
		if i >= n {
			return false
		}
		f, _ := rain[i].(float64)
		return f >= rainWet
	}
	hhmm := func(i int) string {
		t, _ := times[i].(string)
		if len(t) >= 16 {
			return t[11:16]
		}
		return t
	}
	out := M{"rain_now": wet(0), "rain_starts": "", "rain_stops": "", "rain_in_min": -1}
	if wet(0) {
		for i := 1; i < n; i++ {
			if !wet(i) && (i+1 >= n || !wet(i+1)) {
				out["rain_stops"], out["outlook"] = hhmm(i), "Dry by about "+hhmm(i)
				return out
			}
		}
		out["outlook"] = "Rain set to continue for the next few hours"
		return out
	}
	for i := 1; i < n; i++ {
		if wet(i) {
			out["rain_starts"], out["rain_in_min"], out["outlook"] = hhmm(i), i*15, "Rain expected around "+hhmm(i)
			return out
		}
	}
	out["outlook"] = "No rain forecast for the next three hours"
	return out
}
