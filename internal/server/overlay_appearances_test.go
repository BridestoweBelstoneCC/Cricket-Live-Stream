package server

import "testing"

// The player card's club appearances, through the real _showCard: it asks with the batting
// team, shows the figure and since when, leaves the stat off when there's none, and a late
// answer about the last batter doesn't land on the next one's card.
func TestOverlayPlayerCardAppearances(t *testing.T) {
	src := overlayFunctions(t, "_showCard") + `
var els = {};
var document = { getElementById: function (id) {
  return els[id] || (els[id] = { id: id, style: {}, textContent: '' });
} };
var asked = [], pending = [];
function fetch(u) {
  asked.push(u);
  if (u.indexOf('/player/stats') === 0) return Promise.resolve({ json: function () { return {}; } });
  return new Promise(function (res) {
    pending.push(function (d) { res({ json: function () { return d; } }); });
  });
}
var _lastBattingTeam = 'Home CC 1st XI';
var settle = function () { return new Promise(function (r) { setImmediate(r); }); };
var out = {};
(async function () {
  els['pc-club'] = { style: { display: 'contents' } };   // left on from the batter before
  _showCard('', 'JONES', '7');
  out.hiddenAtFirst = els['pc-club'].style.display;
  out.url = asked.filter(function (u) { return u.indexOf('/club/career') === 0; })[0];
  pending.shift()({ ready: true, since: 2014, apps: 100 });
  await settle();
  out.shown = [els['pc-club'].style.display, els['pc-apps'].textContent, els['pc-apps-key'].textContent];

  _showCard('-right', 'RIVAL', '');
  pending.shift()({ ready: true, since: 2014 });
  await settle();
  out.none = els['pc-club-right'].style.display;

  _showCard('', 'SMITH', '');
  var late = pending.shift();
  _showCard('', 'NEWMAN', '');
  late({ ready: true, apps: 55 });
  await settle();
  out.stale = els['pc-club'].style.display;

  var before = asked.length;
  _lastBattingTeam = '';
  _showCard('', 'NOTEAM', '');
  out.noTeamAsks = asked.slice(before).filter(function (u) { return u.indexOf('/club/career') === 0; }).length;
  process.stdout.write(JSON.stringify(out));
})();`
	var got struct {
		HiddenAtFirst string `json:"hiddenAtFirst"`
		URL           string `json:"url"`
		Shown         []any  `json:"shown"`
		None          string `json:"none"`
		Stale         string `json:"stale"`
		NoTeamAsks    int    `json:"noTeamAsks"`
	}
	runJS(t, src, &got)
	mustEq(t, got.HiddenAtFirst, "none")
	mustEq(t, got.URL, "/club/career?name=JONES&num=7&team=Home%20CC%201st%20XI")
	eqv(t, got.Shown, []any{"contents", float64(100), "Club appearances since 2014"})
	mustEq(t, got.None, "none", "no figure, no stat")
	mustEq(t, got.Stale, "none", "a late answer about the last batter")
	mustEq(t, got.NoTeamAsks, 0)
}
