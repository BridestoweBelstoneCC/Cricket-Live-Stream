package server

// Sponsor logo upload. The panel sends the image itself (handy on a phone at the
// ground); it's saved under the next free numeric ID and becomes the sponsor image.

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"cricketstream/internal/logobg"
)

// sniffImage is the file extension for an accepted image, or "". Judged by the file's
// first bytes, never its name or the browser's claimed type. No SVG: an SVG can carry
// script, and these are served from this origin.
func sniffImage(data []byte) string {
	for _, m := range []struct {
		magic, ext string
	}{{"\x89PNG\r\n\x1a\n", "png"}, {"\xff\xd8\xff", "jpg"}, {"GIF87a", "gif"}, {"GIF89a", "gif"}} {
		if bytes.HasPrefix(data, []byte(m.magic)) {
			return m.ext
		}
	}
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "webp"
	}
	return ""
}

var sponsorUploadMu sync.Mutex

var sponsorImageExts = []string{"png", "jpg", "jpeg", "webp", "gif"}

// findSponsorImage is sponsors/<id>.<ext> for a raster image, or "". The id is reduced
// to a bare file name first, so it can't point outside the folder.
func findSponsorImage(id string) string {
	r := strings.NewReplacer("..", "", "/", "", `\`, "", ":", "")
	name := strings.TrimSpace(filepath.Base(r.Replace(id)))
	if name == "" || name == "." {
		return ""
	}
	for _, ext := range sponsorImageExts {
		p := filepath.Join(SponsorDir, name+"."+ext)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

// sponsorTransparentVersion is a transparent-background version of a sponsor logo, or
// nil and the reason. Nothing is saved.
func sponsorTransparentVersion(id, mode string) (image.Image, string) {
	ok := false
	for _, m := range logobg.Modes {
		ok = ok || m == mode
	}
	if !ok {
		return nil, "Unknown mode."
	}
	p := findSponsorImage(id)
	if p == "" {
		return nil, "No image with that ID in the sponsors folder."
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, "Couldn't read that image (" + err.Error() + ")."
	}
	defer f.Close()
	im, _, err := image.Decode(f)
	if err != nil {
		return nil, "Couldn't read that image (" + err.Error() + ")."
	}
	out, _, why := logobg.RemoveBackground(im, mode)
	if out == nil {
		return nil, why
	}
	return out, ""
}

func pngBytes(im image.Image) ([]byte, error) {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	err := enc.Encode(&buf, im)
	return buf.Bytes(), err
}

// saveTransparentSponsor saves the chosen transparent version as a NEW image (the
// original is kept, so Undo is just going back to its ID) and makes it the sponsor image.
func saveTransparentSponsor(id, mode string) (bool, M) {
	out, why := sponsorTransparentVersion(id, mode)
	if out == nil {
		return false, M{"ok": false, "error": why}
	}
	data, err := pngBytes(out)
	if err != nil {
		return false, M{"ok": false, "error": err.Error()}
	}
	sponsorUploadMu.Lock()
	defer sponsorUploadMu.Unlock()
	newID, err := saveSponsorLogoLocked(data, "png")
	if err != nil {
		return false, M{"ok": false, "error": err.Error()}
	}
	recordSponsorVariant(newID, id)
	return true, M{"ok": true, "sponsor_id": newID, "previous_id": id}
}

// Which sponsor images are transparent copies of which: {copy id: original id}. Without
// it, the result card (which shows every logo in the folder) showed one sponsor twice.
func sponsorVariantsFile() string { return filepath.Join(SponsorDir, ".variants.json") }

func sponsorVariants() map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(sponsorVariantsFile())
	if err != nil {
		return out
	}
	var v map[string]any
	if json.Unmarshal(b, &v) != nil {
		return out
	}
	for k, x := range v {
		out[k] = anyStr(M{"x": x}, "x", "")
	}
	return out
}

// recordSponsorVariant: call under sponsorUploadMu. A copy of a copy points at the
// first original.
func recordSponsorVariant(newID, sourceID string) {
	v := sponsorVariants()
	if orig, ok := v[sourceID]; ok {
		v[newID] = orig
	} else {
		v[newID] = sourceID
	}
	b, _ := json.MarshalIndent(v, "", " ")
	tmp := sponsorVariantsFile() + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, sponsorVariantsFile()) // worst case the card shows a sponsor twice
	}
}

func sameDir(a, b string) bool {
	x, _ := filepath.Abs(a)
	y, _ := filepath.Abs(b)
	return strings.EqualFold(filepath.Clean(x), filepath.Clean(y))
}

// sponsorLogosForCard is one logo per sponsor for the result card: an original and its
// transparent copies count as one, shown as whichever is the sponsor image in use, else
// the newest copy. Ordered by original ID.
func sponsorLogosForCard(dir, currentID string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	variants := map[string]string{}
	if sameDir(dir, SponsorDir) {
		variants = sponsorVariants()
	}
	type member struct{ stem, file string }
	families := map[string][]member{}
	for _, e := range entries {
		l := strings.ToLower(e.Name())
		if !(strings.HasSuffix(l, ".png") || strings.HasSuffix(l, ".jpg") || strings.HasSuffix(l, ".jpeg") || strings.HasSuffix(l, ".webp")) {
			continue
		}
		stem := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		root := stem
		if r, ok := variants[stem]; ok {
			root = r
		}
		families[root] = append(families[root], member{stem, e.Name()})
	}
	less := func(a, b string) bool { // numbers first, in number order; then names
		an, aerr := strconv.Atoi(a)
		bn, berr := strconv.Atoi(b)
		ad, bd := aerr == nil && isDigits(a), berr == nil && isDigits(b)
		if ad != bd {
			return ad
		}
		if ad {
			return an < bn
		}
		return a < b
	}
	roots := make([]string, 0, len(families))
	for r := range families {
		roots = append(roots, r)
	}
	sort.Slice(roots, func(i, j int) bool { return less(roots[i], roots[j]) })
	var out []string
	for _, r := range roots {
		ms := families[r]
		var pick *member
		for i := range ms {
			if ms[i].stem == currentID {
				pick = &ms[i]
				break
			}
		}
		if pick == nil {
			pick = &ms[0]
			for i := range ms[1:] {
				if less(pick.stem, ms[i+1].stem) {
					pick = &ms[i+1]
				}
			}
		}
		out = append(out, filepath.Join(dir, pick.file))
	}
	return out
}

// saveSponsorLogo saves an upload as sponsors/<next free number>.<ext> and makes it the
// sponsor image. Locked: two uploads at once would both pick the same number.
func saveSponsorLogo(data []byte) (bool, string) {
	ext := sniffImage(data)
	if ext == "" {
		return false, "That isn't a PNG, JPEG, WebP or GIF image."
	}
	sponsorUploadMu.Lock()
	defer sponsorUploadMu.Unlock()
	id, err := saveSponsorLogoLocked(data, ext)
	if err != nil {
		return false, err.Error()
	}
	return true, id
}

func saveSponsorLogoLocked(data []byte, ext string) (string, error) {
	if err := os.MkdirAll(SponsorDir, 0o755); err != nil {
		return "", err
	}
	highest := 0
	entries, _ := os.ReadDir(SponsorDir)
	for _, e := range entries {
		stem := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		if isDigits(stem) {
			if n, _ := strconv.Atoi(stem); n > highest {
				highest = n
			}
		}
	}
	id := strconv.Itoa(highest + 1)
	p := filepath.Join(SponsorDir, id+"."+ext)
	if err := os.WriteFile(p+".tmp", data, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(p+".tmp", p); err != nil {
		return "", err
	}
	UpdateState(func(s M) { s["sponsor_id"] = id })
	return id, nil
}
