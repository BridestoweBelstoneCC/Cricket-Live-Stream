package server

// Who may call each route, in one table (the code review's C1). Routing was two long
// switches with the login check written into each case, which is easy to leave out: that's
// how /match/fixtures was open until the review found it.
//
// The table is checked before the switches and denies by default: a path that isn't here
// is a 404 whatever the switch says, and a route gets the access written here even if its
// case forgets to ask. The cases' own checks are still there (and some routes ask for more
// inside: /h2h?force=1 and /player/stats/refresh need a login); the stricter of the two
// applies, so the table can never open what a case keeps shut. routes_test.go reads both
// switches and fails if they and the table disagree about which routes exist, and holds
// the list of open routes, so opening one is a change someone has to make on purpose.

import "strings"

type access int

const (
	accOpen     access = iota // anyone who can reach the port
	accSession                // logged in (everyone, when no club password is set)
	accLocal                  // this laptop's own browser with no proxy between, or logged in
	accLoopback               // this laptop only, never through a tunnel or the clubhouse link
	accOverlay                // the OBS overlay (overlayCaller): reading these changes state
)

type route struct {
	method string
	path   string
	prefix bool // path is a prefix (/logo/<club>), not the whole path
	access access
}

var routes = []route{
	// Open: the overlay, the pages themselves, and what they show to anyone watching.
	{"GET", "/", false, accOpen},
	{"GET", "/club/career", false, accOpen},
	{"GET", "/commentary/over", false, accOpen},
	{"GET", "/control", false, accOpen},
	{"GET", "/data/status", false, accOpen},
	{"GET", "/favicon.ico", false, accOpen},
	{"GET", "/favicon.png", false, accOpen},
	{"GET", "/fonts/", true, accOpen},
	{"GET", "/h2h", false, accOpen},
	{"GET", "/headshot/", true, accOpen},
	{"GET", "/health", false, accOpen},
	{"GET", "/highlights/status", false, accOpen},
	{"GET", "/league/table", false, accOpen},
	{"GET", "/library/file", false, accOpen},
	{"GET", "/lineups", false, accOpen},
	{"GET", "/live/view", false, accOpen},
	{"GET", "/logo/", true, accOpen},
	{"GET", "/logos/list", false, accOpen},
	{"GET", "/match/page/latest", false, accOpen},
	{"GET", "/match/scorecard", false, accOpen},
	{"GET", "/overlay", false, accOpen},
	{"GET", "/player/stats", false, accOpen},
	{"GET", "/remote/info", false, accOpen},
	{"GET", "/remote/qr.png", false, accOpen},
	{"GET", "/scoring", false, accOpen},
	{"GET", "/scoring/balls", false, accOpen},
	{"GET", "/scoring/scorecard", false, accOpen},
	{"GET", "/scoring/state", false, accOpen},
	{"GET", "/season/top", false, accOpen},
	{"GET", "/settings", false, accOpen},
	{"GET", "/social/clips/file/", true, accOpen},
	{"GET", "/social/image/latest", false, accOpen},
	{"GET", "/social/photos", false, accOpen},
	{"GET", "/sponsor/", true, accOpen},
	{"GET", "/sponsor/airtime", false, accOpen},
	{"GET", "/state", false, accOpen},
	{"GET", "/stream/monitor", false, accOpen},
	{"GET", "/talk/onair", false, accOpen},
	{"GET", "/voice/", true, accOpen},
	{"GET", "/weather", false, accOpen},
	{"GET", "/weather/radar", false, accOpen},
	{"GET", "/weather/radar/frame/", true, accOpen},
	// The overlay alone: these change state when read.
	{"GET", "/commands", false, accOverlay},
	{"GET", "/live", false, accOverlay},
	// This laptop only: OBS's commentary source.
	{"GET", "/talk/out", false, accLoopback},
	{"GET", "/talk/out/ws", false, accLoopback},
	// This laptop's own browser, or a login.
	{"GET", "/logos/debug", false, accLocal},
	{"GET", "/pcs/debug", false, accLocal},
	{"GET", "/status", false, accLocal},
	// A login.
	{"GET", "/agent/discover", false, accSession},
	{"GET", "/agent/status", false, accSession},
	{"GET", "/auth/log", false, accSession},
	{"GET", "/break", false, accSession},
	{"GET", "/camera/cut_test", false, accSession},
	{"GET", "/camera/side-on", false, accSession},
	{"GET", "/camera/spotter", false, accSession},
	{"GET", "/cameras/discover", false, accSession},
	{"GET", "/checklist", false, accSession},
	{"GET", "/data/export", true, accSession},
	{"GET", "/library", false, accSession},
	{"GET", "/match/fetch", false, accSession},
	{"GET", "/match/fixtures", false, accSession},
	{"GET", "/match/page", false, accSession},
	{"GET", "/obs/stream_check", false, accSession},
	{"GET", "/player/stats/refresh", false, accSession},
	{"GET", "/precheck/last", false, accSession},
	{"GET", "/preview.jpg", false, accSession},
	{"GET", "/rehearsal", false, accSession},
	{"GET", "/report/generate", false, accSession},
	{"GET", "/report/log", false, accSession},
	{"GET", "/save/test", false, accSession},
	{"GET", "/settings/info", false, accSession},
	{"GET", "/social/clips", false, accSession},
	{"GET", "/social/image/generate", true, accSession},
	{"GET", "/social/recent", false, accSession},
	{"GET", "/sponsor/bg_preview", false, accSession},
	{"GET", "/talk/qr.png", false, accSession},
	{"GET", "/talk/status", false, accSession},
	{"GET", "/youtube/chapters", false, accSession},
	{"GET", "/youtube/highlights", false, accSession},
	{"GET", "/youtube/live", false, accSession},
	{"GET", "/youtube/thumbnail", false, accSession},

	// POST. Everything needs a login except signing in itself, and what the overlay calls:
	// a loopback OBS browser source with no login of its own. The checklist never belongs
	// with those: it starts OBS and goes live.
	// Open: the overlay, the pages themselves, and what they show to anyone watching.
	{"POST", "/login", false, accOpen},
	// This laptop's own browser, or a login.
	{"POST", "/break/end", false, accLocal},
	{"POST", "/break/start", false, accLocal},
	{"POST", "/camera/scene", false, accLocal},
	{"POST", "/commentary/over/generate", false, accLocal},
	{"POST", "/replay", false, accLocal},
	{"POST", "/sponsor/airtime", false, accLocal},
	{"POST", "/weather/hide", false, accLocal},
	{"POST", "/weather/show", false, accLocal},
	// A login.
	{"POST", "/agent/pair", false, accSession},
	{"POST", "/auth/logout_all", false, accSession},
	{"POST", "/camera/cut_test", false, accSession},
	{"POST", "/camera/spotter", false, accSession},
	{"POST", "/cameras/use", false, accSession},
	{"POST", "/cards/show", false, accSession},
	{"POST", "/checklist/action", false, accSession},
	{"POST", "/commentary/test", false, accSession},
	{"POST", "/data/reconcile", false, accSession},
	{"POST", "/h2h/show", false, accSession},
	{"POST", "/highlights", false, accSession},
	{"POST", "/library/delete", false, accSession},
	{"POST", "/library/rename", false, accSession},
	{"POST", "/library/upload", false, accSession},
	{"POST", "/match/page", false, accSession},
	{"POST", "/obs/add_camera", false, accSession},
	{"POST", "/precheck", false, accSession},
	{"POST", "/rehearsal", false, accSession},
	{"POST", "/replay/test", false, accSession},
	{"POST", "/report/generate", false, accSession},
	{"POST", "/scorecard/show", false, accSession},
	{"POST", "/scoring/", true, accSession},
	{"POST", "/scoring/setup", false, accSession},
	{"POST", "/settings/check", false, accSession},
	{"POST", "/settings/open_folder", false, accSession},
	{"POST", "/settings/save", false, accSession},
	{"POST", "/social/clips", false, accSession},
	{"POST", "/sponsor/remove_background", false, accSession},
	{"POST", "/sponsor/upload", false, accSession},
	{"POST", "/state", false, accSession},
	{"POST", "/stream/dynamic", false, accSession},
	{"POST", "/stream/quality", false, accSession},
	{"POST", "/talk/control", false, accSession},
	{"POST", "/youtube/chapters", false, accSession},
	{"POST", "/youtube/highlights", false, accSession},
	{"POST", "/youtube/thumbnail", false, accSession},
	{"POST", "/youtube/update", false, accSession},
}

// routeAccess is who may call method path: an exact path first, else the longest prefix
// (the switches' own order: exact matches before prefixes). False for a path that isn't a
// route.
func routeAccess(method, path string) (access, bool) {
	best, found := -1, false
	var acc access
	for _, r := range routes {
		if r.method != method {
			continue
		}
		if !r.prefix {
			if r.path == path {
				return r.access, true
			}
			continue
		}
		if strings.HasPrefix(path, r.path) && len(r.path) > best {
			best, acc, found = len(r.path), r.access, true
		}
	}
	return acc, found
}

// allowed is true if this request has the access a route needs; otherwise it has already
// answered (401, 403 or 404).
func (c *ctx) allowed(acc access) bool {
	switch acc {
	case accOpen:
		return true
	case accSession:
		return c.checkToken()
	case accLocal:
		return c.trustedLoopback() || c.checkToken()
	case accLoopback:
		if !c.trustedLoopback() {
			c.notFound()
			return false
		}
		return true
	case accOverlay:
		return c.overlayCaller()
	}
	c.notFound()
	return false
}
