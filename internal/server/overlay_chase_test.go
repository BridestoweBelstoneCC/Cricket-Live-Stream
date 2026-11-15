package server

// The chase equation on the over summary (the code review's F7; the bowler's spell was
// already there).

import (
	"strings"
	"testing"
)

func TestChaseEquationAtTheOver(t *testing.T) {
	var got map[string]string
	runJS(t, overlayFunctions(t, "chaseEquation")+`
var out = {
  mid:    chaseEquation({innings: 2, runsRequired: 48, ballsRemaining: 36}),
  text:   chaseEquation({innings: "2", runsRequired: "31", ballsRemaining: "27"}),
  last:   chaseEquation({innings: 2, runsRequired: 3, ballsRemaining: 1}),
  first:  chaseEquation({innings: 1, runsRequired: 0, ballsRemaining: 120}),
  won:    chaseEquation({innings: 2, runsRequired: 0, ballsRemaining: 14}),
  noBalls: chaseEquation({innings: 2, runsRequired: 20, ballsRemaining: 0}),
  empty:  chaseEquation({}),
};
process.stdout.write(JSON.stringify(out));`, &got)
	eqv(t, got, map[string]string{
		"mid":   "Need 48 from 36 balls · 8.00 an over",
		"text":  "Need 31 from 27 balls · 6.89 an over",
		"last":  "Need 3 from 1 ball · 18.00 an over",
		"first": "", "won": "", "noBalls": "", "empty": "",
	})
	extras := extractFunction(t, page(t, "overlay.html"), "overExtras")
	if !strings.Contains(extras, "if (chase) msgs.push(esc(chase));") {
		t.Error("the over summary doesn't show the chase equation")
	}
}
