package app

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cricketstream/internal/assets"
)

// Log is everything the app prints — the launcher, the server it supervises, the agent —
// kept for the window's activity log and written to logs/ beside the app, so there's a
// file to send when something goes wrong (there's no console window to scroll back in).
type Log struct {
	mu    sync.Mutex
	lines []logLine
	next  int
	part  string
	file  io.WriteCloser
	echo  io.Writer
	// onLine, if set, sees each complete line (the alerts watch for a server restart).
	onLine func(string)
}

type logLine struct {
	N    int    `json:"n"`
	Text string `json:"t"`
}

// logKeep is how many lines the window can scroll back through.
const logKeep = 3000

func (l *Log) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.echo != nil {
		l.echo.Write(b)
	}
	if l.file != nil {
		l.file.Write(b)
	}
	s := l.part + strings.ReplaceAll(string(b), "\r\n", "\n")
	parts := strings.Split(s, "\n")
	l.part = parts[len(parts)-1]
	for _, p := range parts[:len(parts)-1] {
		l.add(p)
	}
	return len(b), nil
}

func (l *Log) add(text string) {
	// A carriage return redraws the line in a console; keep what was drawn last.
	if i := strings.LastIndex(text, "\r"); i >= 0 {
		text = text[i+1:]
	}
	l.next++
	l.lines = append(l.lines, logLine{N: l.next, Text: text})
	if l.onLine != nil {
		l.onLine(text)
	}
	if len(l.lines) > logKeep {
		l.lines = append([]logLine(nil), l.lines[len(l.lines)-logKeep:]...)
	}
}

// Since is every line after n, and the number to ask from next time.
func (l *Log) Since(n int) ([]logLine, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := []logLine{}
	for _, ln := range l.lines {
		if ln.N > n {
			out = append(out, ln)
		}
	}
	return out, l.next
}

// LogFile is today's log beside the app.
func LogFile() string {
	return assets.Join("logs", "cricketstream-"+time.Now().Format("2006-01-02")+".log")
}

// capture points stdout and stderr (this process's, and every child process it starts)
// into l. The original stdout still gets a copy when there is one — a terminal, or a
// test reading the output. Returns the undo.
func (l *Log) capture() (func(), error) {
	if err := os.MkdirAll(filepath.Dir(LogFile()), 0o755); err == nil {
		if f, err := os.OpenFile(LogFile(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			l.file = f
			if st, err := f.Stat(); err == nil && st.Size() == 0 {
				// A new file: the UTF-8 mark, or Windows PowerShell shows ✓ and — as mojibake.
				f.Write([]byte{0xEF, 0xBB, 0xBF})
			}
			f.WriteString("\n──── " + time.Now().Format("2006-01-02 15:04:05") + " ────\n")
		}
	}
	r, w, err := os.Pipe()
	if err != nil {
		return func() {}, err
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	if hasStdout() {
		l.echo = oldOut
	}
	os.Stdout, os.Stderr = w, w
	done := make(chan struct{})
	go func() {
		io.Copy(l, r)
		close(done)
	}()
	return func() {
		os.Stdout, os.Stderr = oldOut, oldErr
		w.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second): // a child still holding the pipe open
		}
		l.mu.Lock()
		if l.part != "" {
			l.add(l.part)
			if l.file != nil {
				l.file.Write([]byte("\n"))
			}
			l.part = ""
		}
		if l.file != nil {
			l.file.Close()
			l.file = nil
		}
		l.mu.Unlock()
	}, nil
}

// hasStdout: is there anywhere for the copy to go? (A GUI build started from Explorer
// has no stdout at all.)
func hasStdout() bool {
	st, err := os.Stdout.Stat()
	return err == nil && st != nil
}
