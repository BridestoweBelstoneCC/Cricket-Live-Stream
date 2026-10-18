package tools

// Read the Reolink camera's own encoder settings (resolution, bitrate, fps).
//
// Why this matters: OBS re-encodes whatever the camera sends it. If the camera's main
// stream is itself encoded at a low bitrate, the picture is already soft before OBS sees
// it, and raising the OBS bitrate cannot recover detail that was never captured. Last
// season's telemetry showed the stream pinned at 1.21 Mbps against a 2,500 kbps ceiling —
// the camera is the prime suspect.
//
// Credentials are read from the RTSP URL already stored in the OBS 'Cam' source, so
// nothing new needs storing, and they are sent in a POST body rather than a query string.
//
//	cricketstream camera-encoder                  # read settings from the OBS Cam source
//	cricketstream camera-encoder --host 192.0.2.10 --user admin      # prompts for password
//	cricketstream camera-encoder --source Cam2
//
// Must be run on the club network — the camera is not reachable from anywhere else.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"cricketstream/internal/obsws"
	"cricketstream/internal/pyval"
)

// cameraPort is the camera's HTTP port (tests move it).
var cameraPort = "80"

// CameraOptions are the command-line settings.
type CameraOptions struct {
	Source  string // OBS source to read the RTSP URL from (default Cam)
	Host    string // camera IP (skips the OBS lookup)
	User    string
	Channel int
}

// obsRTSPURL pulls the RTSP URL out of the named OBS media source ("" if it can't).
func obsRTSPURL(source string) string {
	c, err := obsws.Dial(obsTarget(), 8*time.Second)
	if err != nil {
		return ""
	}
	defer c.Close()
	r, err := c.Request("GetInputSettings", map[string]any{"inputName": source})
	if err != nil || !r.OK {
		return ""
	}
	s, _ := r.Data["inputSettings"].(map[string]any)
	u, _ := s["input"].(string)
	return u
}

// cameraAPI POSTs to the Reolink CGI API. Credentials go in the body, never the query
// string.
func cameraAPI(host string, params url.Values, body any) (any, error) {
	addr := host
	if cameraPort != "80" {
		addr = net.JoinHostPort(host, cameraPort)
	}
	b, _ := json.Marshal(body)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Post(
		"http://"+addr+"/cgi-bin/api.cgi?"+params.Encode(), "application/json", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP Error %d", resp.StatusCode)
	}
	var out any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// firstEntry is `out[0] if isinstance(out, list) and out else {}`.
func firstEntry(out any) map[string]any {
	if l, ok := out.([]any); ok && len(l) > 0 {
		if m, ok := l[0].(map[string]any); ok {
			return m
		}
	}
	return map[string]any{}
}

func isCode0(entry map[string]any) bool {
	f, ok := entry["code"].(float64)
	return ok && f == 0
}

func cameraLogin(host, user, password string) (string, error) {
	out, err := cameraAPI(host, url.Values{"cmd": {"Login"}}, []any{map[string]any{
		"cmd": "Login", "param": map[string]any{"User": map[string]any{"userName": user, "password": password}}}})
	if err != nil {
		return "", err
	}
	entry := firstEntry(out)
	if !isCode0(entry) {
		var detail any = entry
		if e, ok := entry["error"].(map[string]any); ok && e["detail"] != nil {
			detail = e["detail"]
		}
		return "", fmt.Errorf("login rejected: %s", pyStrRepr(detail))
	}
	v, _ := entry["value"].(map[string]any)
	t, _ := v["Token"].(map[string]any)
	name, ok := t["name"].(string)
	if !ok {
		return "", errors.New("'Token'")
	}
	return name, nil
}

// pyStrRepr is str() in an f-string: strings plain, containers as repr.
func pyStrRepr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return pyRepr(v)
}

func describe(stream map[string]any, label string) {
	fmt.Printf("\n  %s\n", label)
	if pyval.Truthy(stream["width"]) {
		fmt.Printf("    resolution : %sx%s\n", pyval.Str(stream["width"]), pyval.Str(stream["height"]))
	} else {
		fmt.Printf("    size       : %s\n", pyval.Str(stream["size"]))
	}
	fmt.Printf("    bitrate    : %s kbps\n", pyval.Str(stream["bitRate"]))
	fmt.Printf("    framerate  : %s fps\n", pyval.Str(stream["frameRate"]))
	fmt.Printf("    profile    : %s\n", pyval.Str(stream["profile"]))
	if pyval.Truthy(stream["gop"]) {
		fmt.Printf("    gop        : %s\n", pyval.Str(stream["gop"]))
	}
}

// CameraEncoder is `cricketstream camera-encoder`.
func CameraEncoder(o CameraOptions) int {
	if o.Source == "" {
		o.Source = "Cam"
	}
	host, user, password := o.Host, o.User, ""
	if host == "" {
		raw := obsRTSPURL(o.Source)
		if raw == "" {
			return fail("Could not read an RTSP URL from OBS source '%s'. Is OBS running? Otherwise pass --host and --user.", o.Source)
		}
		u, err := url.Parse(raw)
		if err != nil {
			return fail("Could not read an RTSP URL from OBS source '%s' (%v).", o.Source, err)
		}
		// Go decodes %40 etc. in the userinfo; the original sent them still encoded, which
		// failed for any password containing a reserved character.
		host = u.Hostname()
		if u.User != nil {
			user = u.User.Username()
			password, _ = u.User.Password()
		}
		fmt.Printf("  camera %s (from OBS source '%s', user '%s')\n", host, o.Source, user)
	}
	if password == "" {
		pw, ok := readPassword(fmt.Sprintf("password for %s@%s: ", user, host))
		if !ok {
			return fail("\n  no password given")
		}
		password = pw
	}

	// Fail fast with a clear message rather than a long socket timeout.
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, cameraPort), 4*time.Second)
	if err != nil {
		return fail("\n  Cannot reach %s:80 (%s). Are you on the same network as the camera?", host, errName(err))
	}
	conn.Close()

	token, err := cameraLogin(host, user, password)
	if err != nil {
		return fail("  login failed: %v", err)
	}

	out, err := cameraAPI(host, url.Values{"cmd": {"GetEnc"}, "token": {token}},
		[]any{map[string]any{"cmd": "GetEnc", "action": 1, "param": map[string]any{"channel": o.Channel}}})
	if err != nil {
		return fail("  GetEnc failed: %v", err)
	}
	entry := firstEntry(out)
	if !isCode0(entry) {
		return fail("  GetEnc failed: %s", pyRepr(entry))
	}

	value, _ := entry["value"].(map[string]any)
	val, _ := value["Enc"].(map[string]any)
	var rng map[string]any
	switch r := entry["range"].(type) {
	case []any:
		if len(r) > 0 {
			rng, _ = r[0].(map[string]any)
		}
	case map[string]any:
		rng = r
	}
	rngEnc, _ := rng["Enc"].(map[string]any)

	fmt.Println("\n=== current encoder settings ===")
	mainStream, _ := val["mainStream"].(map[string]any)
	if len(mainStream) > 0 {
		describe(mainStream, "main stream  (h264Preview_01_main — what OBS uses)")
	}
	if sub, _ := val["subStream"].(map[string]any); len(sub) > 0 {
		describe(sub, "sub stream   (h264Preview_01_sub)")
	}
	fmt.Printf("\n  audio enabled: %s\n", pyval.Str(val["audio"]))

	if mainRng, _ := rngEnc["mainStream"].(map[string]any); len(mainRng) > 0 {
		fmt.Println("\n=== what the camera will allow (main stream) ===")
		for _, kv := range [][2]string{{"size", "resolutions"}, {"bitRate", "bitrates (kbps)"}, {"frameRate", "framerates"}} {
			if v := mainRng[kv[0]]; pyval.Truthy(v) {
				fmt.Printf("  %-18s: %s\n", kv[1], pyStrRepr(v))
			}
		}
	}

	if cur := mainStream["bitRate"]; pyval.Truthy(cur) {
		kbps, _ := pyval.Float(cur)
		fmt.Println("\n=== read-out ===")
		if kbps < 4096 {
			fmt.Printf("  Main stream is %s kbps. That is low for this resolution — it is a"+
				" strong candidate for the soft picture, and raising it costs nothing on"+
				" a wired LAN. Change it in the camera's web UI: Settings -> Display ->"+
				" Stream (or Encode).\n", pyval.Str(cur))
		} else {
			fmt.Printf("  Main stream is %s kbps, which is healthy. The bitrate cap is"+
				" probably NOT the limiter — look at OBS output resolution instead.\n", pyval.Str(cur))
		}
	}
	return 0
}
