package server

// Commentary from a phone. The clubhouse screen's Commentary tab switches it on and shows
// a QR code; someone on the boundary scans it, gives their name and asks to go on air;
// the operator says yes on the screen, and their phone's microphone goes into the stream.
//
//	phone ──HTTPS chunks──▶ this server ──WebSocket──▶ OBS "Commentary" browser source
//
// Why it's built this way:
//   - A phone only hands a web page its microphone on HTTPS, and the panel is plain HTTP.
//     So while commentary is on there's a second listener, with a self-signed certificate
//     (talktls.go), serving the phone's routes and NOTHING else: a phone that has clicked
//     through the certificate warning can't reach the panel's routes through it. With the
//     Cloudflare tunnel running, the QR code uses that instead (a real certificate, no
//     warning, works on mobile data), so the phone's routes are on the main port too.
//   - Audio goes up as small POSTs, not a WebSocket: iOS Safari has refused wss:// to a
//     self-signed certificate even after the page itself was accepted; fetch follows the
//     page's exception.
//   - It reaches OBS through its own browser source with "control audio via OBS", in every
//     scene (a source not in the live scene goes silent) — never through the Overlay page,
//     so nothing here can get in the way of the scorebar, and the mixer has its own fader.
//   - It's off at every normal server start and the key in the QR code is new each time it's
//     switched on, so a photo of last week's code does nothing. Nobody is heard until the
//     operator says yes: this puts a stranger's voice on a public stream.
//   - Volume and mute are applied here, to the samples, not on OBS's fader: instant, working
//     with OBS closed, and nothing left turned down in OBS for next week's match. A muted
//     voice never reaches OBS at all, so the ground's sound comes back under it.
//   - A phone that goes quiet (locked, a call, no signal) comes off the strap after 20s, but
//     keeps its place on air for two minutes, so a blip doesn't need the operator again.
//   - A server the launcher restarts after a crash picks up where it was (talk_session.json,
//     deleted on a clean stop): the same QR code, and whoever was on air still on.
//   - /talk/out is the same trap as /live in reverse: loopback only, never on the
//     clubhouse's allowlist.

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gorilla/websocket"

	"cricketstream/internal/assets"
	"cricketstream/internal/obsws"
)

const (
	TalkSource        = "Commentary"      // the OBS source
	talkDuckFilter    = "Commentary duck" // the sidechain compressor on each camera's sound
	talkPortEnv       = "CRICKETSTREAM_TALK_PORT"
	talkLocalIPHeader = "X-Talk-Local-Ip" // from the clubhouse link: the card the screen came in on
	talkPortDef       = 5095
	talkNameMax       = 24
	talkMaxWaiting    = 6
	talkChunkMax      = 32 << 10 // ~0.7s of 24 kHz 16-bit mono; the page sends 0.15s at a time
	talkMaxPending    = 2        // chunks held for one that may arrive out of order
	talkSeqRestart    = 16       // this far behind, the page has started counting again
	talkGainStep      = 3        // dB a press; about as little as an ear notices
	talkGainMin       = -24
	talkGainMax       = 12
)

// How long a phone may say nothing before it's gone (a locked phone, no signal). Vars for
// the tests. talkGoneFor outlasts a replay's break in the wifi without keeping a dead
// strap on air for long; a finished phone's record stays so it can read why it finished.
var (
	talkGoneFor    = 20 * time.Second // on air but silent: off the strap, place kept
	talkLostFor    = 2 * time.Minute  // and then off air
	talkWaitGone   = 30 * time.Second // asking, and no longer answering
	talkMicGone    = 8 * time.Second  // on air, polling, but no audio: a dead mic, so off the strap
	talkForgetFor  = time.Minute
	talkSaveEvery  = 30 * time.Second // talk_session.json touched this often: it's how a restart knows how long it's been
	talkSessionFor = 5 * time.Minute  // a restart picks up after this long at most; the launcher's take seconds, a hung server's about a minute
	talkOBSEvery   = 15 * time.Second // source and filters re-checked: this often till ready, then half as
	talkReloadWait = 15 * time.Second // the page has this long to connect before a reload
)

type talkState string

const (
	talkWaiting talkState = "waiting"
	talkOnAir   talkState = "on_air"
	talkRefused talkState = "refused"
	talkEnded   talkState = "ended"
)

type talkPhone struct {
	id, secret, name string
	state            talkState
	why              string // why it ended, for the phone to say
	joined, seen     time.Time
	onAirAt, lastPCM time.Time
	micFrom          time.Time // when audio could first be expected: put on air, or a restart
	level            int       // the last chunk's peak, 0-100, for the screen's meter
	gainDB           int       // the volume: 0 is as the phone sends it
	muted            bool      // by the clubhouse
	phoneMuted       bool      // by the phone, which then sends no audio: its polls say so, or it'd look like a dead mic
	nextSeq          int64
	started          bool
	held             map[int64][]byte
	heldAt           time.Time
}

// talkOut is one OBS page listening.
type talkOut struct {
	send    chan []byte
	playing bool // its AudioContext is running: a suspended one would be silent
}

var talk = struct {
	sync.Mutex
	on        bool
	key       string
	phones    map[string]*talkPhone
	srv       *http.Server
	port      int
	listenErr string
	outs      map[*talkOut]bool
	onAt      time.Time
	lastOut   time.Time // the page's last connection or disconnection
	reloadAt  time.Time
	reloads   int
	obsAt     time.Time
	obsReady  bool
	obsNote   string
	obsBusy   bool
	localIP   string // the card the clubhouse screen reached this laptop on
	chosenIP  string // the card the operator picked for the QR code, which beats any guess
	savedAt   time.Time
}{phones: map[string]*talkPhone{}, outs: map[*talkOut]bool{}}

// talkLANIP is the address in the QR code. Swappable for tests.
var talkLANIP = func() string {
	if ip := lanIP(); ip != "" {
		return ip
	}
	// No route to the internet is normal at a ground: any private address still reaches a
	// phone on the club's own wifi.
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && n.IP.IsPrivate() {
			return n.IP.String()
		}
	}
	return ""
}

// talkNoteLocalIP is the address the screen came in on, from the link. A laptop can have
// several (VirtualBox, a VPN, cameras on a cable of their own), and the route to the
// internet is only a guess at which one phones can reach: the card the club's own screen
// reached it on is the club's network.
func talkNoteLocalIP(s string) {
	ip := net.ParseIP(strings.TrimSpace(s))
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || !talkIsLocal(ip) {
		return
	}
	talk.Lock()
	talk.localIP = ip.String()
	talk.Unlock()
}

func talkIsLocal(ip net.IP) bool {
	for _, l := range talkLocalIPs() {
		if l.Equal(ip) {
			return true
		}
	}
	return false
}

// talkCard is one address a phone might reach this laptop on, named for the operator.
type talkCard struct{ IP, Name string }

// talkCards is the laptop's network cards, for the operator to choose from. Swappable for
// tests. Only real ones on a private network: a self-assigned address has no network behind
// it, and offering "vEthernet (WSL (Hyper-V firewall))" to a volunteer with a queue of
// phones, as the first version did, is no help at all.
var talkCards = func() []talkCard {
	var out []talkCard
	ifaces, _ := net.Interfaces()
	for _, i := range ifaces {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 || talkCardIsVirtual(i.Name, i.HardwareAddr) {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && n.IP.IsPrivate() {
				out = append(out, talkCard{n.IP.String(), i.Name})
			}
		}
	}
	return out
}

// talkCardList is talkCards, kept for a few seconds: reading them takes about 30ms on
// Windows, and the tab asks every second on the laptop that's encoding the stream.
func talkCardList() []talkCard {
	talkCardCache.Lock()
	defer talkCardCache.Unlock()
	if time.Since(talkCardCache.at) > 5*time.Second {
		talkCardCache.list, talkCardCache.at = talkCards(), time.Now()
	}
	return talkCardCache.list
}

func talkForgetCards() {
	talkCardCache.Lock()
	talkCardCache.at = time.Time{}
	talkCardCache.Unlock()
}

var talkCardCache struct {
	sync.Mutex
	at   time.Time
	list []talkCard
}

// The start of a hardware address says who made the card, and these only make virtual ones:
// VirtualBox, Hyper-V, VMware, Parallels, Docker, TAP. Windows calls VirtualBox's adapter
// "Ethernet 3", so the name alone doesn't catch it.
var talkVirtualMACs = []string{"0a:00:27", "08:00:27", "00:15:5d", "00:50:56", "00:0c:29", "00:05:69", "00:1c:42", "02:42", "00:ff"}

// talkCardIsVirtual: an adapter that exists only inside this laptop, or a VPN's. No phone at
// the ground is on the other side of one.
func talkCardIsVirtual(name string, mac net.HardwareAddr) bool {
	for _, p := range talkVirtualMACs {
		if strings.HasPrefix(mac.String(), p) {
			return true
		}
	}
	n := strings.ToLower(name)
	for _, s := range []string{"vethernet", "virtualbox", "vboxnet", "vmware", "vmnet", "wsl", "hyper-v", "docker", "veth", "virbr",
		"tailscale", "zerotier", "wireguard", "vpn", "bluetooth", "loopback", "awdl", "llw", "utun"} {
		if strings.Contains(n, s) {
			return true
		}
	}
	for _, p := range []string{"tun", "tap", "wg", "br-"} {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

func talkHasCard(ip string) bool {
	for _, c := range talkCardList() {
		if c.IP == ip {
			return true
		}
	}
	return false
}

// talkWantIP is the address to put in the QR code, checked again every time: DHCP moves.
// The screen's card is usually the club's network, but not always: the guide has the laptop
// on the cameras' switch, and a Pi on that cable would point phones on the wifi at a network
// they can't reach. So the operator can choose, and that's believed over any guess.
func talkWantIP() string {
	talk.Lock()
	chosen, hint := talk.chosenIP, talk.localIP
	talk.Unlock()
	if chosen != "" && talkHasCard(chosen) {
		return chosen
	}
	if ip := net.ParseIP(hint); ip != nil && talkIsLocal(ip) {
		return hint
	}
	return talkLANIP()
}

func talkPort() int {
	if v := strings.TrimSpace(os.Getenv(talkPortEnv)); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p >= 0 {
			return p
		}
	}
	return talkPortDef
}

func talkRandom(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// talkCleanName is what's shown on the screen and on air: someone else's text on a public
// stream, so no control or direction characters and a length the strap can hold.
func talkCleanName(s string) string {
	var b strings.Builder
	n, space := 0, false
	for _, r := range strings.TrimSpace(s) {
		if unicode.IsSpace(r) { // first: a newline is a control character too
			space = true
			continue
		}
		if unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		if space && n > 0 {
			if n+1 >= talkNameMax {
				break
			}
			b.WriteRune(' ')
			n++
		}
		space = false
		if n >= talkNameMax {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// ── Switching it on and off ──

// TalkOn opens the door: a new key, the phone's listener, and OBS got ready. Pressing it
// again while it's on changes nothing, so the QR code on the screen stays good.
func TalkOn() {
	talk.Lock()
	if talk.on {
		talk.Unlock()
		return
	}
	talk.on, talk.key, talk.onAt = true, talkRandom(16), time.Now()
	talk.phones = map[string]*talkPhone{}
	talk.listenErr, talk.obsReady, talk.obsNote = "", false, "Getting OBS ready…"
	talk.obsAt, talk.reloadAt, talk.reloads = time.Time{}, time.Time{}, 0
	talkSaveLocked()
	talk.Unlock()
	if err := talkListen(); err != nil {
		talk.Lock()
		talk.listenErr = err.Error()
		talk.Unlock()
		say("  ⚠  Commentary: %v", err)
	}
	say("  🎙  Commentary from a phone: on")
	talkTick()
}

// TalkOff closes the door and takes whoever's on air off it.
func TalkOff() {
	talk.Lock()
	was := talk.on
	talk.on, talk.key = false, ""
	talk.phones = map[string]*talkPhone{}
	srv := talk.srv
	talk.srv, talk.port, talk.listenErr = nil, 0, ""
	talkSaveLocked()
	talk.Unlock()
	if srv != nil {
		srv.Close()
	}
	if was {
		say("  🎙  Commentary from a phone: off")
	}
}

// talkListen opens the HTTPS port on every network card. On purpose, even when the panel
// is "Just this laptop": the operator has just asked for phones, and this port serves
// only the phone's routes behind a key that changes every time.
func talkListen() error {
	cfg, err := talkTLSConfig()
	if err != nil {
		return fmt.Errorf("couldn't make the phone page's certificate: %v", err)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("0.0.0.0", strconv.Itoa(talkPort())))
	if err != nil {
		return fmt.Errorf("port %d is in use, so phones can't connect: %v", talkPort(), err)
	}
	// Time limits: a phone that walks out of range mid-chunk mustn't hold anything open.
	srv := &http.Server{Handler: talkPhoneHandler{}, TLSConfig: cfg, ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 20 * time.Second, WriteTimeout: 20 * time.Second,
		IdleTimeout: time.Minute, ErrorLog: quietLog} // every phone's warning page is a failed handshake: not news
	talk.Lock()
	if !talk.on { // switched off while this was opening
		talk.Unlock()
		ln.Close()
		return nil
	}
	talk.srv, talk.port, talk.listenErr = srv, ln.Addr().(*net.TCPAddr).Port, ""
	talk.Unlock()
	go srv.ServeTLS(ln, "", "")
	return nil
}

// talkURL is what the QR code opens, and which way it goes. The tunnel first: a real
// certificate means no warning on the phone, and it works on mobile data.
func talkURL() (string, string) {
	talk.Lock()
	key, port, on := talk.key, talk.port, talk.on
	talk.Unlock()
	if !on {
		return "", ""
	}
	cfMu.Lock()
	cf := cfURL
	cfMu.Unlock()
	if cf != "" {
		return cf + "/talk?k=" + key, "cloudflare"
	}
	ip := talkWantIP()
	if ip == "" || port == 0 {
		return "", ""
	}
	return fmt.Sprintf("https://%s/talk?k=%s", net.JoinHostPort(ip, strconv.Itoa(port)), key), "lan"
}

// TalkStatus is the screen's Commentary tab. It holds the key (in the URL): token-gated,
// and only for a screen that's unlocked.
func TalkStatus() M {
	u, via := talkURL()
	addresses := []M{}
	address := ""
	if via == "lan" { // the tunnel's address works from anywhere: nothing to choose
		address = talkWantIP()
		for _, c := range talkCardList() {
			addresses = append(addresses, M{"ip": c.IP, "card": c.Name})
		}
	}
	talk.Lock()
	defer talk.Unlock()
	waiting := []M{}
	var onAir any
	t := time.Now()
	for _, p := range talk.phones {
		switch p.state {
		case talkWaiting:
			waiting = append(waiting, M{"id": p.id, "name": p.name, "joined": p.joined.Unix()})
		case talkOnAir:
			level := 0
			hearing := t.Sub(p.lastPCM) < 2*time.Second
			if hearing {
				level = p.level
			}
			onAir = M{"id": p.id, "name": p.name, "for_sec": int(t.Sub(p.onAirAt).Seconds()), "hearing": hearing, "level": level,
				"lost":    p.lost(t),
				"gain_db": p.gainDB, "muted": p.muted, "phone_muted": p.phoneMuted, "can_louder": p.gainDB < talkGainMax, "can_quieter": p.gainDB > talkGainMin}
		}
	}
	// Longest wait first, whatever order the map gives.
	for i := 1; i < len(waiting); i++ {
		for j := i; j > 0 && waiting[j]["joined"].(int64) < waiting[j-1]["joined"].(int64); j-- {
			waiting[j], waiting[j-1] = waiting[j-1], waiting[j]
		}
	}
	playing := false
	for o := range talk.outs {
		playing = playing || o.playing
	}
	note := talk.obsNote
	if talk.obsReady && !playing {
		note = "OBS is loading the commentary…"
	}
	return M{"ok": true, "on": talk.on, "url": u, "via": via, "cert_warning": via == "lan",
		"address": address, "addresses": addresses, "error": talk.listenErr, "waiting": waiting, "on_air": onAir,
		"obs": M{"ready": talk.obsReady && playing, "note": note}}
}

// TalkOnAir is all the overlay and a locked screen get: no key, and nothing to act on.
func TalkOnAir() M {
	talk.Lock()
	defer talk.Unlock()
	out := M{"on": talk.on, "on_air": false, "name": "", "waiting": 0}
	n := 0
	for _, p := range talk.phones {
		switch p.state {
		case talkWaiting:
			n++
		case talkOnAir:
			// A phone that's gone quiet keeps its place, but viewers shouldn't be told
			// someone's commentating when nobody can hear them. A mute keeps the strap: it's
			// usually a cough or a word with someone, and the strap flicking off and on looks
			// worse than a few seconds' silence.
			if t := time.Now(); !p.lost(t) && !p.micDead(t) {
				out["on_air"], out["name"] = true, p.name
			}
		}
	}
	out["waiting"] = n
	return out
}

// TalkControl is the screen's buttons. An error is the sentence to show.
func TalkControl(action, id string, step int, muted bool) error {
	switch action {
	case "on":
		TalkOn()
		return nil
	case "off":
		TalkOff()
		return nil
	case "address":
		// Here id is the address. Someone on air stays on: the port listens on every card.
		talkForgetCards()
		if !talkHasCard(id) {
			return fmt.Errorf("That address isn't on this laptop any more.")
		}
		talk.Lock()
		talk.chosenIP = id
		talk.Unlock()
		return nil
	case "approve", "refuse", "end", "volume", "mute":
	default:
		return fmt.Errorf("action must be on, off, address, approve, refuse, end, volume or mute")
	}
	talk.Lock()
	defer talk.Unlock()
	defer talkSaveLocked()
	p := talk.phones[id]
	if p == nil || p.state == talkRefused || p.state == talkEnded {
		return fmt.Errorf("They've gone — they may have closed the page.")
	}
	switch action {
	case "approve":
		if p.state == talkOnAir {
			return nil
		}
		// One voice at a time: whoever's on hands over.
		for _, o := range talk.phones {
			if o.state == talkOnAir {
				// seen is now: a phone out of signal still has to find out why it finished.
				o.state, o.why, o.seen = talkEnded, "handover", time.Now()
				say("  🎙  Commentary: %s handed over", o.name)
			}
		}
		// Everyone starts as themselves: the last phone's volume isn't this one's.
		p.state, p.onAirAt, p.micFrom, p.seen, p.gainDB, p.muted, p.phoneMuted = talkOnAir, time.Now(), time.Now(), time.Now(), 0, false, false
		say("  🎙  Commentary: %s is on air", p.name)
	case "volume":
		if p.state != talkOnAir {
			return fmt.Errorf("They aren't on air.")
		}
		p.stepGain(step)
	case "mute":
		if p.state != talkOnAir {
			return fmt.Errorf("They aren't on air.")
		}
		p.muted = muted
		if muted {
			say("  🎙  Commentary: %s muted", p.name)
		} else {
			say("  🎙  Commentary: %s unmuted", p.name)
		}
	case "refuse":
		p.state, p.seen = talkRefused, time.Now()
	case "end":
		p.state, p.why, p.seen = talkEnded, "clubhouse", time.Now()
		say("  🎙  Commentary: %s is off air", p.name)
	}
	return nil
}

// ── The phone's routes ──

// talkPhonePaths is everything a phone may reach: all that's on the HTTPS port, and what
// the main handler passes here for the tunnel. Every path is exact.
var talkPhonePaths = map[string]bool{"/talk": true, "/talk/capture.js": true, "/talk/info": true,
	"/talk/join": true, "/talk/poll": true, "/talk/audio": true, "/talk/leave": true, "/talk/level": true}

type talkPhoneHandler struct{}

func talkJSON(w http.ResponseWriter, v any, status int) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(b)
}

func talkKeyOK(k string) bool {
	talk.Lock()
	key, on := talk.key, talk.on
	talk.Unlock()
	return on && key != "" && subtle.ConstantTimeCompare([]byte(k), []byte(key)) == 1
}

func talkSameOrigin(r *http.Request) bool {
	for _, h := range []string{"Origin", "Referer"} {
		if v := r.Header.Get(h); v != "" {
			u, err := url.Parse(v)
			if err != nil || u.Host != r.Host {
				return false
			}
		}
	}
	return true
}

func (talkPhoneHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if p := recover(); p != nil && p != http.ErrAbortHandler {
			logServerError("commentary phone", fmt.Errorf("%v", p))
		}
	}()
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	path := r.URL.Path
	if !talkPhonePaths[path] {
		http.NotFound(w, r)
		return
	}
	if r.Method == "GET" {
		switch path {
		case "/talk":
			body, err := assets.Read("talk.html")
			if err != nil {
				http.NotFound(w, r)
				return
			}
			h.Set("Content-Type", "text/html; charset=utf-8")
			h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'unsafe-inline'; "+
				"connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'")
			h.Set("Permissions-Policy", "microphone=(self)")
			w.Write(body)
		case "/talk/capture.js":
			body, err := assets.Read("talk_capture.js")
			if err != nil {
				http.NotFound(w, r)
				return
			}
			h.Set("Content-Type", "text/javascript; charset=utf-8")
			w.Write(body)
		case "/talk/info":
			if !talkKeyOK(r.URL.Query().Get("k")) {
				talkJSON(w, M{"ok": false, "off": true}, 200)
				return
			}
			talkJSON(w, M{"ok": true, "club": gs(LoadState(), "home_team", "")}, 200)
		default:
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
		}
		return
	}
	if r.Method != "POST" {
		http.Error(w, "Not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !talkSameOrigin(r) {
		talkJSON(w, M{"ok": false, "error": "Cross-origin request rejected"}, 403)
		return
	}
	switch path {
	case "/talk/join":
		talkJoin(w, r)
	case "/talk/poll", "/talk/leave":
		var body struct {
			ID     string `json:"id"`
			Secret string `json:"secret"`
			Muted  bool   `json:"muted"`
		}
		b, _ := io.ReadAll(io.LimitReader(r.Body, 4<<10))
		json.Unmarshal(b, &body)
		talkJSON(w, talkTouch(body.ID, body.Secret, path == "/talk/leave", body.Muted), 200)
	case "/talk/level":
		// Their own volume, up or down, while on air. Not the clubhouse's mute: that stays
		// the clubhouse's.
		var body struct {
			ID     string `json:"id"`
			Secret string `json:"secret"`
			Step   int    `json:"step"`
		}
		b, _ := io.ReadAll(io.LimitReader(r.Body, 4<<10))
		json.Unmarshal(b, &body)
		talk.Lock()
		defer talk.Unlock()
		p := talkFind(body.ID, body.Secret)
		if p == nil {
			talkJSON(w, M{"ok": true, "state": talkEnded, "why": "off"}, 200)
			return
		}
		p.seen = time.Now()
		if p.state == talkOnAir {
			p.stepGain(body.Step)
			talkSaveLocked()
		}
		talkJSON(w, p.reply(), 200)
	case "/talk/audio":
		talkAudio(w, r)
	default:
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
	}
}

func talkJoin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key  string `json:"k"`
		Name string `json:"name"`
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	json.Unmarshal(b, &body)
	if !talkKeyOK(body.Key) {
		talkJSON(w, M{"ok": false, "off": true, "error": "Commentary isn't on at the moment."}, 403)
		return
	}
	name := talkCleanName(body.Name)
	if name == "" {
		talkJSON(w, M{"ok": false, "error": "Type your name first: it's what the clubhouse sees, and what viewers see while you're on."}, 400)
		return
	}
	talk.Lock()
	defer talk.Unlock()
	n := 0
	for _, p := range talk.phones {
		if p.state == talkWaiting {
			n++
		}
	}
	if !talk.on {
		talkJSON(w, M{"ok": false, "off": true, "error": "Commentary isn't on at the moment."}, 403)
		return
	}
	if n >= talkMaxWaiting {
		talkJSON(w, M{"ok": false, "error": "Too many people are waiting. Try again in a minute."}, 429)
		return
	}
	p := &talkPhone{id: talkRandom(8), secret: talkRandom(16), name: name, state: talkWaiting,
		joined: time.Now(), seen: time.Now(), held: map[int64][]byte{}}
	talk.phones[p.id] = p
	say("  🎙  Commentary: %s is asking to go on air", name)
	talkJSON(w, M{"ok": true, "id": p.id, "secret": p.secret, "name": name, "state": p.state}, 200)
}

// talkFind is the caller's own phone. Locked by the caller.
func talkFind(id, secret string) *talkPhone {
	p := talk.phones[id]
	if p == nil || subtle.ConstantTimeCompare([]byte(secret), []byte(p.secret)) != 1 {
		return nil
	}
	return p
}

// reply is what a phone's told after every request. Locked by the caller.
func (p *talkPhone) reply() M {
	return M{"ok": true, "state": p.state, "why": p.why, "gain_db": p.gainDB, "clubhouse_muted": p.muted,
		"can_louder": p.gainDB < talkGainMax, "can_quieter": p.gainDB > talkGainMin}
}

// lost: on air, but nothing's been heard from the phone for a while.
func (p *talkPhone) lost(t time.Time) bool {
	return p.state == talkOnAir && t.Sub(p.seen) > talkGoneFor
}

// micDead: on air and not muted, but no audio's coming, though the phone may still be
// answering. Audio keeps coming in silence, so this isn't someone pausing for thought.
func (p *talkPhone) micDead(t time.Time) bool {
	if p.state != talkOnAir || p.muted || p.phoneMuted {
		return false
	}
	last := p.micFrom
	if p.lastPCM.After(last) {
		last = p.lastPCM
	}
	return t.Sub(last) > talkMicGone
}

// stepGain moves the volume one step up (step > 0) or down, within its range. Locked by
// the caller.
func (p *talkPhone) stepGain(step int) {
	switch {
	case step > 0 && p.gainDB < talkGainMax:
		p.gainDB += talkGainStep
	case step < 0 && p.gainDB > talkGainMin:
		p.gainDB -= talkGainStep
	}
}

// talkGain is the samples at a volume: a new slice, so what's on the way is left alone.
// Past full scale it clips, as any mixer would; the meter shows it.
func talkGain(pcm []byte, db int) []byte {
	if db == 0 {
		return pcm
	}
	f := math.Pow(10, float64(db)/20)
	out := make([]byte, len(pcm))
	for i := 0; i+1 < len(pcm); i += 2 {
		v := math.Round(float64(int16(binary.LittleEndian.Uint16(pcm[i:]))) * f)
		v = math.Max(-32768, math.Min(32767, v))
		binary.LittleEndian.PutUint16(out[i:], uint16(int16(v)))
	}
	return out
}

// talkTouch is a phone saying it's still there; what it needs to know, for the page. A
// phone the server doesn't know has ended: the page stops its microphone.
func talkTouch(id, secret string, leave, muted bool) M {
	talk.Lock()
	defer talk.Unlock()
	p := talkFind(id, secret)
	if p == nil {
		return M{"ok": true, "state": talkEnded, "why": "off"} // a phone from before the switch went off
	}
	p.seen = time.Now()
	p.phoneMuted = muted
	if leave && (p.state == talkWaiting || p.state == talkOnAir) {
		if p.state == talkOnAir {
			say("  🎙  Commentary: %s left", p.name)
		}
		p.state, p.why = talkEnded, "left"
		talkSaveLocked()
	}
	return p.reply()
}

// talkAudio takes one chunk: raw 16-bit little-endian samples at 24 kHz, numbered. Only
// the phone on air gets as far as the OBS page; everything else is thrown away unheard.
func talkAudio(w http.ResponseWriter, r *http.Request) {
	seq, err := strconv.ParseInt(r.URL.Query().Get("seq"), 10, 64)
	if err != nil || seq < 0 {
		talkJSON(w, M{"ok": false, "error": "Bad chunk"}, 400)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, talkChunkMax))
	if err != nil {
		talkJSON(w, M{"ok": false, "error": "Chunk too big"}, 413)
		return
	}
	if len(body) == 0 || len(body)%2 != 0 {
		talkJSON(w, M{"ok": false, "error": "Bad chunk"}, 400)
		return
	}
	talk.Lock()
	defer talk.Unlock()
	p := talkFind(r.Header.Get("X-Talk-Id"), r.Header.Get("X-Talk-Secret"))
	if p == nil {
		talkJSON(w, M{"ok": true, "state": talkEnded, "why": "off"}, 200)
		return
	}
	p.seen = time.Now()
	p.phoneMuted = false // it's sending, so it isn't muted
	if p.state == talkOnAir {
		p.takeChunk(seq, body)
	}
	talkJSON(w, p.reply(), 200)
}

// takeChunk puts chunks back in order: the page has a couple in flight at once. One that's
// lost is skipped once more have come after it — better a click than falling behind.
func (p *talkPhone) takeChunk(seq int64, body []byte) {
	if !p.started {
		p.started, p.nextSeq = true, seq
	}
	// Far behind isn't late, it's a page that reloaded and started counting again: left to
	// the check below, they'd be silent until it counted past where it had got to.
	if seq+talkSeqRestart < p.nextSeq {
		p.nextSeq, p.held = seq, map[int64][]byte{}
	}
	if seq < p.nextSeq {
		return // arrived after we gave up on it
	}
	// A gap left waiting across a pause (a mute, a dropped signal): that audio is old news.
	if len(p.held) > 0 && time.Since(p.heldAt) > time.Second {
		p.held = map[int64][]byte{}
		p.nextSeq = seq
	}
	if len(p.held) == 0 {
		p.heldAt = time.Now()
	}
	p.held[seq] = body
	for {
		for {
			b, ok := p.held[p.nextSeq]
			if !ok {
				break
			}
			delete(p.held, p.nextSeq)
			p.nextSeq++
			out := talkGain(b, p.gainDB)
			p.lastPCM, p.level = time.Now(), talkPeak(out)
			if !p.muted { // muted: the meter still shows the operator they're talking
				talkBroadcast(out)
			}
		}
		if len(p.held) <= talkMaxPending {
			return
		}
		next := int64(-1)
		for s := range p.held {
			if next < 0 || s < next {
				next = s
			}
		}
		p.nextSeq = next
	}
}

func talkPeak(pcm []byte) int {
	peak := 0
	for i := 0; i+1 < len(pcm); i += 2 {
		v := int(int16(binary.LittleEndian.Uint16(pcm[i:])))
		if v < 0 {
			v = -v
		}
		if v > peak {
			peak = v
		}
	}
	return peak * 100 / 32768
}

// talkBroadcast hands a chunk to the OBS page, dropping it rather than waiting for a slow
// page: the phone's request must never sit on OBS. Locked by the caller.
func talkBroadcast(b []byte) {
	for o := range talk.outs {
		select {
		case o.send <- b:
		default:
		}
	}
}

// ── The OBS page ──

var talkUpgrader = websocket.Upgrader{} // its default origin check: this page, no other

// talkOutWS is OBS's browser source listening. Loopback only (the caller checks).
func talkOutWS(w http.ResponseWriter, r *http.Request) {
	conn, err := talkUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	o := &talkOut{send: make(chan []byte, 32)}
	talk.Lock()
	talk.outs[o] = true
	talk.lastOut, talk.reloads = time.Now(), 0
	talk.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			// The page reports every 2s: one that's stopped has hung, so let it be reloaded.
			conn.SetReadDeadline(time.Now().Add(15 * time.Second))
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var st struct {
				Audio string `json:"audio"`
			}
			if json.Unmarshal(msg, &st) == nil {
				talk.Lock()
				o.playing = st.Audio == "running"
				talk.Unlock()
			}
		}
	}()
	defer func() {
		talk.Lock()
		delete(talk.outs, o)
		talk.lastOut = time.Now()
		talk.Unlock()
		conn.Close()
	}()
	ping := time.NewTicker(10 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-done:
			return
		case b := <-o.send:
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if conn.WriteMessage(websocket.BinaryMessage, b) != nil {
				return
			}
		case <-ping.C:
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if conn.WriteMessage(websocket.PingMessage, nil) != nil {
				return
			}
		}
	}
}

// ── Keeping it going ──

// talkTick lets go of phones that have stopped answering, and keeps OBS ready.
func talkTick() {
	t := time.Now()
	talk.Lock()
	if !talk.on {
		talk.Unlock()
		return
	}
	for id, p := range talk.phones {
		switch p.state {
		case talkWaiting:
			if t.Sub(p.seen) > talkWaitGone {
				p.state, p.why, p.seen = talkEnded, "lost", t
			}
		case talkOnAir:
			if t.Sub(p.seen) > talkLostFor {
				say("  🎙  Commentary: lost %s's phone", p.name)
				p.state, p.why, p.seen = talkEnded, "lost", t
				talkSaveLocked()
			}
		default:
			if t.Sub(p.seen) > talkForgetFor {
				delete(talk.phones, id)
			}
		}
	}
	if t.Sub(talk.savedAt) >= talkSaveEvery {
		talkSaveLocked()
	}
	every := talkOBSEvery
	if talk.obsReady {
		every *= 2 // nothing wrong: a connection to OBS less often
	}
	due := !talk.obsBusy && t.Sub(talk.obsAt) >= every
	if due {
		talk.obsBusy, talk.obsAt = true, t
	}
	// The port was in use when it was switched on (another copy shutting down): try again.
	retry := talk.srv == nil && talk.listenErr != ""
	talk.Unlock()
	if retry {
		if err := talkListen(); err != nil {
			talk.Lock()
			talk.listenErr = err.Error()
			talk.Unlock()
		}
	}
	if !due {
		return
	}
	background.Add(1)
	go func() {
		defer background.Done()
		defer func() {
			recover() // nothing here may touch the stream
			talk.Lock()
			talk.obsBusy = false
			talk.Unlock()
		}()
		ready, note := talkEnsureOBS()
		talk.Lock()
		talk.obsReady, talk.obsNote = ready, note
		talk.Unlock()
	}()
}

// talkDuckSettings: the ground's sound dips under a voice, and only while there IS a voice
// — a sidechain compressor keyed on the Commentary source, so a phone that goes quiet or
// crashes can never leave the sound turned down, as a volume change could. A voice at
// -20 dB takes the ground down by about 10 dB, a loud one at -10 by about 17.
func talkDuckSettings() M {
	return M{"ratio": 3.0, "threshold": -35.0, "attack_time": 20, "release_time": 700,
		"output_gain": 0.0, "sidechain_source": TalkSource}
}

// talkOBSNames is the scenes it must sound in and the cameras whose sound dips.
func talkOBSNames(s M) (scenes, cameras []string) {
	// A source that isn't in the live scene falls silent: every scene a match can be on,
	// the replay's included.
	scenes = []string{gsOr(s, "obs_main_scene", "Main"), gsOr(s, "obs_bowler_scene", "Main-Bowler"), gsOr(s, "obs_replay_scene", "Replay")}
	cameras = []string{gsOr(s, "obs_camera_name", "Cricket Camera"), gsOr(s, "obs_camera2_name", "Bowler End Camera")}
	return
}

// talkEnsureOBS makes the source if it's missing, in the right scenes, and the filters on
// the cameras; run again and again it changes nothing. It's made here, not by OBS setup,
// so a club that never uses commentary never gets it in its mixer.
func talkEnsureOBS() (bool, string) {
	s := LoadState()
	if !obsIsLocal(s) {
		return false, "OBS is on another computer, and commentary only reaches an OBS on this laptop."
	}
	// Each obsCall is a new connection, on the laptop that's encoding the stream: the reads go
	// together, and a healthy pass makes two. A change, which is rare, is one more each.
	call := func(typ string, data any) M {
		r := obsCall(s, []obsws.Req{{Type: typ, Data: data}}, 6*time.Second)
		if len(r) == 0 {
			return nil
		}
		return r[0]
	}
	notOpen := "OBS isn't open yet. Commentary joins by itself when it is."
	first := obsCall(s, []obsws.Req{{Type: "GetSceneList"}, {Type: "GetInputList"}}, 6*time.Second)
	if len(first) != 2 || first[0] == nil || first[1] == nil {
		return false, notOpen
	}
	sl, il := first[0], first[1]
	existing := map[string]bool{}
	for _, sc := range toMs(gl(sl, "scenes")) {
		existing[gs(sc, "sceneName", "")] = true
	}
	inputs := map[string]bool{}
	for _, in := range toMs(gl(il, "inputs")) {
		inputs[gs(in, "inputName", "")] = true
	}
	wanted, cameras := talkOBSNames(s)
	current := gs(sl, "currentProgramSceneName", "")
	var scenes []string
	seen := map[string]bool{}
	for _, sc := range wanted {
		if existing[sc] && !seen[sc] {
			seen[sc] = true
			scenes = append(scenes, sc)
		}
	}
	if len(scenes) == 0 {
		return false, "OBS has no match scenes yet: press Start OBS under Get ready."
	}
	// 127.0.0.1, not localhost: same reason as the Overlay source (obs/setup.go).
	pageURL := fmt.Sprintf("http://127.0.0.1:%d/talk/out", Port)
	settings := M{"url": pageURL, "width": 16, "height": 16, "fps": 5, "fps_custom": true,
		"shutdown": false, "restart_when_active": false, "reroute_audio": true, "css": ""}
	// Everything else it needs to know, read together. A read OBS refused is nil, which isn't
	// the same as "it isn't there": taking it for that would add a second Commentary to the
	// scene for good.
	var reads []obsws.Req
	for _, sc := range scenes {
		reads = append(reads, obsws.Req{Type: "GetSceneItemList", Data: M{"sceneName": sc}})
	}
	var camsHere []string
	for _, cam := range cameras {
		if inputs[cam] {
			camsHere = append(camsHere, cam)
			reads = append(reads, obsws.Req{Type: "GetSourceFilterList", Data: M{"sourceName": cam}})
		}
	}
	if inputs[TalkSource] {
		reads = append(reads, obsws.Req{Type: "GetInputSettings", Data: M{"inputName": TalkSource}},
			obsws.Req{Type: "GetInputMute", Data: M{"inputName": TalkSource}})
	}
	got := obsCall(s, reads, 6*time.Second)
	if len(got) != len(reads) {
		return false, notOpen
	}
	sceneItems, filterLists := got[:len(scenes)], got[len(scenes):len(scenes)+len(camsHere)]
	var muteReply M
	fresh, freshID := "", any(nil)
	if !inputs[TalkSource] {
		r := call("CreateInput", M{"sceneName": scenes[0], "inputName": TalkSource, "inputKind": "browser_source",
			"inputSettings": settings, "sceneItemEnabled": true})
		if r == nil {
			return false, "OBS wouldn't add the commentary source."
		}
		fresh, freshID = scenes[0], r["sceneItemId"]
		say("  🎙  Commentary: added its source to OBS")
	} else {
		cfg := got[len(got)-2]
		muteReply = got[len(got)-1]
		if cfg != nil {
			if is, _ := cfg["inputSettings"].(M); gs(is, "url", "") != pageURL || is["reroute_audio"] != true {
				call("SetInputSettings", M{"inputName": TalkSource, "inputSettings": settings, "overlay": true})
			}
		}
	}
	for i, sc := range scenes {
		// OBS puts a new source on top. Nothing shows from this one, but the bottom is where
		// it can't get in the way of anything the club moves about.
		if sc == fresh {
			if freshID != nil {
				call("SetSceneItemIndex", M{"sceneName": sc, "sceneItemId": freshID, "sceneItemIndex": 0})
			}
			continue
		}
		if sceneItems[i] == nil {
			continue // next time
		}
		present := false
		for _, it := range toMs(gl(sceneItems[i], "sceneItems")) {
			present = present || gs(it, "sourceName", "") == TalkSource
		}
		if !present {
			r := call("CreateSceneItem", M{"sceneName": sc, "sourceName": TalkSource, "sceneItemEnabled": true})
			if r == nil {
				continue // not fatal: it plays in the scenes it's in
			}
			call("SetSceneItemIndex", M{"sceneName": sc, "sceneItemId": r["sceneItemId"], "sceneItemIndex": 0})
		}
	}
	for i, cam := range camsHere {
		if filterLists[i] == nil {
			continue // next time
		}
		has := false
		for _, f := range toMs(gl(filterLists[i], "filters")) {
			has = has || gs(f, "filterName", "") == talkDuckFilter
		}
		if !has {
			call("CreateSourceFilter", M{"sourceName": cam, "filterName": talkDuckFilter,
				"filterKind": "compressor_filter", "filterSettings": talkDuckSettings()})
		}
	}
	// OBS loads a browser source once: if OBS opened before this server was listening, the
	// page is an error page that never retries (the Overlay's own trap, overlaywatch.go).
	talk.Lock()
	connected := len(talk.outs) > 0
	since := talk.onAt
	if talk.lastOut.After(since) {
		since = talk.lastOut
	}
	reload := !connected && time.Since(since) >= talkReloadWait && time.Since(talk.reloadAt) >= talkReloadWait && talk.reloads < 3
	if reload {
		talk.reloadAt = time.Now()
		talk.reloads++
	}
	talk.Unlock()
	if reload {
		call("PressInputPropertiesButton", M{"inputName": TalkSource, "propertyName": "refreshnocache"})
	}
	// Two ways for everything above to be right and viewers still to hear nothing.
	if current != "" && !seen[current] {
		return false, fmt.Sprintf("OBS is showing the scene \"%s\", which doesn't have the commentary in it. It's heard on your match scenes.", current)
	}
	if muteReply != nil && muteReply["inputMuted"] == true {
		return false, "Commentary is muted in OBS's mixer, so viewers can't hear it. Unmute it in OBS."
	}
	return true, ""
}

// ── After a crash ──

// talkSession is what a server the launcher restarts needs to carry on. Only a crash leaves
// it behind: a clean stop goes through TalkOff, which deletes it, so commentary is still off
// at the start of the next match day. So does a flat battery or a Windows restart, though,
// and coming back on air from one of those, hours later and with nobody's say-so, would be
// wrong. So it's kept fresh while commentary's on (talkSaveEvery), and only a restart within
// talkSessionFor picks it up.
type talkSession struct {
	Key   string    `json:"key"`
	At    time.Time `json:"at"`
	Phone *struct {
		ID, Secret, Name string
		GainDB           int
		Muted            bool
		OnAirAt          time.Time
	} `json:"phone,omitempty"`
}

func talkSessionFile() string { return filepath.Join(appDir, "talk_session.json") }

// talkSaveLocked keeps talk_session.json up to date. Locked by the caller. It holds the QR
// code's key and a phone's secret, so it's for this laptop's eyes only; failing to save only
// means a crash loses the session, so it's not worth stopping for.
func talkSaveLocked() {
	if !talk.on {
		os.Remove(talkSessionFile())
		return
	}
	talk.savedAt = time.Now()
	sess := talkSession{Key: talk.key, At: time.Now()}
	for _, p := range talk.phones {
		if p.state == talkOnAir {
			sess.Phone = &struct {
				ID, Secret, Name string
				GainDB           int
				Muted            bool
				OnAirAt          time.Time
			}{p.id, p.secret, p.name, p.gainDB, p.muted, p.onAirAt}
		}
	}
	b, err := json.Marshal(sess)
	if err != nil {
		return
	}
	tmp := talkSessionFile() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, talkSessionFile())
	}
}

// TalkResume picks up after a crash, if there's a session to pick up.
func TalkResume() {
	b, err := os.ReadFile(talkSessionFile())
	if err != nil {
		return
	}
	var sess talkSession
	if json.Unmarshal(b, &sess) != nil || sess.Key == "" || time.Since(sess.At) > talkSessionFor {
		os.Remove(talkSessionFile())
		return
	}
	talk.Lock()
	if talk.on {
		talk.Unlock()
		return
	}
	talk.on, talk.key, talk.onAt = true, sess.Key, time.Now()
	talk.phones = map[string]*talkPhone{}
	talk.listenErr, talk.obsReady, talk.obsNote = "", false, "Getting OBS ready…"
	talk.obsAt, talk.reloadAt, talk.reloads = time.Time{}, time.Time{}, 0
	name := ""
	if q := sess.Phone; q != nil {
		talk.phones[q.ID] = &talkPhone{id: q.ID, secret: q.Secret, name: q.Name, state: talkOnAir, joined: q.OnAirAt, seen: time.Now(),
			micFrom: time.Now(), onAirAt: q.OnAirAt, gainDB: q.GainDB, muted: q.Muted, held: map[int64][]byte{}}
		name = q.Name
	}
	talk.Unlock()
	if err := talkListen(); err != nil {
		talk.Lock()
		talk.listenErr = err.Error()
		talk.Unlock()
	}
	if name != "" {
		say("  🎙  Commentary: picked up after the restart, with %s on air", name)
	} else {
		say("  🎙  Commentary: picked up after the restart")
	}
	talkTick()
}

// StartTalkWatch runs the keep-going loop for the life of the server; it does nothing
// while commentary is off.
func StartTalkWatch() { loop("Commentary", 2*time.Second, talkTick) }
