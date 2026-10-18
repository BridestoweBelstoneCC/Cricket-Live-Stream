package server

// Finding the club's IP camera from the control panel (Setup → Cameras → Find cameras)
// instead of typing an RTSP address with the camera's password in it. internal/onvif
// does the asking; this saves the result.
//
// The stream address holds the camera's password, so it never goes back to the browser
// (the same rule as every secret): it's saved straight into camera_rtsp_url /
// camera2_rtsp_url, and the panel is told only a masked copy.

import (
	"errors"
	"net"
	"net/url"
	"strings"
	"time"

	"cricketstream/internal/onvif"
)

// Swappable for tests.
var (
	discoverCameras = func() ([]onvif.Camera, error) { return onvif.Discover(3 * time.Second) }
	cameraStreamURL = onvif.StreamURL
	cameraReachable = func(rtsp string) bool {
		u, err := url.Parse(rtsp)
		if err != nil || u.Hostname() == "" {
			return false
		}
		port := u.Port()
		if port == "" {
			port = "554"
		}
		c, err := net.DialTimeout("tcp", net.JoinHostPort(u.Hostname(), port), 2*time.Second)
		if err != nil {
			return false
		}
		c.Close()
		return true
	}
)

// CamerasDiscover is GET /cameras/discover.
func CamerasDiscover() M {
	cams, err := discoverCameras()
	if err != nil {
		return M{"ok": false, "error": err.Error(), "cameras": []any{}}
	}
	out := []any{}
	for _, c := range cams {
		out = append(out, M{"address": c.Address, "name": c.Name, "make": c.Make, "hardware": c.Hardware, "xaddr": c.XAddr})
	}
	r := M{"ok": true, "cameras": out}
	if len(out) == 0 {
		r["error"] = "No camera answered. Check the camera is powered and on this network, and that ONVIF is switched " +
			"on in its own settings (often under Network → Advanced). Or type its RTSP address below."
	}
	return r
}

// CamerasUse is POST /cameras/use: the stream address for one found camera, from its own
// ONVIF answer (or the usual one for its make), saved as camera 1 or 2.
func CamerasUse(d M) (M, int) {
	cam := onvif.Camera{Address: str(d, "address", ""), Name: str(d, "name", ""), Make: str(d, "make", ""),
		Hardware: str(d, "hardware", ""), XAddr: str(d, "xaddr", "")}
	user, pass := str(d, "user", ""), str(d, "pass", "")
	slot := str(d, "slot", "1")
	if net.ParseIP(cam.Address) == nil {
		return M{"ok": false, "error": "Pick a camera from the list first."}, 400
	}
	if u, err := url.Parse(cam.XAddr); cam.XAddr != "" && (err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() != cam.Address) {
		return M{"ok": false, "error": "That isn't the camera's own address."}, 400
	}
	if user == "" {
		return M{"ok": false, "error": "Type the camera's username (often admin) and password."}, 400
	}
	how := "onvif"
	rtsp := ""
	var err error
	if cam.XAddr != "" {
		rtsp, err = cameraStreamURL(cam.XAddr, user, pass)
	} else {
		err = errors.New("no ONVIF address")
	}
	if errors.Is(err, onvif.ErrLogin) {
		return M{"ok": false, "error": "The camera didn't accept that username and password."}, 200
	}
	if err != nil {
		if rtsp = onvif.GuessURL(cam, user, pass); rtsp == "" {
			return M{"ok": false, "error": "The camera wouldn't say its stream address (" + err.Error() + "), and its make " +
				"isn't one CricketStream knows. Type its RTSP address below (the camera's manual has it)."}, 200
		}
		how = "usual"
	}
	if !cameraReachable(rtsp) {
		return M{"ok": false, "error": "Found the address, but the camera doesn't answer on its stream port — " +
			"is RTSP switched on in its own settings?"}, 200
	}
	key := "camera_rtsp_url"
	if slot == "2" {
		key = "camera2_rtsp_url"
	}
	UpdateState(func(st M) { st[key] = rtsp })
	note := "Saved — press Add camera to OBS."
	if how == "usual" {
		note = "Saved the usual address for a " + strings.TrimSpace(cam.Make) + " camera (the camera wouldn't say its own) — press Add camera to OBS, and check the picture."
	}
	return M{"ok": true, "slot": slot, "masked": onvif.Masked(rtsp), "how": how, "note": note}, 200
}
