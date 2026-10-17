// Package tools holds the match-day diagnostic tools: the telemetry logger, the camera
// encoder read-out, the RTSP source refresher and the quality-ladder test. None of them
// is used by the server; each is a subcommand of its own (stream_telemetry.py,
// camera_encoder.py, refresh_cam.py and stream_quality_test.py upstream).
package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"cricketstream/internal/assets"
	"cricketstream/internal/console"
	"cricketstream/internal/ini"
	"cricketstream/internal/obsws"
	"cricketstream/internal/port"
	"cricketstream/internal/pyval"
)

// ServerURL is the local CricketStream server. Tests point it at a fake.
var ServerURL = fmt.Sprintf("http://127.0.0.1:%d", port.Server())

// obsTarget is where OBS is: tests point it at a fake. The password comes from
// config.ini on every call, as each tool reread it when it reconnected.
var obsTarget = func() obsws.Target {
	return obsws.Target{Host: "localhost", Port: 4455, Password: config().Get("OBS", "obs_password", "")}
}

func config() *ini.File {
	f, err := ini.Load(assets.Join("config.ini"))
	if err != nil {
		return ini.New()
	}
	return f
}

// fail is sys.exit("message"): to stderr, then the pause every exit path owes a
// double-clicked console window.
func fail(format string, a ...any) int {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	console.Pause("")
	return 1
}

// getJSON GETs a server path and decodes the JSON body (nil on any failure).
func getJSON(path, token string, timeout time.Duration) (map[string]any, error) {
	req, err := http.NewRequest("GET", ServerURL+path, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP Error %d", resp.StatusCode)
	}
	// UseNumber keeps each number's literal, so "12.0" lands in the CSV as 12.0 the way
	// Python's json module would have kept it.
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// postJSON POSTs JSON; an HTTP error's JSON body is still returned, as the server
// explains refusals in it.
func postJSON(path string, body any, token string, timeout time.Duration) map[string]any {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", ServerURL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil || out == nil {
		if resp.StatusCode >= 400 {
			return map[string]any{"ok": false, "error": fmt.Sprintf("HTTP %d", resp.StatusCode)}
		}
		return map[string]any{}
	}
	return out
}

// pyRepr is Python's repr() of a decoded JSON value, for the read-outs that print a
// list or dict straight from a device's response.
func pyRepr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		x = strings.ReplaceAll(x, `\`, `\\`)
		if strings.Contains(x, "'") && !strings.Contains(x, `"`) {
			return `"` + x + `"`
		}
		return "'" + strings.ReplaceAll(x, "'", `\'`) + "'"
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = pyRepr(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		// Go's decoder doesn't keep key order; sorted is at least stable.
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = pyRepr(k) + ": " + pyRepr(x[k])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return pyval.Str(v)
}

// errName is the Python exception class name the original printed for a failure, which
// is what operators have learned to read ("TimeoutError" means wrong network).
func errName(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	switch {
	case isTimeout(err):
		return "TimeoutError"
	case strings.Contains(s, "refused"):
		return "ConnectionRefusedError"
	case strings.Contains(s, "no such host"):
		return "gaierror"
	case strings.Contains(s, "certificate") || strings.Contains(s, "tls"):
		return "SSLError"
	case strings.Contains(s, "HTTP Error"):
		return "HTTPError"
	}
	return "OSError"
}

func isTimeout(err error) bool {
	type timeout interface{ Timeout() bool }
	for e := err; e != nil; {
		if t, ok := e.(timeout); ok && t.Timeout() {
			return true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			break
		}
		e = u.Unwrap()
	}
	return strings.Contains(err.Error(), "timeout")
}
