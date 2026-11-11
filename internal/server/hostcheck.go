package server

import (
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
)

// hostAllowed is the server's answer to DNS rebinding. A hostile web page opened on this
// laptop can point its own domain at 127.0.0.1; the browser then sends that page's
// requests here with Origin and Host both naming the attacker's domain, so originOK
// passes, and on a laptop with no club password every route is open — settings, the
// checklist that starts OBS and goes live, the loopback-only routes. The app window and
// the clubhouse screen already refuse a Host that isn't theirs; this is the same for the
// server, with every name it's genuinely reached by:
//   - any IP address: rebinding needs a DNS name, and the LAN, Tailscale and QR-code
//     addresses (remoteTargets) are all addresses;
//   - localhost, this computer's own name (bare, .local, .lan, .home, or its Tailscale
//     MagicDNS name), and any *.ts.net name: a tailnet's names aren't the attacker's to
//     point anywhere;
//   - the Cloudflare quick tunnel's current hostname (cloudflared passes it through);
//   - [Network] extra_hosts in config.ini, for a club with its own reverse proxy or domain.
func hostAllowed(hostport string) bool {
	if hostport == "" {
		return true // HTTP/1.0 with no Host: not a browser, so not rebinding
	}
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if net.ParseIP(host) != nil || host == "localhost" || strings.HasSuffix(host, ".ts.net") {
		return true
	}
	if me := ownHostname(); me != "" {
		if host == me {
			return true
		}
		for _, suffix := range []string{".local", ".lan", ".home"} {
			if host == me+suffix {
				return true
			}
		}
	}
	for _, extra := range ExtraHosts {
		if host == extra {
			return true
		}
	}
	cfMu.Lock()
	tunnel := cfURL
	cfMu.Unlock()
	if tunnel != "" {
		if u, err := url.Parse(tunnel); err == nil && strings.EqualFold(u.Hostname(), host) {
			return true
		}
	}
	return false
}

// ExtraHosts is [Network] extra_hosts: more names the server answers to, lower case.
var ExtraHosts []string

// parseExtraHosts reads the setting: names separated by commas or spaces, ports and
// schemes dropped.
func parseExtraHosts(v string) []string {
	var out []string
	for _, h := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
		h = strings.ToLower(h)
		if i := strings.Index(h, "://"); i >= 0 {
			h = h[i+3:]
		}
		if i := strings.IndexAny(h, "/:"); i >= 0 {
			h = h[:i]
		}
		if h = strings.TrimSuffix(h, "."); h != "" {
			out = append(out, h)
		}
	}
	return out
}

var (
	ownHostnameOnce sync.Once
	ownHostnameVal  string
)

// ownHostname is this computer's name, lower case, without any domain part.
func ownHostname() string {
	ownHostnameOnce.Do(func() {
		if h, err := os.Hostname(); err == nil {
			h = strings.ToLower(h)
			if i := strings.IndexByte(h, '.'); i > 0 {
				h = h[:i]
			}
			ownHostnameVal = h
		}
	})
	return ownHostnameVal
}
