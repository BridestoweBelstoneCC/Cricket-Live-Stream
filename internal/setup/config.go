// Package setup is the first-run interview (setup_wizard.py), done in the browser: which
// job this machine has, then — for the streaming laptop — the club's details, and
// config.ini written exactly as the wizard wrote it. The role is saved too, so later runs
// go straight into that mode.
package setup

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"cricketstream/internal/agent"
	"cricketstream/internal/assets"
	"cricketstream/internal/ini"
	"cricketstream/internal/scorerfile"
)

// Roles a machine can have.
const (
	RoleStreaming = "streaming" // the full server: overlay, panel, OBS, the match
	RoleScorer    = "scorer"    // scorer's laptop on the same wifi (scorer agent)
	RoleBridge    = "bridge"    // scorer's machine elsewhere, reached over Tailscale
)

// ScorebarStyles is every style the overlay draws, in the wizard's order.
var ScorebarStyles = []struct{ ID, Name, About string }{
	{"classic", "Classic", "Light grey panels. The original; plain and very legible."},
	{"modern", "Modern", "Dark broadcast slab with a team-colour rule along the top."},
	{"impact", "Impact", "Bold angled cuts and a white score plate, TV-sport styling. The easiest to read on a phone."},
	{"minimal", "Minimal", "Clean white, lots of space. Sits best under a bright daytime picture."},
	{"franchise", "Franchise", "Royal purple and gold with condensed capitals: the big-league T20 look."},
	{"neon", "Neon", "A black bar with an electric glow on the score. Made for evening matches."},
	{"block", "Block", "Flat colour blocks and heavy type; the striker's block turns black."},
}

// ConfigFile is config.ini in the app folder.
func ConfigFile() string { return assets.Join("config.ini") }

// Role is the role saved in config.ini, or "" when setup hasn't run. A config.ini from
// before roles existed (the Python version's) is a streaming laptop's.
func Role() string {
	f, err := ini.Load(ConfigFile())
	if err != nil {
		return ""
	}
	switch r := strings.ToLower(strings.TrimSpace(f.Get("App", "role", ""))); r {
	case RoleScorer, RoleBridge:
		return r
	}
	return RoleStreaming
}

// Values is everything the interview asks (setup_wizard.configure()).
type Values struct {
	Role          string `json:"role"`
	Name          string `json:"name"`
	Abbrev        string `json:"abbrev"`
	Colour        string `json:"colour"`
	Motto         string `json:"motto"`
	PCID          string `json:"pc_id"`
	PCKey         string `json:"pc_key"`
	PCSFolder     string `json:"pcs_folder"`
	ScorebarStyle string `json:"scorebar_style"`
	ReplayFolder  string `json:"replay_folder"`
	YTTitle       string `json:"yt_title"`
	YTKey         string `json:"yt_key"`
	MaxOvers      string `json:"max_overs"`
	AnthropicKey  string `json:"anthropic_key"`
	ClubPassword  string `json:"club_password"`
	AllowRemote   bool   `json:"allow_remote"`
	// Bridge only.
	BridgePort int `json:"bridge_port"`
	// Secrets to remove (form names, e.g. "club_password"): a blank secret field means
	// "keep the saved one", so removing one has to be said out loud.
	Clear []string `json:"clear,omitempty"`
	// Put a CricketStream shortcut on the desktop (Windows; first-run setup offers it).
	DesktopShortcut bool `json:"desktop_shortcut,omitempty"`
}

// AbbrevFromName is the wizard's default abbreviation: the first word that isn't "CC",
// "Cricket", "Club", "and", "the" — up to six letters, upper case.
func AbbrevFromName(name string) string {
	stop := map[string]bool{"cc": true, "cricket": true, "club": true, "and": true, "&": true, "the": true}
	for _, w := range strings.Fields(strings.ReplaceAll(name, "&", "and")) {
		if !stop[strings.ToLower(w)] {
			r := []rune(w)
			if len(r) > 6 {
				r = r[:6]
			}
			return strings.ToUpper(string(r))
		}
	}
	return ""
}

// DefaultReplayFolder is the folder the server falls back to, so accepting the default
// and leaving config.ini blank end up in the same place.
func DefaultReplayFolder() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Videos", "Replays")
}

// DetectPCSFolder is NV Play's output folder on this machine and how it was found:
// "live" (being written now), "stale" (holds an older scoreboard file) or "empty" (the
// standard folder, nothing in it yet). ("", "") when nothing is found.
func DetectPCSFolder() (string, string) { return scorerfile.Detect() }

func newToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Normalise applies the wizard's tidying: abbreviation defaulted and capped at six
// upper-case letters, the colour given its '#', the usual defaults filled in.
func (v *Values) Normalise() {
	v.Role = strings.ToLower(strings.TrimSpace(v.Role))
	if v.Role != RoleScorer && v.Role != RoleBridge {
		v.Role = RoleStreaming
	}
	trim := func(s *string) { *s = strings.TrimSpace(*s) }
	for _, s := range []*string{&v.Name, &v.Abbrev, &v.Colour, &v.Motto, &v.PCID, &v.PCKey, &v.PCSFolder,
		&v.ScorebarStyle, &v.ReplayFolder, &v.YTTitle, &v.YTKey, &v.MaxOvers, &v.AnthropicKey, &v.ClubPassword} {
		trim(s)
	}
	v.PCSFolder = strings.Trim(v.PCSFolder, `"`)
	v.ReplayFolder = strings.Trim(v.ReplayFolder, `"`)
	if v.Abbrev == "" {
		v.Abbrev = AbbrevFromName(v.Name)
	}
	if r := []rune(v.Abbrev); len(r) > 6 {
		v.Abbrev = string(r[:6])
	}
	v.Abbrev = strings.ToUpper(v.Abbrev)
	if v.Colour == "" {
		v.Colour = "#1a3a5c"
	}
	if !strings.HasPrefix(v.Colour, "#") {
		v.Colour = "#" + v.Colour
	}
	ok := false
	for _, s := range ScorebarStyles {
		ok = ok || s.ID == v.ScorebarStyle
	}
	if !ok {
		v.ScorebarStyle = "classic"
	}
	if v.ReplayFolder == "" {
		v.ReplayFolder = DefaultReplayFolder()
	}
	if v.YTTitle == "" {
		v.YTTitle = "LIVE: {home} vs {away}"
	}
	if v.MaxOvers == "" {
		v.MaxOvers = "50"
	}
	if v.BridgePort == 0 {
		v.BridgePort = 5050
	}
}

// Problems is what's missing before the config can be saved (the wizard's required
// questions). Empty means it can be.
func (v Values) Problems() []string {
	var out []string
	if IsAppFolder(v.PCSFolder) {
		out = append(out, "That's CricketStream's own folder, not NV Play's — choose the folder NV Play writes its scoreboard to (Tools → Configuration → Scoreboard → Output Folder).")
	}
	if v.Role != RoleStreaming {
		return out
	}
	if v.Name == "" {
		out = append(out, "The club name is required.")
	}
	if v.PCID == "" {
		out = append(out, "The PlayCricket club ID is required.")
	}
	return out
}

// Config is config.ini as the wizard writes it (setup_wizard.write_config), plus the
// role. A scorer's or bridge machine gets only the role and the scorer's folder.
func (v Values) Config() *ini.File {
	f := ini.New()
	f.Set("App", "role", v.Role)
	if v.Role != RoleStreaming {
		f.Set("Scoring", "pcs_output_folder", v.PCSFolder)
		return f
	}
	f.Set("Club", "name", v.Name)
	f.Set("Club", "abbreviation", v.Abbrev)
	f.Set("Club", "home_colour", v.Colour)
	f.Set("Club", "playcricket_id", v.PCID)
	f.Set("Club", "motto", v.Motto)
	f.Set("API", "playcricket_key", v.PCKey)
	f.Set("API", "anthropic_key", v.AnthropicKey)
	f.Set("Scoring", "pcs_output_folder", v.PCSFolder)
	f.Set("Scoring", "logos_folder", "")
	f.Set("Scoring", "ground_filter", "")
	// No OBS password: match day switches OBS's WebSocket server on itself and writes
	// the password here.
	f.Set("OBS", "obs_password", "")
	f.Set("OBS", "replay_folder", v.ReplayFolder)
	f.Set("Graphics", "scorebar_style", v.ScorebarStyle)
	f.Set("Stream", "youtube_title", v.YTTitle)
	f.Set("Stream", "youtube_stream_key", v.YTKey)
	f.Set("Stream", "max_overs", v.MaxOvers)
	f.Set("Auth", "control_token", newToken())
	f.Set("Auth", "club_password", v.ClubPassword)
	bind := "127.0.0.1"
	if v.AllowRemote {
		bind = "0.0.0.0"
	}
	f.Set("Network", "bind_host", bind)
	return f
}

// Header is the comment config.ini starts with.
const Header = "# CricketStream Overlay — Club Configuration\n# Written by CricketStream's setup — safe to edit by hand.\n\n"

// Save writes config.ini (owner-only: it holds keys and the club password).
func (v Values) Save(path string) error {
	data := append([]byte(Header), v.Config().Bytes()...)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// InstallNVPlayTemplate puts scoreboard.template into NV Play's Templates folder when NV
// Play is on this machine (what the setup guide used to have the club do by hand).
// ("installed" | "updated" | "unchanged" | "", folder). Best-effort.
func InstallNVPlayTemplate(pcsFolder string) (string, string) {
	defer func() { recover() }()
	return agent.EnsureTemplate(pcsFolder)
}

// IsAppFolder: p is the folder CricketStream itself lives in. Chosen as NV Play's output
// folder, the newest .json there is one of CricketStream's own files (scorer_agent.json),
// which then reads as a "live" scoreboard — seen on a clean machine.
func IsAppFolder(p string) bool {
	p = strings.TrimSpace(p)
	if p == "" {
		return false
	}
	a, err1 := filepath.Abs(p)
	b, err2 := filepath.Abs(assets.Dir())
	return err1 == nil && err2 == nil && strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}
