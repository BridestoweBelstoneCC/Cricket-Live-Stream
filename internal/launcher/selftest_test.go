package launcher

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"cricketstream/internal/server"
)

// selfTestOutput runs the pre-flight check against a /health with no scorer's folder set.
func selfTestOutput(t *testing.T, scoring string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(M{"pcs": M{"folder_set": false}, "stats": M{}, "assets": M{}, "match": M{}, "keys": M{}})
	}))
	t.Cleanup(srv.Close)
	_, p, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	saved := server.Port
	server.Port, _ = strconv.Atoi(p)
	t.Cleanup(func() { server.Port = saved })

	r, w, _ := os.Pipe()
	out := os.Stdout
	os.Stdout = w
	SelfTest(scoring)
	os.Stdout = out
	w.Close()
	b, _ := io.ReadAll(r)
	return string(b)
}

// A practice match or scoring by hand has no scorer's feed to find: the pre-flight check
// says so rather than warn a new club about a PCS folder they don't need.
func TestSelfTestExpectsNoFeedForPracticeOrManual(t *testing.T) {
	if out := selfTestOutput(t, ""); !strings.Contains(out, "PCS output folder not set") {
		t.Errorf("NV Play match day lost its warning:\n%s", out)
	}
	for _, scoring := range []string{"practice", "manual"} {
		out := selfTestOutput(t, scoring)
		if strings.Contains(out, "PCS") || !strings.Contains(out, "Scorer feed: not needed") {
			t.Errorf("%s:\n%s", scoring, out)
		}
	}
}
