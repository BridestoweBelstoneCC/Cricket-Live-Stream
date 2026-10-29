package server

// Every load→change→save of match_state.json holds stateRMWMu: UpdateState (the panel),
// ReplaceState (the launcher) and SeedStateFromConfig (server start and the window's
// pre-match check, both in a process that may be saving something else at the time).
// Without the lock, a writer that loaded before another's save writes its stale copy back
// and the other change is lost — a panel edit gone with no error anywhere.

import (
	"fmt"
	"os"
	"sync"
	"testing"
)

func TestStateWritersNeverLoseEachOthersChanges(t *testing.T) {
	sandbox(t, "")
	setState(t, M{"n": 0})
	const rounds = 40
	var wg sync.WaitGroup
	for i := 0; i < rounds; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			UpdateState(func(s M) { s["n"] = num(s["n"]) + 1 })
		}()
		go func(i int) {
			defer wg.Done()
			if err := ReplaceState(func(e M) M { e[fmt.Sprintf("k%d", i)] = true; return e }); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	st := LoadState()
	if num(st["n"]) != rounds {
		t.Errorf("panel changes lost under the launcher's writes: n = %v, want %d", st["n"], rounds)
	}
	for i := 0; i < rounds; i++ {
		if st[fmt.Sprintf("k%d", i)] != true {
			t.Errorf("launcher write k%d lost", i)
		}
	}
}

func TestSeedingFromConfigDoesntLoseAConcurrentChange(t *testing.T) {
	for try := 0; try < 5; try++ {
		sandbox(t, "")
		setState(t, M{"n": 0})
		if err := os.WriteFile(ConfigFile, []byte("[Club]\nname = Seeded CC\nhome_colour = #123456\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		const rounds = 30
		var wg sync.WaitGroup
		wg.Add(rounds + 1)
		go func() { defer wg.Done(); SeedStateFromConfig() }()
		for i := 0; i < rounds; i++ {
			go func() {
				defer wg.Done()
				UpdateState(func(s M) { s["n"] = num(s["n"]) + 1 })
			}()
		}
		wg.Wait()
		st := LoadState()
		if num(st["n"]) != rounds || st["home_team"] != "Seeded CC" {
			t.Fatalf("try %d: n = %v (want %d), home_team = %v", try, st["n"], rounds, st["home_team"])
		}
	}
}

// num is a counter as read back from JSON (float64) or as just set (int).
func num(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	}
	return 0
}
