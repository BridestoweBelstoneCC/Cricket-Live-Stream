package server

import (
	"errors"
	"strings"
	"testing"

	"cricketstream/internal/onvif"
)

func stubCameras(t *testing.T, cams []onvif.Camera, stream func(xaddr, user, pass string) (string, error), reachable bool) {
	t.Helper()
	sd, ss, sr := discoverCameras, cameraStreamURL, cameraReachable
	t.Cleanup(func() { discoverCameras, cameraStreamURL, cameraReachable = sd, ss, sr })
	discoverCameras = func() ([]onvif.Camera, error) { return cams, nil }
	cameraStreamURL = stream
	cameraReachable = func(string) bool { return reachable }
}

var reolink = onvif.Camera{Address: "192.168.1.50", Name: "RLC-510A", Make: "Reolink", XAddr: "http://192.168.1.50:8000/onvif/device_service"}

// Found on the network, logged into, saved — and the password never comes back.
func TestFindAndUseACamera(t *testing.T) {
	s := newTestServer(t, "")
	stubCameras(t, []onvif.Camera{reolink}, func(xaddr, user, pass string) (string, error) {
		if user != "admin" || pass != "hunter2" {
			return "", onvif.ErrLogin
		}
		return "rtsp://admin:hunter2@192.168.1.50:554/h264Preview_01_main", nil
	}, true)

	_, d := s.getJSON("/cameras/discover")
	cams := d["cameras"].([]any)
	if len(cams) != 1 || cams[0].(M)["name"] != "RLC-510A" {
		t.Fatalf("discover %v", d)
	}
	cam := cams[0].(M)
	body := M{"address": cam["address"], "name": cam["name"], "make": cam["make"], "xaddr": cam["xaddr"], "user": "admin", "pass": "hunter2"}

	status, out := s.postJSON("/cameras/use", body)
	if status != 200 || out["ok"] != true || out["how"] != "onvif" {
		t.Fatalf("use: %d %v", status, out)
	}
	if strings.Contains(out["masked"].(string), "hunter2") {
		t.Error("the password came back to the browser")
	}
	if LoadState()["camera_rtsp_url"] != "rtsp://admin:hunter2@192.168.1.50:554/h264Preview_01_main" {
		t.Errorf("not saved: %v", LoadState()["camera_rtsp_url"])
	}
	if r := s.get("/state"); strings.Contains(string(r.body), "hunter2") {
		t.Error("GET /state shows the camera password")
	}

	// The bowler-end camera, and a wrong password.
	body["slot"] = "2"
	s.postJSON("/cameras/use", body)
	if !strings.Contains(LoadState()["camera2_rtsp_url"].(string), "h264Preview_01_main") {
		t.Error("slot 2 not saved")
	}
	body["pass"] = "wrong"
	if _, out := s.postJSON("/cameras/use", body); out["ok"] != false || !strings.Contains(out["error"].(string), "didn't accept") {
		t.Errorf("wrong password: %v", out)
	}
}

// ONVIF won't say: the usual address for the make, saying so; an unknown make or a
// camera that doesn't answer on its stream port is reported, not saved.
func TestCameraFallbacks(t *testing.T) {
	s := newTestServer(t, "")
	stubCameras(t, nil, func(string, string, string) (string, error) { return "", errors.New("no media service") }, true)
	body := M{"address": "192.168.1.50", "name": "RLC-510A", "make": "Reolink", "xaddr": reolink.XAddr, "user": "admin", "pass": "pw"}
	if _, out := s.postJSON("/cameras/use", body); out["ok"] != true || out["how"] != "usual" {
		t.Errorf("Reolink fallback: %v", out)
	}
	if !strings.HasSuffix(LoadState()["camera_rtsp_url"].(string), "/h264Preview_01_main") {
		t.Errorf("saved %v", LoadState()["camera_rtsp_url"])
	}
	setState(t, M{"camera_rtsp_url": ""})
	odd := M{"address": "192.168.1.60", "name": "Mystery", "xaddr": "http://192.168.1.60/onvif", "user": "admin", "pass": "pw"}
	if _, out := s.postJSON("/cameras/use", odd); out["ok"] != false || !strings.Contains(out["error"].(string), "Type its RTSP address") {
		t.Errorf("unknown make: %v", out)
	}
	cameraReachable = func(string) bool { return false }
	if _, out := s.postJSON("/cameras/use", body); out["ok"] != false || !strings.Contains(out["error"].(string), "stream port") {
		t.Errorf("unreachable: %v", out)
	}
	if LoadState()["camera_rtsp_url"] != "" {
		t.Error("saved a camera that didn't answer")
	}
}

// The ONVIF address must be the camera's own (no asking other hosts with its password),
// and none of this works without the login.
func TestCameraRoutesRefuse(t *testing.T) {
	s := newTestServer(t, "pw")
	stubCameras(t, []onvif.Camera{reolink}, func(string, string, string) (string, error) { return "", errors.New("x") }, true)
	if status, _ := s.getJSON("/cameras/discover"); status != 401 {
		t.Errorf("discover without the login: %d", status)
	}
	h := s.bearer("pw")
	elsewhere := M{"address": "192.168.1.50", "xaddr": "http://evil.example/onvif", "user": "admin", "pass": "pw"}
	if status, _ := s.postJSON("/cameras/use", elsewhere, h); status != 400 {
		t.Errorf("xaddr on another host: %d", status)
	}
	if status, _ := s.postJSON("/cameras/use", M{"address": "not-an-ip", "user": "a"}, h); status != 400 {
		t.Errorf("bad address: %d", status)
	}
	if status, _ := s.postJSON("/cameras/use", M{"address": "192.168.1.50", "user": "a"}); status != 401 {
		t.Errorf("use without the login: %d", status)
	}
}
