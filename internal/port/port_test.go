package port

import "testing"

func TestServer(t *testing.T) {
	for env, want := range map[string]int{"": 5000, "5050": 5050, "nope": 5000, "0": 5000, "70000": 5000} {
		t.Setenv(Env, env)
		if got := Server(); got != want {
			t.Errorf("%s=%q: %d, want %d", Env, env, got, want)
		}
	}
}
