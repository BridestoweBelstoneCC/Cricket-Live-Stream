// Package onvif finds IP cameras on the local network and works out their stream address,
// so nobody has to type an RTSP URL (with the camera's password in the middle of it) on
// match morning.
//
// Finding: WS-Discovery, the multicast "who's there?" every ONVIF camera answers (Reolink,
// Hikvision, Dahua, Amcrest, Axis, TP-Link and most others — on some it has to be
// switched on in the camera's own settings). Each answer carries the camera's ONVIF
// service address and its name and model.
//
// The stream address: ONVIF's own GetCapabilities → GetProfiles → GetStreamUri, signed
// with the camera's login (WS-Security UsernameToken). Where that fails — ONVIF login off,
// an older model — the usual address for the make is offered instead.
package onvif

import (
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Camera is one camera that answered.
type Camera struct {
	Address  string `json:"address"` // its IP
	Name     string `json:"name"`
	Hardware string `json:"hardware"`
	Make     string `json:"make"`
	XAddr    string `json:"xaddr"` // its ONVIF device service
}

// ProbeAddr is where the discovery question goes (the WS-Discovery multicast group;
// tests point it at a fake camera).
var ProbeAddr = "239.255.255.250:3702"

func uuid() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func probeMessage() []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>` +
		`<e:Envelope xmlns:e="http://www.w3.org/2003/05/soap-envelope" xmlns:w="http://schemas.xmlsoap.org/ws/2004/08/addressing" ` +
		`xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery" xmlns:dn="http://www.onvif.org/ver10/network/wsdl">` +
		`<e:Header><w:MessageID>uuid:` + uuid() + `</w:MessageID>` +
		`<w:To e:mustUnderstand="true">urn:schemas-xmlsoap-org:ws:2005:04:discovery</w:To>` +
		`<w:Action e:mustUnderstand="true">http://schemas.xmlsoap.org/ws/2005/04/discovery/Probe</w:Action></e:Header>` +
		`<e:Body><d:Probe><d:Types>dn:NetworkVideoTransmitter</d:Types></d:Probe></e:Body></e:Envelope>`)
}

var (
	reXAddrs = regexp.MustCompile(`(?s)<(?:[A-Za-z0-9]+:)?XAddrs>(.*?)</(?:[A-Za-z0-9]+:)?XAddrs>`)
	reScopes = regexp.MustCompile(`(?s)<(?:[A-Za-z0-9]+:)?Scopes(?:\s[^>]*)?>(.*?)</(?:[A-Za-z0-9]+:)?Scopes>`)
)

// parseProbeMatch reads one discovery answer (nil if it isn't one).
func parseProbeMatch(b []byte, from string) *Camera {
	m := reXAddrs.FindSubmatch(b)
	if m == nil {
		return nil
	}
	c := &Camera{Address: from}
	// The service address on the IP that answered; a camera with two network cards may
	// list only its other one, so failing that, its first address moved onto this IP
	// (the one we know reaches it — and the only one the panel will ask).
	var first *url.URL
	for _, x := range strings.Fields(string(m[1])) {
		if u, err := url.Parse(x); err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https") {
			if u.Hostname() == from {
				c.XAddr = x
				break
			}
			if first == nil {
				first = u
			}
		}
	}
	if c.XAddr == "" {
		if first == nil {
			return nil
		}
		host := from
		if strings.Contains(from, ":") {
			host = "[" + from + "]"
		}
		if p := first.Port(); p != "" {
			host += ":" + p
		}
		first.Host = host
		c.XAddr = first.String()
	}
	if s := reScopes.FindSubmatch(b); s != nil {
		for _, sc := range strings.Fields(string(s[1])) {
			for _, key := range []string{"/name/", "/hardware/", "/mfr/", "/manufacturer/"} {
				if i := strings.Index(sc, key); i >= 0 {
					v, err := url.PathUnescape(sc[i+len(key):])
					if err != nil {
						v = sc[i+len(key):]
					}
					v = strings.TrimSpace(strings.ReplaceAll(v, "_", " "))
					switch key {
					case "/name/":
						c.Name = v
					case "/hardware/":
						c.Hardware = v
					default:
						c.Make = v
					}
				}
			}
		}
	}
	if c.Make == "" {
		c.Make = guessMake(c.Name + " " + c.Hardware)
	}
	if c.Name == "" {
		c.Name = strings.TrimSpace(c.Make + " " + c.Hardware)
	}
	if c.Name == "" {
		c.Name = "Camera"
	}
	return c
}

// Discover asks the network who's a camera and collects the answers for timeout.
func Discover(timeout time.Duration) ([]Camera, error) {
	dst, err := net.ResolveUDPAddr("udp4", ProbeAddr)
	if err != nil {
		return nil, err
	}
	// One socket per local address, so the question goes out on every network the
	// laptop is on (wifi and the camera's wired network, say), not only the default one.
	var conns []*net.UDPConn
	addrs := []string{"0.0.0.0"}
	if !dst.IP.IsLoopback() {
		addrs = localIPv4s()
	}
	for _, a := range addrs {
		if c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(a)}); err == nil {
			conns = append(conns, c)
		}
	}
	if len(conns) == 0 {
		return nil, errors.New("couldn't open a network socket to search on")
	}
	var mu sync.Mutex
	found := map[string]Camera{}
	var wg sync.WaitGroup
	deadline := time.Now().Add(timeout)
	for _, c := range conns {
		wg.Add(1)
		go func(c *net.UDPConn) {
			defer wg.Done()
			defer c.Close()
			msg := probeMessage()
			for i := 0; i < 2; i++ { // UDP: say it twice
				c.WriteToUDP(msg, dst)
			}
			c.SetReadDeadline(deadline)
			buf := make([]byte, 64<<10)
			for {
				n, from, err := c.ReadFromUDP(buf)
				if err != nil {
					return
				}
				if cam := parseProbeMatch(buf[:n], from.IP.String()); cam != nil {
					mu.Lock()
					found[cam.XAddr] = *cam
					mu.Unlock()
				}
			}
		}(c)
	}
	wg.Wait()
	out := make([]Camera, 0, len(found))
	for _, c := range found {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out, nil
}

func localIPv4s() []string {
	var out []string
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		as, _ := i.Addrs()
		for _, a := range as {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
				out = append(out, n.IP.String())
			}
		}
	}
	if len(out) == 0 {
		out = []string{"0.0.0.0"}
	}
	return out
}

// ── The stream address ──

var client = &http.Client{Timeout: 6 * time.Second}

// security is the WS-Security UsernameToken header ONVIF uses: the password never
// travels, only SHA1(nonce + created + password).
func security(user, pass string, skew time.Duration) string {
	if user == "" {
		return ""
	}
	nonce := make([]byte, 16)
	rand.Read(nonce)
	created := time.Now().Add(skew).UTC().Format("2006-01-02T15:04:05.000Z")
	h := sha1.New()
	h.Write(nonce)
	h.Write([]byte(created))
	h.Write([]byte(pass))
	return `<s:Header><Security s:mustUnderstand="1" xmlns="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd">` +
		`<UsernameToken><Username>` + xmlEscape(user) + `</Username>` +
		`<Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordDigest">` +
		base64.StdEncoding.EncodeToString(h.Sum(nil)) + `</Password>` +
		`<Nonce EncodingType="http://docs.oasis-open.org/wss/2004/01/oasis-200401-soap-message-security-1.0#Base64Binary">` +
		base64.StdEncoding.EncodeToString(nonce) + `</Nonce>` +
		`<Created xmlns="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd">` + created + `</Created>` +
		`</UsernameToken></Security></s:Header>`
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	for _, r := range s {
		switch r {
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '&':
			b.WriteString("&amp;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&apos;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ErrLogin is the camera refusing the username and password.
var ErrLogin = errors.New("the camera didn't accept that username and password")

func call(endpoint, user, pass, body string, skew time.Duration) (string, error) {
	env := `<?xml version="1.0" encoding="UTF-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" ` +
		`xmlns:tds="http://www.onvif.org/ver10/device/wsdl" xmlns:trt="http://www.onvif.org/ver10/media/wsdl" ` +
		`xmlns:tt="http://www.onvif.org/ver10/schema">` + security(user, pass, skew) + `<s:Body>` + body + `</s:Body></s:Envelope>`
	resp, err := client.Post(endpoint, "application/soap+xml; charset=utf-8", strings.NewReader(env))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	s := string(b)
	if resp.StatusCode == 401 || strings.Contains(s, "NotAuthorized") || strings.Contains(s, "FailedAuthentication") {
		return "", ErrLogin
	}
	if resp.StatusCode >= 400 || strings.Contains(s, ":Fault>") {
		return "", fmt.Errorf("the camera said no (%d)", resp.StatusCode)
	}
	return s, nil
}

func first(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

var (
	reMediaXAddr = regexp.MustCompile(`(?s)<(?:[A-Za-z0-9]+:)?Media>.*?<(?:[A-Za-z0-9]+:)?XAddr>(.*?)</`)
	reProfile    = regexp.MustCompile(`<(?:[A-Za-z0-9]+:)?Profiles[^>]*\btoken="([^"]+)"`)
	reURI        = regexp.MustCompile(`(?s)<(?:[A-Za-z0-9]+:)?Uri>(.*?)</(?:[A-Za-z0-9]+:)?Uri>`)
)

// StreamURL asks the camera (its ONVIF device service, xaddr) for its main stream's RTSP
// address, with user and pass put into it as OBS needs.
func StreamURL(xaddr, user, pass string) (string, error) {
	skew := clockSkew(xaddr)
	caps, err := call(xaddr, user, pass, `<tds:GetCapabilities><tds:Category>Media</tds:Category></tds:GetCapabilities>`, skew)
	if err != nil {
		return "", err
	}
	media := first(reMediaXAddr, caps)
	if media == "" {
		media = xaddr
	}
	media = onHostOf(media, xaddr)
	profiles, err := call(media, user, pass, `<trt:GetProfiles/>`, skew)
	if err != nil {
		return "", err
	}
	token := first(reProfile, profiles) // the first profile is the main (full-quality) stream
	if token == "" {
		return "", errors.New("the camera listed no video profiles")
	}
	su, err := call(media, user, pass, `<trt:GetStreamUri><trt:StreamSetup><tt:Stream>RTP-Unicast</tt:Stream>`+
		`<tt:Transport><tt:Protocol>RTSP</tt:Protocol></tt:Transport></trt:StreamSetup>`+
		`<trt:ProfileToken>`+xmlEscape(token)+`</trt:ProfileToken></trt:GetStreamUri>`, skew)
	if err != nil {
		return "", err
	}
	uri := strings.ReplaceAll(first(reURI, su), "&amp;", "&")
	if !strings.HasPrefix(strings.ToLower(uri), "rtsp://") {
		return "", errors.New("the camera didn't give an RTSP address")
	}
	return WithLogin(onHostOf(uri, xaddr), user, pass), nil
}

// onHostOf moves an address the camera gave onto the host we reached it at, keeping its
// own port and path. Cameras often report a stale or internal IP — the factory default,
// another network card — in their media and stream addresses.
func onHostOf(addr, reached string) string {
	u, err := url.Parse(addr)
	r, err2 := url.Parse(reached)
	if err != nil || err2 != nil || u.Hostname() == r.Hostname() {
		return addr
	}
	host := r.Hostname()
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if p := u.Port(); p != "" {
		host += ":" + p
	}
	u.Host = host
	return u.String()
}

var reUTCDateTime = regexp.MustCompile(`(?s)UTCDateTime>(.*?)</(?:[A-Za-z0-9]+:)?UTCDateTime>`)

// clockSkew is how far the camera's clock is from this laptop's. The login is signed
// with a time, and most cameras refuse one more than a few seconds off theirs — at a
// ground with no internet the camera's clock is usually wrong, and the right password
// would read as the wrong one. Asking the time needs no login. 0 if it won't say.
func clockSkew(xaddr string) time.Duration {
	s, err := call(xaddr, "", "", `<tds:GetSystemDateAndTime/>`, 0)
	if err != nil {
		return 0
	}
	block := first(reUTCDateTime, s)
	if block == "" {
		return 0
	}
	// Each field on its own: cameras don't all put Date and Time in the same order.
	n := map[string]int{}
	for _, f := range []string{"Year", "Month", "Day", "Hour", "Minute", "Second"} {
		re := regexp.MustCompile(`<(?:[A-Za-z0-9]+:)?` + f + `>\s*(\d+)\s*<`)
		v := first(re, block)
		if v == "" {
			return 0
		}
		n[f], _ = strconv.Atoi(v)
	}
	cam := time.Date(n["Year"], time.Month(n["Month"]), n["Day"], n["Hour"], n["Minute"], n["Second"], 0, time.UTC)
	return time.Until(cam).Round(time.Second)
}

// WithLogin puts the camera's username and password into an RTSP address.
func WithLogin(uri, user, pass string) string {
	u, err := url.Parse(uri)
	if err != nil || user == "" {
		return uri
	}
	u.User = url.UserPassword(user, pass)
	return u.String()
}

// Masked is an RTSP address with the password hidden, to show on screen.
func Masked(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.User == nil {
		return uri
	}
	if _, has := u.User.Password(); has {
		return strings.Replace(u.String(), u.User.String()+"@", url.PathEscape(u.User.Username())+":•••@", 1)
	}
	return u.String()
}

// The usual main-stream address for each make, for when ONVIF won't say.
var makePaths = []struct{ match, path string }{
	{"reolink", "/h264Preview_01_main"},
	{"hikvision", "/Streaming/Channels/101"},
	{"hiwatch", "/Streaming/Channels/101"},
	{"annke", "/Streaming/Channels/101"},
	{"dahua", "/cam/realmonitor?channel=1&subtype=0"},
	{"amcrest", "/cam/realmonitor?channel=1&subtype=0"},
	{"lorex", "/cam/realmonitor?channel=1&subtype=0"},
	{"tapo", "/stream1"},
	{"tp-link", "/stream1"},
	{"axis", "/axis-media/media.amp"},
	{"uniview", "/unicast/c1/s0/live"},
}

func guessMake(s string) string {
	l := strings.ToLower(s)
	for _, m := range makePaths {
		if strings.Contains(l, m.match) {
			return map[string]string{"reolink": "Reolink", "hikvision": "Hikvision", "hiwatch": "HiWatch",
				"annke": "ANNKE", "dahua": "Dahua", "amcrest": "Amcrest", "lorex": "Lorex", "tapo": "TP-Link Tapo",
				"tp-link": "TP-Link", "axis": "Axis", "uniview": "Uniview"}[m.match]
		}
	}
	// Reolink models are often just "RLC-510A" / "E1 Zoom" with no maker in the name.
	if strings.HasPrefix(l, "rlc") || strings.Contains(l, " rlc-") || strings.Contains(l, "e1 ") || strings.HasPrefix(l, "duo") {
		return "Reolink"
	}
	return ""
}

// GuessURL is the usual main-stream address for this camera's make ("" if unknown).
func GuessURL(c Camera, user, pass string) string {
	l := strings.ToLower(c.Make + " " + c.Name + " " + c.Hardware)
	if g := guessMake(l); g != "" {
		l = strings.ToLower(g)
	}
	for _, m := range makePaths {
		if strings.Contains(l, m.match) {
			return WithLogin("rtsp://"+net.JoinHostPort(c.Address, "554")+m.path, user, pass)
		}
	}
	return ""
}
