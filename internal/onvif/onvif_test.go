package onvif

import (
	"crypto/sha1"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeCamera answers discovery like a Reolink and serves ONVIF's device and media calls,
// checking the WS-Security digest against its own password.
type fakeCamera struct {
	http *httptest.Server
	udp  *net.UDPConn
	pass string
	got  []string
	// clock is how far the camera's clock is from the real time (a ground with no
	// internet: never set). It refuses a login signed more than 5 seconds off it, as
	// real cameras do.
	clock time.Duration
}

var reTag = func(tag string) *regexp.Regexp {
	return regexp.MustCompile(`(?s)<(?:[A-Za-z0-9]+:)?` + tag + `(?:\s[^>]*)?>(.*?)</`)
}

func newFakeCamera(t *testing.T, pass string) *fakeCamera {
	t.Helper()
	f := &fakeCamera{pass: pass}
	f.http = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s := string(b)
		now := time.Now().Add(f.clock).UTC()
		if strings.Contains(s, "GetSystemDateAndTime") { // no login needed
			io.WriteString(w, `<s:Envelope><s:Body><tds:GetSystemDateAndTimeResponse><tds:SystemDateAndTime>`+
				`<tt:DateTimeType>NTP</tt:DateTimeType><tt:UTCDateTime><tt:Time><tt:Hour>`+itoa(now.Hour())+`</tt:Hour>`+
				`<tt:Minute>`+itoa(now.Minute())+`</tt:Minute><tt:Second>`+itoa(now.Second())+`</tt:Second></tt:Time>`+
				`<tt:Date><tt:Year>`+itoa(now.Year())+`</tt:Year><tt:Month>`+itoa(int(now.Month()))+`</tt:Month>`+
				`<tt:Day>`+itoa(now.Day())+`</tt:Day></tt:Date></tt:UTCDateTime></tds:SystemDateAndTime>`+
				`</tds:GetSystemDateAndTimeResponse></s:Body></s:Envelope>`)
			return
		}
		created, _ := time.Parse("2006-01-02T15:04:05.000Z", firstOf(reTag("Created"), s))
		if d := created.Sub(now); d > 5*time.Second || d < -5*time.Second {
			w.WriteHeader(400)
			io.WriteString(w, `<s:Envelope><s:Body><s:Fault><s:Code><s:Subcode><s:Value>ter:NotAuthorized</s:Value></s:Subcode></s:Code></s:Fault></s:Body></s:Envelope>`)
			return
		}
		// The digest: SHA1(nonce + created + password).
		user := firstOf(reTag("Username"), s)
		digest := firstOf(reTag("Password"), s)
		nonce, _ := base64.StdEncoding.DecodeString(firstOf(reTag("Nonce"), s))
		h := sha1.New()
		h.Write(nonce)
		h.Write([]byte(firstOf(reTag("Created"), s)))
		h.Write([]byte(f.pass))
		if user != "admin" || digest != base64.StdEncoding.EncodeToString(h.Sum(nil)) {
			w.WriteHeader(400)
			io.WriteString(w, `<s:Envelope><s:Body><s:Fault><s:Code><s:Subcode><s:Value>ter:NotAuthorized</s:Value></s:Subcode></s:Code></s:Fault></s:Body></s:Envelope>`)
			return
		}
		switch {
		case strings.Contains(s, "GetCapabilities"):
			f.got = append(f.got, "caps")
			io.WriteString(w, `<s:Envelope><s:Body><tds:GetCapabilitiesResponse><tds:Capabilities><tt:Media><tt:XAddr>`+
				strings.Replace(f.http.URL, "127.0.0.1", "192.168.1.64", 1)+`/onvif/media_service</tt:XAddr></tt:Media></tds:Capabilities></tds:GetCapabilitiesResponse></s:Body></s:Envelope>`)
		case strings.Contains(s, "GetProfiles") && r.URL.Path == "/onvif/media_service":
			f.got = append(f.got, "profiles")
			io.WriteString(w, `<s:Envelope><s:Body><trt:GetProfilesResponse><trt:Profiles token="000" fixed="true"><tt:Name>mainStream</tt:Name></trt:Profiles>`+
				`<trt:Profiles token="001"><tt:Name>subStream</tt:Name></trt:Profiles></trt:GetProfilesResponse></s:Body></s:Envelope>`)
		case strings.Contains(s, "GetStreamUri") && strings.Contains(s, ">000<"):
			f.got = append(f.got, "uri")
			io.WriteString(w, `<s:Envelope><s:Body><trt:GetStreamUriResponse><trt:MediaUri><tt:Uri>rtsp://192.168.1.64:554/h264Preview_01_main?a=1&amp;b=2</tt:Uri>`+
				`</trt:MediaUri></trt:GetStreamUriResponse></s:Body></s:Envelope>`)
		default:
			w.WriteHeader(400)
			io.WriteString(w, `<s:Envelope><s:Body><s:Fault/></s:Body></s:Envelope>`)
		}
	}))
	t.Cleanup(f.http.Close)
	var err error
	f.udp, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.udp.Close() })
	go func() {
		buf := make([]byte, 8192)
		for {
			n, from, err := f.udp.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if !strings.Contains(string(buf[:n]), "NetworkVideoTransmitter") {
				continue
			}
			f.udp.WriteToUDP([]byte(`<?xml version="1.0"?><SOAP-ENV:Envelope><SOAP-ENV:Body><d:ProbeMatches><d:ProbeMatch>`+
				`<wsa:EndpointReference><wsa:Address>urn:uuid:1</wsa:Address></wsa:EndpointReference>`+
				`<d:Scopes>onvif://www.onvif.org/type/video_encoder onvif://www.onvif.org/name/RLC-510A onvif://www.onvif.org/hardware/RLC-510A onvif://www.onvif.org/location/country/china</d:Scopes>`+
				`<d:XAddrs>`+f.http.URL+`/onvif/device_service</d:XAddrs></d:ProbeMatch></d:ProbeMatches></SOAP-ENV:Body></SOAP-ENV:Envelope>`), from)
		}
	}()
	return f
}

func itoa(n int) string { return strconv.Itoa(n) }

func firstOf(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

func TestFindsACameraAndItsStream(t *testing.T) {
	f := newFakeCamera(t, "s3cret&<pass>")
	saved := ProbeAddr
	ProbeAddr = f.udp.LocalAddr().String()
	t.Cleanup(func() { ProbeAddr = saved })

	cams, err := Discover(800 * time.Millisecond)
	if err != nil || len(cams) != 1 {
		t.Fatalf("found %v, %v", cams, err)
	}
	c := cams[0]
	if c.Address != "127.0.0.1" || c.Name != "RLC-510A" || c.Make != "Reolink" || !strings.HasSuffix(c.XAddr, "/onvif/device_service") {
		t.Errorf("camera %+v", c)
	}

	u, err := StreamURL(c.XAddr, "admin", "s3cret&<pass>")
	if err != nil {
		t.Fatal(err)
	}
	want := "rtsp://admin:s3cret&%3Cpass%3E@127.0.0.1:554/h264Preview_01_main?a=1&b=2"
	if u != want {
		t.Errorf("stream url %q, want %q", u, want)
	}
	if strings.Join(f.got, ",") != "caps,profiles,uri" {
		t.Errorf("calls %v", f.got)
	}
	// A camera whose clock is an hour and a half out (never set) still takes the right
	// password: the login is signed with its time, not the laptop's.
	f.clock = -90 * time.Minute
	f.got = nil
	if u2, err := StreamURL(c.XAddr, "admin", "s3cret&<pass>"); err != nil || u2 != want {
		t.Errorf("camera with a wrong clock: %q, %v", u2, err)
	}
	if _, err := StreamURL(c.XAddr, "admin", "wrong"); err != ErrLogin {
		t.Errorf("wrong password: %v", err)
	}
	if m := Masked(u); strings.Contains(m, "s3cret") || !strings.HasPrefix(m, "rtsp://admin:•••@127.0.0.1:554/") {
		t.Errorf("masked %q", m)
	}
}

func TestUsualAddressForTheMake(t *testing.T) {
	for _, c := range []struct {
		cam  Camera
		want string
	}{
		{Camera{Address: "192.168.1.50", Name: "RLC-510A"}, "rtsp://admin:pw@192.168.1.50:554/h264Preview_01_main"},
		{Camera{Address: "192.168.1.51", Make: "HIKVISION", Hardware: "DS-2CD2143G2"}, "rtsp://admin:pw@192.168.1.51:554/Streaming/Channels/101"},
		{Camera{Address: "192.168.1.52", Name: "Amcrest IP8M"}, "rtsp://admin:pw@192.168.1.52:554/cam/realmonitor?channel=1&subtype=0"},
		{Camera{Address: "192.168.1.53", Name: "Tapo C320WS"}, "rtsp://admin:pw@192.168.1.53:554/stream1"},
		{Camera{Address: "192.168.1.54", Name: "Mystery Cam"}, ""},
	} {
		if got := GuessURL(c.cam, "admin", "pw"); got != c.want {
			t.Errorf("%+v: %q, want %q", c.cam, got, c.want)
		}
	}
}

func TestProbeAnswersThatArentCameras(t *testing.T) {
	for _, junk := range []string{"", "hello", "<XAddrs></XAddrs>", "<XAddrs>ftp://x/y</XAddrs>"} {
		if c := parseProbeMatch([]byte(junk), "1.2.3.4"); c != nil {
			t.Errorf("%q read as a camera: %+v", junk, c)
		}
	}
}

// The service address is the one on the IP that answered — not one that merely starts
// with it (.1 vs .10) — and a camera listing only its other network card's address is
// asked on the one that answered.
func TestProbeMatchPicksTheAnsweringAddress(t *testing.T) {
	ans := func(xaddrs string) []byte {
		return []byte(`<d:ProbeMatch><d:XAddrs>` + xaddrs + `</d:XAddrs></d:ProbeMatch>`)
	}
	c := parseProbeMatch(ans("http://192.168.1.10/onvif/device_service http://192.168.1.1/onvif/device_service"), "192.168.1.1")
	if c == nil || c.XAddr != "http://192.168.1.1/onvif/device_service" {
		t.Fatalf("%+v", c)
	}
	c = parseProbeMatch(ans("http://192.168.1.10:8000/onvif/device_service"), "192.168.1.1")
	if c == nil || c.XAddr != "http://192.168.1.1:8000/onvif/device_service" {
		t.Fatalf("other card: %+v", c)
	}
	c = parseProbeMatch(ans("http://10.0.0.5/onvif/device_service"), "fe80::1")
	if c == nil || c.XAddr != "http://[fe80::1]/onvif/device_service" {
		t.Fatalf("IPv6: %+v", c)
	}
}
