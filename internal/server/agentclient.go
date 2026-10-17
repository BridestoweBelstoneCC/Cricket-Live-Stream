package server

// Two-laptop mode: the scoring laptop runs PCS Pro and the scorer agent and is otherwise
// left alone; this machine runs the overlay, OBS and the stream, and collects the
// scoreboard over the club wifi via UDP auto-discovery — no address to type, unlike the
// NV Play bridge's URL/token pair (built for reaching a scorer's machine off the local
// network entirely, e.g. over Tailscale).

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"cricketstream/internal/agent"
)

const (
	agentDefaultPort = agent.DefaultHTTPPort
	agentDiscovery   = agent.DefaultDiscoveryPort
	// If the agent goes away mid-match the last score is kept, rather than blanking the
	// scorebar while someone walks over to restart it. After this long with no contact it
	// gives up, so the PlayCricket widget fallback (if enabled) can take over instead of
	// the overlay quietly freezing.
	agentStaleAfter = 120.0
)

var (
	agentMu           sync.Mutex
	agentLastMtime    float64
	agentLastState    M
	agentResolved     string // host:port we are currently talking to
	agentLastError    string // human-readable, surfaced in the control panel
	agentLastOK       float64
	agentNextDiscover float64
)

// ResetAgentClient forgets everything about the agent (tests).
func ResetAgentClient() {
	agentMu.Lock()
	agentLastMtime, agentLastState, agentResolved, agentLastError, agentLastOK, agentNextDiscover = 0, nil, "", "", 0, 0
	agentMu.Unlock()
}

// agentNormalise turns "192.168.1.40" or "192.168.1.40:8788" into "host:port".
func agentNormalise(addr string) string {
	addr = strings.TrimRight(strings.TrimSpace(addr), "/")
	if addr == "" {
		return ""
	}
	addr = strings.TrimPrefix(strings.TrimPrefix(addr, "http://"), "https://")
	addr = strings.SplitN(addr, "/", 2)[0]
	if strings.Contains(addr, ":") {
		return addr
	}
	return fmt.Sprintf("%s:%d", addr, agentDefaultPort)
}

// broadcastAddresses are worth shouting at: the global broadcast works on most club
// networks; the per-interface one is needed on some Windows setups that drop
// 255.255.255.255.
func broadcastAddresses() []string {
	addrs := []string{"255.255.255.255"}
	if ip := lanIP(); ip != "" {
		parts := strings.Split(ip, ".")
		if len(parts) == 4 {
			if subnet := strings.Join(parts[:3], ".") + ".255"; subnet != addrs[0] {
				addrs = append(addrs, subnet)
			}
		}
	}
	return addrs
}

// DiscoverAgents asks the network "is there a scorer agent out there?" and collects the
// replies. Never fails — on a locked-down network it simply returns none. Swappable.
var DiscoverAgents = func(timeout time.Duration) []M {
	found := map[string]M{}
	var order []string
	pc, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		say("  ✗  Agent discovery failed: %v", err)
		return nil
	}
	defer pc.Close()
	setBroadcast(pc)
	for _, a := range broadcastAddresses() {
		dst := &net.UDPAddr{IP: net.ParseIP(a), Port: agentDiscovery}
		for i := 0; i < 2; i++ { // UDP; a lost packet shouldn't cost us the match
			if _, err := pc.WriteTo(agent.DiscoveryMagic, dst); err != nil {
				break
			}
		}
	}
	deadline := time.Now().Add(timeout)
	buf := make([]byte, 8192)
	for time.Now().Before(deadline) {
		pc.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		n, peer, err := pc.ReadFrom(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			break
		}
		var info M
		if json.Unmarshal(buf[:n], &info) != nil || gs(info, "service", "") != agent.ServiceName {
			continue
		}
		host := peer.(*net.UDPAddr).IP.String()
		info["host"] = host
		info["address"] = fmt.Sprintf("%s:%s", host, anyStr(info, "port", strconv.Itoa(agentDefaultPort)))
		key := info["address"].(string)
		if _, seen := found[key]; !seen {
			order = append(order, key)
		}
		found[key] = info
	}
	out := []M{}
	for _, k := range order {
		out = append(out, found[k])
	}
	return out
}

var agentHTTP = &http.Client{}

func agentGet(u string, timeout time.Duration) (M, error) {
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("User-Agent", "CricketStream-Overlay")
	c := *agentHTTP
	c.Timeout = timeout
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var out M
	if err := json.Unmarshal([]byte(strings.ToValidUTF8(string(b), "�")), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// AgentPing asks one agent to identify itself: (info, "") or (nil, why).
func AgentPing(address string, timeout time.Duration) (M, string) {
	address = agentNormalise(address)
	if address == "" {
		return nil, "No address given"
	}
	info, err := agentGet("http://"+address+"/ping", timeout)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return nil, fmt.Sprintf("No answer from %s (timed out)", address)
		}
		return nil, fmt.Sprintf("Could not reach %s: %v", address, err)
	}
	if gs(info, "service", "") != agent.ServiceName {
		return nil, fmt.Sprintf("Something is running at %s, but it is not the scorer agent", address)
	}
	info["address"] = address
	return info, ""
}

// resolveAgentAddress works out which address to collect the score from: whatever the
// user typed in the control panel, then the address we were last talking to, then a
// fresh broadcast. Re-resolution is rate-limited (once per 15s) so a genuine outage costs
// one blocking network call every ~15s, not one on every ~2.5s overlay poll.
func resolveAgentAddress(s M, force bool) string {
	if manual := agentNormalise(gs(s, "agent_host", "")); manual != "" {
		agentMu.Lock()
		agentResolved = manual
		agentMu.Unlock()
		return manual
	}
	agentMu.Lock()
	if agentResolved != "" && !force {
		r := agentResolved
		agentMu.Unlock()
		return r
	}
	if !force && now() < agentNextDiscover {
		agentMu.Unlock()
		return ""
	}
	agentNextDiscover = now() + 15
	agentMu.Unlock()

	if remembered := agentNormalise(gs(s, "agent_last_seen", "")); remembered != "" {
		if info, _ := AgentPing(remembered, 2*time.Second); info != nil {
			agentMu.Lock()
			agentResolved = remembered
			agentMu.Unlock()
			return remembered
		}
	}
	agents := DiscoverAgents(1500 * time.Millisecond)
	if len(agents) == 0 {
		agentMu.Lock()
		defer agentMu.Unlock()
		agentLastError = "No scorer agent found on the network. Is the scorer agent " +
			"running on the scoring laptop, and are both laptops on the same wifi?"
		return agentResolved
	}
	address := gs(agents[0], "address", "")
	if len(agents) > 1 {
		var names []string
		for _, a := range agents {
			names = append(names, fmt.Sprintf("%s (%s)", gs(a, "hostname", "?"), gs(a, "address", "")))
		}
		say("  ⚠  More than one scorer agent answered: %s. Using %s. Set the address by hand in "+
			"the control panel to pick a different one.", strings.Join(names, ", "), address)
	}
	agentMu.Lock()
	agentResolved = address
	agentMu.Unlock()
	// Remember it so a restart mid-match reconnects without another broadcast.
	if gs(s, "agent_last_seen", "") != address {
		UpdateState(func(st M) { st["agent_last_seen"] = address })
	}
	say("  ✓  Found scorer agent at %s (%s)", address, gs(agents[0], "hostname", "unknown host"))
	return address
}

// agentLastGood is the last score collected — only while recent enough to show.
// Caller holds agentMu.
func agentLastGood() M {
	if agentLastState == nil {
		return nil
	}
	if agentLastOK != 0 && now()-agentLastOK > agentStaleAfter {
		return nil
	}
	return agentLastState
}

// ReadAgentFile fetches the scoreboard from the scorer agent and parses it exactly as if
// it had been read from a local folder. nil if the agent can't be reached — the caller
// falls back the same way it would for a missing local file.
func ReadAgentFile(s M) M {
	address := resolveAgentAddress(s, false)
	if address == "" {
		return nil
	}
	agentMu.Lock()
	since := agentLastMtime
	agentMu.Unlock()
	payload, err := agentGet(fmt.Sprintf("http://%s/pcs?since=%s", address, strconv.FormatFloat(since, 'f', -1, 64)), 5*time.Second)
	agentMu.Lock()
	defer agentMu.Unlock()
	if err != nil {
		agentLastError = fmt.Sprintf("Lost contact with the scorer laptop at %s: %v", address, err)
		say("  ✗  %s", agentLastError)
		// Drop the cached address so the next poll can re-discover it — the scoring
		// laptop may have picked up a new IP from the club router.
		if agentNormalise(gs(s, "agent_host", "")) == "" {
			agentResolved = ""
		}
		return agentLastGood()
	}
	agentLastOK = now()
	if !gb(payload, "ok", false) {
		agentLastError = gsOr(payload, "detail", gsOr(payload, "error", "Agent reported a problem"))
		return agentLastGood()
	}
	agentLastError = ""
	if gb(payload, "unchanged", false) {
		return agentLastState
	}
	if state := parseScoreboardRaw(gs(payload, "content", ""), gs(payload, "name", "")); state != nil {
		agentLastMtime = gf(payload, "mtime", 0)
		agentLastState = state
		logScore(state, "PCS(net)")
		return state
	}
	agentLastError = fmt.Sprintf("Received %s from the scorer laptop but could not read it — "+
		"is the right template loaded in PCS Pro?", gsOr(payload, "name", "a file"))
	return agentLastGood()
}

// AgentStatus is everything the control panel needs to explain the connection.
func AgentStatus(s M) M {
	agentMu.Lock()
	defer agentMu.Unlock()
	address := agentResolved
	if address == "" {
		address = agentNormalise(gs(s, "agent_host", ""))
	}
	if address == "" {
		address = agentNormalise(gs(s, "agent_last_seen", ""))
	}
	var ago any
	if agentLastOK != 0 {
		ago = now() - agentLastOK
	}
	return M{
		"source": gs(s, "pcs_source", "local"), "address": address,
		"manual":              agentNormalise(gs(s, "agent_host", "")) != "",
		"connected":           agentLastOK != 0 && now()-agentLastOK < 60,
		"last_ok_seconds_ago": ago, "error": agentLastError, "have_score": agentLastState != nil,
	}
}

// ReadScoreSource reads the ball-by-ball score from whichever source this club has
// configured. Every /live-adjacent call site must come through this door rather than
// ReadPCSFile, or it works in local/bridge mode and silently sees nothing in agent mode.
func ReadScoreSource(s M) M {
	if gs(s, "pcs_source", "local") == "agent" {
		return ReadAgentFile(s)
	}
	return ReadPCSFile(EffectivePCSFolder(s))
}

// ── NV Play bridge sync (remote scorer machine) ────────────────
// Polls the bridge and mirrors the bytes into PCSBridgeCacheDir. Deliberately a dumb
// mirror rather than parsing the fetched bytes — reusing the local-file reader means
// bridge mode can't drift from local-file behaviour.
const pcsBridgePollSec = 2

var (
	pcsBridgeStatus = M{"connected": nil, "last_sync": nil, "last_error": nil}
	pcsBridgeMu     sync.Mutex
)

func bridgeStatusUpdate(kv ...any) {
	pcsBridgeMu.Lock()
	defer pcsBridgeMu.Unlock()
	for i := 0; i+1 < len(kv); i += 2 {
		pcsBridgeStatus[kv[i].(string)] = kv[i+1]
	}
}

func pcsBridgeStatusCopy() M {
	pcsBridgeMu.Lock()
	defer pcsBridgeMu.Unlock()
	return shallow(pcsBridgeStatus)
}

func pcsBridgeTick(state M) {
	u := strings.TrimRight(trim(gs(state, "pcs_bridge_url", "")), "/")
	token := trim(gs(state, "pcs_bridge_token", ""))
	if u == "" {
		return
	}
	req, _ := http.NewRequest("GET", u+"/pcs/latest", nil)
	req.Header.Set("X-Bridge-Token", token)
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	var data M
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			err = fmt.Errorf("HTTP Error %d", resp.StatusCode)
		} else {
			err = json.NewDecoder(resp.Body).Decode(&data)
		}
	}
	if err != nil {
		bridgeStatusUpdate("connected", false, "last_error", truncRunes(err.Error(), 200))
		return
	}
	if !gb(data, "ok", false) || gs(data, "filename", "") == "" {
		// Reachable but no file found yet — not an error, just nothing to mirror. Hold
		// whatever was last mirrored.
		bridgeStatusUpdate("connected", true, "last_sync", now(), "last_error", nil)
		return
	}
	err = func() error {
		if err := os.MkdirAll(PCSBridgeCacheDir, 0o755); err != nil {
			return err
		}
		dest := filepath.Join(PCSBridgeCacheDir, filepath.Base(gs(data, "filename", "")))
		tmp := dest + ".tmp"
		if err := os.WriteFile(tmp, []byte(gs(data, "content", "")), 0o644); err != nil {
			return err
		}
		if err := os.Rename(tmp, dest); err != nil { // a reader must never see a half-written mirror
			return err
		}
		// Keep the remote mtime so staleness checks reflect when NV Play actually wrote
		// it, not merely when we last polled successfully.
		if mt := gf(data, "mtime", 0); mt != 0 {
			t := time.Unix(0, int64(mt*1e9))
			os.Chtimes(dest, t, t)
		}
		return nil
	}()
	if err != nil {
		bridgeStatusUpdate("connected", false, "last_error", truncRunes(err.Error(), 200))
		return
	}
	bridgeStatusUpdate("connected", true, "last_sync", now(), "last_error", nil)
}

// StartPCSBridgeSync mirrors the bridge every 2s. The loop must never die — a dead sync
// thread would silently freeze the mirror.
func StartPCSBridgeSync() {
	go func() {
		for {
			func() {
				defer func() {
					if r := recover(); r != nil {
						say("  ✗  PCS bridge sync failed (will retry): %v", r)
					}
				}()
				if st := LoadState(); trim(gs(st, "pcs_bridge_url", "")) != "" {
					pcsBridgeTick(st)
				}
			}()
			time.Sleep(pcsBridgePollSec * time.Second)
		}
	}()
}
