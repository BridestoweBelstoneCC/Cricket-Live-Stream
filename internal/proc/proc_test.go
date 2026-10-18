package proc

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The test binary re-runs itself as the "external program", so these work anywhere.
func TestMain(m *testing.M) {
	switch os.Getenv("PROC_TEST_CHILD") {
	case "echo":
		os.Stdout.WriteString("out:" + strings.Join(os.Args[1:], ","))
		os.Stderr.WriteString("err-line")
		os.Exit(0)
	case "fail":
		os.Exit(3)
	case "sleep":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "pwd":
		wd, _ := os.Getwd()
		os.Stdout.WriteString(wd)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func child(t *testing.T, mode string) string {
	t.Helper()
	t.Setenv("PROC_TEST_CHILD", mode)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

func TestCapturesOutputAndArgs(t *testing.T) {
	r, err := Run(child(t, "echo"), []string{"a", "b c"}, Options{Timeout: 20 * time.Second})
	if err != nil || r.Code != 0 || r.Stdout != "out:a,b c" || r.Stderr != "err-line" {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestNonZeroExitIsAResultNotAnError(t *testing.T) {
	r, err := Run(child(t, "fail"), nil, Options{Timeout: 20 * time.Second})
	if err != nil || r.Code != 3 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestTimeoutKillsIt(t *testing.T) {
	start := time.Now()
	r, err := Run(child(t, "sleep"), nil, Options{Timeout: 500 * time.Millisecond, BelowNormal: true})
	if !errors.Is(err, ErrTimeout) || r.Code != -1 {
		t.Fatalf("%+v %v", r, err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("wasn't killed at the time limit")
	}
}

func TestMissingProgramIsAnError(t *testing.T) {
	if _, err := Run("no-such-program-cricketstream", nil, Options{Timeout: 5 * time.Second}); err == nil {
		t.Fatal("ran a program that doesn't exist")
	}
}

func TestRunsInTheGivenFolder(t *testing.T) {
	dir := t.TempDir()
	r, err := Run(child(t, "pwd"), nil, Options{Timeout: 20 * time.Second, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got, want := r.Stdout, dir
	if runtime.GOOS == "windows" {
		got, want = strings.ToLower(got), strings.ToLower(want)
	}
	if ev, err := filepath.EvalSymlinks(want); err == nil {
		want = ev
	}
	if ev, err := filepath.EvalSymlinks(got); err == nil {
		got = ev
	}
	if got != want {
		t.Fatalf("ran in %q, want %q", got, want)
	}
}
