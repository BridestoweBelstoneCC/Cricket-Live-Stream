package server

// Stress test for adaptive stream quality: whole match days on a virtual clock, against a
// modelled OBS and upload line, over many random seeds. A bad quality ladder doesn't fail
// a unit test â€” it restarts a club's stream twenty times an afternoon, sits at a third of
// the quality on a line that could carry more, or leaves the stream down after a refused
// request. Each scenario checks the rules that matter on air, every run:
//   - a step up is one rung, never sooner than StreamUpshiftAfter after the last shift,
//     and its restart never covers a delivery
//   - the ladder's idea of the bitrate is OBS's configured bitrate, always
//   - the stream is never down for long without the operator having been told
//   - restarts are bounded (no flapping), and the ladder ends where the line says it should
// The model is deliberately simple; what it gets right is the shape real OBS showed: a
// configured bitrate the line can't carry means congestion and dropped frames, or with
// Dynamic Bitrate a lower rate actually sent and some congestion as it hunts.

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"cricketstream/internal/obsws"
)

type simGap struct {
	start, end float64
	up         bool // the restart was a step up
}

type simSet struct {
	t        float64
	from, to int
	mode     string // "auto" or "manual"
}

type simOBS struct {
	rng        *rand.Rand
	t0, t      float64
	line       func(el float64) float64 // capacity in kbps, el seconds into the day
	dynamic    bool
	undershoot float64 // the encoder sends this share of its bitrate (CBR ~1, a quiet picture less)
	chaos      float64 // chance any one request is refused

	configured int // SimpleOutput VBitrate
	active     bool
	rb         bool    // the replay buffer: it shares the stream's encoder, as on a real OBS
	outH       int     // output height
	stopAt     float64 // the output is still winding down until then
	encB       int     // bitrate the running output started with
	rate       float64 // Dynamic Bitrate's current rate
	bytes      float64
	frames     float64
	skipped    float64
	congestion float64

	sets     []simSet
	mode     string // of the shift in progress
	said     []string
	gaps     []simGap
	gapStart float64 // the stream went down at (0 = up)
	gapUp    bool    // an auto step up is under way
	sentSum  float64 // kbpsÂ·s actually sent, for the average
	liveSecs float64
	alerted  float64 // the operator was told the stream is down, at (0 = not told)
	operator int     // times the operator pressed Start
	silent   float64 // longest the stream was down with nobody told
}

func (o *simOBS) el() float64 { return o.t - o.t0 }

func (o *simOBS) advance(dt float64) {
	for dt > 1e-9 {
		step := math.Min(1, dt)
		dt -= step
		o.t += step
		if !o.active {
			if o.gapStart != 0 {
				if o.alerted == 0 {
					o.silent = math.Max(o.silent, o.t-o.gapStart)
				} else if o.t-o.alerted >= 60 {
					o.start() // the operator presses Start Streaming, as the message said
					o.operator++
				}
			}
			continue
		}
		capKbps := o.line(o.el()) * (1 + 0.1*(2*o.rng.Float64()-1))
		over := 0.0
		var sent float64
		if o.dynamic {
			want := o.rate * o.undershoot
			if want > capKbps {
				over = (want - capKbps) / want
				o.rate = math.Max(capKbps*0.85/o.undershoot, 0.2*float64(o.encB))
			} else {
				o.rate = math.Min(float64(o.encB), o.rate+0.03*float64(o.encB))
			}
			sent = math.Min(want, capKbps)
			o.skipped += 30 * step * over * 0.3
		} else {
			want := float64(o.encB) * o.undershoot
			sent = math.Min(want, capKbps)
			if want > capKbps {
				over = (want - capKbps) / want
			}
			o.skipped += 30 * step * over
		}
		sample := 0.0
		if over > 0 {
			sample = math.Min(1, 0.15+3*over)
		}
		o.congestion = 0.7*o.congestion + 0.3*sample
		o.frames += 30 * step
		o.bytes += sent * 1000 / 8 * step
		o.sentSum += sent * step
		o.liveSecs += step
	}
}

func (o *simOBS) start() bool {
	if o.active || o.t < o.stopAt {
		return false
	}
	if !o.rb { // OBS builds the encoder, reading the bitrate, only when nothing is using it
		o.encB = o.configured
	}
	o.active, o.rate = true, float64(o.encB)
	o.bytes, o.frames, o.skipped, o.congestion = 0, 0, 0, 0
	if o.gapStart != 0 {
		o.gaps = append(o.gaps, simGap{o.gapStart, o.t, o.gapUp})
	}
	o.gapStart, o.gapUp, o.alerted = 0, false, 0
	return true
}

func (o *simOBS) call(reqs []obsws.Req) []map[string]any {
	if o.rng.Float64() < o.chaos/3 {
		return nil // OBS not answering at all
	}
	out := make([]map[string]any, len(reqs))
	for i, r := range reqs {
		if o.rng.Float64() < o.chaos {
			continue // refused
		}
		d, _ := r.Data.(M)
		switch r.Type {
		case "GetStreamStatus":
			out[i] = map[string]any{"outputActive": o.active || o.t < o.stopAt, "outputReconnecting": false,
				"outputCongestion": o.congestion, "outputBytes": int(o.bytes),
				"outputTotalFrames": int(o.frames), "outputSkippedFrames": int(o.skipped)}
		case "GetProfileParameter":
			v := ""
			switch gs(d, "parameterName", "") {
			case "VBitrate":
				v = strconv.Itoa(o.configured)
			case "DynamicBitrate":
				v = strconv.FormatBool(o.dynamic)
			case "Mode":
				v = "Simple"
			}
			out[i] = map[string]any{"parameterValue": v}
		case "StopStream":
			if !o.active {
				continue // OBS: "output not active"
			}
			// A congested line has a backlog to flush first: stopping can take a while.
			wind := 1 + 2*o.rng.Float64()
			if o.congestion > 0.3 {
				wind += 25 * o.rng.Float64()
			}
			o.active, o.stopAt, o.gapStart = false, o.t+wind, o.t
			out[i] = map[string]any{}
		case "GetReplayBufferStatus":
			out[i] = map[string]any{"outputActive": o.rb}
		case "GetVideoSettings":
			out[i] = map[string]any{"outputWidth": o.outH * 16 / 9, "outputHeight": o.outH}
		case "SetVideoSettings":
			if o.active || o.rb || o.t < o.stopAt {
				continue // OBS refuses while any output runs
			}
			o.outH = gi(d, "outputHeight", o.outH)
			out[i] = map[string]any{}
		case "GetRecordStatus":
			out[i] = map[string]any{"outputActive": false}
		case "StopReplayBuffer":
			if !o.rb {
				continue
			}
			o.rb = false
			out[i] = map[string]any{}
		case "StartReplayBuffer":
			if o.rb {
				continue
			}
			if !o.active && o.t >= o.stopAt {
				o.encB = o.configured
			}
			o.rb = true
			out[i] = map[string]any{}
		case "StartStream":
			if o.start() {
				out[i] = map[string]any{}
			}
		case "SetProfileParameter":
			n, _ := strconv.Atoi(gs(d, "parameterValue", ""))
			o.sets = append(o.sets, simSet{o.t, o.configured, n, o.mode})
			if n > o.configured && o.gapStart != 0 && o.mode == "auto" {
				o.gapUp = true
			}
			o.configured = n
			out[i] = map[string]any{}
		default:
			out[i] = map[string]any{}
		}
	}
	return out
}

// â”€â”€ The match â”€â”€

type simBall struct {
	t                    float64
	innings, legal, runs int
	wkts                 int
}

// simMatch is two 35-over innings starting at start: deliveries 25-50s apart, wides,
// over gaps of 45-100s, drinks after the 17th over and a 25-minute innings break.
func simMatch(rng *rand.Rand, start float64) []simBall {
	var balls []simBall
	t := start
	for inn := 1; inn <= 2; inn++ {
		legal, runs, wkts := 0, 0, 0
		for over := 0; over < 35; over++ {
			for b := 0; b < 6; {
				t += 25 + 25*rng.Float64()
				r := []int{0, 0, 0, 1, 1, 1, 2, 4, 6}[rng.Intn(9)]
				if rng.Float64() < 0.06 { // a wide: runs, no ball
					runs++
				} else {
					b++
					legal++
					runs += r
					if rng.Float64() < 0.03 {
						wkts++
					}
				}
				balls = append(balls, simBall{t, inn, legal, runs, wkts})
			}
			t += 45 + 55*rng.Float64()
			if over == 16 {
				t += 360
			}
		}
		t += 1500
	}
	return balls
}

// frame is what the overlay's /live would carry at t (the scorer's file lags a moment).
func simFrame(balls []simBall, t float64) M {
	if len(balls) == 0 || t < balls[0].t-600 || t > balls[len(balls)-1].t+900 {
		return nil
	}
	i := sort.Search(len(balls), func(i int) bool { return balls[i].t > t-2 }) - 1
	if i < 0 {
		return M{"overs": "0", "score": 0, "wickets": 0}
	}
	b := balls[i]
	return M{"overs": fmt.Sprintf("%d.%d", b.legal/6, b.legal%6), "score": b.runs, "wickets": b.wkts}
}

// â”€â”€ A run â”€â”€

type simScenario struct {
	name       string
	line       func(el float64) float64
	dynamic    bool
	undershoot float64
	chaos      float64
	manual     bool      // an operator pressing the quality buttons now and then
	restarts   []float64 // when the server crashes and the launcher starts a new one (el)
	hours      float64
}

type simResult struct {
	o                 *simOBS
	ups, downs, fails int // fails: a step down within StreamUpshiftFailSec of a step up
	finalStep         int
	quality           float64 // time-average of the configured bitrate's share of baseline
	recoveredAt       float64 // el when the ladder last reached step 0 (0 = never left it)
	wrongRes          int     // monitor ticks live at the wrong resolution for the rung
}

const simBaseline = 4000

func runSim(t *testing.T, sc simScenario, seed int64) simResult {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	sandbox(t, "")
	setState(t, M{"stream_auto_downshift": true})
	resetStreamMon(t, nil)
	setPlay(t, -1, 0, 0)
	o := &simOBS{rng: rng, t0: 1.7e9, t: 1.7e9, line: sc.line, dynamic: sc.dynamic,
		undershoot: sc.undershoot, chaos: sc.chaos, configured: simBaseline, encB: simBaseline, outH: 720}
	playMu.Lock()
	playFrameAt, playChangeAt = 0, 0
	playMu.Unlock()
	clock := func() float64 { return o.t }
	clockOverride.Store(&clock)
	savedCall, savedSleep, savedEnded := obsCall, shiftSleep, streamEnded
	t.Cleanup(func() {
		clockOverride.Store(nil)
		obsCall, shiftSleep, streamEnded = savedCall, savedSleep, savedEnded
	})
	obsCall = func(_ M, reqs []obsws.Req, _ time.Duration) []map[string]any { return o.call(reqs) }
	shiftSleep = func(d time.Duration) { o.advance(d.Seconds()) }
	streamEnded = func() {}

	balls := simMatch(rng, o.t0+20*60)
	o.start()
	o.rb = true // match day: OBS opens with the replay buffer running
	end := o.t0 + sc.hours*3600
	res := simResult{o: o}
	nextTick, nextManual := o.t+StreamPollSec, o.t+3600*rng.Float64()
	qualSum := 0.0
	restarts := sc.restarts
	for o.t < end {
		o.advance(2.5)
		if len(restarts) > 0 && o.el() >= restarts[0] {
			// A new server process: nothing in memory, the state file and OBS as they were.
			restarts = restarts[1:]
			streamMonMu.Lock()
			streamMon, streamSamples = newStreamMon(), nil
			streamMonMu.Unlock()
			playMu.Lock()
			playSig, playBalls, playFrameAt, playChangeAt = "", -1, 0, 0
			playMu.Unlock()
		}
		qualSum += 2.5 * float64(o.configured) / simBaseline
		if f := simFrame(balls, o.t); f != nil {
			notePlay(f)
		}
		if o.t >= nextTick {
			nextTick += StreamPollSec
			if step, reason := streamMonitorTick(); step >= 0 {
				simApply(o, step, reason, "auto")
			}
			streamMonMu.Lock()
			cur, step := gi(streamMon, "current_kbps", 0), gi(streamMon, "step", 0)
			streamMonMu.Unlock()
			if cur != 0 && cur != o.configured {
				t.Fatalf("seed %d at %.0fs: the ladder thinks %d kbps, OBS is set to %d", seed, o.el(), cur, o.configured)
			}
			// The bottom rung at 480p, every other at the club's 720p â€” while live (a
			// refused resolution change is reported, and the next step puts it right).
			if o.active && cur != 0 && (step == len(StreamLadder)-1) != (o.outH == 480) {
				res.wrongRes++
			}
			// What viewers get is the encoder's bitrate, not the setting.
			if cur != 0 && o.active && o.encB != cur {
				t.Fatalf("seed %d at %.0fs: the ladder says %d kbps but the encoder is running at %d; last shifts:%s", seed, o.el(), cur, o.encB,
					strings.Join(append([]string{""}, o.said[max(0, len(o.said)-3):]...), "|"))
			}
			if step == 0 && res.recoveredAt == 0 && len(o.sets) > 0 {
				res.recoveredAt = o.el()
			} else if step != 0 {
				res.recoveredAt = 0
			}
		}
		if sc.manual && o.t >= nextManual {
			nextManual = o.t + 1800 + 3600*rng.Float64()
			streamMonMu.Lock()
			step := gi(streamMon, "step", 0)
			streamMonMu.Unlock()
			switch rng.Intn(3) {
			case 0:
				simApply(o, min(step+1, len(StreamLadder)-1), "operator request", "manual")
			case 1:
				simApply(o, max(step-1, 0), "operator request", "manual")
			default:
				simApply(o, 0, "operator request", "manual")
			}
		}
	}
	res.quality = qualSum / (sc.hours * 3600)
	streamMonMu.Lock()
	res.finalStep = gi(streamMon, "step", 0)
	streamMonMu.Unlock()

	// â”€â”€ The rules, checked on the whole day â”€â”€
	idx := func(kbps int) int {
		for i := range StreamLadder {
			if streamLadderKbps(simBaseline, i) == kbps {
				return i
			}
		}
		return -1
	}
	var lastUp float64
	for i, s := range o.sets {
		if idx(s.to) < 0 {
			t.Fatalf("seed %d: bitrate %d isn't on the ladder", seed, s.to)
		}
		if s.to > s.from && s.mode == "auto" {
			res.ups++
			lastUp = s.t
			if idx(s.from)-idx(s.to) != 1 {
				t.Fatalf("seed %d at %.0fs: stepped up %d â†’ %d, more than one rung", seed, s.t-o.t0, s.from, s.to)
			}
			if i > 0 && s.t-o.sets[i-1].t < StreamUpshiftAfter {
				t.Fatalf("seed %d at %.0fs: stepped up %.0fs after the last change", seed, s.t-o.t0, s.t-o.sets[i-1].t)
			}
		} else if s.to < s.from {
			res.downs++
			if lastUp != 0 && s.t-lastUp < StreamUpshiftFailSec {
				res.fails++
			}
		}
	}
	for _, g := range o.gaps {
		if !g.up {
			continue
		}
		for _, b := range balls {
			if b.t >= g.start-1 && b.t <= g.end+1 {
				t.Fatalf("seed %d: a step up's restart (%.0f-%.0fs) covered a delivery at %.0fs",
					seed, g.start-o.t0, g.end-o.t0, b.t-o.t0)
			}
		}
	}
	if res.wrongRes > 0 && sc.chaos == 0 {
		t.Fatalf("seed %d: %d checks live at the wrong resolution for the rung", seed, res.wrongRes)
	}
	if o.silent > 75 { // the longest a step itself can take: a minute's slow stop, the restart
		t.Fatalf("seed %d: the stream was down %.0fs and nobody was told; last shifts:\n%s", seed, o.silent,
			strings.Join(o.said[max(0, len(o.said)-3):], "\n"))
	}
	return res
}

// simApply is the monitor loop's ApplyStreamQualityStep, plus the operator reading what
// it said: a message saying the stream is down is the operator's cue to press Start.
func simApply(o *simOBS, step int, reason, mode string) {
	o.mode = mode
	ok, msg := ApplyStreamQualityStep(step, reason, mode)
	o.said = append(o.said, fmt.Sprintf("%.0fs %v %s (active %v)", o.el(), ok, msg, o.active))
	if !ok && o.gapStart != 0 && !o.active && (strings.Contains(msg, "Start Streaming") ||
		strings.Contains(msg, "did not STAY up") || strings.Contains(msg, "check OBS")) {
		o.alerted = o.t
	}
}

// â”€â”€ Lines â”€â”€

func constLine(kbps float64) func(float64) float64 { return func(float64) float64 { return kbps } }

func patchLine(good, bad, from, to float64) func(float64) float64 {
	return func(el float64) float64 {
		if el >= from && el < to {
			return bad
		}
		return good
	}
}

// walkLine is a mean-reverting random walk between lo and hi, fixed per seed.
func walkLine(seed int64, lo, hi float64) func(float64) float64 {
	rng := rand.New(rand.NewSource(seed ^ 0x5eed))
	pts := []float64{}
	v := (lo + hi) / 2
	for i := 0; i < 24*60; i++ { // a point a minute for a day
		v += (lo+hi)/2*0.02 - v*0.02 + (hi-lo)*0.08*(2*rng.Float64()-1)
		v = math.Max(lo, math.Min(hi, v))
		pts = append(pts, v)
	}
	return func(el float64) float64 { return pts[min(int(el/60), len(pts)-1)] }
}

func simSeeds() int {
	if testing.Short() {
		return 4
	}
	return 24
}

func TestStressGoodLineNeverTouchesQuality(t *testing.T) {
	for seed := int64(1); seed <= int64(simSeeds()); seed++ {
		for _, dyn := range []bool{false, true} {
			r := runSim(t, simScenario{line: constLine(8000), dynamic: dyn, undershoot: 1, hours: 7}, seed)
			if len(r.o.sets) != 0 {
				t.Fatalf("seed %d dyn %v: changed quality on a good line: %v", seed, dyn, r.o.sets)
			}
		}
	}
}

func TestStressBadPatchRecovers(t *testing.T) {
	// A bad 15 minutes early in the first innings: down, then all the way back.
	worst := 0.0
	for seed := int64(1); seed <= int64(simSeeds()); seed++ {
		for _, dyn := range []bool{false, true} {
			r := runSim(t, simScenario{line: patchLine(8000, 1500, 40*60, 55*60), dynamic: dyn, undershoot: 1, hours: 7}, seed)
			if r.downs == 0 && dyn {
				continue // Dynamic Bitrate rode it out (as a real OBS did at 2400 kbps): fine
			}
			if r.downs == 0 {
				t.Fatalf("seed %d dyn %v: never stepped down for a 1500 kbps line", seed, dyn)
			}
			if r.finalStep != 0 || r.recoveredAt == 0 {
				t.Fatalf("seed %d dyn %v: never got back to full quality (step %d)", seed, dyn, r.finalStep)
			}
			back := r.recoveredAt - 55*60
			worst = math.Max(worst, back)
			if back > 50*60 {
				t.Fatalf("seed %d dyn %v: took %.0f minutes after the line recovered", seed, dyn, back/60)
			}
			if n := r.ups + r.downs; n > 8 {
				t.Fatalf("seed %d dyn %v: %d restarts for one bad patch", seed, dyn, n)
			}
		}
	}
	t.Logf("slowest return to full quality after the line recovered: %.0f minutes", worst/60)
}

func TestStressServerRestartsMidMatch(t *testing.T) {
	// The launcher restarts a crashed or frozen server in seconds, mid-match. A new one
	// used to take the stepped-down bitrate for the full one and never climb back.
	for seed := int64(1); seed <= int64(simSeeds()); seed++ {
		for _, dyn := range []bool{false, true} {
			r := runSim(t, simScenario{line: patchLine(8000, 1500, 40*60, 55*60), dynamic: dyn, undershoot: 1, hours: 7,
				restarts: []float64{50 * 60, 70 * 60, 3 * 3600}}, seed)
			if r.o.configured != simBaseline || r.finalStep != 0 {
				t.Fatalf("seed %d dyn %v: ended at %d kbps (step %d), not the full %d", seed, dyn, r.o.configured, r.finalStep, simBaseline)
			}
		}
	}
}

func TestStressWeakLineSettlesWithoutFlapping(t *testing.T) {
	// 2400 kbps all day on a 4000 kbps baseline: 2000 (50%) is the rung that fits. A few
	// failed tries at 2800 are the price of finding out; they must thin out, not repeat.
	for _, dyn := range []bool{false, true} {
		maxRestarts, sumQ := 0, 0.0
		for seed := int64(1); seed <= int64(simSeeds()); seed++ {
			r := runSim(t, simScenario{line: constLine(2400), dynamic: dyn, undershoot: 1, hours: 7}, seed)
			n := r.ups + r.downs
			maxRestarts = max(maxRestarts, n)
			sumQ += r.quality
			if n > 14 {
				t.Fatalf("seed %d dyn %v: %d restarts in a day on a steady weak line:%s", seed, dyn, n,
					strings.Join(append([]string{""}, r.o.said...), "\n  "))
			}
			// Without Dynamic Bitrate, full quality on this line is 40% dropped frames (a real
			// OBS at 4000 on 2400). With it, OBS holds the stream to the line itself with no
			// drops, so the ladder rightly leaves it alone.
			if r.finalStep == 0 && !dyn {
				t.Fatalf("seed %d: ended at full quality on a line that can't carry it", seed)
			}
		}
		t.Logf("weak line, dynamic %v: at most %d restarts, average quality %.0f%%", dyn, maxRestarts, 100*sumQ/float64(simSeeds()))
	}
}

func TestStressNoisyLine(t *testing.T) {
	for seed := int64(1); seed <= int64(simSeeds()); seed++ {
		for _, dyn := range []bool{false, true} {
			r := runSim(t, simScenario{line: walkLine(seed, 1200, 6500), dynamic: dyn, undershoot: 1, hours: 7}, seed)
			if n := r.ups + r.downs; float64(n)/7 > 5 {
				t.Fatalf("seed %d dyn %v: %d restarts in 7 hours", seed, dyn, n)
			}
		}
	}
}

func TestStressQuietPictureStillClimbs(t *testing.T) {
	// A hardware encoder on a static picture can send well under its bitrate. It must
	// still climb back after a bad patch, or the step up never happens in real life.
	for seed := int64(1); seed <= int64(simSeeds()); seed++ {
		r := runSim(t, simScenario{line: patchLine(8000, 1200, 40*60, 55*60), undershoot: 0.7, hours: 7}, seed)
		if r.finalStep != 0 {
			t.Fatalf("seed %d: stuck at step %d on a good line with a quiet picture", seed, r.finalStep)
		}
	}
}

func TestStressOBSRefusingRequests(t *testing.T) {
	// One request in ten refused, one call in thirty unanswered: the ladder must stay
	// truthful and the stream never silently down (both checked inside runSim).
	for seed := int64(1); seed <= int64(simSeeds()); seed++ {
		for _, line := range []func(float64) float64{patchLine(8000, 1500, 40*60, 55*60), constLine(2400)} {
			runSim(t, simScenario{line: line, undershoot: 1, chaos: 0.1, hours: 7}, seed)
		}
	}
}

func TestStressOperatorPressingButtons(t *testing.T) {
	for seed := int64(1); seed <= int64(simSeeds()); seed++ {
		runSim(t, simScenario{line: walkLine(seed, 1500, 6000), dynamic: seed%2 == 0, undershoot: 1, chaos: 0.03, manual: true, hours: 7}, seed)
	}
}
