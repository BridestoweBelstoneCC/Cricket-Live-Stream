package server

// Stream health check: recommends OBS stream settings instead of expecting a volunteer
// to know what any of that means, from a real upload-speed measurement.

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"time"

	"cricketstream/internal/obs"
	"cricketstream/internal/pyfmt"
)

// Re-test at most weekly unless asked to: this uses real upload data, worth respecting a
// club's ground connection / mobile data allowance.
const networkTestMaxAgeSec = 7 * 24 * 3600

// measureUploadMbps uploads 4 MB to Cloudflare's public speed-test endpoint and times
// it. Swappable for tests.
var measureUploadMbps = func() (float64, error) {
	payload := make([]byte, 4*1024*1024) // enough to smooth out a slow first packet
	rand.Read(payload)
	req, _ := http.NewRequest("POST", "https://speed.cloudflare.com/__up", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/octet-stream")
	// Cloudflare refuses a bare client's User-Agent — it needs to look like a browser.
	req.Header.Set("User-Agent", "Mozilla/5.0 (CricketStreamOverlay stream-health-check)")
	start := time.Now()
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return 0, err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("HTTP Error %d", resp.StatusCode)
	}
	elapsed := max(time.Since(start).Seconds(), 0.001)
	return float64(len(payload)) * 8 / elapsed / 1e6, nil
}

// uploadTestNeeded: would getUploadMbps run a real test rather than reuse the cache?
func uploadTestNeeded(state M, force bool) bool {
	at := gf(state, "network_test_at", 0)
	return force || at == 0 || now()-at >= networkTestMaxAgeSec
}

// getUploadMbps is the cached upload test — only re-measured if forced or stale.
func getUploadMbps(state M, force bool) (float64, bool, error) {
	if !uploadTestNeeded(state, force) {
		return gf(state, "network_test_mbps", 0), false, nil
	}
	mbps, err := measureUploadMbps()
	if err != nil {
		return 0, false, err
	}
	UpdateState(func(s M) {
		s["network_test_mbps"] = pyfmt.Round(mbps, 2)
		s["network_test_at"] = now()
	})
	return mbps, true, nil
}

// obsBitrateSanityCheck is /health's pre-flight check: is OBS's configured bitrate
// suspiciously low against the last network test's recommendation? It catches a leftover
// downshift from a previous match (the ladder's VBitrate persists in OBS's profile while
// its step counter resets) before anyone presses Start Streaming. Reads the stream
// monitor's cached poll — never its own OBS connection, since /health must stay cheap.
func obsBitrateSanityCheck(state M) M {
	streamMonMu.Lock()
	reachable, simple, configured := gb(streamMon, "reachable", false), streamMon["simple_mode"], gi(streamMon, "configured_kbps", 0)
	hasConfigured := streamMon["configured_kbps"] != nil
	streamMonMu.Unlock()
	if !reachable || !hasConfigured {
		return M{"available": false}
	}
	if simple == false {
		return M{"available": false, "note": "Advanced output mode — this check only applies to Simple output"}
	}
	mbps := gf(state, "network_test_mbps", 0)
	if mbps == 0 {
		return M{"available": true, "configured_kbps": configured, "recommended_kbps": nil, "suspicious": false,
			"note": "No network test on record yet — run a stream health check " +
				"(/obs/stream_check) to give this something to compare against."}
	}
	rec := obs.RecommendStreamSettings(mbps).BitrateKbps
	// 80%: below normal week-to-week jitter in the recommendation, but well above any of
	// the ladder's downshift fractions — so this only fires on a real leftover downshift.
	suspicious := float64(configured) < float64(rec)*0.8
	note := ""
	if suspicious {
		note = fmt.Sprintf("OBS is configured at %d kbps, well below the %d kbps your last network test "+
			"recommended — looks like a leftover downshift from a previous match. Check OBS Settings → "+
			"Output before going live.", configured, rec)
	}
	return M{"available": true, "configured_kbps": configured, "recommended_kbps": rec,
		"network_test_at": valOr(state, "network_test_at", 0), "suspicious": suspicious, "note": note}
}
