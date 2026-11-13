package obsws_test

import (
	"testing"
	"time"

	"cricketstream/internal/obsws"
	"cricketstream/internal/obsws/obswstest"
)

func TestAuthResponseMatchesOBSsAlgorithm(t *testing.T) {
	// Python: base64(sha256(base64(sha256(pw+salt)) + challenge))
	if got := obsws.AuthResponse("supersecretpassword", "lM1GncleQOaCu9lT1yeUZhFYnqhsLLP1G5lAGo3ixaI=",
		"+IxH4CnCiqpX1rM9scsNynZzbOe4KhDeYcTNS3PDaeY="); got != "1Ct943GAT+6YQUUX47Ia/ncufilbe6+oD6lY+5kaCu4=" {
		t.Fatal(got)
	}
}

func TestCallWithPasswordSkipsEventsAndReportsRefusals(t *testing.T) {
	fake := obswstest.New("pw", func(rt string, d map[string]any) (bool, map[string]any) {
		switch rt {
		case "GetVersion":
			return true, map[string]any{"obsVersion": "32.1.1"}
		case "StartReplayBuffer":
			return false, nil
		}
		return true, nil
	})
	defer fake.Close()
	res := obsws.Call(fake.Target(), []obsws.Req{{Type: "GetVersion"}, {Type: "StartReplayBuffer"}, {Type: "SetCurrentProgramScene", Data: map[string]any{"sceneName": "Main"}}}, 3*time.Second)
	if res == nil || len(res) != 3 {
		t.Fatal(res)
	}
	if res[0]["obsVersion"] != "32.1.1" || res[1] != nil || res[2] == nil {
		t.Fatalf("%#v", res)
	}
}

func TestWrongPasswordIsUnreachable(t *testing.T) {
	fake := obswstest.New("right", nil)
	defer fake.Close()
	tgt := fake.Target()
	tgt.Password = "wrong"
	if res := obsws.Call(tgt, []obsws.Req{{Type: "GetVersion"}}, 2*time.Second); res != nil {
		t.Fatal(res)
	}
}

func TestNothingListeningIsUnreachable(t *testing.T) {
	if res := obsws.Call(obsws.Target{Host: "127.0.0.1", Port: 1}, []obsws.Req{{Type: "GetVersion"}}, time.Second); res != nil {
		t.Fatal(res)
	}
}

func TestFastReusesTheSocketAndReconnectsAfterARestart(t *testing.T) {
	fake := obswstest.New("", nil)
	var f obsws.Fast
	for i := 0; i < 3; i++ {
		if ok, reached := f.Call(fake.Target(), "SetCurrentProgramScene", map[string]any{"sceneName": "Main"}, time.Second); !ok || !reached {
			t.Fatal(ok, reached)
		}
	}
	if fake.Conns != 1 {
		t.Fatalf("%d connections for 3 cuts", fake.Conns)
	}
	// OBS restarts on the same port: the stale socket fails once, then a fresh one works.
	target := fake.Target()
	fake.Close()
	if ok, reached := f.Call(target, "SetCurrentProgramScene", nil, time.Second); ok || reached {
		t.Fatal("OBS is down; expected unreachable")
	}
}

// Meters asks for the sound-level events by name (they're never sent otherwise) and
// reports each input's loudest peak; an ordinary connection still asks for no such thing.
func TestMetersReadsEachInputsPeak(t *testing.T) {
	f := obswstest.New("pw", nil)
	defer f.Close()
	n := 0
	f.Meters = func() map[string][][]float64 {
		n++
		mic := 0.02
		if n == 3 {
			mic = 0.4 // one loud moment
		}
		return map[string][][]float64{
			"Ground mic":    {{0.01, mic, mic}, {0.01, mic / 2, mic / 2}},
			"Desktop Audio": {{0, 0, 0}, {0, 0, 0}},
			"No channels":   {},
		}
	}
	peaks, err := obsws.Meters(f.Target(), 300*time.Millisecond, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if peaks["Ground mic"] != 0.4 || peaks["Desktop Audio"] != 0 {
		t.Errorf("peaks %v, want the mic's loudest moment and silence", peaks)
	}
	if _, ok := peaks["No channels"]; !ok {
		t.Errorf("an input with no levels should still be listed: %v", peaks)
	}
	c, err := obsws.Dial(f.Target(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if len(f.Subscriptions) != 2 || f.Subscriptions[0] != 1<<16 || f.Subscriptions[1] != -1 {
		t.Errorf("subscriptions %v, want the meters for the listener and none named for an ordinary connection", f.Subscriptions)
	}
}

func TestMetersWithoutAnyLevelsIsAnError(t *testing.T) {
	f := obswstest.New("", nil) // an OBS that sends no levels
	defer f.Close()
	if _, err := obsws.Meters(f.Target(), 150*time.Millisecond, time.Second); err == nil {
		t.Error("no levels at all read as silence: it must be 'couldn't tell'")
	}
	if _, err := obsws.Meters(obsws.Target{Host: "127.0.0.1", Port: 1}, 100*time.Millisecond, 300*time.Millisecond); err == nil {
		t.Error("no OBS at all")
	}
}
