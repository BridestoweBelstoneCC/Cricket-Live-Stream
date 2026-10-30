package server

// Pre-match check: one button, run days before a match — does every credential and
// connection actually work? Each check makes the real call (a configured key isn't a
// working key; a silently expired YouTube login is what this exists to catch), runs in
// parallel with its own time limit, and answers ok / warn / bad with what to do. Nothing
// is changed anywhere: every call is a read. Keep ✗ for things wrong NOW: it re-runs
// every 6 hours and a ✗ puts a red badge in the panel, so "expected days before" states
// (cameras away from the ground, OBS closed) must be ⚠.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cricketstream/internal/ai"
	"cricketstream/internal/clubhouse"
	"cricketstream/internal/ini"
	"cricketstream/internal/obs"
	"cricketstream/internal/obsws"
	"cricketstream/internal/pyfmt"
	"cricketstream/internal/pyval"
)

// Swappable for tests: everything that reaches outside the process.
var (
	precheckTimeout    = 20 * time.Second
	precheckYT         = ytService
	precheckDiskFree   = diskFreeBytes
	precheckPortOpen   = func(port int) bool { return obs.PortOpen(port, "", time.Second) }
	precheckDial       = func(addr string) (net.Conn, error) { return net.DialTimeout("tcp", addr, 3*time.Second) }
	precheckProfileINI = func() string { return obs.ActiveProfileINI(obs.ConfigDir()) }
)

const (
	ytTestingWarnDays  = 5 // Testing-mode Google apps' logins last 7 days
	precheckAutoEvery  = 6 * time.Hour
	precheckDiskWarnGB = 10
	precheckDiskBadGB  = 2
)

// Version is this build's version ("" when unknown); set by the binary at link time.
var Version = ""

// CheckResult is (status, detail, fix); status is ok | warn | bad | skip.
type CheckResult struct{ Status, Detail, Fix string }

func chk(s, d, f string) CheckResult { return CheckResult{s, d, f} }

func pcYouTube(st M) CheckResult {
	if !fileExists(YTCredsFile) {
		return chk("warn", "YouTube isn't set up.", "Optional: without it the stream title won't update itself and there are "+
			"no sponsor viewer-minutes. See the YouTube section of the setup guide.")
	}
	yt, msg := precheckYT(false)
	if yt == nil {
		return chk("bad", msg, "Renew the login on the streaming laptop, with the club's Google account.")
	}
	r, err := yt.Get("channels", url.Values{"part": {"snippet"}, "mine": {"true"}})
	if err != nil {
		panic(err)
	}
	it := items(r)
	if len(it) == 0 {
		return chk("bad", "Signed in, but that Google account has no YouTube channel.", "Sign in again with the club's Google account.")
	}
	title := gs(gm(it[0], "snippet"), "title", "")
	if !strings.Contains(strings.ToLower(title), clubFragment(st)) {
		return chk("warn", fmt.Sprintf("Signed in as the channel “%s”.", title),
			"Is that the club's channel? If not, sign in again with the club's account.")
	}
	if signed := gf(st, "youtube_signed_in_at", 0); signed != 0 {
		days := (now() - signed) / 86400
		if days >= ytTestingWarnDays {
			renew := time.Unix(int64(signed+7*86400), 0).Format("Mon 02 Jan")
			return chk("warn", fmt.Sprintf("Signed in as “%s”, %d days ago.", title, int(days)),
				fmt.Sprintf("If the club's Google app is in Testing mode this login stops working "+
					"about %s — sign in again before a match after that. Publishing "+
					"the app (Google Cloud → OAuth consent screen) stops the weekly expiry.", renew))
		}
		return chk("ok", fmt.Sprintf("Signed in as “%s” (%d %s ago).", title, int(days), plural(int(days), "day")), "")
	}
	return chk("ok", fmt.Sprintf("Signed in as “%s”.", title), "")
}

func pcPlayCricket(st M) CheckResult {
	key := trim(gs(st, "playcricket_api_key", ""))
	site := trim(gsOr(st, "home_club_id", ""))
	if key == "" {
		return chk("warn", "No PlayCricket API key.", "Fetch today's match, season stats and result cards need one (Setup).")
	}
	if !isDigits(site) {
		return chk("warn", "Key set, but no club ID.", "Enter the club's PlayCricket ID (Setup).")
	}
	d, err := pcGetJSON(fmt.Sprintf("https://play-cricket.com/api/v2/matches.json?api_token=%s&site_id=%s&season=%d", key, site, time.Now().Year()), 15*time.Second)
	var he *httpError
	if errors.As(err, &he) {
		if he.Code == 401 || he.Code == 403 {
			return chk("bad", "PlayCricket rejected the API key.", "Check the key in Setup, or ask PlayCricket for a new one.")
		}
		return chk("bad", fmt.Sprintf("PlayCricket answered HTTP %d.", he.Code), "Try again later.")
	}
	if err != nil {
		panic(err)
	}
	if _, ok := d["matches"]; !ok {
		return chk("bad", "PlayCricket's answer wasn't a fixture list.", "Check the key and club ID in Setup.")
	}
	return chk("ok", fmt.Sprintf("Key works — %d fixtures this season.", len(gl(d, "matches"))), "")
}

func pcAnthropic(st M) CheckResult {
	key := anthropicKey(st)
	if key == "" {
		return chk("warn", "No Anthropic API key.", "Optional: AI commentary, match reports and captions need one (Setup).")
	}
	err := ai.CheckKey(context.Background(), key) // free: no tokens used
	switch ai.StatusCode(err) {
	case 0:
		if err != nil {
			panic(err)
		}
	case 401:
		return chk("bad", "Anthropic rejected the API key.", "Check the key in Setup.")
	case 403:
		return chk("bad", "The Anthropic key isn't allowed to use the API.",
			"Check the account's billing and key settings at console.anthropic.com.")
	default:
		panic(err)
	}
	return chk("ok", "Key works.", "")
}

func obsIsLocal(st M) bool {
	h := trim(gs(st, "obs_host", "localhost"))
	return h == "localhost" || h == "127.0.0.1" || h == ""
}

func pcOBS(st M) CheckResult {
	port := gi(st, "obs_port", 4455)
	if port == 0 {
		port = 4455
	}
	if obsIsLocal(st) && !precheckPortOpen(port) {
		return chk("warn", "OBS isn't open.", "Fine before match day — on the day, the checklist's Start OBS opens it.")
	}
	res := obsCall(st, []obsws.Req{{Type: "GetVersion"}}, 6*time.Second)
	if res == nil || res[0] == nil {
		return chk("bad", "OBS didn't accept the connection.", "Check the WebSocket password and port in Setup match OBS "+
			"(Tools → WebSocket Server Settings).")
	}
	return chk("ok", strings.ReplaceAll(fmt.Sprintf("Connected to OBS %s.", gs(res[0], "obsVersion", "")), " .", "."), "")
}

func pcScorer(st M) CheckResult {
	fresh, detail := scoreFeedStatus(st, 0)
	if fresh {
		return chk("ok", detail, "")
	}
	if strings.Contains(detail, "No scoreboard folder set") {
		return chk("bad", detail, "Set where the scorer's file comes from (Setup → Scoring source).")
	}
	return chk("warn", detail, "Expected before the match — the scorer's software writes it once they start.")
}

func pcDisk(st M) CheckResult {
	folder := trim(gs(st, "replay_folder", ""))
	if folder == "" {
		folder = defaultReplayFolder()
	}
	probe := folder
	for probe != "" && !isDir(probe) {
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		probe = parent
	}
	free, err := precheckDiskFree(probe)
	if err != nil {
		panic(err)
	}
	gb := float64(free) / 1e9
	where := folder
	if !isDir(folder) {
		where = folder + " (doesn't exist yet — OBS creates it)"
	}
	switch {
	case gb < precheckDiskBadGB:
		return chk("bad", fmt.Sprintf("Only %s GB free for replays in %s.", pyfmt.Fixed(gb, 1), where),
			"Free up space: replays and the highlights reel need several GB a match.")
	case gb < precheckDiskWarnGB:
		return chk("warn", fmt.Sprintf("%s GB free for replays in %s.", pyfmt.Fixed(gb, 1), where),
			"Probably enough for one match; worth clearing old clips.")
	}
	return chk("ok", fmt.Sprintf("%s GB free for replays in %s.", pyfmt.Fixed(gb, 0), where), "")
}

// targetBitrate is the bitrate the launcher will put OBS on, by OBS setup's rule:
// config.ini [Stream] bitrate_kbps if a number, nothing if "manual", else the last upload
// test's recommendation. (kbps or 0, why).
func targetBitrate(st M) (int, string) {
	f, _ := ini.Load(ConfigFile)
	raw := trim(f.Get("Stream", "bitrate_kbps", ""))
	switch strings.ToLower(raw) {
	case "manual", "off", "no":
		return 0, "manual"
	}
	if n, err := strconv.Atoi(raw); err == nil && isDigits(raw) && n != 0 {
		return n, "config"
	}
	if mbps := gf(st, "network_test_mbps", 0); mbps != 0 {
		return obs.RecommendStreamSettings(mbps).BitrateKbps, "test"
	}
	return 0, "none"
}

// pcOBSBitrate catches a leftover quality-ladder downshift: the ladder writes the lower
// bitrate into OBS's profile, where it survives the match (it bit twice). Read from OBS's
// own settings file, so it works days before with OBS closed; the live monitor's view is
// used instead when OBS is open (OBS rewrites the file on exit).
func pcOBSBitrate(st M) CheckResult {
	want, why := targetBitrate(st)
	if why == "manual" {
		return chk("skip", "Bitrate is set by hand (config.ini: bitrate_kbps = manual).", "")
	}
	streamMonMu.Lock()
	configured := 0
	if gb(streamMon, "reachable", false) {
		configured = gi(streamMon, "configured_kbps", 0)
	}
	streamMonMu.Unlock()
	if configured == 0 {
		p := precheckProfileINI()
		if p == "" {
			return chk("skip", "OBS hasn't been set up on this laptop yet.", "")
		}
		if mode, _ := obs.ReadINIValue(p, "Output", "Mode"); mode != "" && mode != "Simple" {
			return chk("skip", "OBS is in Advanced output mode — bitrate is managed there.", "")
		}
		if raw, ok := obs.ReadINIValue(p, "SimpleOutput", "VBitrate"); ok && isDigits(raw) {
			configured, _ = strconv.Atoi(raw)
		}
	}
	if configured == 0 || want == 0 {
		return chk("skip", "No bitrate to compare against yet.", "")
	}
	if float64(configured) < float64(want)*0.8 {
		return chk("bad", fmt.Sprintf("OBS is set to %d kbps, not the usual %d.", configured, want),
			"Looks like a quality downshift left over from a match. Quickstart resets it "+
				"when it starts OBS; or set it in OBS → Settings → Output.")
	}
	if mbps := gf(st, "network_test_mbps", 0); mbps != 0 && float64(configured) > mbps*1000*0.8 {
		return chk("warn", fmt.Sprintf("OBS is set to %d kbps — more than your last upload test (%s Mbps) can carry.", configured, pyfmt.Fixed(mbps, 1)),
			fmt.Sprintf("Quickstart sets it to %d kbps when it starts OBS for a match; if you "+
				"start OBS yourself, set it in OBS → Settings → Output.", want))
	}
	return chk("ok", fmt.Sprintf("OBS is set to %d kbps.", configured), "")
}

// pcCameras: can this laptop reach each camera? A TCP connect to its RTSP port — the
// address and password stay out of the report (they're in the URL).
func pcCameras(st M) CheckResult {
	type cam struct{ name, url string }
	var cams []cam
	for _, c := range []cam{{"Camera", gs(st, "camera_rtsp_url", "")}, {"Bowler-end camera", gs(st, "camera2_rtsp_url", "")}} {
		if trim(c.url) != "" {
			cams = append(cams, cam{c.name, trim(c.url)})
		}
	}
	if len(cams) == 0 {
		return chk("skip", "No camera addresses set.", "Fine if the cameras were added in OBS directly.")
	}
	var down []string
	for _, c := range cams {
		u, err := url.Parse(c.url)
		if err != nil || u.Hostname() == "" {
			down = append(down, c.name)
			continue
		}
		port := u.Port()
		if port == "" {
			port = "554"
		}
		conn, err := precheckDial(net.JoinHostPort(u.Hostname(), port))
		if err != nil {
			down = append(down, c.name)
			continue
		}
		conn.Close()
	}
	if len(down) > 0 {
		// ⚠, not ✗: days before a match the cameras are at the ground and this laptop
		// usually isn't — a ✗ would keep the panel's badge red all week.
		return chk("warn", strings.Join(down, " and ")+" not answering.",
			"Expected away from the ground. On match day: is it powered on and on the same network as this laptop?")
	}
	s := ""
	if len(cams) > 1 {
		s = "s"
	}
	return chk("ok", fmt.Sprintf("%d camera%s answering.", len(cams), s), "")
}

// pcSurnames: two players sharing a surname. The scorer's feed only gives surnames, so
// season stats are withheld for both unless the roster's shirt numbers tell them apart.
func pcSurnames(st M) CheckResult {
	seasonStatsMu.Lock()
	lookup := gm(seasonStats, "lookup")
	seasonStatsMu.Unlock()
	if len(lookup) == 0 {
		return chk("skip", "Season stats aren't loaded yet.", "Match day → Refresh season stats from PlayCricket, then run this again.")
	}
	names := map[string]bool{}
	for _, v := range lookup {
		if r, ok := v.(map[string]any); ok {
			names[gs(r, "name", "")] = true
		}
	}
	letters := regexp.MustCompile(`[^A-Za-z ]`)
	bySurname := map[string]map[string]bool{}
	for n := range names {
		toks := strings.Fields(letters.ReplaceAllString(n, " "))
		if len(toks) >= 2 {
			k := strings.ToLower(toks[len(toks)-1])
			if bySurname[k] == nil {
				bySurname[k] = map[string]bool{}
			}
			bySurname[k][n] = true
		}
	}
	rostered := map[string]bool{}
	for _, v := range gm(st, "roster") {
		rostered[strings.ToLower(trim(pyval.Str(v)))] = true
	}
	var clashes []string
	keys := make([]string, 0, len(bySurname))
	for k := range bySurname {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		set := bySurname[k]
		if len(set) < 2 {
			continue
		}
		all := true
		var ns []string
		for n := range set {
			ns = append(ns, n)
			all = all && rostered[strings.ToLower(n)]
		}
		if !all {
			sort.Strings(ns)
			clashes = append(clashes, strings.Join(ns, ", "))
		}
	}
	if len(clashes) == 0 {
		return chk("ok", "No unresolved surname clashes.", "")
	}
	shown := clashes
	more := ""
	if len(shown) > 3 {
		shown, more = shown[:3], " …"
	}
	return chk("warn", "Players sharing a surname: "+strings.Join(shown, "; ")+more+".",
		"Their season stats won't show on player cards until each is in the Squad "+
			"Roster (Setup) with a shirt number. Only matters for your own players.")
}

func pcFixture(st M) CheckResult {
	key := trim(gs(st, "playcricket_api_key", ""))
	site := trim(gsOr(st, "home_club_id", ""))
	if key == "" || !isDigits(site) {
		return chk("skip", "Needs the PlayCricket key and club ID.", "")
	}
	r := FetchTodaysMatch(key, site)
	if e := gs(r, "error", ""); e != "" {
		return chk("skip", e+".", "On match morning this checks today's fixture is found.")
	}
	opp := gsOr(r, "away_club", gsOr(r, "away_team", "the opposition"))
	comp := ""
	if c := gs(r, "competition", ""); c != "" {
		comp = " (" + c + ")"
	}
	return chk("ok", "Today: v "+opp+comp+".", "")
}

func pcBadge(st M) CheckResult {
	cid := trim(gsOr(st, "away_club_id", ""))
	opp := gsOr(st, "away_team", "the opposition")
	if !isDigits(cid) {
		return chk("skip", "No opposition club ID yet.", "Fetch today's match fills it in on match morning.")
	}
	if badgeFile(logosFolder(st), cid) != "" {
		return chk("ok", fmt.Sprintf("Badge found for %s.", opp), "")
	}
	return chk("warn", fmt.Sprintf("No badge for %s (logos/%s.png).", opp, cid),
		"The scorebar and result card will show initials instead. See CLUB_LOGOS.md.")
}

func pcUpload(st M) CheckResult {
	mbps, at := gf(st, "network_test_mbps", 0), gf(st, "network_test_at", 0)
	if mbps == 0 {
		return chk("skip", "No upload-speed test yet.", "Run one at the ground: Setup → Stream health → Check now.")
	}
	days := (now() - at) / 86400
	want, _ := targetBitrate(st)
	if want == 0 {
		want = obs.RecommendStreamSettings(mbps).BitrateKbps
	}
	if float64(want) > mbps*1000*0.8 {
		return chk("bad", fmt.Sprintf("Last upload test: %s Mbps — too slow for %d kbps.", pyfmt.Fixed(mbps, 1), want),
			"Lower the bitrate, or re-test at the ground.")
	}
	note := fmt.Sprintf("%s Mbps upload, %d %s ago — enough for %d kbps.", pyfmt.Fixed(mbps, 1), int(days), plural(int(days), "day"), want)
	if days > 7 {
		return chk("warn", note, "That test is old — run a fresh one at the ground on the day.")
	}
	return chk("ok", note, "")
}

// obsSignedInToYouTube: OBS is streaming through "Connect account" rather than a stream
// key. OBS records the sign-in in the profile's basic.ini ([Auth] Type=YouTube - RTMPS).
func obsSignedInToYouTube() bool {
	p := obs.ActiveProfileINI(obs.ConfigDir())
	if p == "" {
		return false
	}
	v, _ := obs.ReadINIValue(p, "Auth", "Type")
	return strings.Contains(strings.ToLower(v), "youtube")
}

// pcAutoQuality: auto quality scaling is off by default, because a quality step stops
// and restarts the stream, and with OBS signed in to YouTube ("Connect account") that
// ENDS the broadcast. So rather than switching it on for everyone, this says when a club
// should (an upload with little room over the stream's bitrate, streaming with a key),
// and warns a club that has it on in the mode where it would end their broadcast.
func pcAutoQuality(st M) CheckResult {
	auto, signedIn := gb(st, "stream_auto_downshift", false), obsSignedInToYouTube()
	keyFix := "Stream with a stream key instead (YouTube Studio → copy the stream key → OBS Settings → Stream), " +
		"or switch Auto quality scaling off (Match day → Stream quality)."
	if auto && signedIn {
		return chk("warn", "Auto quality scaling is on, but OBS is signed in to YouTube (\"Connect account\"): "+
			"a quality step stops the stream, and in that mode stopping it ends the broadcast.", keyFix)
	}
	if auto {
		return chk("ok", "On — steps the stream down when the line struggles, and back up when it recovers.", "")
	}
	mbps := gf(st, "network_test_mbps", 0)
	want, _ := targetBitrate(st)
	if want == 0 && mbps > 0 {
		want = obs.RecommendStreamSettings(mbps).BitrateKbps
	}
	if mbps > 0 && want > 0 && mbps*1000 < 2*float64(want) && !signedIn {
		return chk("warn", fmt.Sprintf("Off, and the last upload test (%s Mbps) leaves little room over the stream's %d kbps.",
			pyfmt.Fixed(mbps, 1), want), "Switch on Auto quality scaling (Match day → Stream quality).")
	}
	return chk("skip", "Off.", "")
}

var versionNums = regexp.MustCompile(`\d+`)

func versionTuple(v string) []int {
	var out []int
	for _, m := range versionNums.FindAllString(v, -1) {
		n, _ := strconv.Atoi(m)
		out = append(out, n)
	}
	return out
}

func versionNewer(a, b string) bool {
	// A pre-release ("3.0.0-beta.1") comes BEFORE its release: read as one run of numbers
	// it was 3.0.0.1, newer than 3.0.0, so a beta tester was never told the release was
	// out. The Python original never tagged pre-releases, so its tuple compare didn't care.
	ac, ap, _ := strings.Cut(a, "-")
	bc, bp, _ := strings.Cut(b, "-")
	if newer, same := tupleNewer(versionTuple(ac), versionTuple(bc)); !same {
		return newer
	}
	switch {
	case ap == "" || bp == "":
		return ap == "" && bp != "" // the release beats its pre-releases
	}
	newer, _ := tupleNewer(versionTuple(ap), versionTuple(bp))
	return newer
}

// tupleNewer compares number runs; same reports a tie (3.0 and 3.0.0 count as one).
func tupleNewer(x, y []int) (newer, same bool) {
	for i := 0; i < len(x) || i < len(y); i++ {
		var p, q int
		if i < len(x) {
			p = x[i]
		}
		if i < len(y) {
			q = y[i]
		}
		if p != q {
			return p > q, false
		}
	}
	return false, true
}

// releasesURL is where the newest release is announced.
var releasesURL = "https://api.github.com/repos/BridestoweBelstoneCC/Cricket-Live-Stream/releases/latest"

func pcUpdate(st M) CheckResult {
	have := Version
	if have == "" {
		return chk("skip", "Couldn't tell which version this is.", "")
	}
	d, err := httpGetJSON(releasesURL, 8*time.Second, map[string]string{"User-Agent": "CricketStream-precheck"})
	if err != nil {
		panic(err)
	}
	latest := strings.TrimPrefix(gs(d, "tag_name", ""), "v")
	if strings.Contains(have, "-") {
		// A beta: releases/latest is never a pre-release, so the next beta is in the list.
		latest = newestListed(have, latest)
	}
	if latest != "" && versionNewer(latest, have) {
		return chk("warn", fmt.Sprintf("Version %s is out — this laptop has %s.", latest, have),
			"Press Update now in the CricketStream window (Windows), or download it from the GitHub Releases page.")
	}
	return chk("ok", fmt.Sprintf("Up to date (v%s).", have), "")
}

// newestListed is the newest of the recent releases (betas included, drafts not) and
// latest, as long as it's newer than have.
func newestListed(have, latest string) string {
	req, _ := http.NewRequest("GET", strings.TrimSuffix(releasesURL, "/latest")+"?per_page=20", nil)
	req.Header.Set("User-Agent", "CricketStream-precheck")
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return latest
	}
	defer resp.Body.Close()
	var rels []struct {
		Tag   string `json:"tag_name"`
		Draft bool   `json:"draft"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rels) != nil {
		return latest
	}
	best := latest
	if best == "" || !versionNewer(best, have) {
		best = have
	}
	for _, r := range rels {
		if v := strings.TrimPrefix(r.Tag, "v"); !r.Draft && v != "" && versionNewer(v, best) {
			best = v
		}
	}
	return best
}

// pcScorerLink: two-laptop setups — is the scorer's machine reachable?
func pcScorerLink(st M) CheckResult {
	if gs(st, "pcs_source", "local") == "agent" {
		state := ReadAgentFile(st)
		a := AgentStatus(st)
		if state != nil || gb(a, "connected", false) {
			return chk("ok", fmt.Sprintf("Scorer's laptop answering at %s.", orStr(gs(a, "address", ""), "its address")), "")
		}
		return chk("bad", "Can't reach the scorer's laptop.", "Is the scorer agent running on it, on the same wifi? "+
			"Setup → Find scorer laptop, or type its address.")
	}
	if u := strings.TrimRight(trim(gs(st, "pcs_bridge_url", "")), "/"); u != "" {
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(u + "/pcs/ping")
		if err == nil {
			resp.Body.Close()
		}
		if err != nil || resp.StatusCode >= 400 {
			return chk("bad", "The NV Play bridge isn't answering.", "Is the bridge running on the scorer's machine, and Tailscale "+
				"connected on both? See BRIDGE.md.")
		}
		return chk("ok", "NV Play bridge answering.", "")
	}
	return chk("skip", "Scoring on this laptop (no second laptop to check).", "")
}

// pcClubhouse: can the clubhouse screen switch this laptop on? Only when the screen is
// switched on ([Clubhouse] enabled); ⚠ at worst — a laptop on wifi at home days before
// the match isn't wrong, and this runs every six hours (a ✗ would sit red all week).
func pcClubhouse(M) CheckResult {
	cfg, err := ini.Load(ConfigFile)
	if err != nil || !cfg.GetBool("Clubhouse", "enabled", false) {
		return chk("skip", "No clubhouse screen.", "")
	}
	return chk(clubhouse.WakeAdvice(clubhouse.CheckWake()))
}

// Prechecks is the list, in display order. Add new external dependencies here; a check
// may panic — that's reported as "bad", never a crash.
var Prechecks = []struct {
	ID, Name string
	Fn       func(M) CheckResult
}{
	{"youtube", "YouTube", pcYouTube},
	{"playcricket", "PlayCricket", pcPlayCricket},
	{"anthropic", "AI (Anthropic)", pcAnthropic},
	{"obs", "OBS", pcOBS},
	{"scorer", "Scorer feed", pcScorer},
	{"scorer_link", "Scorer's laptop", pcScorerLink},
	{"disk", "Disk space", pcDisk},
	{"obs_bitrate", "Stream bitrate", pcOBSBitrate},
	{"upload", "Upload speed", pcUpload},
	{"auto_quality", "Auto quality", pcAutoQuality},
	{"cameras", "Cameras", pcCameras},
	{"fixture", "Today's fixture", pcFixture},
	{"badge", "Opposition badge", pcBadge},
	{"surnames", "Player names", pcSurnames},
	{"update", "Software version", pcUpdate},
	{"clubhouse", "Clubhouse screen", pcClubhouse},
}

var (
	precheckLast   M
	precheckLastMu sync.Mutex
)

// RunPrecheck runs every check in parallel. A check that fails or runs past its time
// limit becomes "bad" — one broken service never hides the others' answers.
func RunPrecheck() M {
	st := LoadState()
	type res struct {
		i int
		r CheckResult
	}
	ch := make(chan res, len(Prechecks))
	for i, c := range Prechecks {
		go func(i int, fn func(M) CheckResult) {
			var r CheckResult
			func() {
				defer func() {
					if p := recover(); p != nil {
						r = chk("bad", "Check failed: "+truncRunes(fmt.Sprint(p), 200), "")
					}
				}()
				r = fn(st)
			}()
			ch <- res{i, r}
		}(i, c.Fn)
	}
	results := make([]*CheckResult, len(Prechecks))
	deadline := time.After(precheckTimeout)
collect:
	for got := 0; got < len(Prechecks); got++ {
		select {
		case r := <-ch:
			rr := r.r
			results[r.i] = &rr
		case <-deadline:
			break collect // a hung call can finish in the background
		}
	}
	out := []any{}
	ok := true
	for i, c := range Prechecks {
		r := results[i]
		if r == nil {
			r = &CheckResult{"bad", fmt.Sprintf("No answer within %ss.", strconv.FormatFloat(precheckTimeout.Seconds(), 'f', -1, 64)),
				"Check the internet connection, then run it again."}
		}
		if r.Status == "bad" {
			ok = false
		}
		out = append(out, M{"id": c.ID, "name": c.Name, "status": r.Status, "detail": r.Detail, "fix": r.Fix})
	}
	result := M{"ok": ok, "checks": out, "ran_at": nowISO()}
	precheckLastMu.Lock()
	precheckLast = result
	precheckLastMu.Unlock()
	return result
}

// StartPrecheckLoop runs the check shortly after start and every 6 hours, so the panel
// can flag problems without anyone pressing the button.
func StartPrecheckLoop() {
	go func() {
		time.Sleep(30 * time.Second) // let the server and OBS settle first
		loop("Pre-match check", precheckAutoEvery, func() { RunPrecheck() })
	}()
}

// VersionNewer reports whether version a is newer than b (numeric parts compared).
func VersionNewer(a, b string) bool { return versionNewer(a, b) }
