package clubhouse

// The laptop's side of keeping the screens up to date (screenupdate.go is the screen's):
// which version each kind of screen says it is, kept in clubhouse_screen.json for the
// pre-match check, and "Update the screen", passed on in the status the screens poll.

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"time"

	"cricketstream/internal/assets"
)

// updateAskFor: how long "Update the screen" stays in the status. A screen polls every
// couple of seconds when it's up; one that's off when it's pressed checks at its next start
// anyway.
var updateAskFor = 15 * time.Minute

var versionOK = regexp.MustCompile(`^[0-9A-Za-z.+-]{1,40}$`)

// cleanVersion is a header's version, or "" for anything that isn't one (it's shown on
// the laptop's card and written to a file).
func cleanVersion(v string) string {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if !versionOK.MatchString(v) {
		return ""
	}
	return v
}

// noteVersion records a screen's version (l.mu held), saving it when it changes.
func (l *Link) noteVersion(dst *string, header string, view bool) {
	v := cleanVersion(header)
	if v == "" || v == *dst {
		return
	}
	*dst = v
	rec := LastScreenVersions()
	if rec == nil {
		rec = &ScreenVersions{}
	}
	if view {
		rec.View = v
	} else {
		rec.Control = v
	}
	rec.At = time.Now()
	saveScreenVersions(*rec)
}

// Versions is what the screens last said they were ("" for one not heard from since the
// link started).
func (l *Link) Versions() (control, view string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.version, l.viewVersion
}

// AskUpdate is "Update the screen" on the laptop.
func (l *Link) AskUpdate() {
	l.mu.Lock()
	l.updateAsk = time.Now()
	l.mu.Unlock()
}

// askedUpdate is the ask's time (unix seconds) while it stands, else 0.
func (l *Link) askedUpdate() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.updateAsk.IsZero() || time.Since(l.updateAsk) > updateAskFor {
		return 0
	}
	return l.updateAsk.Unix()
}

// ScreenVersions is clubhouse_screen.json: the screens' versions as last heard, for the
// pre-match check (the server, which has no link of its own).
type ScreenVersions struct {
	Control string    `json:"control,omitempty"`
	View    string    `json:"view,omitempty"`
	At      time.Time `json:"at"`
}

func screenVersionsFile() string { return assets.Join("clubhouse_screen.json") }

// LastScreenVersions is the file's record, or nil.
func LastScreenVersions() *ScreenVersions {
	b, err := os.ReadFile(screenVersionsFile())
	if err != nil {
		return nil
	}
	var v ScreenVersions
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	v.Control, v.View = cleanVersion(v.Control), cleanVersion(v.View)
	return &v
}

func saveScreenVersions(v ScreenVersions) {
	b, _ := json.Marshal(v)
	tmp := screenVersionsFile() + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, screenVersionsFile())
	}
}
