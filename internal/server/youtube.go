package server

// YouTube broadcast manager. With key-based streaming (recommended — survives restarts
// and quality changes), OBS's "Manage Broadcast" panel disappears, so the broadcast's
// title, description, privacy and category have to be set through the YouTube Data API
// instead.
//
// yt_token.json keeps the exact format Python's google-auth wrote, so a club that
// authorised YouTube with the old app keeps its login.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"cricketstream/internal/browser"
)

var ytScopes = []string{"https://www.googleapis.com/auth/youtube"}

var ytPrivacy = []string{"public", "unlisted", "private"}

// YTCategories are the categories worth offering a grassroots cricket stream (id,
// label). 17 = Sports is the sensible default.
var YTCategories = [][2]string{{"17", "Sports"}, {"24", "Entertainment"}, {"22", "People & Blogs"},
	{"19", "Travel & Events"}, {"28", "Science & Technology"},
	{"25", "News & Politics"}, {"20", "Gaming"}}

const ytAPI = "https://www.googleapis.com/youtube/v3/"

// ytSnippetPayload merges the requested title/description onto the broadcast's CURRENT
// snippet for liveBroadcasts.update part=snippet. An update replaces the whole part, so
// current values (and the API-required scheduledStartTime) are always carried through.
// 'Made for kids' is deliberately NOT handled: YouTube rejects modifying it via update
// (only at creation, in Studio), so sending it at all — even its current value — 403s.
func ytSnippetPayload(cur M, title, description *string) M {
	s := M{"title": gs(cur, "title", "")}
	if v := gs(cur, "scheduledStartTime", ""); v != "" {
		s["scheduledStartTime"] = v
	}
	s["description"] = gs(cur, "description", "")
	if title != nil {
		s["title"] = *title
	}
	if description != nil {
		s["description"] = *description
	}
	return s
}

// ── The token file (google-auth's authorized-user JSON) ──

type ytTokenFile struct {
	Token          string   `json:"token"`
	RefreshToken   string   `json:"refresh_token"`
	TokenURI       string   `json:"token_uri"`
	ClientID       string   `json:"client_id"`
	ClientSecret   string   `json:"client_secret"`
	Scopes         []string `json:"scopes"`
	UniverseDomain string   `json:"universe_domain,omitempty"`
	Account        string   `json:"account"`
	Expiry         string   `json:"expiry,omitempty"`
}

type ytClientSecrets struct {
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	AuthURI      string   `json:"auth_uri"`
	TokenURI     string   `json:"token_uri"`
	RedirectURIs []string `json:"redirect_uris"`
}

func loadClientSecrets() (*oauth2.Config, error) {
	b, err := os.ReadFile(YTCredsFile)
	if err != nil {
		return nil, err
	}
	var wrap map[string]ytClientSecrets
	if err := json.Unmarshal(b, &wrap); err != nil {
		return nil, err
	}
	cs, ok := wrap["installed"]
	if !ok {
		cs, ok = wrap["web"]
	}
	if !ok || cs.ClientID == "" {
		return nil, errors.New("yt_credentials.json isn't a Google OAuth client file")
	}
	auth, tok := cs.AuthURI, cs.TokenURI
	if auth == "" {
		auth = "https://accounts.google.com/o/oauth2/auth"
	}
	if tok == "" {
		tok = "https://oauth2.googleapis.com/token"
	}
	return &oauth2.Config{ClientID: cs.ClientID, ClientSecret: cs.ClientSecret, Scopes: ytScopes,
		Endpoint: oauth2.Endpoint{AuthURL: auth, TokenURL: tok}}, nil
}

func readYTToken() (*oauth2.Token, *ytTokenFile) {
	b, err := os.ReadFile(YTTokenFile)
	if err != nil {
		return nil, nil
	}
	var f ytTokenFile
	if json.Unmarshal(b, &f) != nil {
		return nil, nil
	}
	t := &oauth2.Token{AccessToken: f.Token, RefreshToken: f.RefreshToken, TokenType: "Bearer"}
	if f.Expiry != "" {
		for _, layout := range []string{"2006-01-02T15:04:05.999999Z", time.RFC3339Nano, "2006-01-02T15:04:05.999999"} {
			if e, err := time.Parse(layout, f.Expiry); err == nil {
				t.Expiry = e
				break
			}
		}
	}
	return t, &f
}

// writeYTToken persists the token with owner-only permissions: it grants channel access,
// so it must not be world-readable on a shared machine.
func writeYTToken(cfg *oauth2.Config, t *oauth2.Token, prev *ytTokenFile) error {
	f := ytTokenFile{Token: t.AccessToken, RefreshToken: t.RefreshToken, TokenURI: cfg.Endpoint.TokenURL,
		ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, Scopes: ytScopes, UniverseDomain: "googleapis.com"}
	if f.RefreshToken == "" && prev != nil {
		f.RefreshToken = prev.RefreshToken
	}
	if !t.Expiry.IsZero() {
		f.Expiry = t.Expiry.UTC().Format("2006-01-02T15:04:05.000000Z")
	}
	b, _ := json.Marshal(f)
	if err := os.WriteFile(YTTokenFile, b, 0o600); err != nil {
		return err
	}
	os.Chmod(YTTokenFile, 0o600) // tighten even if the file pre-existed at 0644
	return nil
}

// savingSource writes refreshed tokens back to yt_token.json.
type savingSource struct {
	mu   sync.Mutex
	src  oauth2.TokenSource
	cfg  *oauth2.Config
	last string
	prev *ytTokenFile
}

func (s *savingSource) Token() (*oauth2.Token, error) {
	t, err := s.src.Token()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.AccessToken != s.last {
		s.last = t.AccessToken
		writeYTToken(s.cfg, t, s.prev)
	}
	return t, nil
}

// YTClient is an authorised YouTube Data API client.
type YTClient struct{ http *http.Client }

// interactiveYTLogin opens Google's login on THIS machine and waits for it (Python's
// InstalledAppFlow.run_local_server(port=8091)). Swappable for tests.
var interactiveYTLogin = func(cfg *oauth2.Config) (*oauth2.Token, error) {
	ln, err := net.Listen("tcp", "localhost:8091")
	if err != nil {
		return nil, err
	}
	defer ln.Close()
	c := *cfg
	c.RedirectURL = "http://localhost:8091/"
	state := randHex(16)
	verifier := oauth2.GenerateVerifier()
	authURL := c.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.S256ChallengeOption(verifier))
	codes := make(chan string, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state {
			http.Error(w, "state mismatch", 400)
			return
		}
		fmt.Fprint(w, "The authentication flow has completed. You may close this window.")
		select {
		case codes <- r.URL.Query().Get("code"):
		default:
		}
	})}
	go srv.Serve(ln)
	defer srv.Close()
	say("  Opening the Google login in your browser — sign in with the club's YouTube account.")
	browser.Open(authURL)
	select {
	case code := <-codes:
		if code == "" {
			return nil, errors.New("Google login was cancelled")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return c.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	case <-time.After(5 * time.Minute):
		return nil, errors.New("timed out waiting for the Google login")
	}
}

// ytService authorises and returns the client, or "" plus an error message.
//
// allowInteractive: the FIRST authorisation opens a browser on THIS machine (the
// streaming laptop) and blocks for the Google login. A remote operator can't complete
// that — the browser would open on the wrong computer — so only genuine loopback callers
// pass true; a remote first run gets a clear "authorise once locally" message instead.
func ytService(allowInteractive bool) (*YTClient, string) {
	if !fileExists(YTCredsFile) {
		return nil, "yt_credentials.json not found — see YouTube setup instructions in the control panel"
	}
	cfg, err := loadClientSecrets()
	if err != nil {
		return nil, err.Error()
	}
	tok, prev := readYTToken()
	valid := tok != nil && tok.AccessToken != "" && (tok.Expiry.IsZero() || time.Until(tok.Expiry) > 10*time.Second)
	if !valid {
		refreshed := false
		if tok != nil && tok.RefreshToken != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			nt, err := cfg.TokenSource(ctx, &oauth2.Token{RefreshToken: tok.RefreshToken}).Token()
			cancel()
			var re *oauth2.RetrieveError
			switch {
			case err == nil:
				writeYTToken(cfg, nt, prev)
				tok, refreshed = nt, true
			case errors.As(err, &re):
				// Revoked — or, most often, a Google OAuth app left in "Testing" mode,
				// whose logins expire after 7 days. Only a fresh login fixes it.
				say("  ✗  YouTube login has expired (%v) — reconnect it in the panel", err)
			default:
				// Network trouble isn't an expired login: never pop a Google login up on
				// the streaming laptop because the wifi blipped.
				return nil, fmt.Sprintf("Couldn't reach Google to refresh the YouTube login (%v)", err)
			}
		}
		if !refreshed {
			switch {
			case allowInteractive:
				nt, err := interactiveYTLogin(cfg)
				if err != nil {
					return nil, err.Error()
				}
				writeYTToken(cfg, nt, prev)
				tok = nt
				// When it was signed in: an app in Google's "Testing" mode gets logins that
				// last 7 days, and the pre-match check warns before that runs out.
				UpdateState(func(st M) { st["youtube_signed_in_at"] = now() })
			case tok != nil:
				return nil, "YouTube's login has expired — reconnect it ON THE STREAMING " +
					"LAPTOP: Match day tab → \"Update YouTube broadcast now\" " +
					"opens the Google login there. If it keeps expiring every " +
					"week, publish the app in Google Cloud (OAuth consent screen " +
					"→ In production)."
			default:
				return nil, "YouTube isn't authorised yet — do it once ON THE STREAMING " +
					"LAPTOP (the control panel there), not remotely: the Google " +
					"login opens a browser on the machine running the server."
			}
		}
	}
	src := &savingSource{src: cfg.TokenSource(context.Background(), tok), cfg: cfg, last: tok.AccessToken, prev: prev}
	hc := oauth2.NewClient(context.Background(), src)
	hc.Timeout = 20 * time.Second
	return &YTClient{http: hc}, ""
}

// Get calls a YouTube Data API list endpoint.
func (y *YTClient) Get(resource string, params url.Values) (M, error) {
	resp, err := y.http.Get(ytAPI + resource + "?" + params.Encode())
	if err != nil {
		return nil, err
	}
	return ytDecode(resp)
}

// Put calls an update endpoint.
func (y *YTClient) Put(resource, part string, body M) (M, error) {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("PUT", ytAPI+resource+"?part="+url.QueryEscape(part), bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := y.http.Do(req)
	if err != nil {
		return nil, err
	}
	return ytDecode(resp)
}

func ytDecode(resp *http.Response) (M, error) {
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out M
	json.Unmarshal(raw, &out)
	if resp.StatusCode >= 400 {
		msg := gs(gm(out, "error"), "message", "")
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		return nil, fmt.Errorf("<HttpError %d \"%s\">", resp.StatusCode, msg)
	}
	if out == nil {
		out = M{}
	}
	return out, nil
}

func items(m M) []M {
	var out []M
	for _, it := range gl(m, "items") {
		if x, ok := it.(map[string]any); ok {
			out = append(out, x)
		}
	}
	return out
}

// findLiveBroadcast picks the broadcast to update.
//
// YouTube's broadcast state and "OBS is streaming" are only loosely coupled: with a
// stream key a broadcast can be 'ready'/'testing' (data flowing, Go Live not yet
// clicked), and a persistent "Stream now" broadcast can read as 'complete' after a
// stop/restart even though the key still works. So: active, then upcoming, then the
// channel's NEWEST broadcast that isn't deleted.
func findLiveBroadcast(yt *YTClient) (M, string, error) {
	for _, status := range []string{"active", "upcoming"} {
		r, err := yt.Get("liveBroadcasts", url.Values{"part": {"id,snippet"}, "broadcastStatus": {status},
			"broadcastType": {"all"}, "maxResults": {"10"}})
		if err != nil {
			return nil, "", err
		}
		if it := items(r); len(it) > 0 {
			return it[0], "", nil
		}
	}
	r, err := yt.Get("liveBroadcasts", url.Values{"part": {"id,snippet,status"}, "mine": {"true"},
		"broadcastType": {"all"}, "maxResults": {"50"}})
	if err != nil {
		return nil, "", err
	}
	var all []M
	for _, b := range items(r) {
		if gs(gm(b, "status"), "lifeCycleStatus", "") != "revoked" {
			all = append(all, b)
		}
	}
	if len(all) == 0 {
		return nil, "No broadcast found on this YouTube account — create one in YouTube " +
			"Studio (Go Live). If OBS is streaming but you see this, the broadcast " +
			"may need 'Go Live' clicked in Studio, or auto-start enabled.", nil
	}
	key := func(b M) string {
		sn := gm(b, "snippet")
		return gsOr(sn, "scheduledStartTime", gsOr(sn, "publishedAt", ""))
	}
	// Newest by scheduled start (else publish time) — the operator's current/next stream.
	sort.SliceStable(all, func(i, j int) bool { return key(all[i]) > key(all[j]) })
	return all[0], "", nil
}

// UpdateYouTubeBroadcast updates the current broadcast's metadata; nil arguments are
// left unchanged. Each piece is a SEPARATE API call (title/description, privacy,
// category) so one part failing doesn't sink the others — the message says exactly what
// did and didn't apply.
func UpdateYouTubeBroadcast(title, description, privacy, categoryID *string, allowInteractive bool) (bool, string) {
	if privacy != nil {
		ok := false
		for _, p := range ytPrivacy {
			ok = ok || *privacy == p
		}
		if !ok {
			return false, "privacy must be one of " + strings.Join(ytPrivacy, ", ")
		}
	}
	yt, msg := ytService(allowInteractive)
	if yt == nil {
		return false, msg
	}
	bc, findErr, err := findLiveBroadcast(yt)
	if err != nil {
		return false, err.Error()
	}
	if findErr != "" {
		return false, findErr
	}
	bid := gs(bc, "id", "")
	var done, failed []string
	if title != nil || description != nil {
		if _, err := yt.Put("liveBroadcasts", "snippet", M{"id": bid, "snippet": ytSnippetPayload(gm(bc, "snippet"), title, description)}); err != nil {
			failed = append(failed, fmt.Sprintf("title/description (%v)", err))
		} else {
			done = append(done, "title/description")
		}
	}
	// Privacy is on the broadcast status — send ONLY privacyStatus.
	if privacy != nil {
		if _, err := yt.Put("liveBroadcasts", "status", M{"id": bid, "status": M{"privacyStatus": *privacy}}); err != nil {
			failed = append(failed, fmt.Sprintf("privacy (%v)", err))
		} else {
			done = append(done, "privacy="+*privacy)
		}
	}
	// Category is on the underlying video resource, not the broadcast.
	if categoryID != nil && *categoryID != "" {
		r, err := yt.Get("videos", url.Values{"part": {"snippet"}, "id": {bid}})
		if err == nil {
			if it := items(r); len(it) > 0 {
				sn := gm(it[0], "snippet")
				sn["categoryId"] = *categoryID
				_, err = yt.Put("videos", "snippet", M{"id": bid, "snippet": sn})
				if err == nil {
					done = append(done, "category")
				}
			}
		}
		if err != nil {
			failed = append(failed, fmt.Sprintf("category (%v)", err))
		}
	}
	// Name the broadcast we targeted so the operator can confirm it's the right one.
	on := ""
	if t := trim(gs(gm(bc, "snippet"), "title", "")); t != "" {
		on = fmt.Sprintf(" on '%s'", t)
	}
	if len(done) == 0 && len(failed) == 0 {
		return true, "Nothing to update" + on
	}
	note := "Nothing updated" + on
	if len(done) > 0 {
		note = "Updated" + on + ": " + strings.Join(done, ", ")
	}
	if len(failed) > 0 {
		note += " — FAILED: " + strings.Join(failed, "; ")
	}
	icon := "⚠"
	if len(done) > 0 && len(failed) == 0 {
		icon = "✓"
	}
	say("  %s  YouTube: %s", icon, note)
	return len(failed) == 0, note
}
