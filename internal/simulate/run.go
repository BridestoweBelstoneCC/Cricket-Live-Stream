package simulate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"cricketstream/internal/assets"
	"cricketstream/internal/ini"
)

// OutputFilename: NV Play writes JSON into a .xml file — kept identical so the server's
// file finder matches it.
const OutputFilename = "nvplay-scoreboard1.xml"

// ConfigureServer points the RUNNING server's PCS folder at the sim folder (logging in
// with config.ini's club_password if auth is on). Best-effort; prints what to restore.
func ConfigureServer(folder string, port int) bool {
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 5 * time.Second}
	post := func(path string, body any, token string) (map[string]any, error) {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", base+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		var d map[string]any
		return d, json.NewDecoder(resp.Body).Decode(&d)
	}
	fail := func(err error) bool {
		fmt.Printf("  ✗  Could not configure the server (%v) — set the PCS Pro output "+
			"folder to the path above manually in the control panel\n", err)
		return false
	}
	cfg, _ := ini.Load(assets.Join("config.ini"))
	token := ""
	if pw := strings.TrimSpace(cfg.Get("Auth", "club_password", "")); pw != "" {
		d, err := post("/login", map[string]string{"password": pw}, "")
		if err != nil {
			return fail(err)
		}
		token, _ = d["session_token"].(string)
	}
	resp, err := client.Get(base + "/state")
	if err != nil {
		return fail(err)
	}
	var st map[string]any
	json.NewDecoder(resp.Body).Decode(&st)
	resp.Body.Close()
	old, _ := st["pcs_output_folder"].(string)
	d, err := post("/state", map[string]any{"pcs_output_folder": folder, "use_widget": false, "demo_mode": false}, token)
	if err != nil {
		return fail(err)
	}
	if d["ok"] == true {
		fmt.Println("  ✓  Server now reading from the simulator folder")
		if old != "" && old != folder {
			fmt.Printf("     Restore afterwards: PCS Pro output folder = %s\n", old)
		}
		return true
	}
	return false
}

// Options are the simulator's command-line settings.
type Options struct {
	Scenario       string
	Folder         string
	BallSeconds    float64
	Overs          int
	Seed           *int64
	Configure      bool
	Chaos          bool
	List           bool
	ClearingTicker bool
}

// Run writes the scenario's frames to the folder at match pace.
func Run(o Options) int {
	if o.List {
		for _, name := range ScenarioNames() {
			fmt.Printf("  %-9s %s\n", name, Scenarios[name])
		}
		return 0
	}
	if o.Folder == "" {
		o.Folder = assets.Join("sim_pcs_output")
	}
	seed := time.Now().UnixNano()
	if o.Seed != nil {
		seed = *o.Seed
	}
	m, err := NewMatch(o.Scenario, o.Overs, seed, !o.ClearingTicker)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if err := os.MkdirAll(o.Folder, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	path := filepath.Join(o.Folder, OutputFilename)
	seedNote := ""
	if o.Seed != nil {
		seedNote = fmt.Sprintf(", seed %d", *o.Seed)
	}
	fmt.Printf("\n  Match simulator — scenario: %s, %d overs, %gs/ball%s\n", o.Scenario, o.Overs, o.BallSeconds, seedNote)
	fmt.Printf("  Writing: %s\n", path)
	fmt.Printf("  Teams:   %s v %s\n", HomeTeam, AwayTeam)
	fmt.Println("  Roster lines for the control panel (rehearses the brothers fix):")
	for _, p := range HomeXI[:2] {
		fmt.Printf("      %s = %s\n", p.Number, p.Name)
	}
	if o.Configure {
		ConfigureServer(o.Folder, 5000)
	} else {
		fmt.Printf("  Control panel → PCS Pro output folder → %s\n", o.Folder)
	}
	fmt.Println("  Ctrl+C stops the simulation.")
	fmt.Println()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	sleep := func(d time.Duration) bool {
		select {
		case <-stop:
			fmt.Println("\n  Simulation stopped. Remember to point the PCS Pro output folder back " +
				"at the real scorer's folder before the next match.")
			return false
		case <-time.After(d):
			return true
		}
	}
	pace := map[string]float64{"prematch": 3.0, "prematch2": 3.0, "break": 5.0, "end": 3.0, "ball": 1.0}
	rng := rand.New(rand.NewSource(seed))
	secs := func(x float64) time.Duration { return time.Duration(x * float64(time.Second)) }
	last := ""
	for _, fr := range m.Frames() {
		if o.Chaos && fr.Label == "ball" && rng.Float64() < 0.04 {
			os.WriteFile(path, nil, 0o644) // caught mid-write: empty file
			if !sleep(600 * time.Millisecond) {
				return 0
			}
		}
		b, _ := json.Marshal(fr.Data)
		os.WriteFile(path, b, 0o644)
		tick := fr.Data["last_ball"]
		if tick == "" {
			tick = "—"
		}
		line := fmt.Sprintf("  inn%s %s-%s (%s ov)  %s", fr.Data["innings"], fr.Data["runs"], fr.Data["wickets"], fr.Data["overs"], tick)
		if line != last {
			fmt.Println(line)
			last = line
		}
		p, ok := pace[fr.Label]
		if !ok {
			p = 1
		}
		if !sleep(secs(o.BallSeconds * p)) {
			return 0
		}
		if o.Chaos && fr.Label == "ball" && rng.Float64() < 0.02 {
			fmt.Println("  …chaos: feed stalls for 8s (watch the health strip go amber)")
			if !sleep(8 * time.Second) {
				return 0
			}
		}
	}
	return 0
}
