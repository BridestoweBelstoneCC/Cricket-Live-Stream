package server

import (
	"net/http/httptest"
	"testing"
)

// Origin/Referer: absent passes (curl, the overlay in some OBS builds); this host passes;
// another host, or none at all ("null" from a sandboxed iframe or a file:// page), fails.
func TestOriginOK(t *testing.T) {
	for _, c := range []struct {
		origin, referer string
		want            bool
	}{
		{"", "", true},
		{"http://127.0.0.1:5000", "", true},
		{"", "http://127.0.0.1:5000/control", true},
		{"http://evil.example", "", false},
		{"null", "", false},
		{"", "file:///C:/page.html", false},
		{"http://127.0.0.1:5000", "http://evil.example/", false},
	} {
		r := httptest.NewRequest("POST", "http://127.0.0.1:5000/state", nil)
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if c.referer != "" {
			r.Header.Set("Referer", c.referer)
		}
		if got := (&ctx{r: r}).originOK(); got != c.want {
			t.Errorf("Origin %q Referer %q: %v, want %v", c.origin, c.referer, got, c.want)
		}
	}
}
