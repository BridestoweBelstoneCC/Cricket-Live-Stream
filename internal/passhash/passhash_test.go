package passhash

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	Iterations = 1000
	os.Exit(m.Run())
}

func TestHashVerifies(t *testing.T) {
	h, err := Hash("50%off the bar")
	if err != nil {
		t.Fatal(err)
	}
	if !IsHash(h) || strings.Contains(h, "50%off") || strings.ContainsAny(h, "% \n") {
		t.Fatalf("not a clean hash: %q", h)
	}
	if !Verify(h, "50%off the bar") {
		t.Error("the right password was refused")
	}
	for _, wrong := range []string{"", "50%off the ba", "50%OFF the bar", h} {
		if Verify(h, wrong) {
			t.Errorf("%q was accepted", wrong)
		}
	}
	if again, _ := Hash("50%off the bar"); again == h {
		t.Error("two hashes of one password are the same: no salt")
	}
}

// A hash made at another iteration count still verifies: the count can be raised later.
func TestHashCarriesItsOwnCount(t *testing.T) {
	Iterations = 500
	h, _ := Hash("pw")
	Iterations = 1000
	if !strings.HasPrefix(h, "pbkdf2-sha256$500$") || !Verify(h, "pw") {
		t.Errorf("hash %q", h)
	}
}

func TestPlainPasswordStillWorks(t *testing.T) {
	if IsHash("hunter2") || !Verify("hunter2", "hunter2") || Verify("hunter2", "hunter3") {
		t.Error("a password typed into config.ini by hand must work until it's hashed")
	}
	if Verify("", "") || Verify("", "x") {
		t.Error("no password set must never match")
	}
	// Looks like a hash but isn't one we wrote: a plain password, compared as typed.
	for _, s := range []string{"pbkdf2-sha256$x$y$z", "pbkdf2-sha256$1000$$", "pbkdf2-sha256$0$YQ$YQ"} {
		if IsHash(s) || !Verify(s, s) {
			t.Errorf("%q", s)
		}
	}
	if h, _ := Hash(""); h != "" {
		t.Error("no password must stay no password")
	}
}

func TestLocalLogin(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.ini")
	write := func(s string) { os.WriteFile(p, []byte(s), 0o600) }
	if LocalLogin(p) != nil {
		t.Error("no file")
	}
	write("[Auth]\ncontrol_token = tok\nclub_password =\n")
	if LocalLogin(p) != nil {
		t.Error("no password: nothing to log in with")
	}
	write("[Auth]\ncontrol_token = tok\nclub_password = typed-by-hand\n")
	if b := LocalLogin(p); b["password"] != "typed-by-hand" || len(b) != 1 {
		t.Errorf("plain: %v", b)
	}
	h, _ := Hash("pw")
	write("[Auth]\ncontrol_token = tok\nclub_password = " + h + "\n")
	if b := LocalLogin(p); b["local_key"] != "tok" || len(b) != 1 {
		t.Errorf("hashed: %v", b)
	}
}
