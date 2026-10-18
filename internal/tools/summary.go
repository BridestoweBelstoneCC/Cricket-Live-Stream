package tools

import (
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"cricketstream/internal/pyfmt"
)

// Summarise prints the read-out of a telemetry CSV: restarts, bitrate spread, congestion,
// drops (and when the worst came), ladder steps, scorer-feed staleness and the headroom
// probes. Output matches the original's line for line.
func Summarise(w io.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("[Errno 2] No such file or directory: %s", pyRepr(path))
	}
	defer f.Close()
	recs, err := csv.NewReader(stripBOM(f)).ReadAll()
	if err != nil && len(recs) == 0 {
		return err
	}
	var rows []map[string]string
	if len(recs) > 1 {
		head := recs[0]
		for _, rec := range recs[1:] {
			m := map[string]string{}
			for i, k := range head {
				if i < len(rec) {
					m[k] = rec[i]
				}
			}
			rows = append(rows, m)
		}
	}
	if len(rows) == 0 {
		fmt.Fprintln(w, "no rows")
		return nil
	}
	p := func(format string, a ...any) { fmt.Fprintf(w, format+"\n", a...) }
	f2 := func(x float64) string { return pyfmt.Fixed(x, 2) }

	nums := func(key string) []float64 {
		var out []float64
		for _, r := range rows {
			if x, ok := pyFloat(r[key]); ok {
				out = append(out, x)
			}
		}
		return out
	}

	p("\n=== %s ===", filepath.Base(path))
	p("samples      : %d", len(rows))
	p("window       : %s  ->  %s", rows[0]["iso_time"], rows[len(rows)-1]["iso_time"])

	// Stream restarts show up as frames_total going backwards.
	var restarts []string
	var prevFrames *float64
	for _, r := range rows {
		fr, ok := pyFloat(r["frames_total"])
		if !ok {
			continue
		}
		if prevFrames != nil && fr < *prevFrames {
			restarts = append(restarts, r["iso_time"])
		}
		prevFrames = &fr
	}
	line := fmt.Sprintf("stream restarts: %d", len(restarts))
	if len(restarts) > 0 {
		line += "  (" + strings.Join(restarts, ", ") + ")"
	}
	p("%s", line)

	var mbps []float64
	for _, m := range nums("inst_mbps") {
		if m >= 0 && m < 100 {
			mbps = append(mbps, m)
		}
	}
	if len(mbps) > 0 {
		ordered := append([]float64(nil), mbps...)
		sort.Float64s(ordered)
		p("\nbitrate (Mbps, %d valid samples)", len(mbps))
		p("  mean %s | median %s | min %s | max %s | stdev %s",
			f2(mean(mbps)), f2(median(mbps)), f2(ordered[0]), f2(ordered[len(ordered)-1]), pyfmt.Fixed(pstdev(mbps), 3))
		p("  p05 %s | p95 %s", f2(ordered[int(.05*float64(len(ordered)))]), f2(ordered[int(.95*float64(len(ordered)))]))
	}

	if cong := nums("congestion"); len(cong) > 0 {
		bad := 0
		for _, c := range cong {
			if c > 0 {
				bad++
			}
		}
		p("\ncongestion")
		p("  mean %s | max %s | samples above zero: %d/%d", pyfmt.Fixed(mean(cong), 3), pyfmt.Fixed(maxf(cong), 3), bad, len(cong))
	}

	if ds := nums("delta_skipped"); len(ds) > 0 {
		drops := 0
		total := 0.0
		for _, d := range ds {
			total += d
			if d > 0 {
				drops++
			}
		}
		p("\ndropped frames")
		p("  total %d | sample windows with drops: %d/%d", int(total), drops, len(ds))
		if drops > 0 {
			worst, best := 0, math.Inf(-1)
			for i, r := range rows {
				v := r["delta_skipped"]
				if v == "" {
					v = "0"
				}
				x, _ := pyFloat(v)
				if x > best {
					worst, best = i, x
				}
			}
			r := rows[worst]
			p("  worst window at %s: %s frames (congestion %s, score %s-%s %s ov)",
				r["iso_time"], r["delta_skipped"], r["congestion"], r["score"], r["wickets"], r["overs"])
		}
	}

	var steps []string
	for _, r := range rows {
		if s, ok := r["ladder_step"]; ok && s != "" {
			steps = append(steps, s)
		}
	}
	if len(steps) > 0 {
		p("\nquality ladder")
		seen := map[string]bool{}
		var uniq []string
		for _, s := range steps {
			if !seen[s] {
				seen[s] = true
				uniq = append(uniq, s)
			}
		}
		sort.Strings(uniq)
		reprs := make([]string, len(uniq))
		for i, s := range uniq {
			reprs[i] = pyRepr(s)
		}
		p("  step values seen: [%s]", strings.Join(reprs, ", "))
		changed := false
		for i := 1; i < len(steps); i++ {
			// Paired with rows[i] as the original does, which is the step's own row
			// whenever no sample is missing a step.
			if steps[i] != steps[i-1] {
				p("  %s: step %s -> %s", rows[i]["iso_time"], steps[i-1], steps[i])
				changed = true
			}
		}
		if !changed {
			p("  no downshifts during this window")
		}
	}

	stale := 0
	for _, r := range rows {
		if strings.ToLower(r["pcs_fresh"]) == "false" {
			stale++
		}
	}
	p("\nscorer feed")
	p("  stale samples: %d/%d", stale, len(rows))

	// Headroom probes: what the line could actually carry, versus what was being sent.
	type probe struct {
		t string
		v float64
	}
	var probes []probe
	var skipped []string
	for _, r := range rows {
		if s := strings.TrimSpace(r["probe_mbps"]); s != "" {
			v, _ := pyFloat(r["probe_mbps"])
			probes = append(probes, probe{r["iso_time"], v})
		}
		if strings.TrimSpace(r["probe_skipped_why"]) != "" {
			skipped = append(skipped, r["probe_skipped_why"])
		}
	}
	if len(probes) > 0 || len(skipped) > 0 {
		p("\nheadroom probes")
	}
	if len(probes) > 0 {
		vals := make([]float64, len(probes))
		for i, pr := range probes {
			vals[i] = pr.v
		}
		lo := minf(vals)
		p("  %d probe(s): mean %s | min %s | max %s Mbps", len(probes), f2(mean(vals)), f2(lo), f2(maxf(vals)))
		first, last := probes[0], probes[len(probes)-1]
		p("  first %s = %s  ->  last %s = %s", first.t, f2(first.v), last.t, f2(last.v))
		if len(mbps) > 0 {
			sent := median(mbps)
			p("  median sent %s Mbps vs min measured %s Mbps -> headroom ~%s Mbps", f2(sent), f2(lo), f2(math.Max(lo-sent, 0)))
		}
		worst := probes[0]
		for _, pr := range probes[1:] {
			if pr.v < worst.v {
				worst = pr
			}
		}
		p("  worst probe: %s Mbps at %s", f2(worst.v), worst.t)
	}
	if len(skipped) > 0 {
		// Counter.most_common: by count, ties in first-seen order.
		counts := map[string]int{}
		var order []string
		for _, s := range skipped {
			if counts[s] == 0 {
				order = append(order, s)
			}
			counts[s]++
		}
		sort.SliceStable(order, func(i, j int) bool { return counts[order[i]] > counts[order[j]] })
		for _, why := range order {
			p("  skipped x%d: %s", counts[why], why)
		}
	}
	p("")
	return nil
}

// pyFloat is float(s) on a CSV cell.
func pyFloat(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "", "true", "false", "none":
		return 0, false
	}
	x, err := strconv.ParseFloat(strings.ReplaceAll(s, "_", ""), 64)
	return x, err == nil
}

func stripBOM(r io.Reader) io.Reader {
	b, _ := io.ReadAll(r)
	return strings.NewReader(strings.TrimPrefix(string(b), string([]byte{0xEF, 0xBB, 0xBF})))
}

func mean(xs []float64) float64 {
	// statistics.mean is exact (fractions); summing in order is within the 2-3 decimal
	// places printed.
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

func median(xs []float64) float64 {
	o := append([]float64(nil), xs...)
	sort.Float64s(o)
	n := len(o)
	if n%2 == 1 {
		return o[n/2]
	}
	return (o[n/2-1] + o[n/2]) / 2
}

func pstdev(xs []float64) float64 {
	m := mean(xs)
	s := 0.0
	for _, x := range xs {
		s += (x - m) * (x - m)
	}
	return math.Sqrt(s / float64(len(xs)))
}

func maxf(xs []float64) float64 {
	m := xs[0]
	for _, x := range xs[1:] {
		if x > m {
			m = x
		}
	}
	return m
}

func minf(xs []float64) float64 {
	m := xs[0]
	for _, x := range xs[1:] {
		if x < m {
			m = x
		}
	}
	return m
}
