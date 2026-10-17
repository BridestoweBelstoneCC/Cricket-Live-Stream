package ini

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"testing"
)

const sample = "\xef\xbb\xbf# CricketStream Overlay — Club Configuration\n" +
	"[Club]\nName = Home CC\nabbreviation: HCC\n; a comment\n\n" +
	"[Auth]\nclub_password = 50%%off\ncontrol_token =\n" +
	"[Multi]\nlist = one\n  two\n\tthree\n"

func TestParseLikeConfigparser(t *testing.T) {
	f, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ s, k, want string }{
		{"Club", "name", "Home CC"}, {"Club", "NAME", "Home CC"}, {"Club", "abbreviation", "HCC"},
		{"Auth", "club_password", "50%off"}, {"Auth", "control_token", ""}, {"Multi", "list", "one\ntwo\nthree"},
	} {
		if got := f.Get(c.s, c.k, "?"); got != c.want {
			t.Errorf("%s/%s: got %q, want %q", c.s, c.k, got, c.want)
		}
	}
	if f.Get("Nope", "x", "fb") != "fb" || f.GetInt("Club", "name", 7) != 7 {
		t.Fatal("fallbacks")
	}
}

func TestRoundTripThroughPython(t *testing.T) {
	py, err := exec.LookPath("python")
	if err != nil {
		t.Skip("no python")
	}
	f := New()
	f.Set("Auth", "club_password", "50%off")
	f.Set("Club", "Name", "Home & District CC")
	f.Set("Club", "motto", "")
	path := filepath.Join(t.TempDir(), "c.ini")
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(py, "-I", "-c", `import configparser, sys
c = configparser.ConfigParser(); c.read(sys.argv[1], encoding="utf-8")
sys.stdout.write("|".join([c.get("Auth", "club_password"), c.get("Club", "name"), c.get("Club", "motto")]))`, path).Output()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, []byte("50%off|Home & District CC|")) {
		t.Fatalf("%q", out)
	}
}
