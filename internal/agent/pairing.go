package agent

// Pairing codes: the scorer's laptop shows a short code (K8Q2-7WMX) that the streaming
// laptop's control panel takes instead of an IP address — for wifi where the automatic
// discovery is blocked (guest networks, some club routers), without anyone reading out
// "192.168.1.40 colon 8788".
//
// The code IS the address, written in Crockford base32 (no I, L, O or U, so it reads and
// types cleanly, and a mistyped O or I still decodes), with a check character so a typo
// is caught rather than connecting somewhere else. 4 address bytes on the default port
// make 8 characters (K8Q2-7WMX); another port makes 11 (K8Q2-7WMX-4RT).

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// ErrBadCode is a pairing code that doesn't decode (or fails its check).
var ErrBadCode = errors.New("that pairing code isn't right — check it on the scorer's laptop")

func encode32(b []byte, chars int) string {
	var n uint64
	for _, x := range b {
		n = n<<8 | uint64(x)
	}
	out := make([]byte, chars)
	for i := chars - 1; i >= 0; i-- {
		out[i] = crockford[n&31]
		n >>= 5
	}
	return string(out)
}

// checkChar is the check character over the code's own characters, with odd weights:
// an odd weight shares no factor with 32, so changing any one character always changes
// the check (even weights let a change of 16 cancel out — a test found exactly that).
func checkChar(body string) byte {
	sum := 0
	for i := 0; i < len(body); i++ {
		sum += strings.IndexByte(crockford, body[i]) * (2*i + 1)
	}
	return crockford[sum%32]
}

// PairingCode is the code for ip:port ("" if ip isn't an IPv4 address).
func PairingCode(ip string, port int) string {
	v4 := net.ParseIP(ip).To4()
	if v4 == nil {
		return ""
	}
	b := []byte(v4)
	body := encode32(b, 7) // 32 bits in 7 characters
	if port != DefaultHTTPPort {
		b = append(b, byte(port>>8), byte(port))
		body = encode32(b, 10) // 48 bits in 10
	}
	code := body + string(checkChar(body))
	var groups []string
	for i := 0; i < len(code); i += 4 {
		end := i + 4
		if end > len(code) {
			end = len(code)
		}
		groups = append(groups, code[i:end])
	}
	return strings.Join(groups, "-")
}

// ParsePairingCode turns a typed code back into "ip:port". Spaces, dashes and case don't
// matter; O reads as 0 and I or L as 1.
func ParsePairingCode(code string) (string, error) {
	c := strings.NewReplacer("-", "", " ", "", "O", "0", "I", "1", "L", "1").Replace(strings.ToUpper(strings.TrimSpace(code)))
	var b []byte
	var body string
	switch len(c) {
	case 8:
		body = c[:7]
	case 11:
		body = c[:10]
	default:
		return "", ErrBadCode
	}
	var n uint64
	for i := 0; i < len(body); i++ {
		v := strings.IndexByte(crockford, body[i])
		if v < 0 {
			return "", ErrBadCode
		}
		n = n<<5 | uint64(v)
	}
	nbytes := 4
	if len(c) == 11 {
		nbytes = 6
	}
	if n>>(8*nbytes) != 0 {
		return "", ErrBadCode
	}
	for i := 0; i < nbytes; i++ {
		b = append(b, byte(n>>(8*uint(nbytes-1-i))))
	}
	if c[len(c)-1] != checkChar(body) {
		return "", ErrBadCode
	}
	port := DefaultHTTPPort
	if nbytes == 6 {
		port = int(b[4])<<8 | int(b[5])
	}
	return fmt.Sprintf("%d.%d.%d.%d:%d", b[0], b[1], b[2], b[3], port), nil
}
