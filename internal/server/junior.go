package server

// Junior match mode (the code review's F2): one switch for a match with children in it.
//
// Names. Every player is shown as first name and surname initial ("Patrick E"), the form
// the result card already used for youth sides. The swap is made on the scorer's frame
// BEFORE anything is logged (see live in handler.go), so the scorebar, every graphic, the
// ball database, the scorecard, the match page, the AI report and the video's chapters
// can only ever hold the junior form: there's no later place to forget it.
//
// A first name comes from the squad roster, today's XIs on PlayCricket, or the scorer's
// feed if it has one. With none, it's initials ("J S", or "S" from a bare surname): never
// the surname. Two players who'd share a label get more of the surname ("Jack Sm",
// "Jack St"); a label once given is kept for the match (and across a restart, in
// junior_names.json), because the logs are keyed by it.
//
// Everything else the mode does is a list of things switched off while it's on, here and
// in the overlay's applyJuniorMode: player cards and headshots, club records, season
// leaders, AI and spoken commentary, the AI camera check (it sends pictures of the ground
// to Anthropic) and the automatic highlights upload. Live replays stay on.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"cricketstream/internal/pyval"
)

// juniorMode: the panel's "Junior match" switch.
func juniorMode(s M) bool { return pyval.Truthy(s["junior_mode"]) }

// LooksJunior: one of these team or competition names is an age-group side's (U13,
// Under 15, Colts, Juniors...). Today's fixture switches junior mode on by it.
func LooksJunior(names ...string) bool {
	for _, n := range names {
		if teamKey(n) == "youth" {
			return true
		}
	}
	return false
}

func juniorNamesFile() string { return filepath.Join(appDir, "junior_names.json") }

// juniorBook is one match's labels: who (an identity key) has which label.
type juniorBook struct {
	Match  string            `json:"match"`
	Labels map[string]string `json:"labels"` // identity key -> label
	loaded bool
}

var (
	juniorMu sync.Mutex
	jBook    juniorBook
)

// juniorBookFor returns the book for match, loading or starting it. Under juniorMu.
func juniorBookFor(match string) *juniorBook {
	if jBook.loaded && jBook.Match == match {
		return &jBook
	}
	jBook = juniorBook{Match: match, Labels: map[string]string{}, loaded: true}
	if b, err := os.ReadFile(juniorNamesFile()); err == nil {
		var saved juniorBook
		if json.Unmarshal(b, &saved) == nil && saved.Match == match && saved.Labels != nil {
			jBook.Labels = saved.Labels
		}
	}
	return &jBook
}

func (b *juniorBook) save() {
	if data, err := json.Marshal(b); err == nil {
		writeFileAtomic(juniorNamesFile(), data) // best-effort: only a restart needs it
	}
}

func (b *juniorBook) taken(label, by string) bool {
	for k, l := range b.Labels {
		if l == label && k != by {
			return true
		}
	}
	return false
}

// blankName: the feed's "nobody" (no batter in yet, no bowler named).
func blankName(s string) bool {
	s = trim(s)
	return s == "" || s == "—" || s == "-" || strings.EqualFold(s, "unsure")
}

// juniorFullName is the full name behind a feed name, if anyone knows it: the roster by
// shirt number, then a roster or line-up name it can only be (the whole name, else first
// initial + surname, else the surname when one player has it). "" when there's none.
func juniorFullName(feed, num string, known []string) string {
	hasFirst := func(n string) bool {
		t := strings.Fields(n)
		return len(t) >= 2 && len([]rune(strings.Trim(t[0], "."))) > 1
	}
	if full, byNum := resolvePlayer(feed, num); byNum && hasFirst(full) {
		return full
	}
	k := nameKeys(feed)
	if k.surname == "" {
		return ""
	}
	multi := len(strings.Fields(feed)) >= 2
	var same, initsur, sur []string
	for _, cand := range known {
		if !hasFirst(cand) {
			continue
		}
		ck := nameKeys(cand)
		switch {
		case ck.full == k.full:
			same = append(same, cand)
		case multi && ck.initsur == k.initsur:
			initsur = append(initsur, cand)
		case !multi && ck.surname == k.surname:
			sur = append(sur, cand)
		}
	}
	for _, tier := range [][]string{same, initsur, sur} {
		if len(tier) == 1 {
			return tier[0]
		}
		if len(tier) > 1 {
			return "" // could be either: nobody's first name is guessed
		}
	}
	if hasFirst(feed) {
		return feed
	}
	return ""
}

// juniorKnownNames are the full names on record today: the squad roster and the XIs.
func juniorKnownNames(s M) []string {
	var out []string
	for _, v := range gm(s, "roster") {
		if n := trim(pyval.Str(v)); n != "" {
			out = append(out, n)
		}
	}
	out = append(out, lineupNamesCached()...)
	sort.Strings(out)
	return out
}

// label is the junior form of one feed name, kept for the match. Under juniorMu.
func (b *juniorBook) label(feed, num string, known []string) string {
	if blankName(feed) {
		return feed
	}
	first, sur := "", ""
	key := nameKeys(feed).full
	if full := juniorFullName(feed, num, known); full != "" {
		t := strings.Fields(full)
		first, sur = pyTitle(strings.Trim(t[0], ".")), t[len(t)-1]
		key = "full:" + nameKeys(full).full
	} else {
		t := strings.Fields(feed)
		sur = t[len(t)-1]
		if len(t) >= 2 {
			first = strings.ToUpper(string([]rune(strings.Trim(t[0], "."))[:1]))
		}
		key = "feed:" + key
	}
	if l, ok := b.Labels[key]; ok {
		return l
	}
	rs := []rune(pyTitle(strings.ToLower(sur)))
	label := ""
	for k := 1; k <= len(rs); k++ {
		label = trim(first + " " + string(rs[:k]))
		// The last letters of a surname are never the way out of a clash: stop one short.
		if !b.taken(label, key) || k >= len(rs)-1 {
			break
		}
	}
	if label == "" {
		label = "Player"
	}
	for n := 2; b.taken(label, key); n++ { // two people with one name
		label = trim(first+" "+string(rs[:1])) + " (" + pyval.Str(n) + ")"
	}
	b.Labels[key] = label
	b.save()
	return label
}

// Words in a dismissal that aren't anyone's name.
var dismissalWords = map[string]bool{"c": true, "b": true, "st": true, "lbw": true, "run": true, "out": true,
	"not": true, "hit": true, "wicket": true, "wkt": true, "retired": true, "hurt": true, "ill": true,
	"absent": true, "handled": true, "the": true, "ball": true, "obstructing": true, "field": true,
	"timed": true, "sub": true, "and": true, "did": true, "bat": true, "caught": true, "bowled": true,
	"stumped": true, "ct": true, "ro": true, "dnb": true, "twice": true}

var wordRun = regexp.MustCompile(`[\pL][\pL'’.\-]*(?:\s+[\pL][\pL'’.\-]*)*`)

// juniorDismissal relabels the names in a dismissal ("c Smith b A Jones" -> "c S b A J"):
// every run of words that isn't cricket's own is a name.
func (b *juniorBook) dismissal(text string, known []string, bySurname map[string]string) string {
	return wordRun.ReplaceAllStringFunc(text, func(run string) string {
		words := strings.Fields(run)
		var out []string
		var name []string
		flush := func() {
			if len(name) > 0 {
				out = append(out, b.person(strings.Join(name, " "), known, bySurname))
				name = nil
			}
		}
		for _, w := range words {
			if dismissalWords[strings.ToLower(strings.Trim(w, "."))] {
				flush()
				out = append(out, w)
			} else {
				name = append(name, w)
			}
		}
		flush()
		return strings.Join(out, " ")
	})
}

// person labels a name met in passing (who took the catch, who was out): the player
// already on this frame with that name or surname if there's exactly one, else a label
// of its own.
func (b *juniorBook) person(name string, known []string, bySurname map[string]string) string {
	if blankName(name) {
		return name
	}
	if len(strings.Fields(name)) == 1 {
		if l, ok := bySurname[nameKeys(name).surname]; ok && l != "" {
			return l
		}
	}
	return b.label(name, "", known)
}

var trailingScore = regexp.MustCompile(`^(.*?)(\s+\d+\*?)$`)

// juniorLive is the scorer's frame with every player's name in the junior form. A copy:
// the feed reader's cached map isn't touched.
func juniorLive(state M, s M) M {
	juniorMu.Lock()
	defer juniorMu.Unlock()
	book := juniorBookFor(CurrentMatchID())
	known := juniorKnownNames(s)
	out := shallow(state)
	out["junior"] = true // the overlay: these names are already the junior form
	// A first name the scorer's own frame gives ("Oliver Marsh" bowling) is on record too:
	// "b O Marsh" and "b Marsh" in a dismissal are then the same boy.
	for _, key := range []string{"batter1", "batter2", "bowler"} {
		if n := trim(gs(gm(out, key), "name", "")); len(strings.Fields(n)) >= 2 {
			known = append(known, n)
		}
	}
	for _, list := range []string{"batters", "bowlers"} {
		for _, r := range gl(gm(out, "card"), list) {
			if row, ok := r.(M); ok {
				if n := trim(gs(row, "name", "")); len(strings.Fields(n)) >= 2 {
					known = append(known, n)
				}
			}
		}
	}
	known = uniqueStrings(known)

	// The players named outright on this frame, and who each surname can only be.
	bySurname := map[string]string{}
	seen := map[string]string{}
	note := func(real, label string) {
		sn := nameKeys(real).surname
		if sn == "" {
			return
		}
		if prev, ok := seen[sn]; ok && prev != label {
			bySurname[sn] = "" // two of them: a bare surname says nobody
			return
		}
		seen[sn], bySurname[sn] = label, label
	}
	player := func(key string) {
		p, ok := out[key].(M)
		if !ok {
			return
		}
		p = shallow(p)
		real := gs(p, "name", "")
		if !blankName(real) {
			p["name"] = book.label(real, pyval.Str(orNil(p["number"])), known)
			note(real, gs(p, "name", ""))
		}
		out[key] = p
	}
	player("batter1")
	player("batter2")
	player("bowler")

	card := shallow(gm(out, "card"))
	rows := func(key string) []M {
		var list []M
		for _, r := range gl(card, key) {
			row, ok := r.(M)
			if !ok {
				continue
			}
			row = shallow(row)
			if real := gs(row, "name", ""); !blankName(real) {
				row["name"] = book.label(real, "", known)
				note(real, gs(row, "name", ""))
			}
			list = append(list, row)
		}
		return list
	}
	batters, bowlers := rows("batters"), rows("bowlers")

	// Then the text that mentions them.
	for _, key := range []string{"batter1", "batter2"} {
		if p, ok := out[key].(M); ok && gs(p, "howOut", "") != "" {
			p["howOut"] = book.dismissal(gs(p, "howOut", ""), known, bySurname)
		}
	}
	for _, row := range batters {
		if o := gs(row, "out", ""); o != "" {
			row["out"] = book.dismissal(o, known, bySurname)
		}
	}
	if card != nil && (batters != nil || bowlers != nil) {
		bl, wl := make([]any, len(batters)), make([]any, len(bowlers))
		for i, r := range batters {
			bl[i] = r
		}
		for i, r := range bowlers {
			wl[i] = r
		}
		card["batters"], card["bowlers"] = bl, wl
		out["card"] = card
	}
	if v := gs(out, "lastWicketHowOut", ""); v != "" {
		out["lastWicketHowOut"] = book.dismissal(v, known, bySurname)
	}
	for _, key := range []string{"lastWicketBatter", "lastWicketBowler", "lastWicketFielder"} {
		v := gs(out, key, "")
		if blankName(v) {
			continue
		}
		name, tail := v, ""
		if m := trailingScore.FindStringSubmatch(v); m != nil { // "MARTY 12"
			name, tail = m[1], m[2]
		}
		out[key] = book.person(name, known, bySurname) + tail
	}
	return out
}

// juniorNames relabels the "name" of every player in a list from PlayCricket (today's
// XIs, a head-to-head's best performers): full names, so first name + initial.
func juniorNames(list []any) []any {
	out := make([]any, 0, len(list))
	for _, x := range list {
		p, ok := x.(M)
		if !ok {
			out = append(out, x)
			continue
		}
		p = shallow(p)
		if n := gs(p, "name", ""); !blankName(n) {
			p["name"] = juniorPlain(n)
		}
		out = append(out, p)
	}
	return out
}

// juniorPlain is the junior form of a name with no match behind it: "Jack Smith" ->
// "Jack S", "J Smith" -> "J S", "Smith" -> "S".
func juniorPlain(name string) string {
	t := strings.Fields(name)
	switch {
	case len(t) == 0:
		return name
	case len(t) == 1:
		return strings.ToUpper(string([]rune(t[0])[:1]))
	}
	first := strings.Trim(t[0], ".")
	if len([]rune(first)) > 1 {
		return shortName(name, true)
	}
	return strings.ToUpper(first + " " + string([]rune(t[len(t)-1])[:1]))
}

var h2hPerformer = regexp.MustCompile(`^(.*\S)(\s+\d+(?:-\d+)?\*?)$`)

// juniorH2H relabels a head-to-head's best performers ("Jack Smith 45*" -> "Jack S 45*"),
// wherever they are in it.
func juniorH2H(v any) any {
	switch x := v.(type) {
	case M:
		out := make(M, len(x))
		for k, val := range x {
			if s, ok := val.(string); ok && (k == "top_bat" || k == "best_bowl") {
				if m := h2hPerformer.FindStringSubmatch(s); m != nil {
					out[k] = juniorPlain(m[1]) + m[2]
				} else {
					out[k] = juniorPlain(s)
				}
				continue
			}
			out[k] = juniorH2H(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = juniorH2H(val)
		}
		return out
	case []M:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = juniorH2H(val)
		}
		return out
	}
	return v
}

// uniqueStrings is list without repeats (one person named twice isn't two candidates).
func uniqueStrings(list []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range list {
		if k := nameKeys(s).full; !seen[k] {
			seen[k] = true
			out = append(out, s)
		}
	}
	return out
}
