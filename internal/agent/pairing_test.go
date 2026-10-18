package agent

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

func TestPairingCodesRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 2000; i++ {
		ip := fmt.Sprintf("%d.%d.%d.%d", rng.Intn(256), rng.Intn(256), rng.Intn(256), rng.Intn(256))
		port := DefaultHTTPPort
		if i%3 == 0 {
			port = 1 + rng.Intn(65535)
		}
		code := PairingCode(ip, port)
		got, err := ParsePairingCode(code)
		if err != nil || got != fmt.Sprintf("%s:%d", ip, port) {
			t.Fatalf("%s:%d -> %q -> %q, %v", ip, port, code, got, err)
		}
	}
}

func TestPairingCodeShape(t *testing.T) {
	c := PairingCode("192.168.1.40", DefaultHTTPPort)
	if len(c) != 9 || c[4] != '-' || strings.ContainsAny(c, "ILOU") {
		t.Errorf("default-port code %q", c)
	}
	if c2 := PairingCode("192.168.1.40", 9000); len(c2) != 13 || c2[4] != '-' || c2[9] != '-' || c2 == c {
		t.Errorf("other-port code %q", c2)
	}
	if PairingCode("::1", DefaultHTTPPort) != "" || PairingCode("nope", DefaultHTTPPort) != "" {
		t.Error("a non-IPv4 address got a code")
	}
}

// Typed the way people type: lower case, no dash, spaces, O for 0 and I/L for 1 — and a
// typo is caught, not sent somewhere else.
func TestPairingCodeAsTyped(t *testing.T) {
	want := "10.0.0.17:8788"
	c := PairingCode("10.0.0.17", DefaultHTTPPort)
	variants := []string{c, strings.ToLower(c), strings.ReplaceAll(c, "-", ""), " " + strings.ReplaceAll(c, "-", " ") + " ",
		strings.ReplaceAll(c, "0", "O"), strings.ReplaceAll(strings.ReplaceAll(c, "1", "I"), "1", "l")}
	for _, v := range variants {
		if got, err := ParsePairingCode(v); err != nil || got != want {
			t.Errorf("%q -> %q, %v", v, got, err)
		}
	}
	for i := 0; i < len(c); i++ {
		if c[i] == '-' {
			continue
		}
		for _, r := range crockford {
			if byte(r) == c[i] {
				continue
			}
			typo := c[:i] + string(r) + c[i+1:]
			if got, err := ParsePairingCode(typo); err == nil && got != want {
				// One wrong character must never decode to a different address.
				t.Fatalf("typo %q decoded to %s", typo, got)
			}
		}
	}
	pc := PairingCode("10.0.0.17", 9001)
	for i := 0; i < len(pc); i++ {
		if pc[i] == '-' {
			continue
		}
		for _, r := range crockford {
			if typo := pc[:i] + string(r) + pc[i+1:]; typo != pc {
				if got, err := ParsePairingCode(typo); err == nil && got != "10.0.0.17:9001" {
					t.Fatalf("typo %q decoded to %s", typo, got)
				}
			}
		}
	}
	for _, bad := range []string{"", "ABC", "K8Q2-7WM", "K8Q2-7WMX-ZZZZ", "UUUU-UUUU", "!!!!-!!!!", "ZZZZ-ZZZZ"} {
		if _, err := ParsePairingCode(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
