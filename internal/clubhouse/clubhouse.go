// Package clubhouse is the optional clubhouse screen: a touch screen on a Raspberry Pi
// (or any small Linux box) in the clubhouse that switches the streaming laptop on, runs
// match day from big buttons, shows the score and a picture of the stream, and switches
// the laptop off again afterwards.
//
// It has two halves, one on each machine:
//
//   - The LINK (link.go) runs inside CricketStream's window on the streaming laptop, when
//     setup's [Clubhouse] enabled = yes. It listens on the club network (port 5096) for a
//     screen holding the pairing key, and does what the screen asks through the window's
//     own match-day code: start, answer the launcher's questions, stop, switch off. The
//     control panel's routes the screen needs are reached through it, on an allowlist,
//     with the window's own login — the screen never holds the club password.
//   - The SCREEN (screen.go) is `cricketstream clubhouse` on the Pi: a page on the Pi's
//     own loopback port, shown full screen by Chromium. It must be served by the Pi,
//     because the laptop is OFF when the screen is first touched — the screen wakes it
//     (wol.go) and waits for the link to answer.
//
// Nothing here runs unless a club turns it on: a laptop with no [Clubhouse] section
// opens no port, and a club without a Pi never runs the screen.
package clubhouse

import (
	"crypto/rand"
	"crypto/subtle"
	"os"
	"strconv"
	"strings"
)

// DefaultPort is the link's port on the streaming laptop, TCP for requests and UDP for
// the screen's "where are you?". Next to the window's own 5098; the server keeps 5000.
const DefaultPort = 5096

// DefaultScreenPort is the screen's page, on the Pi's loopback only.
const DefaultScreenPort = 5097

// PortEnv moves the link's port (tests; a clash on the laptop).
const PortEnv = "CRICKETSTREAM_CLUBHOUSE_PORT"

// KeyHeader carries the pairing key on every request to the link but /clubhouse/hello.
const KeyHeader = "X-Clubhouse-Key"

// ProbeMagic is the screen's UDP "where are you?". The reply names the service and
// port, never anything secret: the key is what proves the screen is the club's.
var ProbeMagic = []byte("CRICKETSTREAM-CLUBHOUSE?")

// Service is the name the link answers to (hello, discovery).
const Service = "cricketstream-clubhouse"

// Port is the link's port: PortEnv, else DefaultPort.
func Port() int {
	if v := os.Getenv(PortEnv); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n < 65536 {
			return n
		}
	}
	return DefaultPort
}

// crockford is the agent's pairing alphabet: no I, L, O or U, so a key reads aloud and
// types cleanly on a touch keyboard, and a mistyped O or I still matches.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// KeyLen is 8 characters: 40 bits. The link refuses a guesser after a few wrong keys
// (link.go), so that is far beyond what anyone on the club wifi could try.
const KeyLen = 8

// NewKey is a fresh pairing key (8 characters, no separators).
func NewKey() string {
	b := make([]byte, KeyLen)
	rand.Read(b)
	out := make([]byte, KeyLen)
	for i, x := range b {
		out[i] = crockford[int(x)&31]
	}
	return string(out)
}

// NormaliseKey is a key as typed reduced to its characters: case, spaces and dashes
// don't matter; O reads as 0 and I or L as 1 (as the agent's pairing code does).
func NormaliseKey(s string) string {
	s = strings.NewReplacer("-", "", " ", "", "O", "0", "I", "1", "L", "1").Replace(strings.ToUpper(strings.TrimSpace(s)))
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(crockford, s[i]) < 0 {
			return ""
		}
	}
	return s
}

// FormatKey is a key as shown: ABCD-EFGH.
func FormatKey(k string) string {
	k = NormaliseKey(k)
	if len(k) <= 4 {
		return k
	}
	return k[:4] + "-" + k[4:]
}

// KeyMatches: typed is the key, compared in constant time. An empty or malformed key
// never matches, even another empty one.
func KeyMatches(want, typed string) bool {
	w, t := NormaliseKey(want), NormaliseKey(typed)
	if len(w) != KeyLen || len(t) != KeyLen {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(w), []byte(t)) == 1
}
