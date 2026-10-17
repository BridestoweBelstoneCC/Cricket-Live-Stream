package agent

// Finding NV Play's output folder and installing the scoreboard template, against a fake
// home folder (the real one is never read).

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"cricketstream/internal/scorerfile"
)

var nvplay = filepath.Join("Documents", "Cricket Matches", "_Scoreboards", "Output")
var templates = filepath.Join("Documents", "Cricket Matches", "_Scoreboards", "Templates")

func fakeHome(t *testing.T) string {
	home := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	} else {
		t.Setenv("HOME", home)
	}
	return home
}

func mkfolder(t *testing.T, home, rel, file string, age time.Duration) string {
	p := filepath.Join(home, rel)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if file != "" {
		f := filepath.Join(p, file)
		os.WriteFile(f, []byte("{}"), 0o644)
		then := time.Now().Add(-age)
		os.Chtimes(f, then, then)
	}
	return p
}

func detect(t *testing.T, wantFolder, wantHow string) {
	t.Helper()
	f, h := scorerfile.Detect()
	if f != wantFolder || h != wantHow {
		t.Fatalf("got (%q, %q), want (%q, %q)", f, h, wantFolder, wantHow)
	}
}

func TestLiveFolder(t *testing.T) {
	home := fakeHome(t)
	detect(t, mkfolder(t, home, nvplay, "nvplay-scoreboard1.xml", 0), "live")
}

func TestFolderWithAnOldScoreboard(t *testing.T) {
	home := fakeHome(t)
	detect(t, mkfolder(t, home, nvplay, "nvplay-scoreboard1.xml", 30*24*time.Hour), "stale")
}

func TestOneDriveDocuments(t *testing.T) { // Windows 11 often redirects Documents into OneDrive.
	home := fakeHome(t)
	detect(t, mkfolder(t, home, filepath.Join("OneDrive", nvplay), "nvplay-scoreboard1.xml", 0), "live")
}

func TestOneDriveWorkAccount(t *testing.T) {
	home := fakeHome(t)
	detect(t, mkfolder(t, home, filepath.Join("OneDrive - Some Club", nvplay), "nvplay-scoreboard1.xml", 0), "live")
}

func TestNVPlayFolderBeforeAnyMatch(t *testing.T) {
	home := fakeHome(t)
	detect(t, mkfolder(t, home, nvplay, "", 0), "empty")
}

func TestLiveBeatsStale(t *testing.T) {
	home := fakeHome(t)
	mkfolder(t, home, filepath.Join("Documents", "PCS Pro", "Output"), "scoreboard.json", 24*time.Hour)
	detect(t, mkfolder(t, home, filepath.Join("OneDrive", nvplay), "nvplay-scoreboard1.xml", 0), "live")
}

func TestNothingFound(t *testing.T) {
	fakeHome(t)
	detect(t, "", "")
}

func TestTemplateIsBuiltIn(t *testing.T) {
	want, err := os.ReadFile("../../scoreboard.template")
	if err != nil {
		t.Fatal(err)
	}
	if BundledTemplate() != string(want) {
		t.Fatal("built-in template differs from the repo's")
	}
}

func TestInstalledAsAnExactCopy(t *testing.T) {
	folder := mkfolder(t, fakeHome(t), templates, "", 0)
	text := "{\n  \"runs\": \"{{InningsRuns}}\"\n}\n"
	if r := InstallTemplate(folder, text); r != "installed" {
		t.Fatal(r)
	}
	got, _ := os.ReadFile(filepath.Join(folder, TemplateName))
	if string(got) != text { // byte-for-byte: no \r\n on Windows
		t.Fatalf("%q", got)
	}
}

func TestIdenticalCopyLeftAlone(t *testing.T) {
	folder := mkfolder(t, fakeHome(t), templates, "", 0)
	InstallTemplate(folder, "same")
	if r := InstallTemplate(folder, "same"); r != "current" {
		t.Fatal(r)
	}
	if _, err := os.Stat(filepath.Join(folder, TemplateName+".old")); err == nil {
		t.Fatal("made a .old for an identical copy")
	}
}

func TestLineEndingsAloneDontCountAsDifferent(t *testing.T) {
	folder := mkfolder(t, fakeHome(t), templates, "", 0)
	InstallTemplate(folder, "a\r\nb\r\n")
	if r := InstallTemplate(folder, "a\nb\n"); r != "current" {
		t.Fatal(r)
	}
}

func TestDifferentCopyReplacedAndKeptAsOld(t *testing.T) {
	folder := mkfolder(t, fakeHome(t), templates, "", 0)
	os.WriteFile(filepath.Join(folder, TemplateName), []byte("the scorer's old one"), 0o644)
	if r := InstallTemplate(folder, "new"); r != "updated" {
		t.Fatal(r)
	}
	old, _ := os.ReadFile(filepath.Join(folder, TemplateName+".old"))
	if string(old) != "the scorer's old one" {
		t.Fatalf("%q", old)
	}
}

func TestTemplatesFolderBesideTheOutputFolder(t *testing.T) {
	home := fakeHome(t)
	out := mkfolder(t, home, filepath.Join("Elsewhere", "Scoreboards", "Output"), "", 0)
	tpl := mkfolder(t, home, filepath.Join("Elsewhere", "Scoreboards", "Templates"), "", 0)
	if got := FindTemplatesFolder(out); got != tpl {
		t.Fatal(got)
	}
}

func TestTemplatesFolderInOneDrive(t *testing.T) {
	home := fakeHome(t)
	tpl := mkfolder(t, home, filepath.Join("OneDrive", templates), "", 0)
	if got := FindTemplatesFolder(""); got != tpl {
		t.Fatal(got)
	}
}

func TestNoNVPlayMeansNothingWritten(t *testing.T) {
	home := fakeHome(t)
	if r, f := EnsureTemplate(""); r != "" || f != "" {
		t.Fatal(r, f)
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatal("wrote into the home folder")
	}
}

// ── Serving ──────────────────────────────────────────────

func getJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var d map[string]any
	json.NewDecoder(resp.Body).Decode(&d)
	return d
}

func TestServesTheScoreboardAndSkipsUnchanged(t *testing.T) {
	folder := t.TempDir()
	os.WriteFile(filepath.Join(folder, "nvplay-scoreboard1.xml"), []byte("{\"runs\": \"5\"}\r\n"), 0o644)
	st := NewState(folder, 8788)
	srv := httptest.NewServer(st.Handler())
	defer srv.Close()

	d := getJSON(t, srv.URL+"/pcs")
	if d["ok"] != true || d["content"] != "{\"runs\": \"5\"}\n" || d["name"] != "nvplay-scoreboard1.xml" {
		t.Fatal(d)
	}
	mtime := d["mtime"].(float64)
	d = getJSON(t, srv.URL+"/pcs?since="+jsonNum(mtime))
	if d["unchanged"] != true {
		t.Fatal(d)
	}
	if p := getJSON(t, srv.URL+"/ping"); p["service"] != ServiceName || p["file"] != "nvplay-scoreboard1.xml" {
		t.Fatal(p)
	}
	if n, _, _ := st.snapshot(); n != 3 {
		t.Fatal("requests not counted", n)
	}
}

func jsonNum(f float64) string { b, _ := json.Marshal(f); return string(b) }

func TestNoFileIsExplained(t *testing.T) {
	srv := httptest.NewServer(NewState(t.TempDir(), 8788).Handler())
	defer srv.Close()
	if d := getJSON(t, srv.URL+"/pcs"); d["ok"] != false || d["error"] != "no_file" {
		t.Fatal(d)
	}
}

func TestDiscoveryAnswersTheMagicOnly(t *testing.T) {
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	st := NewState(t.TempDir(), 8788)
	go st.RespondToDiscovery(pc)
	defer pc.Close()
	c, err := net.Dial("udp4", pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("HELLO"))
	c.Write(append(append([]byte{}, DiscoveryMagic...), " v1"...))
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 4096)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if json.Unmarshal(buf[:n], &d) != nil || d["service"] != ServiceName || d["port"].(float64) != 8788 {
		t.Fatalf("%s", buf[:n])
	}
}
