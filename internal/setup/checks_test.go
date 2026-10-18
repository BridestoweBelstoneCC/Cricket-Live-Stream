package setup

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cricketstream/internal/ini"
)

func fakePlayCricket(t *testing.T, h http.HandlerFunc) *[]string {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.RawQuery)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	saved := PlayCricketURL
	PlayCricketURL = srv.URL
	t.Cleanup(func() { PlayCricketURL = saved })
	return &seen
}

func TestCheckPlayCricket(t *testing.T) {
	seen := fakePlayCricket(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("api_token") {
		case "good":
			w.Write([]byte(`{"matches":[{},{},{}]}`))
		case "quiet":
			w.Write([]byte(`{"matches":[]}`))
		case "html":
			w.Write([]byte(`<html>maintenance</html>`))
		default:
			w.WriteHeader(401)
		}
	})
	for _, c := range []struct{ key, id, status, detail string }{
		{"good", "4242", "ok", "Key works — 3 fixtures this season."},
		{"quiet", "4242", "warn", "no fixtures this season"},
		{"nope", "4242", "bad", "rejected the API key"},
		{"html", "4242", "bad", "wasn't a fixture list"},
		{"", "4242", "warn", "No API key"},
		{"good", "abc", "bad", "number in your club's"},
		{"good", "", "bad", "number in your club's"},
	} {
		got := CheckPlayCricket(c.key, c.id)
		if got.Status != c.status || !strings.Contains(got.Detail, c.detail) {
			t.Errorf("%q/%q: %+v", c.key, c.id, got)
		}
	}
	if !strings.Contains((*seen)[0], "site_id=4242") || !strings.Contains((*seen)[0], "season=") {
		t.Errorf("query %q", (*seen)[0])
	}
}

func TestCheckPlayCricketOffline(t *testing.T) {
	saved := PlayCricketURL
	PlayCricketURL = "http://127.0.0.1:1/nothing"
	defer func() { PlayCricketURL = saved }()
	if got := CheckPlayCricket("k", "1"); got.Status != "bad" || !strings.Contains(got.Detail, "online") {
		t.Errorf("%+v", got)
	}
}

func TestCheckFolder(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "live")
	os.MkdirAll(live, 0o755)
	os.WriteFile(filepath.Join(live, "nvplay-scoreboard1.xml"), []byte("x"), 0o644)
	old := filepath.Join(dir, "old")
	os.MkdirAll(old, 0o755)
	f := filepath.Join(old, "nvplay-scoreboard1.xml")
	os.WriteFile(f, []byte("x"), 0o644)
	then := time.Now().Add(-48 * time.Hour)
	os.Chtimes(f, then, then)
	empty := filepath.Join(dir, "empty")
	os.MkdirAll(empty, 0o755)
	for _, c := range []struct{ path, status, detail string }{
		{live, "ok", "writing to it right now"},
		{old, "ok", "Found a scoreboard file from"},
		{empty, "warn", "no scoreboard file yet"},
		{filepath.Join(dir, "nope"), "bad", "doesn't exist"},
		{f, "bad", "a file, not a folder"},
		{"", "warn", "No folder"},
		{`"` + live + `"`, "ok", "writing"}, // pasted with quotes from Explorer's "Copy as path"
	} {
		got := CheckFolder(c.path)
		if got.Status != c.status || !strings.Contains(got.Detail, c.detail) {
			t.Errorf("%q: %+v", c.path, got)
		}
	}
}

// A blank key on a configured machine means "the saved one".
func TestRunCheckUsesTheSavedKeyWhenTheFieldIsBlank(t *testing.T) {
	dir := appDir(t)
	os.WriteFile(filepath.Join(dir, "config.ini"), []byte("[API]\nplaycricket_key = good\n"), 0o644)
	fakePlayCricket(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api_token") == "good" {
			w.Write([]byte(`{"matches":[{}]}`))
			return
		}
		w.WriteHeader(401)
	})
	code, out := RunCheck([]byte(`{"what":"playcricket","club_id":"1"}`))
	if c, _ := out.(Check); code != 200 || c.Status != "ok" {
		t.Fatalf("%d %+v", code, out)
	}
	code, out = RunCheck([]byte(`{"what":"playcricket","key":"typed-wrong","club_id":"1"}`))
	if c, _ := out.(Check); c.Status != "bad" {
		t.Fatalf("a typed key wasn't the one tried: %+v", out)
	}
	if code, _ := RunCheck([]byte(`{"what":"nothing"}`)); code != 400 {
		t.Error("unknown check accepted")
	}
	if code, _ := RunCheck([]byte(`not json`)); code != 400 {
		t.Error("garbage accepted")
	}
}

// Removing a saved secret has to be asked for: a blank field keeps it.
func TestSaveRemovesASecretOnlyWhenAsked(t *testing.T) {
	dir := appDir(t)
	os.WriteFile(filepath.Join(dir, "config.ini"), []byte("[App]\nrole = streaming\n\n[API]\nanthropic_key = sk-1\nplaycricket_key = pc-1\n\n[Auth]\nclub_password = pw\n"), 0o644)
	body, _ := json.Marshal(map[string]any{"role": "streaming", "name": "X CC", "pc_id": "1", "clear": []string{"club_password", "anthropic_key"}})
	if code, out, _ := Save(body); code != 200 {
		t.Fatal(code, out)
	}
	f, _ := ini.Load(filepath.Join(dir, "config.ini"))
	if f.Get("Auth", "club_password", "?") != "" || f.Get("API", "anthropic_key", "?") != "" {
		t.Error("asked-for removals didn't happen")
	}
	if f.Get("API", "playcricket_key", "") != "pc-1" {
		t.Error("a secret nobody asked to remove went")
	}
	info := Info()
	if set, _ := info["secrets_set"].(map[string]bool); set["club_password"] || !set["pc_key"] {
		t.Errorf("secrets_set %v", info["secrets_set"])
	}
	b, _ := json.Marshal(info)
	if strings.Contains(string(b), "pc-1") {
		t.Error("a secret's value reached the page")
	}
}
