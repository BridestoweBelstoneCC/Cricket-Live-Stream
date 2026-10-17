package server

// Uploading the weekend sponsor's logo from the control panel (POST /sponsor/upload),
// with the sponsors folder and state in a temp dir (tests/test_sponsor_upload.py).

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"
)

var (
	pngData  = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	jpegData = append([]byte("\xff\xd8\xff\xe0"), make([]byte, 64)...)
	gifData  = append([]byte("GIF89a"), make([]byte, 64)...)
	webpData = append([]byte("RIFF\x24\x00\x00\x00WEBPVP8 "), make([]byte, 64)...)
	svgData  = []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
)

func TestSniffAcceptedFormats(t *testing.T) {
	var got []string
	for _, d := range [][]byte{pngData, jpegData, gifData, webpData} {
		got = append(got, sniffImage(d))
	}
	eqv(t, got, []string{"png", "jpg", "gif", "webp"})
}

func TestSniffRejected(t *testing.T) {
	// SVG can carry script; a renamed text file is still a text file.
	for _, d := range [][]byte{svgData, []byte("hello"), nil, []byte("RIFF\x00\x00\x00\x00WAVE")} {
		if sniffImage(d) != "" {
			t.Fatalf("%q accepted", d)
		}
	}
}

func newUploadServer(t *testing.T) *testServer {
	s := newTestServer(t, "")
	os.MkdirAll(SponsorDir, 0o755)
	for _, n := range []string{"1.png", "2.jpg", "6.png", "README.MD", "club-logo.png"} {
		os.WriteFile(filepath.Join(SponsorDir, n), nil, 0o644)
	}
	setState(t, M{"sponsor_name": "Acme Builders", "sponsor_id": "2"})
	return s
}

func (s *testServer) upload(data []byte, origin, ctype string) (int, M) {
	s.t.Helper()
	if origin == "" {
		origin = "http://" + s.addr
	}
	if ctype == "" {
		ctype = "image/png"
	}
	r := s.raw("POST", "/sponsor/upload", data, map[string]string{"Content-Type": ctype, "Origin": origin})
	var m M
	json.Unmarshal(r.body, &m)
	return r.status, m
}

func sponsorFiles() []string {
	entries, _ := os.ReadDir(SponsorDir)
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func hasFile(name string) bool { return fileExists(filepath.Join(SponsorDir, name)) }

func TestUploadSavedUnderNextNumberAndUsed(t *testing.T) {
	s := newUploadServer(t)
	st, d := s.upload(pngData, "", "")
	mustEq(t, st, 200)
	mustEq(t, d["ok"], any(true))
	mustEq(t, d["sponsor_id"], any("7"))
	b, _ := os.ReadFile(filepath.Join(SponsorDir, "7.png"))
	if !bytes.Equal(b, pngData) {
		t.Fatal("contents changed")
	}
	mustEq(t, LoadState()["sponsor_id"], any("7"))
}

func TestUploadExtensionFromContentsNotClaim(t *testing.T) {
	s := newUploadServer(t)
	s.upload(jpegData, "", "image/png")
	if !hasFile("7.jpg") {
		t.Fatal(sponsorFiles())
	}
}

func TestUploadServedBackToOverlay(t *testing.T) {
	s := newUploadServer(t)
	s.upload(pngData, "", "")
	r := s.get("/sponsor/7")
	mustEq(t, r.header.Get("Content-Type"), "image/png")
	if !bytes.Equal(r.body, pngData) {
		t.Fatal("served different bytes")
	}
}

func TestUploadNotAnImageRefusedNothingSaved(t *testing.T) {
	s := newUploadServer(t)
	before := sponsorFiles()
	for _, c := range []struct {
		d  []byte
		ct string
	}{{svgData, "image/svg+xml"}, {[]byte("just text"), "image/png"}} {
		st, d := s.upload(c.d, "", c.ct)
		mustEq(t, st, 400)
		mustEq(t, d["ok"], any(false))
	}
	eqv(t, sponsorFiles(), before)
	mustEq(t, LoadState()["sponsor_id"], any("2"))
}

func TestUploadTooBigRefused(t *testing.T) {
	// Refused from the Content-Length alone, before reading megabytes of it — so the
	// client may see the connection drop mid-send rather than the 413 itself.
	s := newUploadServer(t)
	big := append(append([]byte{}, pngData...), make([]byte, SponsorUploadMaxBytes)...)
	if st, _ := s.upload(big, "", ""); st != 413 && st != -1 {
		t.Fatal(st)
	}
	if hasFile("7.png") {
		t.Fatal("oversized upload saved")
	}
}

func TestUploadBiggerThanUsualBodyLimitIsFine(t *testing.T) {
	// Every other POST is capped at 1 MB; a phone-exported logo is often bigger.
	s := newUploadServer(t)
	st, _ := s.upload(append(append([]byte{}, pngData...), make([]byte, MaxBodyBytes+1)...), "", "")
	mustEq(t, st, 200)
}

func TestUploadNeedsLogin(t *testing.T) {
	s := newUploadServer(t)
	ClubPassword = "secret"
	st, _ := s.upload(pngData, "", "")
	mustEq(t, st, 401)
	if hasFile("7.png") {
		t.Fatal("saved without login")
	}
}

func TestUploadCrossSiteRefused(t *testing.T) {
	s := newUploadServer(t)
	st, _ := s.upload(pngData, "https://evil.example", "")
	mustEq(t, st, 403)
	if hasFile("7.png") {
		t.Fatal("saved cross-site")
	}
}

func TestUploadEmptyFolderStartsAtOne(t *testing.T) {
	s := newUploadServer(t)
	os.RemoveAll(SponsorDir)
	_, d := s.upload(gifData, "", "")
	mustEq(t, d["sponsor_id"], any("1"))
	if !hasFile("1.gif") {
		t.Fatal(sponsorFiles())
	}
}

// Two logo uploads landing together crashed one of them: the state save's shared .tmp
// file collided across threads ("Permission denied" on Windows), and both uploads could
// pick the same "next free" ID.

func hammer(n int, fn func(i int) error) []error {
	var mu sync.Mutex
	var errs []error
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := fn(i); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	return errs
}

func TestSimultaneousStateSavesWhileBeingRead(t *testing.T) {
	sandbox(t, "")
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		// Far busier than the real overlay: a read every millisecond, so a save
		// regularly lands while the file is open.
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if f, err := os.Open(StateFile); err == nil {
				f.Read(make([]byte, 4096))
				f.Close()
			}
			time.Sleep(time.Millisecond)
		}
	}()
	errs := hammer(8, func(i int) error {
		for k := 0; k < 5; k++ {
			if err := SaveState(M{"n": i, "k": k}); err != nil {
				return err
			}
		}
		return nil
	})
	close(stop)
	<-done
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	b, _ := os.ReadFile(StateFile)
	var m M
	if json.Unmarshal(b, &m) != nil || m["n"] == nil {
		t.Fatalf("state file: %q", b)
	}
}

func TestSimultaneousUploadsGetTheirOwnIDs(t *testing.T) {
	sandbox(t, "")
	var mu sync.Mutex
	var ids []int
	errs := hammer(8, func(int) error {
		ok, id := saveSponsorLogo(pngData)
		if !ok {
			return os.ErrInvalid
		}
		n, _ := strconv.Atoi(id)
		mu.Lock()
		ids = append(ids, n)
		mu.Unlock()
		return nil
	})
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	sort.Ints(ids)
	eqv(t, ids, []int{1, 2, 3, 4, 5, 6, 7, 8})
	mustEq(t, len(sponsorFiles()), 8)
}
