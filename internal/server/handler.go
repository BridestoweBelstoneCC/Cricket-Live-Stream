package server

// The HTTP handler: every route the overlay, control panel and scoring page call.
// Routes are matched in the order Python's do_GET/do_POST chains tested them — exact
// paths before prefixes, so a prefix never swallows a more specific route.

import (
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "image/gif" // sponsor uploads and logo previews decode any accepted type
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/webp"

	"cricketstream/internal/assets"
	"cricketstream/internal/obs"
	"cricketstream/internal/pyfmt"
	"cricketstream/internal/pyval"
)

// ctx is one request: the raw path (undecoded, like Python's urlparse(self.path).path)
// and helpers that mirror the Python handler's _json/_html/_file.
type ctx struct {
	w      http.ResponseWriter
	r      *http.Request
	path   string
	query  url.Values
	method string
}

// Handler serves every route.
type Handler struct{}

func (Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw := r.RequestURI
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	if u, err := url.Parse(raw); err == nil && u.Host != "" { // absolute-form request
		raw = u.EscapedPath()
	}
	c := &ctx{w: w, r: r, path: raw, query: r.URL.Query(), method: r.Method}
	defer func() {
		if p := recover(); p != nil {
			if p == http.ErrAbortHandler {
				panic(p)
			}
			// Record handler failures in /health's flight recorder as well as the console
			// — mid-match, nobody is reading the terminal. The connection is dropped, as
			// Python's handle_error did.
			logServerError("request from "+c.socketIP(), fmt.Errorf("%v", p))
			say("  ✗  Request %s %s failed: %v", r.Method, c.path, p)
			panic(http.ErrAbortHandler)
		}
	}()
	switch r.Method {
	case "GET":
		c.doGET()
	case "POST":
		c.doPOST()
	case "OPTIONS":
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Content-Type,Authorization")
		w.WriteHeader(200)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

// ── Response helpers ──────────────────────────────────────────

// Baseline hardening headers for every panel/API response.
func (c *ctx) securityHeaders() {
	h := c.w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
}

func (c *ctx) json(data any, status ...int) {
	code := 200
	if len(status) > 0 {
		code = status[0]
	}
	body, err := json.Marshal(data)
	if err != nil {
		body, _ = json.Marshal(M{"ok": false, "error": err.Error()})
		code = 500
	}
	h := c.w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	c.securityHeaders()
	// Mutating (POST) responses are same-origin-only — the control panel calls them from
	// its own page on this server, so no CORS header is needed — except the one POST
	// route the overlay itself calls (over-end AI commentary), which keeps it for the
	// same reason /live's GET does.
	if c.method != "POST" || c.path == "/commentary/over/generate" {
		h.Set("Access-Control-Allow-Origin", "*")
	}
	c.w.WriteHeader(code)
	c.w.Write(body)
}

func (c *ctx) html(body []byte) {
	h := c.w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	// NEVER cache the panel/scoring page. They change with every update, and a browser
	// serving a stale copy runs OLD JS against a NEW server — e.g. a control it doesn't
	// know to save, so a toggle looks 'stuck'. no-store forces a fresh page each load.
	h.Set("Cache-Control", "no-store, must-revalidate")
	c.securityHeaders()
	// The panel is one self-contained page (inline <style>/<script>, no external
	// resources), so 'unsafe-inline' is required but everything else is locked down.
	h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; "+
		"style-src 'self' 'unsafe-inline'; img-src 'self' data:; "+
		"connect-src 'self'; frame-ancestors 'none'")
	c.w.WriteHeader(200)
	c.w.Write(body)
}

func (c *ctx) bytes(body []byte, mime string) {
	h := c.w.Header()
	h.Set("Content-Type", mime)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Set("Cache-Control", "no-store")
	c.w.WriteHeader(200)
	c.w.Write(body)
}

func (c *ctx) sendBody(body []byte, mime string, noCache bool) {
	h := c.w.Header()
	h.Set("Content-Type", mime)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	if noCache {
		// HTML (the overlay) must not be cached — OBS could otherwise run old graphics
		// code after an update. Images served here stay cacheable.
		h.Set("Cache-Control", "no-store, must-revalidate")
	}
	c.w.WriteHeader(200)
	c.w.Write(body)
}

func (c *ctx) file(path, mime string, noCache bool) {
	body, err := os.ReadFile(path)
	if err != nil {
		c.w.WriteHeader(404)
		c.w.Write([]byte("Not found"))
		return
	}
	c.sendBody(body, mime, noCache)
}

// asset serves an app file: one beside the exe overrides the embedded copy.
func (c *ctx) asset(name, mime string, noCache bool) {
	body, err := assets.Read(name)
	if err != nil {
		c.w.WriteHeader(404)
		c.w.Write([]byte("Not found"))
		return
	}
	c.sendBody(body, mime, noCache)
}

func (c *ctx) notFound() { c.w.WriteHeader(404) }

func (c *ctx) notFoundEmpty() {
	c.w.Header().Set("Content-Length", "0")
	c.w.WriteHeader(404)
}

// ── Request helpers ───────────────────────────────────────────

func (c *ctx) socketIP() string {
	host, _, err := net.SplitHostPort(c.r.RemoteAddr)
	if err != nil {
		return c.r.RemoteAddr
	}
	return host
}

// clientIP prefers X-Forwarded-For's first hop over the raw socket address — a tunnel
// (Tailscale funnel / Cloudflare quick tunnel) proxies the connection, so the socket
// address would otherwise always be the tunnel's own endpoint.
func (c *ctx) clientIP() string {
	if xff := c.r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	return c.socketIP()
}

// trustedLoopback is true only for a genuine same-machine connection with nothing
// proxying it — e.g. the overlay running in OBS on this box. cloudflared forwards
// tunnelled requests to 127.0.0.1 too, so a bare address check would be fooled by it;
// the giveaway is that a proxied request carries X-Forwarded-For and a direct one never does.
func (c *ctx) trustedLoopback() bool {
	if c.r.Header.Get("X-Forwarded-For") != "" {
		return false
	}
	ip := c.socketIP()
	return ip == "127.0.0.1" || ip == "::1"
}

// checkToken is true if the request carries a valid session token (or auth is
// disabled). Bearer-only by design (see control.html's apiFetch): no cookie fallback, so
// there's no ambient credential a cross-site page could ride along on CSRF-style.
func (c *ctx) checkToken() bool {
	if ClubPassword == "" {
		return true
	}
	auth := c.r.Header.Get("Authorization")
	if tok, ok := strings.CutPrefix(auth, "Bearer "); ok && tok != "" && verifySessionToken(tok) {
		return true
	}
	ip := c.clientIP()
	say("  ✗  Auth: 401 %s [%s]", c.r.RequestURI, ip)
	authLogAdd("401", ip)
	c.json(M{"ok": false, "error": "Unauthorized"}, 401)
	return false
}

// originOK is true unless Origin/Referer is present and points at a different host —
// defense in depth against CSRF-style cross-site requests. Absent headers pass (curl,
// some embedded-browser fetches don't send them); only a *mismatched* one is rejected.
// So is one naming no host at all: "Origin: null" is what a sandboxed iframe or a
// file:// page sends, and the Python original let it through — any web page could post
// to the panel that way (a deliberate difference from upstream).
func (c *ctx) originOK() bool {
	host := c.r.Host
	for _, hdr := range []string{"Origin", "Referer"} {
		val := c.r.Header.Get(hdr)
		if val == "" {
			continue
		}
		u, err := url.Parse(val)
		if err != nil {
			return false
		}
		if u.Host != host {
			return false
		}
	}
	return true
}

// rateLimit is true if the call may go ahead; otherwise it has already sent the 429.
func (c *ctx) rateLimit(path string, key ...string) bool {
	k := path
	if len(key) > 0 {
		k = key[0]
	}
	if wait := rateLimited(path, k); wait > 0 {
		c.json(M{"ok": false, "error": fmt.Sprintf("Please wait %ds before trying again", wait)}, 429)
		return false
	}
	return true
}

func (c *ctx) q(name string) string { return strings.TrimSpace(c.query.Get(name)) }

// truthyFlag is Python's q.get(name, ["0"])[0].lower() in ("1", "true", "yes").
func (c *ctx) truthyFlag(name string) bool {
	switch strings.ToLower(c.query.Get(name)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// bareName reduces a URL segment to a file name that can't leave its folder: '\' would
// otherwise survive and, on Windows, a leading '\' or 'C:\' is an absolute path.
func bareName(raw string) string {
	r := strings.NewReplacer("..", "", "/", "", `\`, "", ":", "")
	return filepath.Base(r.Replace(raw))
}

// basenameOf is os.path.basename on a URL path segment with '\' treated as '/'.
func basenameOf(seg string) string {
	seg = strings.ReplaceAll(seg, `\`, "/")
	if i := strings.LastIndex(seg, "/"); i >= 0 {
		seg = seg[i+1:]
	}
	return seg
}

func today() string { return time.Now().Format("2006-01-02") }

// mapIdentity is a key that is the same for one map however many lookup keys point at
// it (Python's id(rec)).
func mapIdentity(v any) uintptr {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Map {
		return rv.Pointer()
	}
	return 0
}

func uniqueRecords(lookup M) []M {
	seen := map[uintptr]bool{}
	var out []M
	for _, k := range sortedKeys(lookup) {
		rec, ok := lookup[k].(M)
		if !ok {
			continue
		}
		id := mapIdentity(rec)
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, rec)
	}
	return out
}

var imageMimes = []struct{ ext, mime string }{{"png", "image/png"}, {"jpg", "image/jpeg"},
	{"jpeg", "image/jpeg"}, {"svg", "image/svg+xml"}, {"webp", "image/webp"}, {"gif", "image/gif"}}

// serveNamedImage serves dir/<name>.<ext> for the first extension that exists.
// namedImage is dir/name.<any image type>, or "".
func namedImage(dir, name string) string {
	for _, m := range imageMimes {
		if p := filepath.Join(dir, name+"."+m.ext); fileExists(p) {
			return p
		}
	}
	return ""
}

func (c *ctx) serveNamedImage(dir, name string) {
	for _, m := range imageMimes {
		p := filepath.Join(dir, name+"."+m.ext)
		if fileExists(p) {
			c.file(p, m.mime, false)
			return
		}
	}
	c.notFoundEmpty()
}

func headshotsFolder(s M) string {
	if h := trim(gs(s, "headshots_folder", "")); h != "" {
		return expandUser(h)
	}
	return filepath.Join(appDir, "headshots")
}

// seasonBuiltToday: the season stats exist and are today's.
func seasonBuiltToday(ss M) bool {
	return pyval.Truthy(ss["built"]) && ss["date"] == today()
}

var dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// ── GET ───────────────────────────────────────────────────────

func (c *ctx) doGET() {
	path := c.path
	switch {
	case path == "/" || path == "/overlay":
		c.asset("overlay.html", "text/html; charset=utf-8", true)

	case path == "/settings":
		c.settingsPage()

	case path == "/settings/info":
		c.settingsInfo()

	case path == "/rehearsal":
		if !c.checkToken() {
			return
		}
		c.json(RehearsalStatus())

	case path == "/control":
		body, err := controlHTML()
		if err != nil {
			c.json(M{"ok": false, "error": fmt.Sprintf("control.html not found (%v) — reinstall CricketStream", err)}, 500)
			return
		}
		c.html(body)

	case path == "/favicon.ico" || path == "/favicon.png":
		// Browsers ask for /favicon.ico on their own; a PNG under that name is fine for
		// every current one, so the panel and scoring page need no <link> tag.
		c.asset("brand/favicon-64.png", "image/png", false)

	case path == "/scoring":
		// Manual ball-by-ball scoring page — phones/tablets, no NV Play needed
		body, err := assets.Read("scoring.html")
		if err != nil {
			c.json(M{"ok": false, "error": fmt.Sprintf("scoring.html not found (%v)", err)}, 500)
			return
		}
		c.html(body)

	case path == "/scoring/state":
		// Read-only view for the scoring page (no secrets — open, like /live/view)
		c.json(ManualUIState())

	case path == "/scoring/balls":
		// Recent balls with positions, for the edit-a-ball picker
		manualMu.Lock()
		sess := manualSessionLocked()
		var balls any = []any{}
		if sess != nil {
			if bl := sess.BallList(30); bl != nil {
				balls = bl
			}
		}
		manualMu.Unlock()
		c.json(M{"ok": sess != nil, "balls": balls})

	case path == "/scoring/scorecard":
		// Full plain-text scorecard for transcribing into Play-Cricket after the game
		manualMu.Lock()
		text := ""
		if sess := manualSessionLocked(); sess != nil {
			text = sess.ScorecardText()
		}
		manualMu.Unlock()
		if text != "" {
			c.bytes([]byte(text), "text/plain; charset=utf-8")
		} else {
			c.json(M{"ok": false, "error": "no scoring session"}, 404)
		}

	case path == "/state":
		st := LoadState()
		// Boolean flags so the overlay can gate features without seeing raw secrets
		st["anthropic_key_set"] = trim(gs(st, "anthropic_api_key", "")) != ""
		st["playcricket_key_set"] = trim(gs(st, "playcricket_api_key", "")) != ""
		// Same reasoning: the overlay needs to know a second camera is configured (to
		// gate the auto-cut feature) without ever seeing the RTSP URL itself.
		st["camera2_configured"] = trim(gs(st, "camera2_rtsp_url", "")) != ""
		// SECURITY: never send stored secrets over HTTP. Responses carry CORS * (the
		// overlay may be loaded from OBS as a separate origin), which means any web page
		// open on this machine could read this endpoint. The control panel shows the
		// sentinel for a stored key; POSTing the sentinel back leaves the stored value
		// untouched (see POST /state), and posting "" clears it.
		c.json(redactState(st))

	case path == "/weather":
		c.json(FetchWeatherData())

	case path == "/commands":
		c.json(popCommands())

	case path == "/commentary/over":
		c.json(getOverCommentary())

	case strings.HasPrefix(path, "/fonts/"):
		// The built-in fonts (OFL), for the overlay's scorebar styles: the same type on
		// every laptop, whatever fonts it has.
		if name := basenameOf(path[len("/fonts/"):]); strings.HasSuffix(name, ".ttf") && !strings.HasPrefix(name, ".") {
			c.asset("fonts/"+name, "font/ttf", false)
		} else {
			c.notFoundEmpty()
		}

	case strings.HasPrefix(path, "/voice/"):
		// The overlay's spoken commentary (loopback OBS source; same exposure as the
		// commentary text it reads aloud).
		name := basenameOf(path[len("/voice/"):])
		full := filepath.Join(VoiceDir, name)
		if strings.HasSuffix(name, ".wav") && fileExists(full) {
			c.file(full, "audio/wav", true)
		} else {
			c.notFoundEmpty()
		}

	case path == "/camera/spotter":
		if !c.checkToken() {
			return
		}
		c.json(spotterStatus())

	case path == "/precheck/last":
		if !c.checkToken() {
			return
		}
		precheckLastMu.Lock()
		res := precheckLast
		precheckLastMu.Unlock()
		if res == nil {
			c.json(M{"ok": nil, "checks": []any{}, "ran_at": nil})
		} else {
			c.json(res)
		}

	case path == "/logos/debug":
		s2 := LoadState()
		lf := trim(gs(s2, "logos_folder", ""))
		ldir := logosFolder(s2)
		files := []string{}
		exists := isDir(ldir)
		if exists {
			entries, _ := os.ReadDir(ldir)
			for _, e := range entries {
				if !strings.HasPrefix(e.Name(), ".") {
					files = append(files, e.Name())
				}
			}
			sort.Strings(files)
		}
		c.json(M{"logos_folder_setting": lf, "resolved_path": ldir, "directory_exists": exists,
			"files": files, "home_club_id": valOr(s2, "home_club_id", ""),
			"away_club_id": valOr(s2, "away_club_id", ""), "headshots_folder": valOr(s2, "headshots_folder", ""),
			"drinks_over": valOr(s2, "drinks_over", 25)})

	case path == "/pcs/debug":
		c.pcsDebug()

	case path == "/agent/status":
		// How is the link to the scoring laptop doing? Called by the control panel every
		// few seconds while the network source is selected. No secrets beyond LAN
		// topology — open, like /pcs/debug.
		info := AgentStatus(LoadState())
		if addr := gs(info, "address", ""); addr != "" {
			if ping, err := AgentPing(addr, 3*time.Second); ping != nil {
				info["connected"] = true
				info["hostname"] = valOr(ping, "hostname", "")
				info["folder"] = valOr(ping, "folder", "")
				info["file"] = valOr(ping, "file", "")
				info["file_age"] = ping["file_age"]
				info["error"] = ""
				if !pyval.Truthy(ping["file"]) {
					info["error"] = "Connected to the scoring laptop, but no scoreboard file has " +
						"appeared yet. Check that scoreboard output is switched on in PCS Pro."
				}
			} else {
				info["connected"] = false
				info["error"] = err
			}
		}
		c.json(info)

	case path == "/cameras/discover":
		// Shout on the network for IP cameras (ONVIF). Behind the login: it maps the LAN.
		if !c.checkToken() || !c.rateLimit(path) {
			return
		}
		c.json(CamerasDiscover())

	case path == "/agent/discover":
		// Shout on the local network and report every scorer agent that answers.
		if !c.rateLimit(path) {
			return
		}
		st := LoadState()
		agents := DiscoverAgents(2 * time.Second)
		chosen := ""
		if len(agents) > 0 && agentNormalise(gs(st, "agent_host", "")) == "" {
			chosen = gs(agents[0], "address", "")
			UpdateState(func(cur M) { cur["agent_last_seen"] = chosen })
			agentMu.Lock()
			agentResolved = chosen
			agentMu.Unlock()
		}
		errMsg := ""
		if len(agents) == 0 {
			errMsg = "Nothing answered. Check that CricketStream is running in scorer mode on " +
				"the scoring laptop, that both laptops are on the same wifi, and that the " +
				"scoring laptop's firewall is not blocking it."
		}
		if agents == nil {
			agents = []M{}
		}
		c.json(M{"ok": len(agents) > 0, "agents": agents, "chosen": chosen, "error": errMsg})

	case path == "/report/generate":
		if !c.checkToken() {
			return
		}
		rtype := "report"
		if strings.Contains(c.r.RequestURI, "?") && strings.Contains(c.r.RequestURI, "type=social") {
			rtype = "social"
		}
		if !c.rateLimit(path, path+":"+rtype) {
			return
		}
		c.json(GenerateMatchReport(rtype))

	case path == "/data/status":
		c.json(DBStatus())

	case strings.HasPrefix(path, "/data/export"):
		if !c.checkToken() {
			return
		}
		mid := c.q("match_id")
		if mid == "" {
			c.json(M{"ok": false, "error": "match_id required"}, 400)
			return
		}
		csvText, err := ExportMatchCSV(mid)
		if err != nil {
			c.json(M{"ok": false, "error": err.Error()}, 500)
			return
		}
		h := c.w.Header()
		h.Set("Content-Type", "text/csv")
		h.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="match_%s_balls.csv"`, mid))
		h.Set("Content-Length", strconv.Itoa(len(csvText)))
		c.w.WriteHeader(200)
		io.WriteString(c.w, csvText)

	case path == "/social/photos":
		tk := c.q("team")
		c.json(M{"photos": listSocialPhotos(tk), "team": tk})

	case path == "/social/recent":
		if !c.checkToken() {
			return
		}
		// Recent completed matches for the post picker — works for away games too.
		rcfg := LoadState()
		rkey := trim(pyStrOr(rcfg["playcricket_api_key"], rcfg["api_token"]))
		rsite := trim(pyStrOr(rcfg["home_club_id"], rcfg["site_id"]))
		if rkey == "" || rsite == "" {
			c.json(M{"ok": false, "error": "Set your PlayCricket API key and club ID first", "matches": []any{}})
			return
		}
		matches, err := pcRecentMatches(rkey, rsite, strconv.Itoa(time.Now().Year()), rcfg, 12)
		if err != nil {
			c.json(M{"ok": false, "error": err.Error(), "matches": []any{}})
			return
		}
		if matches == nil {
			matches = []M{}
		}
		c.json(M{"ok": true, "matches": matches})

	case strings.HasPrefix(path, "/social/image/generate"):
		c.socialImageGenerate()

	case path == "/social/clips":
		if !c.checkToken() {
			return
		}
		socialClipsMu.Lock()
		out := shallow(socialClipsStatus)
		socialClipsMu.Unlock()
		out["clips"] = listSocialClips()
		c.json(out)

	case strings.HasPrefix(path, "/social/clips/file/"):
		// Same exposure as /social/image/latest: content made to be posted publicly.
		name := basenameOf(path[len("/social/clips/file/"):])
		full := filepath.Join(socialClipsDir(""), name)
		if strings.HasSuffix(name, ".mp4") && fileExists(full) {
			c.file(full, "video/mp4", false)
		} else {
			c.notFoundEmpty()
		}

	case path == "/match/page":
		if !c.checkToken() {
			return
		}
		matchPageMu.Lock()
		out := shallow(matchPageStatus)
		matchPageMu.Unlock()
		out["latest"] = nil
		if p := latestMatchPage(); p != "" {
			out["latest"] = filepath.Base(p)
		}
		c.json(out)

	case path == "/match/page/latest":
		// Same exposure as the result card: a page made to be shared.
		if p := latestMatchPage(); p != "" {
			c.file(p, "text/html; charset=utf-8", true)
		} else {
			c.notFoundEmpty()
		}

	case path == "/social/image/latest":
		// The most recently generated Instagram graphic, for preview/download.
		imgs, _ := filepath.Glob(filepath.Join(appDir, "instagram_result_*.png"))
		sort.SliceStable(imgs, func(i, j int) bool { return mtime(imgs[i]) > mtime(imgs[j]) })
		if len(imgs) > 0 {
			c.file(imgs[0], "image/png", false)
		} else {
			c.notFoundEmpty()
		}

	case path == "/report/log":
		if !c.checkToken() {
			return
		}
		// Raw match log (for debugging / manual editing)
		c.json(matchLogCopy())

	case strings.HasPrefix(path, "/headshot/"):
		c.headshot()

	case path == "/library":
		// The Pictures tab's list (player names from the roster: behind the login).
		if !c.checkToken() {
			return
		}
		out, err := LibraryList(c.q("kind"))
		if err != nil {
			c.json(M{"ok": false, "error": err.Error()}, 400)
			return
		}
		c.json(out)

	case path == "/library/file":
		// A thumbnail. Open, like /logo/ and /headshot/ (an <img> can't send the login,
		// and these are the same pictures the overlay shows anyway).
		f, mime, err := LibraryOpen(c.q("kind"), c.q("file"))
		if err != nil {
			c.notFoundEmpty()
			return
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, SponsorUploadMaxBytes+1))
		if err != nil {
			c.notFoundEmpty()
			return
		}
		c.bytes(b, mime)

	case path == "/logos/list":
		// Badge files in the logos folder, so the control panel can offer a manual picker.
		dir := logosFolder(LoadState())
		out := []M{}
		if entries, err := os.ReadDir(dir); err == nil {
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Name())
			}
			sort.Strings(names)
			for _, f := range names {
				l := strings.ToLower(f)
				for _, ext := range []string{".png", ".jpg", ".jpeg", ".svg", ".webp", ".gif"} {
					if strings.HasSuffix(l, ext) {
						id := f
						if i := strings.LastIndex(f, "."); i >= 0 {
							id = f[:i]
						}
						out = append(out, M{"file": f, "id": id})
						break
					}
				}
			}
		}
		c.json(M{"folder": dir, "logos": out})

	case strings.HasPrefix(path, "/logo/"):
		// Club badge images from the logos folder: by PlayCricket club ID, or by team name
		// for a side whose ID isn't known (no fixture fetched). A name is looked for as
		// typed and as the Pictures tab saves it ("B&BCC" is kept as "BBCC").
		dir := logosFolder(LoadState())
		raw, err := url.PathUnescape(strings.Trim(path[6:], "/"))
		if err != nil {
			raw = strings.Trim(path[6:], "/")
		}
		name := bareName(raw)
		if alt := libraryName(name); alt != "" && alt != name && namedImage(dir, name) == "" {
			name = alt
		}
		c.serveNamedImage(dir, name)

	case path == "/sponsor/bg_preview":
		// Before the /sponsor/<id> prefix below, like /sponsor/airtime. A PNG of what
		// "make transparent" WOULD give, for the panel to show side by side; not saved.
		if !c.checkToken() {
			return
		}
		out, why := sponsorTransparentVersion(c.query.Get("id"), c.query.Get("mode"))
		if out == nil {
			c.json(M{"ok": false, "error": why}, 400)
			return
		}
		b, err := pngBytes(out)
		if err != nil {
			c.json(M{"ok": false, "error": err.Error()}, 400)
			return
		}
		c.bytes(b, "image/png")

	case path == "/sponsor/airtime":
		// Before the /sponsor/<id> prefix below, which would otherwise take it as a logo
		// id. Open like /health: a sponsor name and some durations, no secrets.
		day := c.q("date")
		if !dateRe.MatchString(day) {
			day = ""
		}
		c.json(SponsorAirtimeSummary(day))

	case strings.HasPrefix(path, "/sponsor/"):
		// The weekend sponsor image, named by ID (sponsor_id "3" -> sponsors/3.png), so the
		// same set of images can be reused week to week just by changing the ID.
		c.serveNamedImage(SponsorDir, bareName(strings.Trim(path[9:], "/")))

	case path == "/player/stats":
		c.playerStats()

	case path == "/season/top":
		// Pre-game "season form" panel data: each team's own top scorer/wicket-taker.
		// Same lazy-build-in-background pattern as /player/stats — never blocks the overlay.
		ss := seasonStatsCopy()
		built := seasonBuiltToday(ss)
		if !built && !pyval.Truthy(ss["building"]) {
			safeGo("season stats", func() { BuildSeasonStats(false) })
		}
		if !built {
			c.json(M{"ready": false})
			return
		}
		blank := M{"home": nil, "away": nil}
		scorers, bowlers := ss["top_scorers"], ss["top_bowlers"]
		if !pyval.Truthy(scorers) {
			scorers = blank
		}
		if !pyval.Truthy(bowlers) {
			bowlers = blank
		}
		c.json(M{"ready": true, "scorers": scorers, "bowlers": bowlers})

	case path == "/match/scorecard":
		// The match so far for the clubhouse screen (scorecard.go). Read-only and open,
		// like /live/view: the score is what the stream shows anyway.
		c.json(MatchScorecard())

	case path == "/break":
		// The break show (breakshow.go), for the panel.
		if !c.checkToken() {
			return
		}
		c.json(BreakStatus())

	case path == "/lineups":
		// Today's two XIs as entered on PlayCricket (lineups.go), for the pre-match show.
		c.json(TodaysLineups())

	case path == "/h2h":
		// Head to head with today's opposition (h2h.go). Built in the background:
		// {"ready": false} until it's done. ?force=1 (logged in) builds it again.
		force := c.q("force") == "1"
		if force && !c.checkToken() {
			return
		}
		c.json(HeadToHead(force))

	case path == "/league/table":
		// Pre-game / between-innings "context" panel: where this result leaves the home
		// club in the table. Same lazy-build pattern — never blocks the overlay's poll.
		compID := trim(pyval.Str(orNil(LoadState()["competition_id"])))
		leagueTableMu.Lock()
		lt := shallow(leagueTable)
		leagueTableMu.Unlock()
		built := pyval.Truthy(lt["built"]) && lt["date"] == today() && pyval.Str(orNil(lt["competition_id"])) == compID
		switch {
		case !built:
			safeGo("league table", func() { FetchLeagueTable(false) })
			c.json(M{"ready": false})
		case pyval.Truthy(lt["error"]):
			c.json(M{"ready": true, "usable": false, "error": lt["error"]})
		default:
			home := leagueTableHomeRow(lt)
			if home == nil {
				c.json(M{"ready": true, "usable": false, "error": "Home club not found in this table — " +
					"probably a cup/friendly competition_id, not the league"})
			} else {
				// rows/home_index: the whole table, for the clubhouse screen; the overlay
				// uses row/row_above.
				c.json(M{"ready": true, "usable": true, "name": valOr(lt, "name", ""), "row": home["row"],
					"row_above": home["row_above"], "total_teams": len(gl(lt, "rows")),
					"rows": gl(lt, "rows"), "home_index": home["index"]})
			}
		}

	case path == "/player/stats/refresh":
		if !c.checkToken() {
			return
		}
		// Build/ensure the season stats cache. ?force=1 forces a fresh pull (control-panel
		// button); without it, a same-day cache is reused so restarts don't re-hit the API.
		res := BuildSeasonStats(c.truthyFlag("force"))
		seasonStatsMu.Lock()
		fromCache := seasonStatsLastAction == "cache"
		seasonStatsMu.Unlock()
		c.json(M{"ok": true, "players": len(gm(res, "lookup")), "matches_used": valOr(res, "matches_used", 0),
			"api_calls": valOr(res, "calls", 0), "from_cache": fromCache,
			"opposition": pyval.Truthy(res["away_ok"]), "error": res["error"]})

	case path == "/match/fetch":
		if !c.checkToken() {
			return
		}
		c.matchFetch()

	case path == "/match/fixtures":
		// Every fixture for the season
		s := LoadState()
		u := fmt.Sprintf("https://play-cricket.com/api/v2/matches.json?api_token=%s&site_id=%s&season=%d",
			trim(gs(s, "playcricket_api_key", "")), trim(gs(s, "home_club_id", "")), time.Now().Year())
		data, err := httpGetJSON(u, 15*time.Second, map[string]string{"Accept": "application/json"})
		if err != nil {
			c.json(M{"ok": false, "error": err.Error()})
			return
		}
		matches := gl(data, "matches")
		if len(matches) > 50 {
			matches = matches[:50]
		}
		if matches == nil {
			matches = []any{}
		}
		c.json(M{"ok": true, "matches": matches})

	case path == "/save/test":
		if !c.checkToken() {
			return
		}
		// Quick test: write a test value and read it back
		UpdateState(func(s M) { s["_save_test"] = "ok" })
		s2 := LoadState()
		c.json(M{"saved": s2["_save_test"] == "ok", "state_file": StateFile, "file_exists": fileExists(StateFile)})

	case path == "/highlights/status":
		// Outcome of the last/current background compile (see POST /highlights)
		highlightsMu.Lock()
		out := shallow(highlightsStatus)
		highlightsMu.Unlock()
		c.json(out)

	case path == "/status":
		// Server uptime and clip count
		up := int(time.Since(serverStart).Seconds())
		h, rem := up/3600, up%3600
		mins, secs := rem/60, rem%60
		upStr := fmt.Sprintf("%dm %ds", mins, secs)
		if h > 0 {
			upStr = fmt.Sprintf("%dh %dm %ds", h, mins, secs)
		}
		st := LoadState()
		folder := replayFolder(st)
		clips := replayClips(folder)
		c.json(M{"uptime": upStr, "uptime_sec": up, "clip_count": len(clips),
			"tagged": countTagged(clips), "max_clips": pyval.IntOr(valOr(st, "max_clips", 100), 100), "folder": folder})

	case path == "/health":
		c.health()

	case path == "/auth/log":
		// Auth-gated (unlike /health) — this reveals client IPs, so it shouldn't be
		// readable by anonymous callers. Lets an operator see if something's been probing.
		if !c.checkToken() {
			return
		}
		entries := authLogCopy(50)
		for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 { // most recent first
			entries[i], entries[j] = entries[j], entries[i]
		}
		if entries == nil {
			entries = []M{}
		}
		c.json(M{"ok": true, "entries": entries})

	case path == "/obs/stream_check":
		if !c.checkToken() {
			return
		}
		force := c.truthyFlag("force")
		s := LoadState()
		// Only a real upload test is rate-limited. The panel reads the cached result on
		// every load, and that read used to start the 5-minute cooldown — so "Check now"
		// refused for the first 5 minutes after opening the panel.
		if uploadTestNeeded(s, force) && !c.rateLimit(path) {
			return
		}
		out := M{"ok": true}
		if mbps, fresh, err := getUploadMbps(s, force); err != nil {
			out["network"] = M{"error": "Could not test upload speed: " + err.Error()}
		} else {
			rec := obs.RecommendStreamSettings(mbps)
			out["network"] = M{"upload_mbps": pyfmt.Round(mbps, 2), "freshly_tested": fresh,
				"recommended_kbps": rec.BitrateKbps, "recommended_resolution": rec.Resolution,
				"recommended_fps": rec.FPS, "note": rec.Note}
		}
		// No encoder test: OBS only builds encoders at startup, so switching it over the
		// WebSocket measured the same encoder twice. obs.EnsureEncoder picks one from what
		// OBS itself reports it can use, while OBS is closed.
		c.json(out)

	case path == "/camera/cut_test":
		// The camera cut test's steps so far (the panel polls while it runs).
		if !c.checkToken() {
			return
		}
		c.json(CutTestStatus())

	case path == "/checklist":
		// Auth-gated like the rest of the operator's controls: it's the read side of
		// buttons that start OBS and go live.
		if !c.checkToken() {
			return
		}
		c.json(ChecklistStatus())

	case path == "/stream/monitor":
		// Live congestion/quality picture for the panel (no secrets — open)
		c.json(streamMonitorStatus())

	case path == "/preview.jpg":
		// What's on air, small — the clubhouse screen's corner picture. Behind the
		// login: it's the camera picture, before the stream's own delay.
		if !c.checkToken() {
			return
		}
		jpeg := ProgramPreview()
		if jpeg == nil {
			c.json(M{"ok": false, "error": "No picture — is OBS open?"}, 503)
			return
		}
		c.bytes(jpeg, "image/jpeg")

	case path == "/youtube/chapters":
		// What the match video's chapters would be — nothing is written (POST does that).
		if !c.checkToken() {
			return
		}
		ok, msg, text := AddChaptersToYouTube(CurrentMatchID(), true)
		c.json(M{"ok": ok, "message": msg, "chapters": text})

	case path == "/youtube/live":
		// The live broadcast's video id, for the clubhouse screen to play full size.
		if !c.checkToken() {
			return
		}
		c.json(LiveVideo(c.truthyFlag("force")))

	case path == "/remote/info":
		// Not auth-gated, same reasoning as /health: no secrets beyond a URL, and that URL
		// is only reachable by a device already on the right network/tunnel anyway.
		targets := remoteTargets()
		if targets == nil {
			targets = []M{}
		}
		c.json(M{"ok": len(targets) > 0, "targets": targets})

	case path == "/remote/qr.png":
		via := c.query.Get("via")
		targets := remoteTargets()
		var target M
		for _, t := range targets {
			if gs(t, "via", "") == via {
				target = t
				break
			}
		}
		if target == nil && len(targets) > 0 {
			target = targets[0]
		}
		var png []byte
		if target != nil {
			png = qrPNG(gs(target, "url", ""))
		}
		if png == nil {
			c.notFound()
			return
		}
		c.bytes(png, "image/png")

	case path == "/live" || path == "/live/view":
		c.live(path == "/live")

	// /commentary/over/generate and /commentary/test are POST-only.

	default:
		c.notFound()
	}
}

// orNil maps a missing value to "" so pyval.Str gives Python's str(x or "").
func orNil(v any) any {
	if !pyval.Truthy(v) {
		return ""
	}
	return v
}

// pyStrOr is str(a or b or "").
func pyStrOr(a, b any) string {
	if pyval.Truthy(a) {
		return pyval.Str(a)
	}
	if pyval.Truthy(b) {
		return pyval.Str(b)
	}
	return ""
}

// countTagged is the clips with a real reason on record (replay-test clips excluded) —
// surfaced in the panel so nobody needs a terminal to confirm tagging works.
func countTagged(clips []string) int {
	n := 0
	for _, t := range clipTags(clips) {
		if strings.ToLower(t.Reason) != "test" {
			n++
		}
	}
	return n
}

func (c *ctx) pcsDebug() {
	s2 := LoadState()
	if gs(s2, "pcs_source", "local") == "agent" {
		// Two-laptop mode: the file lives on the scoring laptop, so ask the agent for it
		// rather than hunting a folder that isn't on this machine. Same response shape as
		// local mode (raw_pcs parsed, same diagnostics), so troubleshooting a template
		// misconfiguration isn't worse just because a club is on agent mode.
		address := gs(AgentStatus(s2), "address", "")
		if address == "" {
			c.json(M{"source": "agent", "address": "", "raw_pcs": nil, "diagnostics": nil, "keys": nil,
				"search_note": "No scorer laptop found yet. Start CricketStream in scorer mode on " +
					"the scoring laptop, then press Find scorer laptop in the control panel."})
			return
		}
		ping, perr := AgentPing(address, 4*time.Second)
		if ping == nil {
			c.json(M{"source": "agent", "address": address, "raw_pcs": nil, "diagnostics": nil,
				"keys": nil, "search_note": perr})
			return
		}
		payload, err := agentGet("http://"+address+"/pcs", 5*time.Second)
		if err != nil {
			c.json(M{"source": "agent", "address": address, "raw_pcs": nil, "diagnostics": nil, "keys": nil,
				"search_note": "Reached the scorer laptop but could not collect the file: " + err.Error()})
			return
		}
		content := gs(payload, "content", "")
		okPayload := pyval.Truthy(payload["ok"])
		var raw M
		if okPayload && trim(content) != "" {
			if err := json.Unmarshal([]byte(content), &raw); err != nil {
				raw = M{"error": fmt.Sprintf("Found %s but could not parse as JSON: %v",
					orStr(gs(payload, "name", ""), "a file"), err), "raw_text": truncRunes(content, 8000)}
			}
		}
		var parsed M
		if okPayload {
			parsed = parseScoreboardRaw(content, gs(payload, "name", ""))
		}
		note := ""
		if !okPayload {
			note = pyStrOr(payload["detail"], payload["error"])
		}
		c.json(M{"source": "agent", "address": address, "hostname": valOr(ping, "hostname", ""),
			"remote_folder": valOr(ping, "folder", ""), "file_found": payload["name"],
			"file_age": payload["age"], "diagnostics": pcsDebugDiagnostics(raw), "raw_pcs": rawOrNil(raw),
			"keys": rawKeys(raw), "parsed_ok": len(parsed) > 0, "search_note": note})
		return
	}
	bridgeMode := trim(gs(s2, "pcs_bridge_url", "")) != ""
	folder := EffectivePCSFolder(s2)
	var raw M
	found := ""
	var note any
	switch {
	case folder == "":
		note = "No pcs_output_folder set in the control panel / config.ini."
	case !isDir(expandUser(folder)):
		if bridgeMode {
			note = "NV Play bridge mirror folder doesn't exist yet: " + folder + " — the bridge " +
				"sync hasn't written a file yet, check pcs_bridge_url/token and that the bridge is running"
		} else {
			note = "Folder does not exist: " + folder
		}
	default:
		// The SAME finder as the live feed, so this locates whatever NV Play actually
		// writes — .json OR .xml OR a known filename (it often writes JSON into a .xml).
		found = FindPCSOutputFile(folder)
		if found == "" {
			var listing []string
			if entries, err := os.ReadDir(expandUser(folder)); err == nil {
				for _, e := range entries {
					listing = append(listing, e.Name())
				}
				sort.Strings(listing)
			}
			l := "(folder empty)"
			if len(listing) > 0 {
				l = strings.Join(listing, ", ")
			}
			note = "No PCS output file found (looked for known filenames and any .json/.xml modified " +
				"in the last 10 minutes). Has the scorer written at least one ball? Files currently in " +
				"the folder: " + l
		} else {
			b, err := os.ReadFile(found)
			text := strings.TrimPrefix(string(b), "\ufeff")
			if err == nil {
				err = json.Unmarshal([]byte(text), &raw)
			}
			if err != nil || raw == nil {
				// Not JSON (e.g. the all-fields dump with no template, or XML). Return the
				// raw text so the available field names can be read from it.
				var rawText any
				if b != nil {
					rawText = truncRunes(strings.ToValidUTF8(text, "\ufffd"), 8000)
				}
				if err == nil {
					err = errors.New("not a JSON object")
				}
				raw = M{"error": fmt.Sprintf("Found %s but could not parse as JSON: %v", filepath.Base(found), err),
					"raw_text": rawText}
			}
		}
	}
	// Diagnostics surface the fields most likely to be misconfigured in NV Play (a
	// {{...}} value means NV Play did not recognise that field).
	var fileFound any
	if found != "" {
		fileFound = filepath.Base(found)
	}
	c.json(M{"diagnostics": pcsDebugDiagnostics(raw), "pcs_folder": folder, "file_found": fileFound,
		"search_note": note, "raw_pcs": rawOrNil(raw), "keys": rawKeys(raw)})
}

func rawOrNil(raw M) any {
	if raw == nil {
		return nil
	}
	return raw
}

func rawKeys(raw M) any {
	if raw == nil {
		return nil
	}
	if _, bad := raw["error"]; bad {
		return nil
	}
	return sortedKeys(raw)
}

func (c *ctx) socialImageGenerate() {
	if !c.checkToken() {
		return
	}
	if !c.rateLimit("/social/image/generate") {
		return
	}
	// An Instagram result graphic: photo backdrop + match facts. ?photo=<filename>
	// selects a backdrop from the socials folder; otherwise the newest photo there is
	// used (or a plain backdrop if none).
	chosen, matchID, teamKey := c.q("photo"), c.q("match_id"), c.q("team")
	// The backdrop comes from the team's subfolder if one exists (socials/2nd), else root.
	sfolder := socialsDir(teamKey)
	photos := listSocialPhotos(teamKey)
	photoPath := ""
	for _, p := range photos {
		if chosen != "" && p == chosen {
			photoPath = filepath.Join(sfolder, chosen)
		}
	}
	if photoPath == "" && len(photos) > 0 {
		best := -1.0
		for _, p := range photos {
			full := filepath.Join(sfolder, p)
			if t := mtime(full); t > best {
				best, photoPath = t, full
			}
		}
	}
	// Facts: a chosen past match (away games, no stream) or the streamed match. The match
	// path is deterministic from PlayCricket + an optional AI caption.
	var facts M
	if matchID != "" {
		facts = BuildMatchFactsFromPC(matchID, teamKey)
	} else {
		facts = GenerateSocialGraphicFacts()
	}
	if !pyval.Truthy(facts["ok"]) {
		c.json(M{"ok": false, "error": valOr(facts, "error", "Could not generate facts")})
		return
	}
	facts["caption"] = aiCaptionForFacts(facts)
	out, err := BuildInstagramImage(facts, photoPath, "")
	if err != nil {
		c.json(M{"ok": false, "error": "Image build failed: " + err.Error()})
		return
	}
	var used any
	if photoPath != "" {
		used = filepath.Base(photoPath)
	}
	c.json(M{"ok": true, "file": filepath.Base(out), "caption": valOr(facts, "caption", ""), "used_photo": used})
}

func (c *ctx) headshot() {
	rawSeg, err := url.PathUnescape(c.path[10:])
	if err != nil {
		rawSeg = c.path[10:]
	}
	rawSeg = strings.ReplaceAll(strings.Trim(rawSeg, "/"), "..", "")
	name := strings.NewReplacer("/", "", `\`, "", ":", "").Replace(rawSeg)
	num := c.q("num")
	// One method for both sides: a confirmed number wins (file-by-number or roster name),
	// otherwise fall back to surname matching on the scorebar name.
	resolved, byNum := resolvePlayer(name, num)
	hdir := headshotsFolder(LoadState())
	mimes := map[string]string{"png": "image/png", "jpg": "image/jpeg", "jpeg": "image/jpeg", "webp": "image/webp"}
	var files []string
	if entries, err := os.ReadDir(hdir); err == nil {
		for _, e := range entries {
			n := e.Name()
			ext := n
			if i := strings.LastIndex(n, "."); i >= 0 {
				ext = n[i+1:]
			}
			if _, ok := mimes[strings.ToLower(ext)]; ok {
				files = append(files, n)
			}
		}
	}
	sort.Strings(files)
	if f := matchHeadshot(files, resolved, num, byNum); f != "" {
		ext := strings.ToLower(f[strings.LastIndex(f, ".")+1:])
		c.file(filepath.Join(hdir, f), mimes[ext], false)
		return
	}
	c.notFoundEmpty()
}

// matchHeadshot picks the photo for a player from the photo files' names, tiered so the
// scorebar name finds the photo even when they differ in form:
//
//	number : '21.png'    — only for a CONFIRMED roster match (byNum: the surname agrees),
//	                       so it can't grab a home file for an away player
//	full   : 'P SMITH'   -> 'p.smith'
//	initsur: 'K J JONES' -> 'k.jones'  (first initial + surname)
//	surname: 'WALKER'    -> 'j.walker' (only when one file has that surname)
//
// "" when none fits. The Pictures tab uses it too, to say which players have no photo.
func matchHeadshot(files []string, name, num string, byNum bool) string {
	stem := func(f string) string {
		if i := strings.LastIndex(f, "."); i >= 0 {
			return f[:i]
		}
		return ""
	}
	if byNum && num != "" {
		for _, f := range files {
			if trim(stem(f)) == num {
				return f
			}
		}
	}
	want := nameKeys(name)
	fkeys := make([]keys, len(files))
	for i, f := range files {
		fkeys[i] = nameKeys(stem(f))
	}
	if want.full != "" {
		for i, f := range files {
			if fkeys[i].full == want.full {
				return f
			}
		}
	}
	if want.initsur != "" {
		for i, f := range files {
			if fkeys[i].initsur == want.initsur {
				return f
			}
		}
	}
	if want.surname != "" {
		var hits []string
		for i, f := range files {
			if fkeys[i].surname == want.surname {
				hits = append(hits, f)
			}
		}
		if len(hits) == 1 {
			return hits[0]
		}
	}
	return ""
}

func (c *ctx) playerStats() {
	name, num := c.q("name"), c.q("num")
	// One method for both sides: number→roster if the surname matches, else surname.
	lookupBy, _ := resolvePlayer(name, num)
	ss := seasonStatsCopy()
	// Diagnostic: /player/stats?name=PENBERTHY&debug=1 shows why a name did or didn't match
	if c.query.Get("debug") != "" {
		lk := gm(ss, "lookup")
		k := nameKeys(lookupBy)
		same := []M{}
		for _, rec := range uniqueRecords(lk) {
			if nameKeys(gs(rec, "name", "")).surname == k.surname {
				same = append(same, M{"name": rec["name"], "inn": rec["inn"], "avg": rec["avg"], "hs": rec["hs"]})
			}
		}
		_, fullHit := lk[k.full]
		_, initHit := lk[k.initsur]
		_, surHit := lk[k.surname]
		note := ""
		if len(same) > 1 {
			note = "more than one player shares this surname, so the surname-only match is " +
				"suppressed — add them to the roster with shirt numbers"
		}
		c.json(M{"query": name, "shirt_num": num, "resolved": lookupBy,
			"keys":     M{"full": k.full, "initsur": k.initsur, "surname": k.surname},
			"full_hit": fullHit, "initsur_hit": initHit, "surname_hit": surHit,
			"players_with_this_surname": same, "built": ss["built"], "note": note})
		return
	}
	built := seasonBuiltToday(ss)
	// Kick off a one-time background build the first time stats are asked for. The API
	// is pulled once (then cached to disk for the day); never blocks the overlay.
	if !built && !pyval.Truthy(ss["building"]) {
		safeGo("season stats", func() { BuildSeasonStats(false) })
	}
	blank := M{"avg": "—", "hs": "—", "inn": "—"}
	if !built {
		blank["ready"] = false
		c.json(blank)
		return
	}
	out := M{"ready": true}
	if rec := lookupSeasonStats(lookupBy); rec != nil {
		for k, v := range rec {
			out[k] = v
		}
	} else {
		for k, v := range blank {
			out[k] = v
		}
	}
	c.json(out)
}

func (c *ctx) matchFetch() {
	// Today's match from the PlayCricket API, auto-filling state
	s := LoadState()
	result := FetchTodaysMatch(trim(gs(s, "playcricket_api_key", "")), trim(gs(s, "home_club_id", "")))
	if _, failed := result["error"]; !failed {
		updates := M{}
		// The CLUB name ("Rivals CC"), not PlayCricket's team label ("1st XI", "Under 15")
		// — the label alone reached the overlay, report and card.
		opp := result["away_club"]
		if !pyval.Truthy(opp) {
			opp = result["away_team"]
		}
		if pyval.Truthy(opp) {
			updates["away_team"] = opp
		}
		for _, kv := range [][2]string{{"home_club_id", "home_club_id"}, {"away_club_id", "away_club_id"},
			{"away_abbrev", "away_abbrev"}, {"competition", "competition_name"},
			{"competition_id", "competition_id"}, {"umpire1", "umpire1_name"},
			{"umpire2", "umpire2_name"}, {"match_id", "pc_match_id"}, {"match_time", "match_time"}} {
			if pyval.Truthy(result[kv[0]]) {
				updates[kv[1]] = result[kv[0]]
			}
		}
		if pyval.Truthy(result["ground_lat"]) {
			updates["weather_lat"] = result["ground_lat"]
			updates["weather_lon"] = result["ground_lon"]
		}
		if len(updates) > 0 {
			UpdateState(func(st M) {
				for k, v := range updates {
					st[k] = v
				}
			})
			say("  API: auto-filled %v", sortedKeys(updates))
		}
	}
	_, failed := result["error"]
	c.json(M{"ok": !failed, "result": result})
}

func (c *ctx) health() {
	// One-stop health check for the panel strip and the launcher's self-test.
	s := LoadState()
	t := now()
	bridgeURL := trim(gs(s, "pcs_bridge_url", ""))
	agentMode := gs(s, "pcs_source", "local") == "agent"
	pcsDir := EffectivePCSFolder(s)
	pcsPath := ""
	if pcsDir != "" && !agentMode {
		pcsPath = FindPCSOutputFile(pcsDir)
	}
	var pcsAge any
	ageVal := -1.0
	if pcsPath != "" {
		ageVal = t - mtime(pcsPath)
		pcsAge = int(ageVal)
	}
	manual := ManualSessionActive()
	imgExts := []string{".png", ".jpg", ".jpeg", ".webp", ".svg", ".gif"}
	count := func(d string) int {
		entries, err := os.ReadDir(d)
		if err != nil {
			return 0
		}
		n := 0
		for _, e := range entries {
			l := strings.ToLower(e.Name())
			for _, x := range imgExts {
				if strings.HasSuffix(l, x) {
					n++
					break
				}
			}
		}
		return n
	}
	ss := seasonStatsCopy()
	agentMu.Lock()
	agentHasState, lastOK := agentLastState != nil, agentLastOK
	agentMu.Unlock()
	var fresh bool
	switch {
	case manual:
		// A live manual session IS the feed (it outranks the file in /live), so the feed
		// is healthy regardless of the PCS file's age.
		fresh = true
	case agentMode:
		fresh = lastOK != 0 && t-lastOK < 120
	default:
		fresh = pcsPath != "" && ageVal < 120
	}
	pcs := M{"source": valOr(s, "pcs_source", "local"), "fresh": fresh, "manual_scoring": manual}
	if agentMode {
		pcs["folder_set"], pcs["file_found"] = nil, agentHasState
		pcs["agent"] = AgentStatus(s)
	} else {
		pcs["folder_set"], pcs["file_found"] = pcsDir != "", pcsPath != ""
		pcs["agent"] = M{"enabled": false}
	}
	pcs["file"], pcs["age_sec"] = nil, pcsAge
	if pcsPath != "" {
		pcs["file"] = filepath.Base(pcsPath)
	}
	if bridgeURL != "" {
		b := pcsBridgeStatusCopy()
		var ago any
		if ls := gf(b, "last_sync", 0); ls != 0 {
			ago = int(t - ls)
		}
		pcs["bridge"] = M{"enabled": true, "connected": b["connected"], "last_sync_sec_ago": ago,
			"last_error": b["last_error"]}
	} else {
		pcs["bridge"] = M{"enabled": false}
	}
	wd := watchdogStatusCopy()
	var wdAgo any
	if lr := gf(wd, "last_run", 0); lr != 0 {
		wdAgo = int(t - lr)
	}
	fixes := gl(wd, "fixes_applied")
	if len(fixes) > 5 {
		fixes = fixes[len(fixes)-5:]
	}
	if fixes == nil {
		fixes = []any{}
	}
	c.json(M{
		"ok": true, "uptime_sec": int(time.Since(serverStart).Seconds()),
		"demo_mode": pyval.Truthy(s["demo_mode"]),
		"pcs":       pcs,
		"stats": M{"built": pyval.Truthy(ss["built"]), "building": pyval.Truthy(ss["building"]),
			"players": len(uniqueRecords(gm(ss, "lookup"))), "date": ss["date"], "error": ss["error"]},
		"assets": M{"headshots": count(headshotsFolder(s)), "logos": count(logosFolder(s))},
		"keys": M{"anthropic": trim(gs(s, "anthropic_api_key", "")) != "",
			"playcricket": trim(pyStrOr(s["playcricket_api_key"], s["api_token"])) != "",
			"weather":     trim(gs(s, "weather_api_key", "")) != ""},
		"match": M{"home": valOr(s, "home_team", ""), "away": valOr(s, "away_team", ""),
			"badges": pyval.Truthy(s["home_club_id"]) && pyval.Truthy(s["away_club_id"])},
		"watchdog":     M{"last_run_sec_ago": wdAgo, "runs": valOr(wd, "runs", 0), "fixes_applied": fixes},
		"server":       selfMetrics(),
		"thermal":      thermalState(),
		"obs_bitrate":  obsBitrateSanityCheck(s),
		"obs_recovery": obsGuardStatus(),
		"overlay":      overlayWatchStatus(),
		"errors":       recentServerErrors(0),
	})
}

// live is /live (the OVERLAY's poll, which drives the match pipeline: event buffering,
// ball logging, commentary triggers, and it consumes the event buffer) and /live/view
// (the same picture for any OTHER consumer, with none of the side effects — before the
// split, whichever panel poll landed first ate the overlay's wicket events).
func (c *ctx) live(mutate bool) {
	s := LoadState()
	home, away := gsOr(s, "home_team", "Home CC"), gsOr(s, "away_team", "Opposition CC")
	// home_club_id isn't guaranteed numeric: the panel's manual badge picker sets it to a
	// logo FILENAME stem (for clubs with no PlayCricket ID). A bare int() here crashed
	// every /live poll — and with it the whole overlay.
	clubRaw := trim(pyStrOr(s["test_club_id"], s["home_club_id"]))
	clubID := 0
	if isDigits(clubRaw) {
		clubID, _ = strconv.Atoi(clubRaw)
	}
	clubStr := strconv.Itoa(clubID)

	// match_url pins a specific match. Cache maintenance (rebind on pin, rebuild on
	// unpin) is a side effect, so it belongs to the overlay's /live only — a panel poll
	// observing a cleared match_url must not drop the overlay's cached widget state.
	if mutate {
		overlayPolled()
		pcFromURL := 0
		if mu := trim(gs(s, "match_url", "")); mu != "" {
			pcFromURL, _ = strconv.Atoi(extractPCMatchID(mu))
		}
		rvMu.Lock()
		if pcFromURL != 0 {
			if rvCache.pcID != pcFromURL {
				rvCache = rvCacheT{pcID: pcFromURL, clubID: clubStr, pinned: true, initialised: true}
			}
			// A manually entered RV id bypasses the mapping API
			manualRV, _ := strconv.Atoi(trim(gs(s, "rv_match_id", "")))
			if manualRV != 0 && rvCache.rvID == 0 {
				rvCache.rvID = manualRV
				say("  RV ID set manually: %d", manualRV)
			} else if rvCache.rvID == 0 {
				if t := now(); t-rvCache.lastMapAttempt > 300 {
					rvCache.lastMapAttempt = t
					if rv := fetchRVMapping(pcFromURL); rv != 0 {
						rvCache.rvID, rvCache.lastRVPoll = rv, 0
					}
				}
			}
		} else if rvCache.pinned {
			rvCache = rvCacheT{clubID: clubStr, initialised: true}
		}
		rvMu.Unlock()
	}

	if pyval.Truthy(s["api_token"]) && pyval.Truthy(s["match_id"]) {
		u := fmt.Sprintf("https://play-cricket.com/api/v2/match_detail.json?id=%s&site_id=%s&api_token=%s",
			pyval.Str(s["match_id"]), pyval.Str(valOr(s, "site_id", "")), pyval.Str(s["api_token"]))
		data, err := httpGetJSON(u, 10*time.Second, map[string]string{"User-Agent": "CricketStreamOverlay/1.0"})
		if err != nil {
			c.json(M{"source": "api", "error": err.Error(), "data": nil})
		} else {
			c.json(M{"source": "api", "data": data})
		}
		return
	}

	// A live manual session outranks the PCS file; the PCS file outranks the widget.
	// Manual state is rendered through the same parser, so downstream nothing can tell
	// the difference — both report source "pcs" (the overlay's fast-poll signal),
	// distinguished by "feed".
	manualState := ManualLiveState()
	pcsState := manualState
	if pcsState == nil {
		pcsState = ReadScoreSource(s)
		if pcsState != nil && leftoverFeed(s) {
			pcsState = nil // the last match's file: pre-match graphics, nothing logged
		}
	}
	// Agent mode has no local folder to check — "configured" just means the source is
	// switched on; a missing/unreachable agent still reports "pcs" rather than falling
	// through to the widget with the wrong label.
	configured := gs(s, "pcs_source", "local") == "agent" || EffectivePCSFolder(s) != ""
	switch {
	case pcsState != nil:
		pcsState = shallow(pcsState) // the feed reader may hand back its cached map
		pcsState["home_abbrev"] = strings.ToUpper(trim(gs(s, "home_abbrev", "")))
		pcsState["away_abbrev"] = strings.ToUpper(trim(gs(s, "away_abbrev", "")))
		pcsState["winPredictor"] = safeWinPrediction(pcsState, s)
		events := []M{}
		if mutate {
			notePlay(pcsState) // when a quality step up may restart the stream
			// Logging must never interrupt the stream: each step guards itself.
			BufferPCSEvents(pcsState) // advances event detection
			MatchLogSnapshot(pcsState)
			func() {
				dbLogMu.RLock() // a practice's clean-up waits for this poll's writes
				defer dbLogMu.RUnlock()
				FollowMatchID(pcsState)  // an id change mid-match keeps its data
				LogBallData(pcsState)    // our own ball-by-ball database
				LogLiveFigures(pcsState) // the scorer's totals, for the report/card
				LogMoments(pcsState)     // wickets and milestones, for the video's chapters
			}()
			// Hand buffered events to the overlay and clear — under the lock, so an
			// event landing mid-pop can't be silently dropped
			if ev := drainEvents(); ev != nil {
				events = ev
			}
		}
		feed := "file"
		if manualState != nil {
			feed = "manual"
		}
		c.json(M{"source": "pcs", "state": pcsState, "club_id": clubID, "events": events, "feed": feed})
	case configured:
		// The scorer's feed is configured but hasn't written a match yet (pre-match, or
		// between innings) — still report "pcs" so the overlay keeps fast-polling instead
		// of falling back to slow widget polling and a mislabeled source.
		c.json(M{"source": "pcs", "state": nil, "club_id": clubID, "events": []any{}})
	default:
		var state M
		if pyval.Truthy(valOr(s, "use_widget", true)) {
			// Hard 5s limit: a DNS/connection hang must not block the overlay's poll.
			ch := make(chan M, 1)
			safeGo("widget", func() { ch <- GetLiveState(clubStr, home, away) })
			select {
			case state = <-ch:
			case <-time.After(5 * time.Second):
			}
		}
		if state != nil {
			state = shallow(state)
			state["home_abbrev"] = strings.ToUpper(trim(gs(s, "home_abbrev", "")))
			state["away_abbrev"] = strings.ToUpper(trim(gs(s, "away_abbrev", "")))
			c.json(M{"source": "widget", "state": state, "club_id": clubID})
		} else {
			c.json(M{"source": "widget", "state": nil, "club_id": clubID})
		}
	}
}

func safeWinPrediction(state, cfg M) (out any) {
	defer func() {
		if recover() != nil {
			out = nil
		}
	}()
	if p := WinPrediction(state, cfg); p != nil {
		return p
	}
	return nil
}

// constantTimeEqual compares a password without leaking its length through timing.
func constantTimeEqual(a, b string) bool { return hmac.Equal([]byte(a), []byte(b)) }
