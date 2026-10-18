package server

import "testing"

// Releases, pre-releases, and the Python edition's plain numbers.
func TestVersionNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"2.12", "2.11.3", true},
		{"2.12", "2.12", false},
		{"3.0.0", "2.12", true},
		{"2.12", "3.0.0-beta.1", false}, // the Python edition is not an "update" for a v3 beta
		{"3.0.0-beta.1", "2.12", true},
		{"3.0.0", "3.0.0-beta.3", true}, // the release beats its betas
		{"3.0.0-beta.3", "3.0.0", false},
		{"3.0.0-beta.2", "3.0.0-beta.1", true},
		{"3.0.0-beta.10", "3.0.0-beta.9", true},
		{"3.0.0-rc.1", "3.0.0-beta.4", false}, // numbers only: label words aren't compared
		{"3.0.1", "3.0.0", true},
		{"2.12.0", "2.12", false}, // the same version written two ways
		{"v3.1", "3.0.9", true},
	} {
		if got := VersionNewer(c.a, c.b); got != c.want {
			t.Errorf("VersionNewer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
