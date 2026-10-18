package server

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, 4, 4))
	im.Set(1, 1, color.RGBA{200, 30, 30, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func (s *testServer) addPicture(kind, name string, data []byte, headers map[string]string) (int, M) {
	s.t.Helper()
	h := map[string]string{"Content-Type": "application/octet-stream", "Origin": "http://" + s.addr}
	for k, v := range headers {
		h[k] = v
	}
	r := s.raw("POST", "/library/upload?kind="+kind+"&name="+url.QueryEscape(name), data, h)
	var m M
	json.Unmarshal(r.body, &m)
	return r.status, m
}

// Badges, player photos and match photos added from the Pictures tab land where the
// overlay and the result card look for them — named so they're found — and can be
// renamed and removed there too.
func TestPicturesTabAddsRenamesAndRemoves(t *testing.T) {
	s := newTestServer(t, "")
	setState(t, M{"home_club_id": "29434", "away_club_id": "5512", "home_team": "Bridestowe CC", "away_team": "Visitors CC",
		"roster": M{"7": "John Walker", "11": "Sam Penberthy"}})
	pic := tinyPNG(t)

	// Our badge, by club ID: the overlay's /logo/<id> serves it at once.
	if st, d := s.addPicture("badges", "29434", pic, nil); st != 200 || d["file"] != "29434.png" {
		t.Fatalf("badge: %d %v", st, d)
	}
	if r := s.get("/logo/29434"); r.status != 200 || !bytes.Equal(r.body, pic) {
		t.Errorf("the overlay doesn't see the badge: %d", r.status)
	}
	_, list := s.getJSON("/library?kind=badges")
	if list["home"].(M)["has"] != true || list["away"].(M)["has"] != false {
		t.Errorf("badge status %v / %v", list["home"], list["away"])
	}
	if it := list["items"].([]any)[0].(M); it["who"] != "home" {
		t.Errorf("our badge not marked: %v", it)
	}

	// A player photo by name: the scorebar's photo lookup finds it, and the squad list
	// says who still needs one — by the scorebar's own rules.
	if st, d := s.addPicture("headshots", "John Walker", pic, nil); st != 200 || d["file"] != "John Walker.png" {
		t.Fatalf("photo: %d %v", st, d)
	}
	if r := s.get("/headshot/WALKER"); r.status != 200 {
		t.Errorf("the scorebar doesn't find J Walker's photo: %d", r.status)
	}
	_, list = s.getJSON("/library?kind=headshots")
	has := map[string]bool{}
	for _, p := range list["squad"].([]any) {
		has[p.(M)["name"].(string)] = p.(M)["has"].(bool)
	}
	if !has["John Walker"] || has["Sam Penberthy"] {
		t.Errorf("squad photo status %v", has)
	}

	// Rename (a misspelling) and the thumbnail follows; a clash is refused.
	if st, d := s.postJSON("/library/rename", M{"kind": "headshots", "file": "John Walker.png", "name": "J Walker"}); st != 200 || d["file"] != "J Walker.png" {
		t.Fatalf("rename: %d %v", st, d)
	}
	s.addPicture("headshots", "Sam Penberthy", pic, nil)
	if st, _ := s.postJSON("/library/rename", M{"kind": "headshots", "file": "Sam Penberthy.png", "name": "J Walker"}); st != 400 {
		t.Errorf("rename onto another photo: %d", st)
	}
	if r := s.get("/library/file?kind=headshots&file=" + url.QueryEscape("J Walker.png")); r.status != 200 || !bytes.Equal(r.body, pic) {
		t.Errorf("thumbnail: %d", r.status)
	}

	// Replacing keeps one file per name, whatever format the new one is.
	jpg := append([]byte("\xff\xd8\xff\xe0"), make([]byte, 40)...)
	if st, d := s.addPicture("badges", "29434", jpg, nil); st != 200 || d["file"] != "29434.jpg" {
		t.Fatalf("replace: %d %v", st, d)
	}
	if fileExists(filepath.Join(logosFolder(LoadState()), "29434.png")) {
		t.Error("the old badge was left beside the new one")
	}

	// Match photos (the result card's backdrops) go in the socials folder.
	if st, _ := s.addPicture("photos", "Opening partnership", pic, nil); st != 200 {
		t.Fatalf("match photo: %d", st)
	}
	if !fileExists(filepath.Join(socialsRoot(), "Opening partnership.png")) {
		t.Error("match photo not in the socials folder")
	}

	// Remove.
	if st, _ := s.postJSON("/library/delete", M{"kind": "photos", "file": "Opening partnership.png"}); st != 200 {
		t.Errorf("delete: %d", st)
	}
	if fileExists(filepath.Join(socialsRoot(), "Opening partnership.png")) {
		t.Error("not removed")
	}
}

// A sponsor logo with no name gets the next number and becomes this weekend's, as the
// sponsor card's own upload does.
func TestPicturesTabSponsorLogo(t *testing.T) {
	s := newTestServer(t, "")
	os.MkdirAll(SponsorDir, 0o755)
	os.WriteFile(filepath.Join(SponsorDir, "4.png"), tinyPNG(t), 0o644)
	st, d := s.addPicture("sponsors", "", tinyPNG(t), nil)
	if st != 200 || d["file"] != "5.png" {
		t.Fatalf("%d %v", st, d)
	}
	_, list := s.getJSON("/library?kind=sponsors")
	if list["current"] != "5" || len(list["items"].([]any)) != 2 {
		t.Errorf("%v", list)
	}
}

// What the Pictures tab won't do: anything outside its folders, non-images, unknown kinds,
// and nothing at all without the login or from another site.
func TestPicturesTabRefuses(t *testing.T) {
	s := newTestServer(t, "pw")
	h := s.bearer("pw")
	pic := tinyPNG(t)
	if st, _ := s.addPicture("badges", "1", pic, nil); st != 401 {
		t.Errorf("upload without the login: %d", st)
	}
	if st, _ := s.getJSON("/library?kind=badges"); st != 401 {
		t.Errorf("list without the login: %d", st)
	}
	if st, _ := s.addPicture("badges", "1", pic, map[string]string{"Origin": "http://evil.example", "Authorization": h["Authorization"]}); st != 403 {
		t.Errorf("upload from another site: %d", st)
	}
	if st, d := s.addPicture("badges", "1", []byte("<svg onload=alert(1)>"), h); st != 400 || !strings.Contains(d["error"].(string), "isn't a PNG") {
		t.Errorf("not an image: %d %v", st, d)
	}
	if st, _ := s.addPicture("fonts", "x", pic, h); st != 400 {
		t.Errorf("unknown kind: %d", st)
	}
	// A name can't climb out of the folder: it's cleaned to a plain file name.
	if st, d := s.addPicture("headshots", `..\..\config`, pic, h); st != 200 || strings.ContainsAny(d["file"].(string), `/\`) || strings.HasPrefix(d["file"].(string), ".") {
		t.Errorf("traversal name: %d %v", st, d)
	}
	if fileExists(filepath.Join(appDir, "config.png")) {
		t.Error("a picture escaped its folder")
	}
	for _, bad := range []string{"../config.ini", `..\config.ini`, "config.ini", ".hidden.png", ""} {
		if st, _ := s.postJSON("/library/delete", M{"kind": "headshots", "file": bad}, h); st != 400 {
			t.Errorf("delete %q: %d", bad, st)
		}
	}
	if r := s.get("/library/file?kind=badges&file=..%2Fconfig.ini"); r.status == 200 {
		t.Error("served a file outside the pictures")
	}
}

func TestLibraryName(t *testing.T) {
	for in, want := range map[string]string{
		"  John   Walker ":      "John Walker",
		"O'Neill":               "O'Neill",
		"José Ñúñez":            "José Ñúñez",
		`..\..\config`:          "config",
		"../../etc/passwd":      "etcpasswd",
		"<script>":              "script",
		"...":                   "",
		"12":                    "12",
		strings.Repeat("a", 80): strings.Repeat("a", 60),
	} {
		if got := libraryName(in); got != want {
			t.Errorf("libraryName(%q) = %q, want %q", in, got, want)
		}
	}
}

// A team's own match photos go in its own folder, which the result card prefers for that
// team; anything but a real team key is refused.
func TestPicturesTabTeamPhotos(t *testing.T) {
	s := newTestServer(t, "")
	pic := tinyPNG(t)
	if st, _ := s.addPicture("photos:2nd", "Tea interval", pic, nil); st != 200 {
		t.Fatalf("2nd XI photo: %d", st)
	}
	if !fileExists(filepath.Join(socialsRoot(), "2nd", "Tea interval.png")) {
		t.Error("not in socials/2nd")
	}
	if got := socialsDir("2nd"); got != filepath.Join(socialsRoot(), "2nd") {
		t.Errorf("the result card wouldn't use it: %s", got)
	}
	_, list := s.getJSON("/library?kind=photos:2nd")
	if len(list["items"].([]any)) != 1 {
		t.Errorf("2nd XI list %v", list)
	}
	_, all := s.getJSON("/library?kind=photos")
	if len(all["items"].([]any)) != 0 {
		t.Errorf("the 2nd XI's photo shows under every team: %v", all)
	}
	for _, bad := range []string{"photos:..", "photos:../x", "photos:2ndXI", "photos:", "photos:youth/.."} {
		if st, _ := s.addPicture(bad, "x", pic, nil); st != 400 {
			t.Errorf("%s: %d", bad, st)
		}
	}
}
