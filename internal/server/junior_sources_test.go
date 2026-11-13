package server

// Junior match mode and the score's other sources: the overlay never draws a surname even
// for a name the server didn't relabel, and the raw PlayCricket scorecard isn't passed on.

import (
	"strings"
	"testing"
)

func TestOverlayShortensAnyNameInJuniorMode(t *testing.T) {
	var got map[string]string
	runJS(t, overlayFunctions(t, "fmtSurname", "fmtInitial", "juniorShort")+`
var cfg = { junior_mode: true }, _juniorLabelled = false, out = {};
out.full    = fmtSurname('Patrick Edwards');
out.initial = fmtSurname('T Hatton');
out.dotted  = fmtSurname('T. Hatton');
out.bare    = fmtSurname('EDWARDS');
out.caps    = fmtSurname('PATRICK EDWARDS');
out.three   = fmtSurname('Jack van der Berg');
out.nobody  = fmtSurname('—');
out.init2   = fmtInitial('Patrick Edwards');
_juniorLabelled = true;                       // the server's own labels are left whole
out.label   = fmtSurname('Patrick E');
out.clash   = fmtSurname('Jack St');
out.label2  = fmtInitial('Patrick E');
cfg = { junior_mode: false };                 // and nothing changes with it off
out.offSur  = fmtSurname('Patrick Edwards');
out.offInit = fmtInitial('Patrick Edwards');
process.stdout.write(JSON.stringify(out));`, &got)
	eqv(t, got, map[string]string{
		"full": "Patrick E", "initial": "T H", "dotted": "T H", "bare": "E", "caps": "Patrick E",
		"three": "Jack B", "nobody": "—", "init2": "Patrick E",
		"label": "Patrick E", "clash": "Jack St", "label2": "Patrick E",
		"offSur": "Edwards", "offInit": "P. Edwards",
	})
	apply := extractFunction(t, page(t, "overlay.html"), "applyLiveFrame")
	if !strings.Contains(apply, "_juniorLabelled = !!(liveData.state && liveData.state.junior);") {
		t.Error("the overlay doesn't note whether a frame's names are already the junior form")
	}
}

func TestJuniorFramesAreMarked(t *testing.T) {
	f := juniorFixture(t, nil)
	f.write(0)
	if s := liveState(t, f, "/live/view"); s["junior"] != true {
		t.Error("a junior frame isn't marked as one")
	}
	setState(t, M{"junior_mode": false})
	if s := liveState(t, f, "/live/view"); s["junior"] != nil {
		t.Error("an ordinary frame is marked junior")
	}
}

// The raw PlayCricket scorecard (the old api_token + match_id source) names every player
// in full and goes to the overlay untouched: not in a junior match.
func TestJuniorModeDoesNotPassOnTheRawScorecard(t *testing.T) {
	s := newTestServer(t, "")
	setState(t, M{"junior_mode": true, "api_token": "tok", "match_id": "123", "site_id": "1"})
	st, d := s.getJSON("/live/view")
	mustEq(t, st, 200)
	mustEq(t, d["source"], any("api"))
	if d["data"] != nil || !strings.Contains(gs(d, "error", ""), "Junior match mode") {
		t.Errorf("/live/view: %v", d)
	}
}
