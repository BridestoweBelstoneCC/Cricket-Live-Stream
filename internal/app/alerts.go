package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"cricketstream/internal/port"
)

// Alerts: Windows notifications for what needs someone's attention when nobody's
// looking at CricketStream — the scorer's feed going quiet mid-match, OBS crashing (and
// being reopened), the server restarting, the streaming laptop no longer collecting the
// score, a new version. Each says so once, and once again when it's put right: a 40-minute
// tea break is one notification, not forty.

type notice struct {
	Title, Text string
	Warn        bool
}

type alertState struct {
	feedSeen     bool    // the feed has been live this match day
	feedAlerted  bool    // "gone quiet" said, waiting for it to come back
	obsEventSeen float64 // the last OBS recovery event already reported (0: none yet)
	obsBaseline  bool    // the first look sets the baseline; only later events are news
	collecting   bool    // the streaming laptop has been collecting (scorer's laptop)
	agentAlerted bool
	updateSaid   string
}

// health reads the running server's /health (match day).
func (s *alertState) health(h map[string]any) []notice {
	var out []notice
	pcs, _ := h["pcs"].(map[string]any)
	if pcs != nil && pcs["manual_scoring"] != true {
		fresh, _ := pcs["fresh"].(bool)
		switch {
		case fresh:
			if s.feedAlerted {
				out = append(out, notice{"Scorer's feed is back", "The score is updating again.", false})
			}
			s.feedSeen, s.feedAlerted = true, false
		case s.feedSeen && !s.feedAlerted:
			s.feedAlerted = true
			out = append(out, notice{"The scorer's feed has gone quiet",
				"No update for 2 minutes. Fine at a break; otherwise check NV Play on the scorer's laptop.", true})
		}
	}
	if rec, _ := h["obs_recovery"].(map[string]any); rec != nil {
		ev, _ := rec["last_event"].(map[string]any)
		t, _ := ev["time"].(float64)
		what, _ := ev["what"].(string)
		switch {
		case !s.obsBaseline:
			s.obsBaseline, s.obsEventSeen = true, t
		case t > s.obsEventSeen && what != "":
			s.obsEventSeen = t
			if !strings.Contains(what, "closed normally") {
				// Only a reopen that worked is good news ("reopened, not answering" isn't).
				ok := strings.HasPrefix(what, "OBS crashed — reopened")
				out = append(out, notice{"OBS", strings.ToUpper(what[:1]) + what[1:] + ".", !ok})
			}
		}
	}
	return out
}

// agent reads the scorer's laptop's own status: is the streaming laptop collecting?
func (s *alertState) agent(info map[string]any) []notice {
	ago, has := info["collected_ago"].(int)
	if !has {
		return nil
	}
	switch {
	case ago < 30:
		var out []notice
		if s.agentAlerted {
			out = append(out, notice{"Streaming laptop connected again", "It's collecting the score again.", false})
		}
		s.collecting, s.agentAlerted = true, false
		return out
	case ago > 90 && s.collecting && !s.agentAlerted:
		s.agentAlerted = true
		return []notice{{"The streaming laptop stopped collecting the score",
			"Nothing collected for over a minute — is CricketStream still running there, on the same wifi?", true}}
	}
	return nil
}

func (s *alertState) update(latest string) []notice {
	if latest == "" || latest == s.updateSaid {
		return nil
	}
	s.updateSaid = latest
	return []notice{{"CricketStream v" + latest + " is available", "Open CricketStream to update — your settings stay.", false}}
}

// logLine reacts to what the launcher prints (the server restarting after a crash).
func logLineNotice(line string) *notice {
	if strings.Contains(line, "Server stopped unexpectedly") {
		return &notice{"CricketStream restarted its server", "It stopped unexpectedly and was restarted — the graphics carry on.", true}
	}
	if strings.Contains(line, "keeps crashing") {
		return &notice{"CricketStream needs attention", "The server keeps stopping — see the Activity log.", true}
	}
	return nil
}

// healthURL is the running server's /health (a test seam).
var healthURL = func() string { return fmt.Sprintf("http://127.0.0.1:%d/health", port.Server()) }

func fetchHealth() map[string]any {
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Get(healthURL())
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var h map[string]any
	if json.NewDecoder(resp.Body).Decode(&h) != nil {
		return nil
	}
	return h
}

// watch checks for alerts every few seconds while the app runs.
func (a *App) watch(every time.Duration) {
	var s alertState
	for {
		select {
		case <-a.done:
			return
		case <-time.After(every):
		}
		var out []notice
		a.mu.Lock()
		phase, agentRun, folder, how, agentErr := a.phase, a.agentRun, a.agentFolder, a.agentHow, a.agentErr
		a.mu.Unlock()
		switch phase {
		case phRunning:
			if h := fetchHealth(); h != nil {
				out = append(out, s.health(h)...)
			}
		case phAgent:
			out = append(out, s.agent(agentInfo(agentRun, folder, how, agentErr))...)
		}
		if st := a.upd.status(); st != nil {
			latest, _ := st["latest"].(string)
			out = append(out, s.update(latest)...)
		}
		for _, n := range out {
			a.notify(n)
		}
	}
}

// notify shows a notice (a Windows notification from the icon by the clock) and puts it
// in the activity log.
func (a *App) notify(n notice) {
	mark := "  ✓ "
	if n.Warn {
		mark = "  ⚠ "
	}
	fmt.Println(mark + n.Title + " — " + n.Text)
	a.mu.Lock()
	w := a.win
	a.mu.Unlock()
	if w != nil {
		w.Notify(n.Title, n.Text, n.Warn)
	}
}
