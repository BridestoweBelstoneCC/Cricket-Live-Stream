package discord

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const token = "aBcD-eFgH_iJkLmNoPqRsTuVwXyZ0123456789"

func TestValid(t *testing.T) {
	for _, u := range []string{
		"https://discord.com/api/webhooks/123456789012345678/" + token,
		" https://discordapp.com/api/webhooks/123456789012345678/" + token + " ",
		"https://ptb.discord.com/api/v10/webhooks/123456789012345678/" + token,
	} {
		if !Valid(u) {
			t.Errorf("%q should be a webhook", u)
		}
	}
	for _, u := range []string{"", "discord.com/api/webhooks/123456789012345678/" + token,
		"http://discord.com/api/webhooks/123456789012345678/" + token,               // not https
		"https://discord.com.evil.example/api/webhooks/123456789012345678/" + token, // not Discord
		"https://evil.example/api/webhooks/123456789012345678/" + token,
		"https://user:pw@discord.com/api/webhooks/123456789012345678/" + token,
		"https://discord.com/api/webhooks/123456789012345678",                // no token
		"https://discord.com/channels/123456789012345678/123456789012345678", // a channel link
		"https://discord.com/api/webhooks/123456789012345678/" + token + "/../../users/@me"} {
		if Valid(u) {
			t.Errorf("%q must not count as a webhook", u)
		}
	}
}

// fake is Discord, locally: valid() lets its address through for the test.
func fake(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	saved := valid
	valid = func(u string) bool { return strings.HasPrefix(u, srv.URL) }
	t.Cleanup(func() { valid = saved })
	return srv.URL + "/api/webhooks/1234567890/" + token
}

func TestPostSendsAMessageThatCantPing(t *testing.T) {
	var got map[string]any
	hook := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("%s %s", r.Method, r.Header.Get("Content-Type"))
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(204)
	})
	if err := Post(hook, "@everyone the feed has gone quiet"); err != nil {
		t.Fatal(err)
	}
	if got["content"] != "@everyone the feed has gone quiet" || got["username"] != "CricketStream" {
		t.Fatalf("sent %v", got)
	}
	am, _ := got["allowed_mentions"].(map[string]any)
	if p, ok := am["parse"].([]any); !ok || len(p) != 0 {
		t.Fatalf("mentions must be switched off: %v", got["allowed_mentions"])
	}
	if err := Post(hook, strings.Repeat("é", 2500)); err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(got["content"].(string))); n != 2000 {
		t.Fatalf("a long message is %d characters; Discord takes 2000", n)
	}
}

func TestPostRefusesWhatIsntAWebhook(t *testing.T) {
	if err := Post("https://evil.example/api/webhooks/1234567890/"+token, "x"); err == nil {
		t.Fatal("posted somewhere that isn't Discord")
	}
}

func TestDiscordAnswers(t *testing.T) {
	code := 404
	hook := fake(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
		if r.Method == "GET" && code == 200 {
			io.WriteString(w, `{"name":"Match alerts","token":"`+token+`"}`)
		}
	})
	if err := Post(hook, "x"); !errors.Is(err, Gone) {
		t.Fatalf("404: %v", err)
	}
	code = 429
	if err := Post(hook, "x"); err == nil || !strings.Contains(err.Error(), "limiting") {
		t.Fatalf("429: %v", err)
	}
	code = 200
	if name, err := Check(hook); err != nil || name != "Match alerts" {
		t.Fatalf("check: %q %v", name, err)
	}
}

// Go's own request errors quote the URL — the webhook's token — and these are printed.
func TestErrorsNeverHoldTheLink(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close() // nothing listening there now
	saved := valid
	valid = func(string) bool { return true }
	defer func() { valid = saved }()
	hook := "http://" + addr + "/api/webhooks/1234567890/" + token
	err := Post(hook, "x")
	if err == nil || strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "webhooks") {
		t.Fatalf("error: %v", err)
	}
	if _, err := Check(hook); err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("check error: %v", err)
	}
}
