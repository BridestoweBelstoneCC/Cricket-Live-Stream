package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"cricketstream/internal/ini"
)

const (
	MaxClips              = 100
	MaxBodyBytes          = 1024 * 1024     // every POST body is small JSON; a cheap guard against junk
	SponsorUploadMaxBytes = 8 * 1024 * 1024 // a logo exported from a phone; see /sponsor/upload
)

// DefaultState is everything the control panel edits, at its defaults. Club-agnostic by
// design: this is used by clubs other than the original maintainer's.
func DefaultState() M {
	return M{
		"home_team":                    "Home CC",
		"home_abbrev":                  "HOME",
		"away_abbrev":                  "",
		"away_team":                    "Opposition CC",
		"home_colour":                  "#1a3a5c",
		"away_colour":                  "#7b2d2d",
		"demo_mode":                    true,
		"api_token":                    "",
		"match_id":                     "",
		"match_url":                    "",
		"rv_match_id":                  "",
		"site_id":                      "",
		"max_overs":                    50,
		"drinks_over":                  25,
		"poll_interval":                20,
		"match_notes":                  "",
		"replay_motto":                 "",
		"graphics_fow":                 true,
		"graphics_partnership":         true,
		"graphics_lineup":              true,
		"graphics_boundary_flash":      true,
		"graphics_milestones":          true,
		"graphics_innings_summary":     true,
		"graphics_commentary_over":     false,
		"graphics_over_summary":        true,
		"graphics_partnership_display": true,
		"graphics_runrate_trend":       true,
		"graphics_league_table":        true, // /league/table context panel
		"graphics_player_card":         false,
		// "classic" | "modern" | "impact" | "minimal" | "franchise" | "neon" | "block" — overlay.html's body.style-* CSS
		// blocks and its SCOREBAR_STYLES list. "classic" is the base stylesheet, so an
		// unknown value renders as classic rather than breaking the bar.
		"scorebar_style":   "classic",
		"replay_enabled":   true,
		"replay_on_fifty":  false,
		"obs_host":         "localhost",
		"obs_port":         4455,
		"obs_password":     "CHANGE_ME",
		"obs_main_scene":   "Main",
		"camera_rtsp_url":  "",
		"obs_camera_name":  "Cricket Camera",
		"obs_replay_scene": "Replay",
		// Optional second (bowler-end) camera for a hard cut between two angles. Blank
		// camera2_rtsp_url means "not configured": the whole feature is a no-op.
		"camera2_rtsp_url": "",
		"obs_camera2_name": "Bowler End Camera",
		"obs_bowler_scene": "Main-Bowler",
		// Automatic over-boundary cut to obs_bowler_scene and back ~8s later. Opt-in: a
		// camera cut is a real, visible on-air action.
		"graphics_camera_auto_cut":  false,
		"graphics_win_predictor":    true,
		"graphics_voice_commentary": false, // read the end-of-over line aloud (computer voice)
		"camera_spotter":            false, // AI camera checks while live — spends AI credit
		"camera_spotter_minutes":    5,
		"replay_folder":             "",
		"replay_duration":           18,
		"max_clips":                 500,
		"network_test_mbps":         nil,
		"network_test_at":           0,
		"stream_auto_downshift":     false, // the sentinel may auto-reduce bitrate on sustained congestion
		"youtube_title_template":    "LIVE: {home} vs {away}",
		"youtube_description":       "Live grassroots cricket. {home} v {away}.",
		"youtube_privacy":           "unlisted",
		"youtube_category":          "17",
		"weather_api_key":           "",
		"logos_folder":              "",
		"headshots_folder":          "",
		"roster":                    M{},
		"socials_folder":            "",
		"sponsor_name":              "",
		"social_handle":             "", // shown on vertical social clips, e.g. @YourClubCC
		"sponsor_id":                "",
		"home_club_id":              "",
		"ground_filter":             "",
		"away_club_id":              "",
		// PlayCricket's numeric competition id for TODAY's match (league_table.json takes an
		// id, not a name). Auto-filled by the fixture fetch; may be wrong or blank on
		// cup/friendly days, so the league table is fetched defensively.
		"competition_id":   "",
		"pcs_bridge_url":   "", // NV Play on separate hardware — see the bridge
		"pcs_bridge_token": "",
		// Two-laptop mode. "local" covers both a plain folder and a bridge-mirrored cache
		// dir; "agent" reads over the network from the scorer agent on the scoring laptop.
		"pcs_source":      "local",
		"agent_host":      "", // manual override, "host" or "host:port"; blank = auto
		"agent_last_seen": "", // remembered so a restart mid-match reconnects instantly
	}
}

// SecretKeys must never be sent to a browser. GET /state replaces a stored value with
// SecretSentinel; POST /state ignores fields whose value IS the sentinel, so the panel can
// round-trip its form without wiping stored secrets ("" still clears). Keep any new
// secret field in this list.
var SecretKeys = []string{"anthropic_api_key", "playcricket_api_key", "api_token",
	"weather_api_key", "obs_password", "camera_rtsp_url", "camera2_rtsp_url",
	"control_token", "club_password", "youtube_stream_key", "pcs_bridge_token"}

const SecretSentinel = "••••••••"

func isSecret(k string) bool {
	for _, s := range SecretKeys {
		if s == k {
			return true
		}
	}
	return false
}

// redactState replaces stored secrets with the sentinel.
func redactState(s M) M {
	out := M{}
	for k, v := range s {
		if isSecret(k) && v != nil && v != "" {
			out[k] = SecretSentinel
		} else {
			out[k] = v
		}
	}
	return out
}

// The mtime-keyed cache: LoadState is called several times per overlay poll, but the
// file only changes on a panel save, so re-reading it every time is pure waste on an old
// laptop that's also encoding video. Keyed on (path, mtime, size): SaveState always bumps
// it, and tests that repoint StateFile miss the cache naturally.
type stateKey struct {
	path  string
	mtime int64
	size  int64
}

var (
	stateCacheMu  sync.Mutex
	stateCacheKey stateKey
	stateCache    M
	lastGoodState M
	stateWriteMu  sync.Mutex // every writer shares the one .tmp name
	stateRMWMu    sync.Mutex // load→change→save as one step
)

func shallow(m M) M {
	out := make(M, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// LoadState reads match_state.json over the defaults. A file caught mid-write or
// corrupt never crashes anything: the last good copy (else defaults) is used.
func LoadState() M {
	st, err := os.Stat(StateFile)
	if err != nil {
		return DefaultState()
	}
	key := stateKey{StateFile, st.ModTime().UnixNano(), st.Size()}
	stateCacheMu.Lock()
	if stateCache != nil && stateCacheKey == key {
		// Shallow copy: callers mutate the top level before saving.
		out := shallow(stateCache)
		stateCacheMu.Unlock()
		return out
	}
	stateCacheMu.Unlock()
	b, err := os.ReadFile(StateFile)
	var loaded M
	if err == nil {
		dec := json.NewDecoder(bytes.NewReader(b))
		err = dec.Decode(&loaded)
		if err == nil && loaded == nil {
			err = errors.New("state file holds a non-object, expected object")
		}
	}
	if err != nil {
		say("  ⚠  state read failed (%v); using last-good", err)
		stateCacheMu.Lock()
		defer stateCacheMu.Unlock()
		if lastGoodState != nil {
			return shallow(lastGoodState)
		}
		return DefaultState()
	}
	state := DefaultState()
	for k, v := range loaded {
		state[k] = v
	}
	stateCacheMu.Lock()
	lastGoodState, stateCacheKey, stateCache = state, key, state
	stateCacheMu.Unlock()
	return shallow(state)
}

// UpdateState loads, changes and saves the state as one step; change mutates in place.
// SaveState's own lock only covers the file write, so two threads doing load→modify→save
// at once could each write back a stale copy of the other's keys.
func UpdateState(change func(M)) M {
	stateRMWMu.Lock()
	defer stateRMWMu.Unlock()
	s := LoadState()
	change(s)
	SaveState(s)
	return s
}

// SaveState writes the state atomically: a reader (the overlay polling /state) must
// never see a half-written file. Serialised, because every writer shares the one .tmp
// name and two at once gave "Permission denied" on Windows; the rename is retried for
// ~2s because Windows refuses to replace a file another thread has open for reading.
func SaveState(s M) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s); err != nil {
		return err
	}
	tmp := StateFile + ".tmp"
	stateWriteMu.Lock()
	err := func() error {
		f, err := os.Create(tmp)
		if err != nil {
			return err
		}
		if _, err = f.Write(bytes.TrimRight(buf.Bytes(), "\n")); err == nil {
			err = f.Sync()
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		for attempt := 0; attempt < 20; attempt++ { // backs off to ~2s in all
			if err = os.Rename(tmp, StateFile); err == nil {
				return nil
			}
			time.Sleep(time.Duration(min(10*(attempt+1), 200)) * time.Millisecond)
		}
		return err
	}()
	stateWriteMu.Unlock()
	if err != nil {
		return err
	}
	cp := shallow(s)
	if st, serr := os.Stat(StateFile); serr == nil {
		stateCacheMu.Lock()
		lastGoodState = cp
		stateCacheKey = stateKey{StateFile, st.ModTime().UnixNano(), st.Size()}
		stateCache = cp
		stateCacheMu.Unlock()
	}
	return nil
}

// jsonEqual compares two values as JSON (50 == 50.0, like Python's ==).
func jsonEqual(a, b any) bool { return reflect.DeepEqual(normalise(a), normalise(b)) }

// configMapping is one config.ini key and the match_state.json field the panel shows it
// in (startup seeding and the settings page both use the list).
type configMapping struct {
	section, key, field string
	isInt               bool
}

var configStateMap = []configMapping{
	{"Club", "name", "home_team", false},
	{"Club", "abbreviation", "home_abbrev", false},
	{"Club", "home_colour", "home_colour", false},
	{"Club", "playcricket_id", "home_club_id", false},
	{"Club", "motto", "replay_motto", false},
	{"API", "playcricket_key", "playcricket_api_key", false},
	{"API", "anthropic_key", "anthropic_api_key", false},
	{"Scoring", "pcs_output_folder", "pcs_output_folder", false},
	{"Scoring", "pcs_bridge_url", "pcs_bridge_url", false},
	{"Scoring", "pcs_bridge_token", "pcs_bridge_token", false},
	{"Scoring", "pcs_source", "pcs_source", false},
	{"Scoring", "agent_host", "agent_host", false},
	{"Scoring", "logos_folder", "logos_folder", false},
	{"Scoring", "ground_filter", "ground_filter", false},
	{"OBS", "obs_password", "obs_password", false},
	{"OBS", "replay_folder", "replay_folder", false},
	{"Graphics", "scorebar_style", "scorebar_style", false},
	{"Stream", "youtube_title", "youtube_title_template", false},
	{"Stream", "max_overs", "max_overs", true},
}

// SeedStateFromConfig copies config.ini values into match_state.json for fields still at
// empty/default, so operators who filled in config.ini don't re-enter everything in the
// panel. Panel edits win: once a field has changed from its default, config.ini no
// longer overwrites it. Runs once at startup.
func SeedStateFromConfig() {
	if !fileExists(ConfigFile) {
		return
	}
	cp, err := ini.Load(ConfigFile)
	if err != nil {
		return
	}
	maps := configStateMap
	// Held across load→seed→save: the window's pre-match check seeds while the panel (or
	// the launcher, in the same process) may be saving.
	stateRMWMu.Lock()
	defer stateRMWMu.Unlock()
	state := LoadState()
	defaults := DefaultState()
	var seeded []string
	for _, m := range maps {
		raw, ok := cp.Lookup(m.section, m.key)
		raw = strings.TrimSpace(raw)
		if !ok || raw == "" {
			continue
		}
		cur, has := state[m.field]
		if !has || cur == nil || cur == "" || jsonEqual(cur, defaults[m.field]) {
			if m.isInt {
				n, err := strconv.Atoi(raw)
				if err != nil {
					continue
				}
				state[m.field] = n
			} else {
				state[m.field] = raw
			}
			seeded = append(seeded, m.field)
		}
	}
	if len(seeded) == 0 {
		return
	}
	SaveState(state)
	var visible []string
	for _, f := range seeded {
		if !isSecret(f) {
			visible = append(visible, f)
		}
	}
	if n := len(seeded) - len(visible); n > 0 {
		visible = append(visible, fmt.Sprintf("%d secret key(s)", n))
	}
	say("  ✔  Seeded from config.ini: %s", strings.Join(visible, ", "))
}

// ReplaceState is the launcher's write: build gets match_state.json as it is on disk (see
// LoadExistingState) and returns the whole new state, as one step under the same lock as
// UpdateState, so a save made meanwhile in this process isn't lost. Unlike UpdateState it
// reports a failed save: the launcher can't carry on without the file.
func ReplaceState(build func(existing M) M) error {
	stateRMWMu.Lock()
	defer stateRMWMu.Unlock()
	return SaveState(build(LoadExistingState()))
}

// LoadExistingState is match_state.json exactly as it is on disk — no defaults filled in
// — or an empty object if it's missing, unreadable or not an object. The launcher merges
// over it, so keys the panel owns ride through untouched.
func LoadExistingState() M {
	b, err := os.ReadFile(StateFile)
	if err != nil {
		return M{}
	}
	var m M
	if json.Unmarshal(b, &m) != nil || m == nil {
		return M{}
	}
	return m
}
