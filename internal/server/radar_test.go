package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync"
	"testing"
	"time"
)

func solidPNG(w, h int, c color.Color) []byte {
	im := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			im.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	png.Encode(&b, im)
	return b.Bytes()
}

func TestOutlookFrom(t *testing.T) {
	ts := []any{"2026-06-06T14:00", "2026-06-06T14:15", "2026-06-06T14:30", "2026-06-06T14:45", "2026-06-06T15:00"}
	for _, c := range []struct {
		rain []any
		want string
	}{
		{[]any{0.0, 0.0, 0.0, 0.0, 0.0}, "No rain forecast for the next three hours"},
		{[]any{0.0, 0.05, 0.4, 1.0, 0.0}, "Rain expected around 14:30"},
		{[]any{0.8, 0.5, 0.0, 0.2, 0.0}, "Dry by about 15:00"}, // one dry quarter isn't dry
		{[]any{0.8, 0.5, 0.0, 0.0, 0.0}, "Dry by about 14:30"},
		{[]any{0.8, 0.5, 0.6, 0.7, 0.9}, "Rain set to continue for the next few hours"},
		{[]any{0.8, nil}, "Rain set to continue for the next few hours"},
	} {
		if got := gs(outlookFrom(ts, c.rain), "outlook", ""); got != c.want {
			t.Errorf("%v: %q", c.rain, got)
		}
	}
	o := outlookFrom(ts, []any{0.0, 0.0, 0.3})
	if o["rain_now"] != false || o["rain_starts"] != "14:30" || o["rain_in_min"] != 30 {
		t.Fatalf("%v", o)
	}
	if gs(outlookFrom(nil, nil), "outlook", "x") != "" {
		t.Fatal("no forecast should say nothing")
	}
}

type fakeRadar struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeRadar) get(u string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, u)
	f.mu.Unlock()
	switch {
	case u == radarAPI:
		var past []any
		for i := 0; i < 9; i++ {
			past = append(past, M{"time": 1780000000 + i*600, "path": fmt.Sprintf("/v2/radar/f%d", i)})
		}
		return json.Marshal(M{"host": "https://radar.test", "radar": M{"past": past, "nowcast": []any{}}})
	case strings.Contains(u, "openstreetmap"):
		return solidPNG(256, 256, color.NRGBA{0, 200, 0, 255}), nil
	case strings.HasPrefix(u, "https://radar.test/"):
		return solidPNG(512, 512, color.NRGBA{0, 0, 255, 160}), nil
	}
	return nil, fmt.Errorf("unexpected %s", u)
}

func (f *fakeRadar) count(sub string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.Contains(c, sub) {
			n++
		}
	}
	return n
}

func TestRainRadarFrames(t *testing.T) {
	sandbox(t, "")
	f := &fakeRadar{}
	savedGet, savedWeather := radarGet, weatherGet
	radarGet = f.get
	weatherGet = func(string, time.Duration, map[string]string) (M, error) {
		return M{"minutely_15": M{"time": []any{"2026-06-06T14:00", "2026-06-06T14:15", "2026-06-06T14:30"},
			"precipitation": []any{0.6, 0.0, 0.0}}}, nil
	}
	t.Cleanup(func() {
		radarGet, weatherGet = savedGet, savedWeather
		radarMu.Lock()
		radarResult, radarPNG = nil, map[int][]byte{}
		radarMu.Unlock()
	})
	r := RainRadar()
	if !gb(r, "ok", false) {
		t.Fatal(r)
	}
	frames := gl(r, "frames")
	// The last hour: the newest seven of nine, in order.
	if len(frames) != radarFrames || !strings.HasPrefix(gs(frames[6].(M), "img", ""), "/weather/radar/frame/6.png") ||
		gs(r, "outlook", "") != "Dry by about 14:15" || !strings.Contains(gs(r, "credit", ""), "OpenStreetMap") {
		t.Fatalf("%v", r)
	}
	if f.count("/v2/radar/f1/") != 0 || f.count("/v2/radar/f2/") == 0 {
		t.Fatal("not the newest frames")
	}
	im, err := png.Decode(bytes.NewReader(RadarFramePNG(6)))
	if err != nil {
		t.Fatal(err)
	}
	if b := im.Bounds(); b.Dx() != radarW || b.Dy() != radarH {
		t.Fatal(b)
	}
	// The ground in the middle in white; elsewhere the green map, darkened, under blue rain.
	if r, g, b, _ := im.At(radarW/2, radarH/2).RGBA(); r>>8 != 255 || g>>8 != 255 || b>>8 != 255 {
		t.Fatalf("marker: %d %d %d", r>>8, g>>8, b>>8)
	}
	if r, g, b, _ := im.At(20, 20).RGBA(); !(b>>8 > 100 && g>>8 < 120 && r>>8 < 40) {
		t.Fatalf("map + rain: %d %d %d", r>>8, g>>8, b>>8)
	}
	// Kept for five minutes; built again after, with the map from the disk cache.
	osm := f.count("openstreetmap")
	RainRadar()
	if f.count(radarAPI) != 1 {
		t.Fatal("rebuilt within five minutes")
	}
	radarMu.Lock()
	radarBuilt = time.Now().Add(-radarKeep - time.Second)
	radarMu.Unlock()
	RainRadar()
	if f.count(radarAPI) != 2 || f.count("openstreetmap") != osm {
		t.Fatalf("api %d, map tiles %d then %d", f.count(radarAPI), osm, f.count("openstreetmap"))
	}
	if RadarFramePNG(99) != nil {
		t.Fatal("a frame that doesn't exist")
	}
}

func TestRainRadarWithoutRadar(t *testing.T) {
	sandbox(t, "")
	saved := radarGet
	radarGet = func(string) ([]byte, error) { return nil, fmt.Errorf("offline") }
	t.Cleanup(func() { radarGet = saved })
	if r := RainRadar(); gb(r, "ok", true) || !strings.Contains(gs(r, "error", ""), "offline") {
		t.Fatal(r)
	}
}
