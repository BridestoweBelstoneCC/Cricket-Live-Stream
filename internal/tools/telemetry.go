package tools

// Passive match-day telemetry logger — writes a CSV you can analyse afterwards.
//
// Samples OBS, the CricketStream server and the scorer's feed on a timer and appends one
// row per sample. Everything here is READ-ONLY and cheap: no active bandwidth tests (those
// would compete with the live stream and cause the very stalls we are trying to measure),
// and the match feed is read from /live/view, never /live — /live is a mutating GET that
// only the OBS overlay may call.
//
//	cricketstream telemetry                       # sample every 10s until Ctrl+C
//	cricketstream telemetry --interval 5
//	cricketstream telemetry --summary diagnostics/telemetry_20260725_1355.csv
//
// Rows land in diagnostics/telemetry_<date>_<time>.csv. Open in Excel, or use --summary
// for a quick read-out of the interesting numbers.

import (
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"cricketstream/internal/assets"
	"cricketstream/internal/obsws"
	"cricketstream/internal/pyfmt"
	"cricketstream/internal/pyval"
)

// Fields are the CSV columns, in order.
var Fields = []string{
	"iso_time", "elapsed_s",
	// --- network / encoder ---
	"inst_mbps", "congestion", "delta_skipped", "skipped_total", "frames_total",
	"stream_active", "reconnecting",
	// --- OBS process ---
	"obs_cpu_pct", "obs_fps", "obs_mem_mb", "render_skipped", "render_total",
	// --- adaptive quality ---
	"ladder_step", "ladder_current_kbps", "ladder_baseline_kbps", "ladder_verdict",
	// --- scorer feed ---
	"pcs_fresh", "pcs_age_s",
	// --- match context, so drops can be correlated with what was happening ---
	"batting_team", "score", "wickets", "overs", "last_ball",
	// --- server process ---
	"srv_cpu_pct", "srv_rss_mb", "srv_errors",
	// --- headroom probe (blank on most rows; only populated when a probe ran) ---
	"probe_mbps", "probe_skipped_why",
}

// ── Headroom probe ──
// Passive monitoring cannot measure headroom you are not using: congestion sits at zero
// whenever the stream is comfortably inside the line's capacity, which tells you nothing
// about where the ceiling actually is. A small upload occasionally is the only way to find
// out. Deliberately tiny — 512 KB is under a second of traffic against a 2.5 Mbps stream,
// roughly 0.3% overhead at the default interval — and it refuses to run whenever the
// stream is already struggling, so it can never be the cause of a problem it is meant to
// detect.
const (
	ProbeBytes   = 512 * 1024
	probeTimeout = 20 * time.Second
)

// ProbeURL is where the probe uploads to. Tests point it at a fake.
var ProbeURL = "https://speed.cloudflare.com/__up"

// probeUpload measures achievable upload in Mbps; why is set instead when it failed.
func probeUpload() (mbps float64, why string) {
	req, _ := http.NewRequest("POST", ProbeURL, strings.NewReader(string(make([]byte, ProbeBytes))))
	req.Header.Set("Content-Type", "application/octet-stream")
	// Cloudflare 403s a library's default User-Agent.
	req.Header.Set("User-Agent", "Mozilla/5.0 (CricketStreamOverlay telemetry-probe)")
	start := time.Now()
	resp, err := (&http.Client{Timeout: probeTimeout}).Do(req)
	if err == nil {
		_, err = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if err == nil && resp.StatusCode >= 400 {
			err = fmt.Errorf("HTTP Error %d", resp.StatusCode)
		}
	}
	if err != nil {
		return 0, errName(err)
	}
	elapsed := math.Max(time.Since(start).Seconds(), 0.001)
	return pyfmt.Round(ProbeBytes*8/elapsed/1_000_000, 2), ""
}

// row is one sample: column → value, kept typed until it's written so the probe guard
// can test it the way the original did.
type row map[string]any

// prevSample carries the last counters so rates can be differenced.
type prevSample struct {
	t       time.Time
	bytes   float64
	skipped float64
}

// num is a raw JSON number from OBS as Python held it: integral values are ints.
func num(v any) any {
	if f, ok := v.(float64); ok && f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return int64(f)
	}
	return v
}

// roundOr0 is round(d.get(k, 0) or 0, n): a falsy value stays the int 0.
func roundOr0(d map[string]any, k string, n int) any {
	f, ok := d[k].(float64)
	if !ok || f == 0 {
		return 0
	}
	return pyfmt.Round(f, n)
}

func getf(d map[string]any, k string) float64 {
	f, _ := pyval.Float(d[k])
	return f
}

// sample takes one row. obs may be nil: OBS is often not up yet when this starts on
// match day, and losing the whole log to that would defeat the point. The server-side and
// match-context columns are collected regardless.
func sample(obs *obsws.Conn, prev *prevSample) (row, *prevSample, error) {
	now := time.Now()
	r := row{}
	for _, k := range Fields {
		r[k] = ""
	}
	r["iso_time"] = now.Format("2006-01-02 15:04:05")

	st, gs := map[string]any{}, map[string]any{}
	if obs != nil {
		resp, err := obs.Request("GetStreamStatus", nil)
		if err != nil {
			return nil, prev, err
		}
		if resp.Data != nil {
			st = resp.Data
		}
		resp, err = obs.Request("GetStats", nil)
		if err != nil {
			return nil, prev, err
		}
		if resp.Data != nil {
			gs = resp.Data
		}
	}

	b, sk := getf(st, "outputBytes"), getf(st, "outputSkippedFrames")
	if prev != nil {
		dt := math.Max(now.Sub(prev.t).Seconds(), 0.001)
		// A stream restart zeroes OBS's counters, so a raw difference goes hugely negative
		// and poisons every average taken over the file. Blank the sample instead — the
		// restart itself is still visible as a drop in frames_total.
		if b >= prev.bytes && sk >= prev.skipped {
			r["inst_mbps"] = pyfmt.Round((b-prev.bytes)*8/dt/1_000_000, 3)
			r["delta_skipped"] = int64(sk - prev.skipped)
		}
	}
	r["congestion"] = roundOr0(st, "outputCongestion", 4)
	r["skipped_total"] = orZero(st, "outputSkippedFrames")
	r["frames_total"] = orZero(st, "outputTotalFrames")
	r["stream_active"] = st["outputActive"]
	r["reconnecting"] = st["outputReconnecting"]

	r["obs_cpu_pct"] = roundOr0(gs, "cpuUsage", 2)
	r["obs_fps"] = roundOr0(gs, "activeFps", 1)
	r["obs_mem_mb"] = roundOr0(gs, "memoryUsage", 1)
	r["render_skipped"] = orZero(gs, "renderSkippedFrames")
	r["render_total"] = orZero(gs, "renderTotalFrames")

	if mon, _ := getJSON("/stream/monitor", "", 6*time.Second); len(mon) > 0 {
		r["ladder_step"] = mon["step"]
		r["ladder_current_kbps"] = mon["current_kbps"]
		r["ladder_baseline_kbps"] = mon["baseline_kbps"]
		r["ladder_verdict"] = mon["verdict"]
	}

	if h, _ := getJSON("/health", "", 6*time.Second); len(h) > 0 {
		pcs, _ := h["pcs"].(map[string]any)
		r["pcs_fresh"] = pcs["fresh"]
		r["pcs_age_s"] = pcs["age_sec"]
		srv, _ := h["server"].(map[string]any)
		r["srv_cpu_pct"] = srv["cpu_pct"]
		r["srv_rss_mb"] = srv["max_rss_mb"]
		errs, _ := h["errors"].([]any)
		r["srv_errors"] = len(errs)
	}

	if live, _ := getJSON("/live/view", "", 6*time.Second); len(live) > 0 { // /live/view — never /live
		s, _ := live["state"].(map[string]any)
		r["batting_team"] = s["battingTeamName"]
		if r["batting_team"] == nil {
			r["batting_team"] = ""
		}
		r["score"] = s["score"]
		r["wickets"] = s["wickets"]
		r["overs"] = s["overs"]
		r["last_ball"] = strings.TrimSpace(pyval.StrOr(s["last_ball"], ""))
	}

	return r, &prevSample{t: now, bytes: b, skipped: sk}, nil
}

// orZero is d.get(k, 0): a present null stays None.
func orZero(d map[string]any, k string) any {
	v, ok := d[k]
	if !ok {
		return 0
	}
	return num(v)
}

// cell is how Python's csv module writes a value: str(), with None as blank.
func cell(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case float64:
		return pyfmt.Float(x)
	}
	return pyval.Str(v)
}

// show is str() for the console lines, where None prints as None.
func show(v any) string {
	if f, ok := v.(float64); ok {
		return pyfmt.Float(f)
	}
	return pyval.Str(v)
}

func (r row) record() []string {
	out := make([]string, len(Fields))
	for i, k := range Fields {
		out[i] = cell(r[k])
	}
	return out
}

// TelemetryOptions are the command-line settings.
type TelemetryOptions struct {
	Interval   float64 // seconds between samples (default 10)
	Summary    string  // summarise this CSV and exit
	ProbeEvery float64 // seconds between headroom probes; 0 disables
	// Stop ends the logger (tests); nil means Ctrl+C.
	Stop <-chan struct{}
}

func dialOBS() (*obsws.Conn, error) { return obsws.Dial(obsTarget(), 8*time.Second) }

// Telemetry is `cricketstream telemetry`.
func Telemetry(o TelemetryOptions) int {
	if o.Summary != "" {
		if err := Summarise(os.Stdout, o.Summary); err != nil {
			return fail("%v", err)
		}
		return 0
	}
	if o.Interval <= 0 {
		o.Interval = 10
	}
	diag := assets.Join("diagnostics")
	os.MkdirAll(diag, 0o755)
	path := filepath.Join(diag, "telemetry_"+time.Now().Format("20060102_1504")+".csv")

	stop := o.Stop
	if stop == nil {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt)
		defer signal.Stop(sig)
		ch := make(chan struct{})
		go func() { <-sig; close(ch) }()
		stop = ch
	}

	// Never die because OBS isn't up yet. The launcher starts this automatically with its
	// output discarded, so a crash here would be silent and cost the whole match's log.
	obs, err := dialOBS()
	if err != nil {
		fmt.Printf("  OBS not reachable (%s) — logging server + match data, retrying OBS each sample\n", errName(err))
		obs = nil
	}
	fmt.Printf("  logging to %s\n", path)
	fmt.Printf("  sampling every %ss — Ctrl+C to stop\n\n", pyfmt.Fixed(o.Interval, 0))
	if o.ProbeEvery > 0 {
		fmt.Printf("  headroom probe: %d KB every %ss, skipped while congested\n", ProbeBytes/1024, pyfmt.Fixed(o.ProbeEvery, 0))
	}

	f, err := os.Create(path)
	if err != nil {
		return fail("  cannot write %s: %v", path, err)
	}
	w := csv.NewWriter(f)
	w.UseCRLF = true // what Python's csv module writes
	w.Write(Fields)
	w.Flush()

	start := time.Now()
	var prev *prevSample
	n := 0
	lastProbe := time.Now() // don't probe immediately; let the stream settle first
loop:
	for {
		if obs == nil { // OBS wasn't up at launch — keep trying quietly
			if c, err := dialOBS(); err == nil {
				obs = c
				fmt.Printf("  %s  OBS connected\n", time.Now().Format("15:04:05"))
			}
		}
		r, p, err := sample(obs, prev)
		if err != nil {
			fmt.Printf("  sample failed: %s: %v — reconnecting\n", errName(err), err)
			if obs != nil {
				obs.Close()
			}
			obs, _ = dialOBS()
		} else {
			prev = p
			r["elapsed_s"] = int64(math.RoundToEven(time.Since(start).Seconds()))
			if o.ProbeEvery > 0 && time.Since(lastProbe).Seconds() >= o.ProbeEvery {
				lastProbe = time.Now()
				probeGuarded(r)
			}
			w.Write(r.record())
			w.Flush() // flush every row — a crash must not lose the log
			n++
			if n%6 == 0 {
				fmt.Printf("  %s  %d samples  %s Mbps  cong %s  drops %s  %s-%s (%s ov)\n",
					r["iso_time"], n, show(r["inst_mbps"]), show(r["congestion"]), show(r["delta_skipped"]),
					show(r["score"]), show(r["wickets"]), show(r["overs"]))
			}
		}
		select {
		case <-stop:
			break loop
		case <-time.After(time.Duration(o.Interval * float64(time.Second))):
		}
	}
	f.Close()
	if obs != nil {
		obs.Close()
	}
	fmt.Printf("\n  stopped — %d samples written to %s\n", n, path)
	Summarise(os.Stdout, path)
	return 0
}

// probeGuarded runs the headroom probe, guarded so it can never worsen a struggling
// stream: only while streaming, only with congestion and drops at zero.
func probeGuarded(r row) {
	cong, drops := r["congestion"], r["delta_skipped"]
	if !pyval.Truthy(cong) {
		cong = 0
	}
	if !pyval.Truthy(drops) {
		drops = 0
	}
	cf, _ := pyval.Float(cong)
	df, _ := pyval.Float(drops)
	switch {
	case !pyval.Truthy(r["stream_active"]):
		r["probe_skipped_why"] = "not streaming"
	case cf > 0:
		r["probe_skipped_why"] = "congested (" + show(cong) + ")"
	case df > 0:
		r["probe_skipped_why"] = "dropping frames (" + show(drops) + ")"
	default:
		mbps, why := probeUpload()
		if why == "" {
			r["probe_mbps"] = mbps
			fmt.Printf("  %s  probe: %s Mbps\n", r["iso_time"], pyfmt.Float(mbps))
		} else {
			r["probe_skipped_why"] = why
			fmt.Printf("  %s  probe: failed - %s Mbps\n", r["iso_time"], why)
		}
	}
}
