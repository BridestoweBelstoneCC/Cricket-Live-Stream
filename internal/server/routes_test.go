package server

// The route table (routes.go) against the two routing switches: the same routes in both,
// every route that isn't open really refused, and the list of open routes written down so
// that opening one is deliberate. This is the code review's C1 and its "security boundaries
// as tests that enumerate routes".

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// switchRoutes reads the routes out of a routing function's source: every
// `path == "..."` and `strings.HasPrefix(path, "...")` in its case lines.
func switchRoutes(t *testing.T, file, fn string) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	src := strings.ReplaceAll(string(b), "\r\n", "\n")
	start := strings.Index(src, "func (c *ctx) "+fn+"(")
	if start < 0 {
		t.Fatalf("%s not found in %s", fn, file)
	}
	src = src[start:]
	src = src[:strings.Index(src, "\n}\n")]
	out := map[string]bool{}
	exact := regexp.MustCompile(`path == "([^"]+)"`)
	prefix := regexp.MustCompile(`strings\.HasPrefix\(path, "([^"]+)"\)`)
	for _, line := range strings.Split(src, "\n") {
		// The switch's own cases are one tab in; a nested switch's are deeper.
		if !strings.HasPrefix(line, "\tcase ") && !strings.HasPrefix(line, "\tif path == ") {
			continue
		}
		for _, m := range exact.FindAllStringSubmatch(line, -1) {
			out[m[1]] = true
		}
		for _, m := range prefix.FindAllStringSubmatch(line, -1) {
			out[m[1]+"*"] = true
		}
	}
	if len(out) < 20 {
		t.Fatalf("%s: only %d routes read from the source — has the switch changed shape?", fn, len(out))
	}
	return out
}

func tableRoutes(method string) map[string]bool {
	out := map[string]bool{}
	for _, r := range routes {
		if r.method == method {
			k := r.path
			if r.prefix {
				k += "*"
			}
			out[k] = true
		}
	}
	return out
}

func TestRouteTableMatchesTheSwitches(t *testing.T) {
	for _, c := range []struct{ method, file, fn string }{
		{"GET", "handler.go", "doGET"}, {"POST", "handler_post.go", "doPOST"},
	} {
		inSwitch, inTable := switchRoutes(t, c.file, c.fn), tableRoutes(c.method)
		if c.method == "POST" {
			inSwitch["/login"] = true // answered before the switch
		}
		for r := range inSwitch {
			if !inTable[r] {
				t.Errorf("%s %s is handled in %s but isn't in routes.go: it answers 404 until it's given an access level there", c.method, r, c.fn)
			}
		}
		for r := range inTable {
			if !inSwitch[r] {
				t.Errorf("%s %s is in routes.go but %s doesn't handle it", c.method, r, c.fn)
			}
		}
	}
	seen := map[string]bool{}
	for _, r := range routes {
		k := r.method + " " + r.path
		if seen[k] {
			t.Errorf("%s is in routes.go twice", k)
		}
		seen[k] = true
	}
}

// The routes anyone who can reach the port may call with no login. Adding to this list is
// a decision: what the route returns is public on the club's wifi and through the tunnel,
// and it must not change anything or cost anything. (The pages themselves are here: each
// asks for the login before it shows or does anything.)
var openRoutes = []string{
	"GET /", "GET /overlay", "GET /control", "GET /scoring", "GET /settings",
	"GET /favicon.ico", "GET /favicon.png", "GET /fonts/*", "GET /voice/*",
	// The score and the match, as the stream shows them.
	"GET /live/view", "GET /state", "GET /commentary/over", "GET /match/scorecard",
	"GET /scoring/balls", "GET /scoring/scorecard", "GET /scoring/state",
	"GET /lineups", "GET /h2h", "GET /league/table", "GET /season/top", "GET /club/career",
	"GET /player/stats", "GET /data/status", "GET /weather", "GET /weather/radar",
	"GET /weather/radar/frame/*", "GET /talk/onair",
	// Pictures the overlay and the pages draw.
	"GET /logo/*", "GET /logos/list", "GET /headshot/*", "GET /sponsor/*", "GET /library/file",
	"GET /social/photos",
	// Things made to be shared.
	"GET /match/page/latest", "GET /social/image/latest", "GET /social/clips/file/*",
	"GET /sponsor/airtime", "GET /highlights/status",
	// How it's doing, and how to reach it.
	"GET /health", "GET /stream/monitor", "GET /remote/info", "GET /remote/qr.png",
	"POST /login",
}

func TestOpenRoutesAreTheOnesWrittenDown(t *testing.T) {
	want := map[string]bool{}
	for _, r := range openRoutes {
		want[r] = true
	}
	var got []string
	for _, r := range routes {
		if r.access != accOpen {
			continue
		}
		k := r.method + " " + r.path
		if r.prefix {
			k += "*"
		}
		got = append(got, k)
		if !want[k] {
			t.Errorf("%s is open to anyone who can reach the port, and isn't on the list of open routes. If that's meant, add it to openRoutes; if not, give it a login in routes.go", k)
		}
		delete(want, k)
	}
	for k := range want {
		t.Errorf("%s is on the list of open routes but isn't open (or isn't a route) any more: take it off the list", k)
	}
	sort.Strings(got)
	t.Logf("%d open routes", len(got))
}

// Every route that isn't open refuses a device on the network that has no login, whatever
// its case in the switch does: the table is asked first.
func TestEveryClosedRouteRefusesTheNetwork(t *testing.T) {
	newTestServer(t, testPW)
	n := 0
	for _, r := range routes {
		if r.access == accOpen {
			continue
		}
		path := r.path
		if r.prefix {
			path += "x"
		}
		w := serveAs(r.method, path, "192.168.1.50:5555", "192.168.1.10:5000",
			map[string]string{"Content-Type": "application/json"}, "{}")
		want := map[access]int{accSession: 401, accLocal: 401, accLoopback: 404, accOverlay: 403}[r.access]
		if w.Code != want {
			t.Errorf("%s %s from the network with no login: %d, want %d", r.method, path, w.Code, want)
		}
		n++
	}
	if n < 60 {
		t.Fatalf("only %d closed routes checked", n)
	}
	// And through a proxy on this laptop (a tunnel, the clubhouse link): loopback, but not
	// this laptop's own browser.
	for _, r := range routes {
		if r.access != accLocal && r.access != accLoopback {
			continue
		}
		w := serveAs(r.method, r.path, "127.0.0.1:5555", "127.0.0.1:5000",
			map[string]string{"Content-Type": "application/json", "X-Forwarded-For": "203.0.113.9"}, "{}")
		if w.Code == 200 {
			t.Errorf("%s %s answered a proxied request with no login", r.method, r.path)
		}
	}
}

func TestAPathThatIsNotARouteIsNotFound(t *testing.T) {
	s := newTestServer(t, "")
	for _, p := range []string{"/nope", "/statex", "/live/viewx", "/control/", "/talk/out/wsx", "/settings/save"} {
		if st := s.get(p).status; st != 404 {
			t.Errorf("GET %s: %d, want 404", p, st)
		}
	}
	if st := s.post("/nope", nil).status; st != 404 {
		t.Errorf("POST /nope: %d, want 404", st)
	}
	// With a password, a POST to nowhere is asked for the login first.
	newTestServer(t, testPW)
	w := serveAs("POST", "/nope", "192.168.1.50:5555", "192.168.1.10:5000", nil, "{}")
	mustEq(t, w.Code, 401)
}

func TestRouteAccessPrefersTheExactPathThenTheLongestPrefix(t *testing.T) {
	for _, c := range []struct {
		method, path string
		want         access
		ok           bool
	}{
		{"GET", "/sponsor/airtime", accOpen, true},       // exact
		{"GET", "/sponsor/bg_preview", accSession, true}, // exact, under an open prefix
		{"GET", "/sponsor/acme", accOpen, true},          // the prefix
		{"GET", "/weather/radar", accOpen, true},         // exact
		{"GET", "/weather/radar/frame/3.png", accOpen, true},
		{"GET", "/live", accOverlay, true},
		{"GET", "/live/view", accOpen, true},
		{"GET", "/commands", accOverlay, true},
		{"GET", "/talk/out/ws", accLoopback, true},
		{"GET", "/status", accLocal, true},
		{"POST", "/replay", accLocal, true},
		{"POST", "/checklist/action", accSession, true}, // never the overlay's carve-out
		{"POST", "/state", accSession, true},
		{"POST", "/scoring/ball", accSession, true},
		{"POST", "/login", accOpen, true},
		{"GET", "/login", 0, false},
		{"GET", "/replay", 0, false},
		{"POST", "/live", 0, false},
	} {
		got, ok := routeAccess(c.method, c.path)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("%s %s: access %v (route: %v), want %v (%v)", c.method, c.path, got, ok, c.want, c.ok)
		}
	}
}
