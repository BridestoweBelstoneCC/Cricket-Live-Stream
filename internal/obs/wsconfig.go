package obs

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"cricketstream/internal/pyval"
)

// The WebSocket server: plugin_config/obs-websocket/config.json (OBS 28+). There's no
// API to switch it on (the API IS the WebSocket), so it's done in OBS's own file while
// OBS is closed.

func WSConfigPath(cfgDir string) string {
	return filepath.Join(cfgDir, "plugin_config", "obs-websocket", "config.json")
}

// ReadWSConfig returns the WebSocket settings, or nil if missing or unreadable.
func ReadWSConfig(cfgDir string) map[string]any {
	b, err := os.ReadFile(WSConfigPath(cfgDir))
	if err != nil {
		return nil
	}
	var conf map[string]any
	if json.Unmarshal([]byte(strings.TrimPrefix(string(b), bom)), &conf) != nil {
		return nil
	}
	return conf
}

// WSUsable: enabled, and either open or with a password we can read.
func WSUsable(conf map[string]any) bool {
	return conf != nil && pyval.Truthy(conf["server_enabled"]) &&
		(!pyval.Truthy(conf["auth_required"]) || pyval.Truthy(conf["server_password"]))
}

// WSPassword is the password to connect with ("" for an open server).
func WSPassword(conf map[string]any) string {
	if !pyval.Truthy(conf["auth_required"]) {
		return ""
	}
	s, _ := conf["server_password"].(string)
	return s
}

func tokenURLSafe(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// EnsureWebsocket makes sure OBS's WebSocket server is on. Returns the password and
// whether the file changed.
//
// An existing working setup is left alone and its password reused, so a club already
// running other tools against OBS doesn't have them broken. Only an off or unusable
// server is rewritten, with a fresh random password. Must only be called while OBS is
// closed.
func EnsureWebsocket(cfgDir string, port int) (string, bool, error) {
	conf := ReadWSConfig(cfgDir)
	if WSUsable(conf) {
		return WSPassword(conf), false, nil
	}
	if conf == nil {
		conf = map[string]any{}
	}
	password := tokenURLSafe(16)
	if p := pyval.IntOr(conf["server_port"], 0); p != 0 {
		port = p
	}
	// first_load=true makes obs-websocket generate its OWN password on next start and
	// save over ours; false says "already configured, use what's here".
	conf["first_load"] = false
	conf["server_enabled"] = true
	conf["server_port"] = port
	conf["auth_required"] = true
	conf["server_password"] = password
	conf["alerts_enabled"] = pyval.Truthy(conf["alerts_enabled"])
	path := WSConfigPath(cfgDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", false, err
	}
	b, _ := json.MarshalIndent(conf, "", "  ")
	if err := atomicWrite(path, append(b, '\n')); err != nil {
		return "", false, err
	}
	return password, true, nil
}
