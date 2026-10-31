// Package discord posts CricketStream's match-day alerts to a club's Discord channel
// through a webhook ([Alerts] discord_webhook in config.ini): the scorer's feed going
// quiet, OBS crashing, the stream stopping. Whoever's at the bar with their phone hears
// about it, not just whoever is looking at the streaming laptop.
//
// A webhook URL is a password (anyone holding it can post as it), so it's kept with the
// club's other secrets and never printed: Go's HTTP errors quote the URL, so every error
// from here has it taken out.
package discord

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// The hosts Discord gives webhooks on.
var hosts = map[string]bool{"discord.com": true, "discordapp.com": true, "canary.discord.com": true,
	"ptb.discord.com": true}

var pathRx = regexp.MustCompile(`^/api(/v\d+)?/webhooks/\d{5,30}/[\w-]{20,200}$`)

// Valid: is u a Discord webhook URL (https, Discord's own host, /api/webhooks/<id>/<token>)?
func Valid(u string) bool {
	p, err := url.Parse(strings.TrimSpace(u))
	if err != nil {
		return false
	}
	return p.Scheme == "https" && hosts[strings.ToLower(p.Host)] && pathRx.MatchString(p.Path) &&
		p.User == nil && p.Fragment == ""
}

// valid is Valid (a test seam: tests post to a local server).
var valid = Valid

var client = &http.Client{Timeout: 10 * time.Second}

// Post sends one message. Mentions are switched off, so nothing in it (a club or player
// name with an @) can ping the channel.
func Post(webhook, content string) error {
	webhook = strings.TrimSpace(webhook)
	if !valid(webhook) {
		return errors.New("that isn't a Discord webhook link (Discord → channel → Edit Channel → Integrations → Webhooks → Copy Webhook URL)")
	}
	if r := []rune(content); len(r) > 2000 { // Discord's limit
		content = string(r[:1999]) + "…"
	}
	body, _ := json.Marshal(map[string]any{"content": content, "username": "CricketStream",
		"allowed_mentions": map[string]any{"parse": []string{}}})
	resp, err := client.Post(webhook, "application/json", bytes.NewReader(body))
	if err != nil {
		return clean(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return status(resp.StatusCode)
}

// Check asks Discord about the webhook without posting anything: its name when it's
// there.
func Check(webhook string) (string, error) {
	webhook = strings.TrimSpace(webhook)
	if !valid(webhook) {
		return "", errors.New("that isn't a Discord webhook link")
	}
	resp, err := client.Get(webhook)
	if err != nil {
		return "", clean(err)
	}
	defer resp.Body.Close()
	if err := status(resp.StatusCode); err != nil {
		return "", err
	}
	var d struct {
		Name string `json:"name"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&d)
	return d.Name, nil
}

// Gone: Discord says the webhook doesn't exist (deleted, or the link was mistyped).
var Gone = errors.New("Discord says this webhook doesn't exist any more — it was deleted, or the link is wrong. Make a new one and paste it in Settings.")

func status(code int) error {
	switch {
	case code >= 200 && code < 300:
		return nil
	case code == 401 || code == 403 || code == 404:
		return Gone
	case code == 429:
		return errors.New("Discord is limiting how fast messages can be sent — try again in a minute")
	}
	return fmt.Errorf("Discord answered %d", code)
}

// clean is a request error without the URL in it (the URL holds the webhook's token).
func clean(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	var t interface{ Timeout() bool }
	if errors.As(err, &t) && t.Timeout() {
		return errors.New("Discord didn't answer in time — is this computer online?")
	}
	return fmt.Errorf("couldn't reach Discord: %v", err)
}
