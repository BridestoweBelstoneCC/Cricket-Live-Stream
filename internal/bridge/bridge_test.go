package bridge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"cricketstream/internal/assets"
)

func get(t *testing.T, url, token string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if token != "" {
		req.Header.Set("X-Bridge-Token", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var d map[string]any
	json.NewDecoder(resp.Body).Decode(&d)
	return resp.StatusCode, d
}

func TestTokenGatesTheScoreButNotThePing(t *testing.T) {
	folder := t.TempDir()
	os.WriteFile(filepath.Join(folder, "nvplay-scoreboard1.xml"), []byte("<x/>"), 0o644)
	srv := httptest.NewServer(Handler(folder, "secret"))
	defer srv.Close()
	if code, d := get(t, srv.URL+"/pcs/ping", ""); code != 200 || d["ok"] != true {
		t.Fatal(code, d)
	}
	if code, _ := get(t, srv.URL+"/pcs/latest", ""); code != 401 {
		t.Fatal(code)
	}
	if code, _ := get(t, srv.URL+"/pcs/latest", "wrong"); code != 401 {
		t.Fatal(code)
	}
	code, d := get(t, srv.URL+"/pcs/latest", "secret")
	if code != 200 || d["content"] != "<x/>" || d["filename"] != "nvplay-scoreboard1.xml" {
		t.Fatal(code, d)
	}
	if code, _ := get(t, srv.URL+"/elsewhere", "secret"); code != 404 {
		t.Fatal(code)
	}
}

// A blank configured token must never authenticate a blank/missing header.
func TestBlankTokenNeverAuthenticates(t *testing.T) {
	srv := httptest.NewServer(Handler(t.TempDir(), ""))
	defer srv.Close()
	if code, _ := get(t, srv.URL+"/pcs/latest", ""); code != 401 {
		t.Fatal(code)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	assets.SetDir(t.TempDir())
	want := Config{Folder: `C:\Scores\Output`, Port: 5051, Token: NewToken()}
	if err := Save(want); err != nil {
		t.Fatal(err)
	}
	got, ok := Load()
	if !ok || got != want {
		t.Fatal(got, ok)
	}
	if len(want.Token) != 32 {
		t.Fatal(want.Token)
	}
}
