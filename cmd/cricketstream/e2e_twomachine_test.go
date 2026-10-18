package main

// The two-machine setups, end to end on one computer: a scorer's-laptop process (the
// agent, or the bridge) serving a real NV Play-style file, and a streaming-laptop server
// reading it. Each runs from its own app folder, as on two machines.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cricketstream/internal/simulate"
)

// scorerFolder writes one mid-match NV Play frame and returns the folder and its score.
func scorerFolder(t *testing.T, dir string) (string, string) {
	t.Helper()
	m, err := simulate.NewMatch("full", 4, 5, true)
	if err != nil {
		t.Fatal(err)
	}
	var frame map[string]string
	for _, f := range m.Frames() {
		if f.Label == "ball" && f.Data["runs"] != "0" && f.Data["innings"] == "1" {
			frame = f.Data
		}
	}
	if frame == nil {
		t.Fatal("no frame with runs")
	}
	os.MkdirAll(dir, 0o755)
	b, _ := json.Marshal(frame)
	if err := os.WriteFile(filepath.Join(dir, "nvplay-scoreboard1.xml"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, frame["runs"]
}

// waitScore polls the streaming laptop's /live/view (read-only) until it shows score.
func waitScore(t *testing.T, a *app, want string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	last := ""
	for time.Now().Before(deadline) {
		r := get(t, a.base()+"/live/view")
		var d map[string]any
		if json.Unmarshal([]byte(r.body), &d) == nil {
			if st, _ := d["state"].(map[string]any); st != nil {
				last = fmt.Sprint(st["score"])
				if last == want {
					return
				}
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("streaming laptop never showed score %s (last %q)", want, last)
}

func TestE2EStreamingLaptopReadsTheScorersAgent(t *testing.T) {
	scorer := newApp(t)
	folder, score := scorerFolder(t, filepath.Join(scorer.home, "Output"))
	agentPort, discPort := freePort(t), freePort(t)
	scorer.start("agent", folder, "--port", fmt.Sprint(agentPort), "--discovery-port", fmt.Sprint(discPort))
	waitHTTP(t, fmt.Sprintf("http://127.0.0.1:%d/ping", agentPort), 20*time.Second)

	stream := newApp(t)
	stream.streamingConfig(fmt.Sprintf("\n[Scoring]\npcs_source = agent\nagent_host = 127.0.0.1:%d\n", agentPort))
	stream.start("server")
	waitHTTP(t, stream.base()+"/health", 20*time.Second)
	waitScore(t, stream, score)
	st := get(t, stream.base()+"/agent/status").json(t)
	if !strings.Contains(fmt.Sprint(st), fmt.Sprint(agentPort)) {
		t.Errorf("agent status doesn't name the agent: %v", st)
	}
}

func TestE2EStreamingLaptopReadsTheBridge(t *testing.T) {
	scorer := newApp(t)
	folder, score := scorerFolder(t, filepath.Join(scorer.home, "Output"))
	bport := freePort(t)
	token := "0123456789abcdef0123456789abcdef"
	scorer.write("bridge_config.ini", fmt.Sprintf("[bridge]\nfolder = %s\nport = %d\ntoken = %s\n", folder, bport, token))
	scorer.start("bridge")
	waitHTTP(t, fmt.Sprintf("http://127.0.0.1:%d/pcs/ping", bport), 20*time.Second)

	stream := newApp(t)
	stream.streamingConfig(fmt.Sprintf("\n[Scoring]\npcs_bridge_url = http://127.0.0.1:%d\npcs_bridge_token = %s\n", bport, token))
	stream.start("server")
	waitHTTP(t, stream.base()+"/health", 20*time.Second)
	waitScore(t, stream, score)
	h := get(t, stream.base()+"/health").json(t)
	pcs, _ := h["pcs"].(map[string]any)
	if b, _ := pcs["bridge"].(map[string]any); b["connected"] != true {
		t.Errorf("health doesn't show the bridge connected: %v", pcs["bridge"])
	}
	if strings.Contains(get(t, stream.base()+"/state").body, token) {
		t.Error("the bridge token reached /state")
	}
}
