package server

// Session tokens and config.ini token persistence (tests/test_auth.py).

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"cricketstream/internal/ini"
)

func withToken(t *testing.T) {
	t.Helper()
	tok, hours := ControlToken, SessionHours
	sessionEpochMu.Lock()
	epoch := sessionEpoch
	sessionEpochMu.Unlock()
	ControlToken = strings.Repeat("ab", 32)
	t.Cleanup(func() {
		ControlToken, SessionHours = tok, hours
		sessionEpochMu.Lock()
		sessionEpoch = epoch
		sessionEpochMu.Unlock()
	})
}

func mustToken(t *testing.T) string {
	t.Helper()
	tok, err := makeSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestSessionTokenRoundTrip(t *testing.T) {
	withToken(t)
	if !verifySessionToken(mustToken(t)) {
		t.Fatal("fresh token rejected")
	}
}

func TestExpiredTokenRejected(t *testing.T) {
	withToken(t)
	SessionHours = -1 // issue an already-expired token
	if verifySessionToken(mustToken(t)) {
		t.Fatal("expired token accepted")
	}
}

func TestTamperedTokenRejected(t *testing.T) {
	withToken(t)
	tok := mustToken(t)
	last := "0"
	if tok[len(tok)-1] == '0' {
		last = "1"
	}
	if verifySessionToken(tok[:len(tok)-1] + last) {
		t.Fatal("tampered signature accepted")
	}
	// Extending the expiry without re-signing must also fail
	expiry, rest, _ := strings.Cut(tok, ":")
	n, _ := strconv.Atoi(expiry)
	if verifySessionToken(strconv.Itoa(n+9999) + ":" + rest) {
		t.Fatal("extended expiry accepted")
	}
}

func TestEpochRotationInvalidatesAll(t *testing.T) {
	withToken(t)
	tok := mustToken(t)
	if !verifySessionToken(tok) {
		t.Fatal("fresh token rejected")
	}
	rotateSessionEpoch() // what POST /auth/logout_all does
	if verifySessionToken(tok) {
		t.Fatal("token survived rotation")
	}
}

func TestGarbageTokens(t *testing.T) {
	withToken(t)
	for _, bad := range []string{"", "a:b", "a:b:c", "1:2:3:4", "🏏"} {
		if verifySessionToken(bad) {
			t.Fatal(bad)
		}
	}
}

func TestEmptyControlTokenNeverSignsOrVerifies(t *testing.T) {
	withToken(t)
	ControlToken = ""
	if _, err := makeSessionToken(); err == nil {
		t.Fatal("signed with an empty control_token")
	}
	if verifySessionToken("1:2:3") {
		t.Fatal("verified with an empty control_token")
	}
}

func withConfig(t *testing.T, text string) {
	t.Helper()
	saved := ConfigFile
	ConfigFile = filepath.Join(t.TempDir(), "config.ini")
	t.Cleanup(func() { ConfigFile = saved })
	if err := os.WriteFile(ConfigFile, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readBack(t *testing.T) *ini.File {
	t.Helper()
	f, err := ini.Load(ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestPersistReplacesBlankLineInPlace(t *testing.T) {
	withConfig(t, "[Auth]\ncontrol_token =\nclub_password = pw\n\n[Network]\nbind_host = 127.0.0.1\n")
	if !persistControlToken("deadbeef") {
		t.Fatal("not persisted")
	}
	f := readBack(t)
	mustEq(t, f.Get("Auth", "control_token", ""), "deadbeef")
	mustEq(t, f.Get("Auth", "club_password", ""), "pw") // untouched
	mustEq(t, f.Get("Network", "bind_host", ""), "127.0.0.1")
}

func TestPersistMissingLineInsertedUnderAuthNotAtEOF(t *testing.T) {
	// [Auth] exists but the control_token line was deleted, and [Network] follows — the
	// token must land in [Auth], not the last section.
	withConfig(t, "[Auth]\nclub_password = pw\n\n[Network]\nbind_host = 127.0.0.1\n")
	persistControlToken("deadbeef")
	f := readBack(t)
	mustEq(t, f.Get("Auth", "control_token", ""), "deadbeef")
	mustEq(t, f.Get("Network", "control_token", "<none>"), "<none>")
}

func TestPersistNoAuthSectionAppendsOne(t *testing.T) {
	withConfig(t, "[Club]\nname = Test CC\n")
	persistControlToken("deadbeef")
	mustEq(t, readBack(t).Get("Auth", "control_token", ""), "deadbeef")
}

func TestPersistPreservesComments(t *testing.T) {
	withConfig(t, "# keep me\n[Auth]\n# and me\ncontrol_token =\n")
	persistControlToken("deadbeef")
	b, _ := os.ReadFile(ConfigFile)
	for _, want := range []string{"# keep me", "# and me"} {
		if !strings.Contains(string(b), want) {
			t.Fatal(want)
		}
	}
}
