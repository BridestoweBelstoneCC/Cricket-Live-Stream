package server

// The club password is kept in config.ini as a hash (the code review's "smaller points"),
// and the programs on this laptop that used to read it there to log in (the launcher, for
// the window; the simulator; the quality test) prove themselves with control_token.

import (
	"os"
	"strings"
	"testing"
	"time"

	"cricketstream/internal/passhash"
)

func TestAPasswordTypedIntoConfigIsHashedAtStart(t *testing.T) {
	s := newTestServer(t, "")
	text := "# the club's own notes\n[Club]\nname = Test CC\n\n[Auth]\n# keep this safe\ncontrol_token = " +
		ControlToken + "\nclub_password = typed-by-hand\n\n[Network]\nbind_host = 127.0.0.1\n"
	if err := os.WriteFile(ConfigFile, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	LoadAuthConfig()
	mustEq(t, ClubPassword, "typed-by-hand")
	HashClubPassword()

	b, _ := os.ReadFile(ConfigFile)
	if strings.Contains(string(b), "typed-by-hand") {
		t.Fatal("config.ini still holds the password as typed")
	}
	for _, keep := range []string{"# the club's own notes", "# keep this safe", "control_token = " + ControlToken, "name = Test CC", "bind_host = 127.0.0.1"} {
		if !strings.Contains(string(b), keep) {
			t.Errorf("hashing lost %q from config.ini", keep)
		}
	}
	stored := readBack(t).Get("Auth", "club_password", "")
	if !passhash.IsHash(stored) || stored != ClubPassword {
		t.Fatalf("config.ini has %q, the server %q", stored, ClubPassword)
	}
	// What the server reads at its next start is the same password.
	LoadAuthConfig()
	HashClubPassword() // and nothing more to do
	mustEq(t, ClubPassword, stored)

	loginSleep = func(time.Duration) {}
	t.Cleanup(func() { loginSleep = time.Sleep })
	if st, d := s.postJSON("/login", M{"password": "typed-by-hand"}); st != 200 || d["ok"] != true {
		t.Errorf("the password stopped working once hashed: %d %v", st, d)
	}
	// The hash itself is not a password: a copy of config.ini isn't a login.
	if st, _ := s.postJSON("/login", M{"password": stored}); st != 401 {
		t.Errorf("the stored hash logged in: %d", st)
	}
}

func TestHashingAPasswordNeverBreaksAConfigThatCantBeWritten(t *testing.T) {
	newTestServer(t, "plain")
	ConfigFile = ConfigFile + ".not-there"
	HashClubPassword()
	mustEq(t, ClubPassword, "plain") // carries on working as typed
}

// local_key: only from this laptop, with no proxy in between, and only the real token.
func TestLocalKeyLogsInOnlyFromThisLaptop(t *testing.T) {
	s := newTestServer(t, testPW)
	loginSleep = func(time.Duration) {}
	t.Cleanup(func() { loginSleep = time.Sleep })
	hdr := map[string]string{"Content-Type": "application/json"}
	body := `{"local_key":"` + ControlToken + `"}`

	if st, d := s.postJSON("/login", M{"local_key": ControlToken}); st != 200 || d["ok"] != true || d["session_token"] == "" {
		t.Fatalf("this laptop's own login was refused: %d %v", st, d)
	} else if !verifySessionToken(d["session_token"].(string)) {
		t.Error("the session it got isn't valid")
	}
	if st, _ := s.postJSON("/login", M{"local_key": ControlToken + "x"}); st != 401 {
		t.Errorf("a wrong key: %d", st)
	}
	// From another device on the network.
	if w := serveAs("POST", "/login", "192.168.1.50:5555", "192.168.1.10:5000", hdr, body); w.Code != 401 {
		t.Errorf("another device logged in with the key: %d", w.Code)
	}
	// Through a tunnel or the clubhouse link: loopback socket, but forwarded.
	fwd := map[string]string{"Content-Type": "application/json", "X-Forwarded-For": "203.0.113.9"}
	if w := serveAs("POST", "/login", "127.0.0.1:5555", "127.0.0.1:5000", fwd, body); w.Code != 401 {
		t.Errorf("a proxied request logged in with the key: %d", w.Code)
	}
	// No token set at all: an empty key must not match an empty token.
	saved := ControlToken
	ControlToken = ""
	t.Cleanup(func() { ControlToken = saved })
	if w := serveAs("POST", "/login", "127.0.0.1:5555", "127.0.0.1:5000", hdr, `{"local_key":""}`); w.Code != 401 {
		t.Errorf("an empty key logged in: %d", w.Code)
	}
}
