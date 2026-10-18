package server

// The club's pictures, managed from the control panel's Pictures tab instead of by
// dropping files into folders: club badges, player photos, sponsor logos and match
// photos. The folders are still where they live (and where the overlay, the result card
// and the export read them), so a club that already has them loses nothing — but nobody
// has to find them, or know that a badge must be named after a PlayCricket club ID.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

type libraryKind struct {
	label string
	dir   func(M) string
	exts  []string
}

var libraryKinds = map[string]libraryKind{
	"badges":    {"club badge", logosFolder, []string{"png", "webp", "jpg", "jpeg"}},
	"headshots": {"player photo", headshotsFolder, []string{"png", "jpg", "jpeg", "webp"}},
	"sponsors":  {"sponsor logo", func(M) string { return SponsorDir }, sponsorImageExts},
	"photos":    {"match photo", func(M) string { return socialsRoot() }, []string{"jpg", "jpeg", "png", "webp"}},
}

var photoTeam = regexp.MustCompile(`^(youth|[1-9][0-9]?(st|nd|rd|th))$`)

// libraryKindFor is a kind by name; "photos:2nd" is the 2nd XI's own match photos
// (socials/2nd, which the result card prefers for that team), "photos:youth" the stock
// photos used for youth sides.
func libraryKindFor(kind string) (libraryKind, error) {
	if team, ok := strings.CutPrefix(kind, "photos:"); ok {
		if !photoTeam.MatchString(team) {
			return libraryKind{}, fmt.Errorf("unknown team %q", team)
		}
		k := libraryKinds["photos"]
		k.dir = func(M) string { return filepath.Join(socialsRoot(), team) }
		return k, nil
	}
	k, ok := libraryKinds[kind]
	if !ok {
		return k, fmt.Errorf("unknown kind of picture %q", kind)
	}
	return k, nil
}

var libraryNameJunk = regexp.MustCompile(`[^\p{L}\p{N} .'_-]+`)

// libraryName makes a typed name safe as a file name: letters, numbers, spaces and
// . ' _ - only, no leading dots, at most 60 characters. "" if nothing's left.
func libraryName(raw string) string {
	n := libraryNameJunk.ReplaceAllString(strings.TrimSpace(raw), "")
	n = strings.Join(strings.Fields(n), " ")
	n = strings.TrimLeft(n, ". ")
	if r := []rune(n); len(r) > 60 {
		n = strings.TrimSpace(string(r[:60]))
	}
	return n
}

func hasExt(name string, exts []string) (stem, ext string, ok bool) {
	i := strings.LastIndex(name, ".")
	if i <= 0 {
		return "", "", false
	}
	ext = strings.ToLower(name[i+1:])
	for _, e := range exts {
		if e == ext {
			return name[:i], ext, true
		}
	}
	return "", "", false
}

// LibraryList is GET /library?kind=: the pictures of one kind, with what the panel needs
// to label them (whose badge, which players still need a photo).
func LibraryList(kind string) (M, error) {
	k, err := libraryKindFor(kind)
	if err != nil {
		return nil, err
	}
	st := LoadState()
	dir := k.dir(st)
	items := []M{}
	var files []string
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			stem, _, ok := hasExt(e.Name(), k.exts)
			if !ok {
				continue
			}
			files = append(files, e.Name())
			info, _ := e.Info()
			item := M{"file": e.Name(), "name": stem}
			if info != nil {
				item["size"], item["mtime"] = info.Size(), float64(info.ModTime().Unix())
			}
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return strings.ToLower(gs(items[i], "name", "")) < strings.ToLower(gs(items[j], "name", ""))
	})
	out := M{"kind": kind, "items": items}
	switch kind {
	case "badges":
		home, away := trim(gs(st, "home_club_id", "")), trim(gs(st, "away_club_id", ""))
		out["home"] = M{"id": home, "team": gs(st, "home_team", ""), "has": home != "" && badgeFile(dir, home) != ""}
		out["away"] = M{"id": away, "team": gs(st, "away_team", ""), "has": away != "" && badgeFile(dir, away) != ""}
		for _, it := range items {
			switch gs(it, "name", "") {
			case home:
				it["who"] = "home"
			case away:
				it["who"] = "away"
			}
		}
	case "headshots":
		// The squad roster's players, and whether the scorebar would find a photo for
		// each — by the same rules it uses (matchHeadshot), so the two never disagree.
		var squad []M
		for num, v := range gm(st, "roster") {
			name, _ := v.(string)
			name = trim(name)
			if name == "" {
				continue
			}
			squad = append(squad, M{"num": num, "name": name, "has": matchHeadshot(files, name, num, true) != ""})
		}
		sort.Slice(squad, func(i, j int) bool { return gs(squad[i], "name", "") < gs(squad[j], "name", "") })
		out["squad"] = squad
	case "sponsors":
		out["current"] = trim(gs(st, "sponsor_id", ""))
	}
	return out, nil
}

// LibrarySave adds (or replaces) a picture: data must really be an image; name is what
// it's called (a club ID, a player's name, a sponsor number). A sponsor logo with no name
// gets the next number, as the sponsor card's upload does. Returns the file name.
func LibrarySave(kind, name string, data []byte) (string, error) {
	k, err := libraryKindFor(kind)
	if err != nil {
		return "", err
	}
	ext := sniffImage(data)
	if ext == "" {
		return "", fmt.Errorf("that isn't a PNG, JPEG, WebP or GIF image")
	}
	if _, _, ok := hasExt("x."+ext, k.exts); !ok {
		return "", fmt.Errorf("a %s can't be a %s — use PNG, JPEG or WebP", k.label, strings.ToUpper(ext))
	}
	stem := libraryName(name)
	if kind == "sponsors" && stem == "" {
		ok, id := saveSponsorLogo(data)
		if !ok {
			return "", fmt.Errorf("%s", id)
		}
		return findSponsorFile(id), nil
	}
	if kind == "badges" && stem != "" && !isDigits(stem) && !anyLetter(stem) {
		return "", fmt.Errorf("a club badge is named by the club's PlayCricket ID (or its name)")
	}
	if stem == "" {
		return "", fmt.Errorf("give the %s a name", k.label)
	}
	dir := k.dir(LoadState())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	removeStem(dir, stem, k.exts) // replacing: the old one may be another format
	file := stem + "." + ext
	p := filepath.Join(dir, file)
	if err := os.WriteFile(p+".tmp", data, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(p+".tmp", p); err != nil {
		return "", err
	}
	return file, nil
}

func anyLetter(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

func findSponsorFile(id string) string {
	if p := findSponsorImage(id); p != "" {
		return filepath.Base(p)
	}
	return id
}

// removeStem deletes every picture called stem (whatever its format).
func removeStem(dir, stem string, exts []string) {
	for _, e := range exts {
		os.Remove(filepath.Join(dir, stem+"."+e))
	}
}

// libraryFile is one existing picture's path, refusing anything that isn't a plain file
// name of the right kind in that kind's folder.
func libraryFile(kind, file string) (string, libraryKind, error) {
	k, err := libraryKindFor(kind)
	if err != nil {
		return "", k, err
	}
	if file == "" || file != filepath.Base(file) || strings.ContainsAny(file, `/\:`) || strings.HasPrefix(file, ".") {
		return "", k, fmt.Errorf("not a picture name")
	}
	if _, _, ok := hasExt(file, k.exts); !ok {
		return "", k, fmt.Errorf("not a %s", k.label)
	}
	p := filepath.Join(k.dir(LoadState()), file)
	if !fileExists(p) {
		return "", k, fmt.Errorf("that %s isn't there any more", k.label)
	}
	return p, k, nil
}

// LibraryDelete removes one picture.
func LibraryDelete(kind, file string) error {
	p, _, err := libraryFile(kind, file)
	if err != nil {
		return err
	}
	return os.Remove(p)
}

// LibraryRename gives a picture a new name (a different club ID, a corrected spelling).
func LibraryRename(kind, file, name string) (string, error) {
	p, k, err := libraryFile(kind, file)
	if err != nil {
		return "", err
	}
	stem := libraryName(name)
	if stem == "" {
		return "", fmt.Errorf("give the %s a name", k.label)
	}
	_, ext, _ := hasExt(file, k.exts)
	to := stem + "." + ext
	if to == file {
		return file, nil
	}
	dir := filepath.Dir(p)
	for _, e := range k.exts {
		if other := filepath.Join(dir, stem+"."+e); fileExists(other) && !strings.EqualFold(other, p) {
			return "", fmt.Errorf("there's already a %s called %s — remove that one first", k.label, stem)
		}
	}
	if err := os.Rename(p, filepath.Join(dir, to)); err != nil {
		return "", err
	}
	return to, nil
}

// LibraryOpen is one picture's bytes, for the panel's thumbnails.
func LibraryOpen(kind, file string) (io.ReadCloser, string, error) {
	p, _, err := libraryFile(kind, file)
	if err != nil {
		return nil, "", err
	}
	f, err := os.Open(p)
	k, _ := libraryKindFor(kind)
	_, ext, _ := hasExt(file, k.exts)
	mime := map[string]string{"png": "image/png", "jpg": "image/jpeg", "jpeg": "image/jpeg", "webp": "image/webp", "gif": "image/gif"}[ext]
	return f, mime, err
}
