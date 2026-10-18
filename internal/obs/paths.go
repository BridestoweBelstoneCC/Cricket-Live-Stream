// Package obs is everything that drives OBS Studio: preparing its own config files while
// it is CLOSED (prep), building scenes and sources over its WebSocket while it is OPEN
// (setup), and the placement rules both share.
package obs

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"cricketstream/internal/obsws"
)

const (
	WSPort        = 4455
	ReplaySeconds = "25"
)

// LaunchArgs: --startreplaybuffer because the buffer is enabled in basic.ini before
// launch. --disable-shutdown-check suppresses the crash prompt on OBS releases that still
// have it; it is NOT what stops the prompt on OBS 30+ (OBS 32.1.1 contains no trace of
// the option and showed the prompt with it passed) — ClearCrashSentinel() does that.
var LaunchArgs = []string{"--startreplaybuffer", "--disable-shutdown-check"}

// PortableEnv names a PORTABLE OBS copy to use instead of the installed one. Testing
// against a real OBS must only ever use a portable copy: overriding APPDATA doesn't
// isolate OBS on Windows (it asks Windows for the folder directly), and an "isolated"
// test once rearranged a maintainer's real scenes. A folder without
// obs_portable_mode.txt is refused.
const PortableEnv = "CRICKETSTREAM_OBS_PORTABLE"

// ErrNotPortable is returned when PortableEnv names a folder that isn't portable OBS.
var ErrNotPortable = errors.New(PortableEnv + " is set but that folder has no obs_portable_mode.txt — refusing to touch it")

func portableRoot() (string, error) {
	root := os.Getenv(PortableEnv)
	if root == "" {
		return "", nil
	}
	if _, err := os.Stat(filepath.Join(root, "obs_portable_mode.txt")); err != nil {
		return "", ErrNotPortable
	}
	return root, nil
}

// ConfigDir is OBS's per-user settings folder (it may not exist yet on a fresh install).
// Swappable for tests.
var ConfigDir = defaultConfigDir

func defaultConfigDir() string {
	if root, err := portableRoot(); err != nil {
		panic(err)
	} else if root != "" {
		return filepath.Join(root, "config", "obs-studio")
	}
	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("APPDATA")
		if base == "" {
			home, _ := os.UserHomeDir()
			base = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(base, "obs-studio")
	case "darwin":
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Application Support", "obs-studio")
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "obs-studio")
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// FindOBS returns the OBS executable (the .app bundle on a Mac), or "". Swappable.
var FindOBS = func() string {
	if root, err := portableRoot(); err != nil {
		return ""
	} else if root != "" {
		p := filepath.Join(root, "bin", "64bit", "obs64.exe")
		if isFile(p) {
			return p
		}
		return ""
	}
	switch runtime.GOOS {
	case "windows":
		// The installer records its folder in the registry; honours a non-default drive.
		candidates := registryOBSFolders()
		for _, env := range []string{"ProgramFiles", "ProgramW6432", "ProgramFiles(x86)"} {
			if v := os.Getenv(env); v != "" {
				candidates = append(candidates, filepath.Join(v, "obs-studio"))
			}
		}
		for _, c := range candidates {
			if p := filepath.Join(c, "bin", "64bit", "obs64.exe"); isFile(p) {
				return p
			}
		}
		return ""
	case "darwin":
		home, _ := os.UserHomeDir()
		for _, c := range []string{"/Applications/OBS.app", filepath.Join(home, "Applications", "OBS.app")} {
			if isDir(c) {
				return c
			}
		}
		return ""
	}
	p, _ := exec.LookPath("obs")
	return p
}

// Running reports whether an OBS process is running. It errs towards true when it
// can't tell, because the cost of a wrong false is writing config files OBS will then
// overwrite on exit. Swappable.
var Running = func() bool {
	if runtime.GOOS == "windows" && sandboxed() {
		return len(PIDs()) > 0 // only the portable OBS counts
	}
	if runtime.GOOS == "windows" {
		out, err := hiddenCommand("tasklist", "/FI", "IMAGENAME eq obs64.exe", "/NH").Output()
		if err != nil {
			return true
		}
		return strings.Contains(strings.ToLower(string(out)), "obs64.exe")
	}
	err := exec.Command("pgrep", "-x", "-i", "obs").Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return false // pgrep exits 1 when nothing matches
	}
	return true // running, or couldn't tell
}

// PIDs lists running OBS processes (Windows only; nil elsewhere or on error). With
// PortableEnv set, only the portable copy's: everything that closes, kills or waits on OBS
// goes through here, and the OBS installed on the machine is never a sandbox's to touch.
func PIDs() []int {
	all := allPIDs()
	if !sandboxed() {
		return all
	}
	var ours []int
	for _, pid := range all {
		if insidePortable(processPath(pid)) {
			ours = append(ours, pid)
		}
	}
	return ours
}

// sandboxed: PortableEnv is set, so the only OBS this program may see is the portable one.
func sandboxed() bool { return os.Getenv(PortableEnv) != "" }

// insidePortable: exe belongs to the portable copy ("" — unreadable — never does).
func insidePortable(exe string) bool {
	root, err := portableRoot()
	if err != nil || root == "" || exe == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(exe))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// foreignOBS lists the OBS processes running that aren't the portable copy, while
// sandboxed (nil otherwise): their exe paths, or a description where it can't be read.
// Swappable for tests.
var foreignOBS = func() []string {
	if !sandboxed() {
		return nil
	}
	if runtime.GOOS != "windows" {
		if err := exec.Command("pgrep", "-x", "-i", "obs").Run(); err == nil {
			return []string{"an OBS (not a portable copy)"}
		}
		return nil
	}
	var out []string
	for _, pid := range allPIDs() {
		if exe := processPath(pid); !insidePortable(exe) {
			if exe == "" {
				exe = fmt.Sprintf("obs64.exe (process %d)", pid)
			}
			out = append(out, exe)
		}
	}
	return out
}

// sandboxGuard is obsws's check before every connection: while sandboxed, never to an
// OBS on this machine that isn't the portable copy. OBS listens on the same port wherever
// it was installed, so the port alone can't tell them apart — the guard exists because a
// sandboxed copy once connected to the maintainer's own OBS and re-ran its setup.
func sandboxGuard(t obsws.Target) error {
	// OBS's own port: the only one CricketStream reaches a real OBS on (a test's fake OBS
	// listens on a port of its own).
	if !sandboxed() || t.Port != WSPort {
		return nil
	}
	switch strings.ToLower(t.Host) {
	case "", "localhost", "127.0.0.1", "::1":
	default:
		return nil
	}
	if f := foreignOBS(); len(f) > 0 {
		return fmt.Errorf("refusing to connect: %s is set, but the OBS running is %s — not the portable copy", PortableEnv, f[0])
	}
	return nil
}

func init() { obsws.Guard = sandboxGuard }

func allPIDs() []int {
	if runtime.GOOS != "windows" {
		return nil
	}
	out, err := hiddenCommand("tasklist", "/FI", "IMAGENAME eq obs64.exe", "/FO", "CSV", "/NH").Output()
	if err != nil {
		return nil
	}
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		cols := strings.Split(strings.TrimSpace(line), `","`)
		if len(cols) > 1 && strings.EqualFold(strings.Trim(cols[0], `"`), "obs64.exe") {
			if n, err := strconv.Atoi(strings.Trim(cols[1], `"`)); err == nil {
				pids = append(pids, n)
			}
		}
	}
	return pids
}

// PortOpen reports whether something accepts TCP connections on the port.
func PortOpen(port int, host string, timeout time.Duration) bool {
	if host == "" {
		host = "127.0.0.1"
	}
	c, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), timeout)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// WaitForWebsocket waits for OBS's WebSocket port to answer. Swappable.
var WaitForWebsocket = func(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if PortOpen(port, "", time.Second) {
			return true
		}
		sleep(time.Second)
	}
	return false
}

var sleep = time.Sleep

// Launch starts OBS detached, so closing this console doesn't take OBS with it.
// Swappable.
var Launch = func(exe string, args []string) error {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", append([]string{"-a", exe, "--args"}, args...)...).Start()
	}
	cmd := exec.Command(exe, args...)
	// The working directory matters on Windows: OBS resolves its data folder relative
	// to it and fails to start ("Failed to find locale/en-US.ini") from anywhere else.
	cmd.Dir = filepath.Dir(exe)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// Kill force-closes OBS. Only for an OBS that's already frozen: a force-close leaves the
// crash marker behind, which is what lets the crash recovery reopen it afterwards.
func Kill() {
	for _, pid := range PIDs() {
		hiddenCommand("taskkill", "/F", "/PID", strconv.Itoa(pid)).Run()
	}
}

func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
