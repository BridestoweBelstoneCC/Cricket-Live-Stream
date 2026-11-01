package obs

import (
	"os"
	"path/filepath"
	"testing"
)

// Finish runs after match day has stopped — the server's OBS guard has gone, and the
// launcher's last look saw OBS still open — so it is the last chance to put OBS's update
// check back. The clubhouse screen's Finish left it paused until the next match.
func TestFinishPutsTheUpdateCheckBack(t *testing.T) {
	dir := cfgDir(t)
	oc, or := ConfigDir, Running
	t.Cleanup(func() { ConfigDir, Running = oc, or })
	ConfigDir = func() string { return dir }
	Running = func() bool { return false } // OBS has gone

	os.WriteFile(filepath.Join(dir, "global.ini"), []byte("[General]\nEnableAutoUpdates=true\n"), 0o644)
	if ok, err := PauseUpdateCheck(dir); !ok || err != nil {
		t.Fatalf("pause: %v %v", ok, err)
	}
	if ok, why := Finish(filepath.Join(t.TempDir(), "config.ini")); !ok {
		t.Fatalf("Finish refused with OBS closed: %s", why)
	}
	if v, _ := ReadINIValue(filepath.Join(dir, "global.ini"), "General", "EnableAutoUpdates"); v != "true" {
		t.Errorf("update check left as %q, want true", v)
	}
	if _, err := os.Stat(filepath.Join(dir, UpdatesMarker)); !os.IsNotExist(err) {
		t.Error("the paused marker is still there")
	}
}
