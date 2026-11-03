package launcher

import (
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

// The restart loop waits on the old process again when a relaunch fails. Waiting twice must
// work on the real process, not only on a test's fake: it once blocked for ever, which ended
// the restarts for the rest of the match with nothing said.
func TestWaitingOnAFinishedServerTwiceReturns(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := watchProc(cmd, nil)
	for i := 0; i < 2; i++ {
		done := make(chan struct{})
		go func() { p.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Fatalf("wait %d never returned", i+1)
		}
	}
}

// oneWaitProc is a real process's old Wait: a channel with one value in it.
type realExitedProc struct{ *osProc }

// A relaunch that fails is tried again, on a process that has really gone.
func TestAFailedRelaunchIsTriedAgain(t *testing.T) {
	_, died := stubShutdown(t)
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	first := watchProc(cmd, nil)
	launches := 0
	launch := func() (Proc, error) {
		launches++
		return nil, errors.New("couldn't start it")
	}
	code := make(chan int, 1)
	go func() {
		code <- RunServerWithRestarts(realExitedProc{first}, launch, "", 3, 0, func(time.Duration) {}, make(chan os.Signal))
	}()
	select {
	case c := <-code:
		if c != 1 || !*died {
			t.Fatalf("code %d, gave up: %v", c, *died)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("stopped trying after %d relaunch(es), with nothing said", launches)
	}
	if launches != 3 {
		t.Errorf("%d relaunches, want 3", launches)
	}
}
