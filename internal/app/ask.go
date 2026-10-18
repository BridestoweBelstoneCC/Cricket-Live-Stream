package app

import (
	"strings"
	"sync"
)

// The console's prompts, answered in the window. One question at a time: the page shows
// it over whatever screen it's on, and the code that asked waits for the answer.

type question struct {
	ID      int      `json:"id"`
	Kind    string   `json:"kind"` // "pause", "input" or "confirm"
	Text    string   `json:"text"`
	Title   string   `json:"title,omitempty"` // a fatal problem shown with a pause
	Lines   []string `json:"lines,omitempty"`
	Default bool     `json:"default,omitempty"`
	Button  string   `json:"button,omitempty"`

	answer chan answer
}

type answer struct {
	Text      string
	Yes       bool
	Dismissed bool
}

type asker struct {
	one     sync.Mutex // one question at a time
	mu      sync.Mutex
	current *question
	nextID  int
	problem *question // the last Problem, shown with the next Pause
	closed  bool
	gone    chan struct{} // closed when the window is: every question gets its default
	show    func()        // bring the app's page up (the panel may be showing)
}

func newAsker() *asker { return &asker{gone: make(chan struct{})} }

func (a *asker) ask(q *question) answer {
	a.one.Lock()
	defer a.one.Unlock()
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return answer{Yes: q.Default, Dismissed: true}
	}
	a.nextID++
	q.ID = a.nextID
	q.answer = make(chan answer, 1)
	a.current = q
	show := a.show
	a.mu.Unlock()
	if show != nil {
		show()
	}
	var ans answer
	select {
	case ans = <-q.answer:
	case <-a.gone:
		ans = answer{Yes: q.Default, Dismissed: true}
	}
	a.mu.Lock()
	a.current = nil
	a.mu.Unlock()
	return ans
}

// Current is the question showing now (nil if none).
func (a *asker) Current() *question {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.current
}

// Answer answers question id; false if that isn't the one showing (answered already).
func (a *asker) Answer(id int, ans answer) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.current == nil || a.current.ID != id {
		return false
	}
	select {
	case a.current.answer <- ans:
		return true
	default:
		return false
	}
}

// close: the window's gone, so nobody can answer — waiting questions take their defaults.
func (a *asker) close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.closed {
		a.closed = true
		close(a.gone)
	}
}

// ── console.UI ──

// windowWording turns the console's "press Enter" instructions into the window's button.
func windowWording(msg string) string {
	msg = strings.TrimSpace(msg)
	if strings.HasPrefix(msg, "Press Enter to close this window") {
		return ""
	}
	r := strings.NewReplacer("press Enter here", "press Continue", "just press Enter", "just press Continue",
		"press Enter", "press Continue", "Press Enter", "Press Continue")
	return strings.TrimSuffix(r.Replace(msg), "...")
}

func (a *asker) Pause(msg string) {
	q := &question{Kind: "pause", Text: windowWording(msg), Button: "Continue"}
	a.mu.Lock()
	if p := a.problem; p != nil {
		q.Title, q.Lines, q.Button = p.Title, p.Lines, "Close CricketStream"
		a.problem = nil
	}
	a.mu.Unlock()
	a.ask(q)
}

func (a *asker) Input(prompt string) (string, bool) {
	ans := a.ask(&question{Kind: "input", Text: strings.TrimSuffix(strings.TrimSpace(prompt), ":")})
	return ans.Text, !ans.Dismissed
}

func (a *asker) Confirm(text string, def bool) (bool, bool) {
	ans := a.ask(&question{Kind: "confirm", Text: text, Default: def})
	if ans.Dismissed {
		return def, false
	}
	return ans.Yes, true
}

func (a *asker) Problem(title string, lines []string) {
	a.mu.Lock()
	a.problem = &question{Title: title, Lines: append([]string(nil), lines...)}
	a.mu.Unlock()
}
