package clubhouse

// Pairing without typing: the screen asks, the laptop says yes. The screen sends each
// laptop it finds a secret of its own; the laptop gives it a three-digit code to show and
// lists the request on its Clubhouse card. Whoever's at the laptop checks the code on the
// screen matches and presses Match-day screen or Score-only; the next time the screen
// asks (with its secret), it's handed that key — once.
//
// Asking needs no key (that's the point), so it can't be allowed to do anything else:
// the laptop chooses the code (a guesser can't put the screen's code on the card), a
// request shows only on the laptop's own card, an address has one request at a time and
// there are only a few at once, and the key goes only to the secret's holder after a
// person said yes. Plain HTTP on the club network, like the typed key.

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"
	"unicode"
)

// PairRequest is a screen asking, as the laptop's card shows it.
type PairRequest struct {
	ID     string `json:"id"`
	Code   string `json:"code"`
	Name   string `json:"name"` // the screen's computer name, as it said (not proof of anything)
	From   string `json:"from"`
	AgoSec int    `json:"ago"`
}

type askEntry struct {
	PairRequest
	secret  string
	at      time.Time
	answer  string // "", "control", "view" or "no"
	settled time.Time
}

// askFor is how long a request waits for an answer; maxAsks how many wait at once.
var askFor = 10 * time.Minute

const (
	maxAsks       = 6
	askSecretLen  = 32 // hex characters
	maxAskName    = 40
	askAnswerKept = time.Minute // a "no" stays for the screen to collect
)

func randomDigits(n int) string {
	out := make([]byte, n)
	for i := range out {
		d, _ := rand.Int(rand.Reader, big.NewInt(10))
		out[i] = byte('0' + d.Int64())
	}
	return string(out)
}

func randomID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func validSecret(s string) bool {
	if len(s) != askSecretLen {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// cleanName keeps a screen's name to a short line of printable text: it's shown on the
// laptop's card, and anyone on the network can send one.
func cleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, strings.TrimSpace(s))
	if r := []rune(s); len(r) > maxAskName {
		s = string(r[:maxAskName])
	}
	return s
}

// pruneLocked drops requests that waited too long, and answers already collected or
// left uncollected for a minute. l.mu held.
func (l *Link) pruneLocked(now time.Time) {
	for id, e := range l.asks {
		if now.Sub(e.at) > askFor || (!e.settled.IsZero() && now.Sub(e.settled) > askAnswerKept) {
			delete(l.asks, id)
		}
	}
}

// ask: POST /clubhouse/ask {secret, name} → {code}.
func (l *Link) ask(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Secret string `json:"secret"`
		Name   string `json:"name"`
	}
	if !readJSON(r, &body) || !validSecret(body.Secret) {
		writeJSON(w, map[string]any{"ok": false, "error": "Bad request"}, 400)
		return
	}
	ip, now := remoteIP(r), time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.asks == nil {
		l.asks = map[string]*askEntry{}
	}
	l.pruneLocked(now)
	for id, e := range l.asks {
		if e.From == ip && e.answer == "" {
			delete(l.asks, id) // one request per address: asking again replaces it
		}
	}
	if len(l.asks) >= maxAsks {
		writeJSON(w, map[string]any{"ok": false, "error": "Too many screens are asking at once — try again in a few minutes."}, 429)
		return
	}
	e := &askEntry{PairRequest: PairRequest{ID: randomID(), Code: randomDigits(3), Name: cleanName(body.Name), From: ip},
		secret: body.Secret, at: now}
	for _, o := range l.asks { // two waiting with one code would leave the person guessing
		if o.Code == e.Code {
			e.Code = randomDigits(3)
		}
	}
	l.asks[e.ID] = e
	fmt.Printf("  Clubhouse screen (%s) is asking to pair — code %s. Answer it on the match-day screen.\n", ip, e.Code)
	writeJSON(w, map[string]any{"ok": true, "code": e.Code, "club": l.cfg.Name, "wait": int(askFor.Seconds())}, 200)
}

// askPoll: POST /clubhouse/ask/poll {secret} → {status: waiting|control|view|no|unknown,
// key (when said yes)}. The key is handed over once; the request is gone after.
func (l *Link) askPoll(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Secret string `json:"secret"`
	}
	if !readJSON(r, &body) || !validSecret(body.Secret) {
		writeJSON(w, map[string]any{"ok": false, "error": "Bad request"}, 400)
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(time.Now())
	for id, e := range l.asks {
		if subtle.ConstantTimeCompare([]byte(e.secret), []byte(body.Secret)) != 1 {
			continue
		}
		out := map[string]any{"ok": true, "status": "waiting"}
		switch e.answer {
		case "control", "view":
			key := l.cfg.Key
			if e.answer == "view" {
				key = l.cfg.ViewKey
			}
			out["status"], out["key"] = e.answer, key
			delete(l.asks, id)
		case "no":
			out["status"] = "no"
			delete(l.asks, id)
		}
		writeJSON(w, out, 200)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "status": "unknown"}, 200)
}

// Requests are the screens waiting for an answer, oldest first.
func (l *Link) Requests() []PairRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.pruneLocked(now)
	out := []PairRequest{}
	for _, e := range l.asks {
		if e.answer == "" {
			p := e.PairRequest
			p.AgoSec = int(now.Sub(e.at).Seconds())
			out = append(out, p)
		}
	}
	for i := 1; i < len(out); i++ { // a handful at most
		for j := i; j > 0 && out[j].AgoSec > out[j-1].AgoSec; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Answer says yes (as "control" — the match-day screen — or "view", score-only) or
// "no" to a request.
func (l *Link) Answer(id, as string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(time.Now())
	e := l.asks[id]
	if e == nil || e.answer != "" {
		return errors.New("That screen isn't asking any more — press Pair on it again.")
	}
	switch as {
	case "control":
		if len(NormaliseKey(l.cfg.Key)) != KeyLen {
			return errors.New("Tick “Let a clubhouse screen run match day” first.")
		}
	case "view":
		if len(NormaliseKey(l.cfg.ViewKey)) != KeyLen {
			return errors.New("Tick “Let score-only screens show the score” first.")
		}
	case "no":
	default:
		return errors.New("Bad answer")
	}
	e.answer, e.settled = as, time.Now()
	what := map[string]string{"control": "paired as the match-day screen", "view": "paired as a score-only screen",
		"no": "turned down"}[as]
	fmt.Printf("  Clubhouse screen (%s, code %s): %s\n", e.From, e.Code, what)
	return nil
}
