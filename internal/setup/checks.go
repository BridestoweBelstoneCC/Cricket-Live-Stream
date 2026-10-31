package setup

// The setup and settings pages' "Check" buttons: try a PlayCricket key, an Anthropic
// key or a scoreboard folder for real, there and then, so a typo shows up while the
// person is still on the page — not as a missing fixture on match morning. Worded like
// the pre-match check, which tries the same things again before every match.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cricketstream/internal/ai"
	"cricketstream/internal/discord"
	"cricketstream/internal/ini"
	"cricketstream/internal/scorerfile"
)

// Check is one result: Status is "ok", "warn" or "bad".
type Check struct {
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// PlayCricketURL is the fixture list a key is tried against (tests point it at a fake).
var PlayCricketURL = "https://play-cricket.com/api/v2/matches.json"

var checkClient = &http.Client{Timeout: 15 * time.Second}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// CheckPlayCricket fetches this season's fixtures with the key and club ID.
func CheckPlayCricket(key, clubID string) Check {
	key, clubID = strings.TrimSpace(key), strings.TrimSpace(clubID)
	if !isDigits(clubID) {
		return Check{"bad", "The club ID is the number in your club's play-cricket.com address."}
	}
	if key == "" {
		return Check{"warn", "No API key to try — fixtures, season stats and result cards need one."}
	}
	q := url.Values{"api_token": {key}, "site_id": {clubID}, "season": {fmt.Sprint(time.Now().Year())}}
	resp, err := checkClient.Get(PlayCricketURL + "?" + q.Encode())
	if err != nil {
		return Check{"bad", "Couldn't reach PlayCricket — is this computer online?"}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return Check{"bad", "PlayCricket rejected the API key."}
	case resp.StatusCode >= 400:
		return Check{"bad", fmt.Sprintf("PlayCricket answered HTTP %d — try again later.", resp.StatusCode)}
	}
	var d map[string]any
	if json.NewDecoder(resp.Body).Decode(&d) != nil {
		return Check{"bad", "PlayCricket's answer wasn't a fixture list — check the key and club ID."}
	}
	matches, ok := d["matches"].([]any)
	if !ok {
		return Check{"bad", "PlayCricket's answer wasn't a fixture list — check the key and club ID."}
	}
	if len(matches) == 0 {
		return Check{"warn", "The key works, but there are no fixtures this season for that club ID — is it the right number?"}
	}
	return Check{"ok", fmt.Sprintf("Key works — %d fixtures this season.", len(matches))}
}

// CheckAnthropic proves a key works without spending anything (listing models is free).
func CheckAnthropic(key string) Check {
	key = strings.TrimSpace(key)
	if key == "" {
		return Check{"warn", "No key — AI commentary, match reports and captions stay off."}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := ai.CheckKey(ctx, key)
	switch ai.StatusCode(err) {
	case 0:
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return Check{"bad", "Anthropic didn't answer in time — is this computer online?"}
			}
			return Check{"bad", "Couldn't reach Anthropic — is this computer online?"}
		}
		return Check{"ok", "Key works."}
	case 401:
		return Check{"bad", "Anthropic rejected the API key."}
	case 403:
		return Check{"bad", "The key isn't allowed to use the API — check billing at console.anthropic.com."}
	}
	return Check{"bad", fmt.Sprintf("Anthropic answered HTTP %d — try again later.", ai.StatusCode(err))}
}

// CheckDiscord posts a test message to the webhook: the one place a person can see it
// arrive.
func CheckDiscord(hook, club string) Check {
	hook = strings.TrimSpace(hook)
	if hook == "" {
		return Check{"warn", "No webhook — alerts show on this laptop only."}
	}
	if club = strings.TrimSpace(club); club == "" {
		club = "your club"
	}
	if err := discordPost(hook, "✅ Test from CricketStream at "+club+": match-day alerts will appear in this channel."); err != nil {
		return Check{"bad", capitalise(err.Error()) + "."}
	}
	return Check{"ok", "Sent — look for the test message in the channel."}
}

// discordPost is discord.Post (a test seam).
var discordPost = discord.Post

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + strings.TrimSuffix(s[1:], ".")
}

// CheckFolder says whether NV Play's scoreboard output is (or will be) in folder.
func CheckFolder(folder string) Check {
	folder = strings.Trim(strings.TrimSpace(folder), `"`)
	if folder == "" {
		return Check{"warn", "No folder — fine if the scorer is on another laptop, or you'll score at /scoring."}
	}
	st, err := os.Stat(folder)
	if err != nil {
		return Check{"bad", "That folder doesn't exist on this computer."}
	}
	if !st.IsDir() {
		return Check{"bad", "That's a file, not a folder — use the folder it's in."}
	}
	// Find doesn't age-check NV Play's own filenames, so look at the file's age here.
	if f := scorerfile.Find(folder, -1); f != "" {
		mt := time.Unix(int64(scorerfile.MTime(f)), 0)
		if time.Since(mt) < 2*time.Minute {
			return Check{"ok", fmt.Sprintf("NV Play is writing to it right now (%s).", filepath.Base(f))}
		}
		when := mt.Format("2 Jan 2006")
		return Check{"ok", fmt.Sprintf("Found a scoreboard file from %s (%s) — the right place.", when, filepath.Base(f))}
	}
	return Check{"warn", "The folder exists but has no scoreboard file yet — fine before the first match, once NV Play's scoreboard output is switched on."}
}

// CheckRequest is a "Check" button press: {what, key, club_id, folder}. A blank key on a
// configured machine means "the one already saved" (the page never sees saved secrets).
type CheckRequest struct {
	What   string `json:"what"`
	Key    string `json:"key"`
	ClubID string `json:"club_id"`
	Folder string `json:"folder"`
	Club   string `json:"club"`
}

// RunCheck answers a CheckRequest.
func RunCheck(body []byte) (int, any) {
	var c CheckRequest
	if json.Unmarshal(body, &c) != nil {
		return 400, map[string]any{"ok": false, "error": "That wasn't a check request."}
	}
	saved := func(section, key string) string {
		if f, err := ini.Load(ConfigFile()); err == nil {
			return strings.TrimSpace(f.Get(section, key, ""))
		}
		return ""
	}
	switch c.What {
	case "playcricket":
		if strings.TrimSpace(c.Key) == "" {
			c.Key = saved("API", "playcricket_key")
		}
		return 200, CheckPlayCricket(c.Key, c.ClubID)
	case "anthropic":
		if strings.TrimSpace(c.Key) == "" {
			c.Key = saved("API", "anthropic_key")
		}
		return 200, CheckAnthropic(c.Key)
	case "folder":
		return 200, CheckFolder(c.Folder)
	case "discord":
		if strings.TrimSpace(c.Key) == "" {
			c.Key = saved("Alerts", "discord_webhook")
		}
		if strings.TrimSpace(c.Club) == "" {
			c.Club = saved("Club", "name")
		}
		return 200, CheckDiscord(c.Key, c.Club)
	}
	return 400, map[string]any{"ok": false, "error": "Nothing to check."}
}
