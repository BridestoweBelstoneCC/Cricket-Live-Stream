package app

import "testing"

// Sound and picture (the server's /health "av" block): each said once, and once again
// when it's put right.
func TestSoundAndPictureAlerts(t *testing.T) {
	av := func(sound string, frozen bool) map[string]any {
		return map[string]any{"av": map[string]any{"watching": true, "sound": sound,
			"cameras": []any{map[string]any{"name": "Camera 2", "frozen": frozen}}}}
	}
	var s alertState
	if n := s.health(av("ok", false)); len(n) != 0 {
		t.Fatalf("all well: %v", n)
	}
	if n := s.health(av("none yet", false)); len(n) != 0 {
		t.Fatalf("a stream with no sound yet: %v", n)
	}
	n := s.health(av("silent", false))
	if len(n) != 1 || n[0].Title != "No sound on the stream" || !n[0].Warn || n[0].Local {
		t.Fatalf("silence: %+v", n)
	}
	if n := s.health(av("silent", false)); len(n) != 0 {
		t.Fatalf("said twice: %v", n)
	}
	n = s.health(av("ok", true))
	if len(n) != 2 || n[0].Title != "The sound is back" || n[1].Title != "Camera 2 has frozen" || !n[1].Warn {
		t.Fatalf("sound back, camera frozen: %+v", n)
	}
	if n := s.health(av("ok", true)); len(n) != 0 {
		t.Fatalf("said twice: %v", n)
	}
	n = s.health(av("ok", false))
	if len(n) != 1 || n[0].Title != "Camera 2 is moving again" || n[0].Warn {
		t.Fatalf("camera back: %+v", n)
	}
	// Off air the block says nothing, whatever it holds.
	off := map[string]any{"av": map[string]any{"watching": false, "sound": "silent"}}
	if n := s.health(off); len(n) != 0 {
		t.Fatalf("off air: %v", n)
	}
}
