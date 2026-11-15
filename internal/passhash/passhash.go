// Package passhash keeps the club password in config.ini as a salted hash, so a copy of
// the file (a backup, a "move to another laptop" file, a screenshot of it open) doesn't
// give away a password the club may use elsewhere.
//
// PBKDF2 from Go's own library: no new dependency, and slow enough that a stolen file
// can't be guessed at speed. A plain password typed into config.ini by hand still works
// (the forgotten-password fix is still "type a new one in the file"); the server hashes it
// in place the next time it starts.
package passhash

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"strconv"
	"strings"

	"cricketstream/internal/ini"
)

const prefix = "pbkdf2-sha256$"

// Iterations for a new hash (OWASP's figure for PBKDF2-HMAC-SHA256). A stored hash carries
// its own count, so raising this never locks anyone out. A var so tests needn't wait.
var Iterations = 600_000

var b64 = base64.RawStdEncoding // no '%' (configparser's escape) and no padding

// IsHash: stored is a hash this package wrote, not a password typed in by hand.
func IsHash(stored string) bool {
	_, _, _, ok := parse(stored)
	return ok
}

func parse(stored string) (iter int, salt, sum []byte, ok bool) {
	rest, found := strings.CutPrefix(stored, prefix)
	if !found {
		return 0, nil, nil, false
	}
	parts := strings.Split(rest, "$")
	if len(parts) != 3 {
		return 0, nil, nil, false
	}
	iter, err := strconv.Atoi(parts[0])
	if err != nil || iter < 1 || iter > 100_000_000 {
		return 0, nil, nil, false
	}
	if salt, err = b64.DecodeString(parts[1]); err != nil || len(salt) == 0 {
		return 0, nil, nil, false
	}
	if sum, err = b64.DecodeString(parts[2]); err != nil || len(sum) == 0 {
		return 0, nil, nil, false
	}
	return iter, salt, sum, true
}

// Hash is password as it's stored. "" stays "" (no password set).
func Hash(password string) (string, error) {
	if password == "" {
		return "", nil
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	sum, err := pbkdf2.Key(sha256.New, password, salt, Iterations, 32)
	if err != nil {
		return "", err
	}
	return prefix + strconv.Itoa(Iterations) + "$" + b64.EncodeToString(salt) + "$" + b64.EncodeToString(sum), nil
}

// Verify: typed is the password stored (a hash, or a plain password not yet hashed). An
// empty stored or typed value never matches.
func Verify(stored, typed string) bool {
	if stored == "" || typed == "" {
		return false
	}
	iter, salt, want, ok := parse(stored)
	if !ok {
		return subtle.ConstantTimeCompare([]byte(stored), []byte(typed)) == 1
	}
	got, err := pbkdf2.Key(sha256.New, typed, salt, iter, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

// LocalLogin is the body a program on this laptop posts to the server's /login (the
// launcher, the simulator, the quality test): it can read config.ini, which no longer
// holds a password it could send. With a hashed password it sends config.ini's
// control_token, which the server accepts from this laptop only; with a plain one (typed
// by hand, server not restarted since) the password itself. nil when no password is set.
func LocalLogin(configFile string) map[string]string {
	f, err := ini.Load(configFile)
	if err != nil {
		return nil
	}
	pw := strings.TrimSpace(f.Get("Auth", "club_password", ""))
	if pw == "" {
		return nil
	}
	if tok := strings.TrimSpace(f.Get("Auth", "control_token", "")); tok != "" && IsHash(pw) {
		return map[string]string{"local_key": tok}
	}
	return map[string]string{"password": pw}
}
