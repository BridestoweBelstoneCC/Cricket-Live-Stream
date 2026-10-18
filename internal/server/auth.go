package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"cricketstream/internal/ini"
)

// ── Auth ─────────────────────────────────────────────────────
// control_token: signing key for session tokens — never shown to users.
// club_password:  what operators type in the login form.
// Auth is disabled when club_password is empty (safe on localhost).
var (
	ControlToken     string
	ClubPassword     string
	SessionHours     = 12
	BindHost         = "127.0.0.1"
	CloudflareTunnel bool
)

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

var controlTokenLine = regexp.MustCompile(`^\s*control_token\s*=`)

// persistControlToken writes a freshly generated control_token into config.ini's [Auth]
// section, editing the line in place (a whole-file rewrite would strip every comment)
// and atomically.
func persistControlToken(token string) bool {
	b, err := os.ReadFile(ConfigFile)
	if err != nil {
		say("  ✗  Could not persist generated control_token to config.ini: %v", err)
		return false
	}
	lines := strings.SplitAfter(string(b), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	inAuth, found := false, false
	for i, line := range lines {
		s := strings.TrimSpace(line)
		if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
			inAuth = s == "[Auth]"
			continue
		}
		if inAuth && controlTokenLine.MatchString(line) {
			lines[i] = "control_token = " + token + "\n"
			found = true
			break
		}
	}
	if !found {
		// Directly under the [Auth] header — appending at EOF would land the line inside
		// whichever section happens to be last, where it's never read back, so a new token
		// got appended every restart.
		at := -1
		for i, l := range lines {
			if strings.TrimSpace(l) == "[Auth]" {
				at = i
				break
			}
		}
		if at < 0 {
			lines = append(lines, "\n[Auth]\n", "control_token = "+token+"\n")
		} else {
			lines = append(lines[:at+1], append([]string{"control_token = " + token + "\n"}, lines[at+1:]...)...)
		}
	}
	if err := writeFileAtomic(ConfigFile, []byte(strings.Join(lines, ""))); err != nil {
		say("  ✗  Could not persist generated control_token to config.ini: %v", err)
		return false
	}
	return true
}

// LoadAuthConfig reads [Auth] and [Network] from config.ini.
func LoadAuthConfig() {
	if f, err := ini.Load(ConfigFile); err == nil {
		ControlToken = strings.TrimSpace(f.Get("Auth", "control_token", ""))
		ClubPassword = strings.TrimSpace(f.Get("Auth", "club_password", ""))
		BindHost = strings.TrimSpace(os.Getenv("BBCC_BIND_HOST"))
		if BindHost == "" {
			BindHost = strings.TrimSpace(f.Get("Network", "bind_host", "127.0.0.1"))
		}
		CloudflareTunnel = f.GetBool("Network", "cloudflare_tunnel", false)
	}
	// Binding beyond localhost with no password would expose every control-panel action
	// to the network with nothing gating it. Fail closed.
	if BindHost != "127.0.0.1" && BindHost != "localhost" && ClubPassword == "" {
		say("  ✗  bind_host is set to %s but club_password is blank — binding 127.0.0.1 instead. "+
			"Set [Auth] club_password in config.ini to enable remote access.", BindHost)
		BindHost = "127.0.0.1"
	}
}

// EnsureControlToken: sessions are HMAC-signed with control_token, and a blank one means
// anyone who's read the (public) source can forge a session. Generate one the first time
// a password is set. Called at server start, never as a side effect of loading.
func EnsureControlToken() {
	if ClubPassword != "" && ControlToken == "" {
		tok := randHex(32)
		if persistControlToken(tok) {
			ControlToken = tok
			say("  ✓  No control_token was set — generated one and saved it to config.ini")
		} else {
			BindHost = "127.0.0.1"
			say("  ✗  Generated a control_token but could not save it to config.ini — refusing to " +
				"bind beyond 127.0.0.1 until [Auth] control_token is set manually")
		}
	}
}

// Mixed into every session signature. Rotating it ("log everyone out") invalidates every
// previously issued session instantly; a fresh value at each start does the same.
var (
	sessionEpoch   = randHex(8)
	sessionEpochMu sync.Mutex
)

func sign(expiry, nonce, epoch string) string {
	mac := hmac.New(sha256.New, []byte(ControlToken+":"+epoch))
	mac.Write([]byte(expiry + ":" + nonce))
	return hex.EncodeToString(mac.Sum(nil))
}

// makeSessionToken issues a signed, expiring session token: "{expiry}:{nonce}:{sig}".
func makeSessionToken() (string, error) {
	if ControlToken == "" {
		return "", errors.New("refusing to sign a session with an empty control_token")
	}
	expiry := strconv.FormatInt(time.Now().Unix()+int64(SessionHours)*3600, 10)
	nonce := randHex(8)
	sessionEpochMu.Lock()
	epoch := sessionEpoch
	sessionEpochMu.Unlock()
	return fmt.Sprintf("%s:%s:%s", expiry, nonce, sign(expiry, nonce, epoch)), nil
}

// verifySessionToken: a valid signature under the current epoch, not expired.
func verifySessionToken(token string) bool {
	if ControlToken == "" {
		return false
	}
	parts := strings.SplitN(token, ":", 3)
	if len(parts) != 3 {
		return false
	}
	exp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || float64(exp) < now() {
		return false
	}
	sessionEpochMu.Lock()
	epoch := sessionEpoch
	sessionEpochMu.Unlock()
	return hmac.Equal([]byte(parts[2]), []byte(sign(parts[0], parts[1], epoch)))
}

func rotateSessionEpoch() {
	sessionEpochMu.Lock()
	sessionEpoch = randHex(8)
	sessionEpochMu.Unlock()
}

// ── Login brute-force protection ──────────────────────────────
// In-memory per-process is fine: a club's server restarts between matches anyway, and
// this only needs to survive the length of one brute-force attempt.
const (
	LoginMaxFailures = 5
	LoginWindowSec   = 600 // failures older than this don't count towards the lockout
	LoginLockoutSec  = 600 // how long an IP is rejected once it trips the limit
)

type loginRecord struct {
	failures    []float64
	lockedUntil float64
}

var (
	loginAttempts   = map[string]*loginRecord{}
	loginAttemptsMu sync.Mutex
)

// ── Auth event log ────────────────────────────────────────────
// A small ring so an operator can see (via /health) if something's probing the panel
// during a match — IP + time + event kind only, never the attempted password.
const AuthLogMax = 200

var (
	authLog   []M
	authLogMu sync.Mutex
)

func authLogAdd(event, ip string) {
	authLogMu.Lock()
	defer authLogMu.Unlock()
	authLog = append(authLog, M{"time": now(), "event": event, "ip": ip})
	if len(authLog) > AuthLogMax {
		authLog = authLog[len(authLog)-AuthLogMax:]
	}
}

func authLogCopy(n int) []M {
	authLogMu.Lock()
	defer authLogMu.Unlock()
	if n > len(authLog) || n <= 0 {
		n = len(authLog)
	}
	return append([]M(nil), authLog[len(authLog)-n:]...)
}

// writeFileAtomic writes via a temp file + rename, fsynced.
func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
