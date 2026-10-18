// Package voice is spoken commentary: text to a WAV file with the computer's own voices.
//
// Free and offline — Windows' built-in speech engine (preferring a British English voice
// such as Microsoft Hazel), macOS `say` (Daniel), or espeak on Linux. The text always
// goes through a file, never a command line, so a commentary line can't break or inject
// into the command. Optional and off by default in the panel: these voices are
// serviceable, not broadcast quality.
package voice

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"cricketstream/internal/proc"
)

const psScript = `
param([string]$TextFile, [string]$OutFile, [string]$Culture)
Add-Type -AssemblyName System.Speech
$s = New-Object System.Speech.Synthesis.SpeechSynthesizer
$v = $s.GetInstalledVoices() | Where-Object { $_.Enabled -and $_.VoiceInfo.Culture.Name -eq $Culture } | Select-Object -First 1
if ($v) { $s.SelectVoice($v.VoiceInfo.Name) }
$s.Rate = 0
$s.SetOutputToWaveFile($OutFile)
$s.Speak([System.IO.File]::ReadAllText($TextFile, [System.Text.Encoding]::UTF8))
$s.Dispose()
`

// Swappable for tests.
var (
	run      proc.Runner = proc.Run
	lookPath             = exec.LookPath
	goos                 = runtime.GOOS
)

// LastLine is the last line of a program's stderr (capped at 200 chars), or def.
func LastLine(s, def string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	lines := strings.Split(s, "\n")
	return Truncate(strings.TrimRight(lines[len(lines)-1], "\r"), 200)
}

// Truncate cuts s to at most n runes (Python's s[:n]).
func Truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// Synthesize writes text as speech to outPath (WAV). Returns (ok, message): the path
// on success, the reason otherwise. Never panics.
func Synthesize(text, outPath, culture string, timeout time.Duration) (ok bool, msg string) {
	defer func() {
		if r := recover(); r != nil {
			ok, msg = false, "speech failed"
		}
	}()
	if culture == "" {
		culture = "en-GB"
	}
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return false, "nothing to say"
	}
	work, err := os.MkdirTemp("", "voice_")
	if err != nil {
		return false, Truncate(err.Error(), 200)
	}
	defer os.RemoveAll(work)
	txt := filepath.Join(work, "line.txt")
	if err := os.WriteFile(txt, []byte(text), 0o644); err != nil {
		return false, Truncate(err.Error(), 200)
	}
	if abs, err := filepath.Abs(outPath); err == nil {
		os.MkdirAll(filepath.Dir(abs), 0o755)
	}
	var name string
	var args []string
	switch goos {
	case "windows":
		ps1 := filepath.Join(work, "speak.ps1")
		if err := os.WriteFile(ps1, []byte(psScript), 0o644); err != nil {
			return false, Truncate(err.Error(), 200)
		}
		name = "powershell"
		args = []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
			"-File", ps1, "-TextFile", txt, "-OutFile", outPath, "-Culture", culture}
	case "darwin":
		name = "say"
		args = []string{"-v", "Daniel", "-o", outPath, "--data-format=LEI16@22050", "-f", txt}
	default:
		engine, err := lookPath("espeak-ng")
		if err != nil {
			engine, err = lookPath("espeak")
		}
		if err != nil {
			return false, "no speech engine (install espeak-ng)"
		}
		name = engine
		args = []string{"-v", "en-gb", "-w", outPath, "-f", txt}
	}
	r, err := run(name, args, proc.Options{Timeout: timeout})
	if err == nil && r.Code != 0 && goos == "darwin" {
		// Daniel not installed: the default voice is better than silence
		r, err = run("say", []string{"-o", outPath, "--data-format=LEI16@22050", "-f", txt}, proc.Options{Timeout: timeout})
	}
	if errors.Is(err, proc.ErrTimeout) {
		return false, "speech engine took too long"
	}
	if err != nil {
		return false, Truncate(err.Error(), 200)
	}
	if st, serr := os.Stat(outPath); r.Code != 0 || serr != nil || st.Size() < 100 {
		return false, LastLine(r.Stderr, "speech failed")
	}
	return true, outPath
}
