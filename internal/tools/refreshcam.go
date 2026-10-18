package tools

// Periodically reload an OBS media source to stop an RTSP camera drifting out of sync.
//
// Match-day workaround. An RTSP feed in OBS's ffmpeg source can accumulate latency over a
// long session, so the picture slowly falls behind the overlay graphics — the score
// updates before you see the ball. Re-applying the source's settings tears the ffmpeg
// input down and rebuilds it, which is exactly what happens when you open Properties and
// click OK; this just does it on a timer instead of by hand.
//
// Each reload costs a visible hitch of roughly a second, so prefer the longest interval
// that keeps drift tolerable rather than the shortest one that feels safe. (The usual root
// cause is a scene without the camera source in it — see the OBS notes in CLAUDE.md; with
// the camera in every scene the drift rarely appears at all.)
//
//	cricketstream refresh-cam                  # reload "Cam" every 60s
//	cricketstream refresh-cam --interval 180   # every 3 minutes
//	cricketstream refresh-cam --source Cam2    # a differently-named source
//	cricketstream refresh-cam --once           # single reload, then exit
//
// Ctrl+C to stop. Never fails on a reload — a refresher that dies mid-match is worse than
// one that skips a beat, so errors are logged and the loop continues.

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"time"

	"cricketstream/internal/obsws"
	"cricketstream/internal/pyfmt"
)

// RefreshOptions are the command-line settings.
type RefreshOptions struct {
	Source   string  // media source name (default Cam)
	Interval float64 // seconds between reloads (default 60)
	Once     bool
	Host     string
	Port     int
	Stop     <-chan struct{} // tests; nil means Ctrl+C
}

func stamp(msg string) { fmt.Printf("  %s  %s\n", time.Now().Format("15:04:05"), msg) }

// reloadSource re-applies the source's own settings, which makes OBS rebuild the ffmpeg
// input. Settings are read back and written unchanged, so this never edits the camera's
// configuration — no risk of clobbering the URL or the hardware-decode flag.
func reloadSource(c *obsws.Conn, name string) error {
	got, err := c.Request("GetInputSettings", map[string]any{"inputName": name})
	if err != nil {
		return err
	}
	if !got.OK {
		if got.Comment != "" {
			return errors.New(got.Comment)
		}
		return fmt.Errorf("source '%s' not found", name)
	}
	settings := got.Data["inputSettings"]
	if settings == nil {
		settings = map[string]any{}
	}
	put, err := c.Request("SetInputSettings", map[string]any{"inputName": name, "inputSettings": settings, "overlay": true})
	if err != nil {
		return err
	}
	if !put.OK {
		if put.Comment != "" {
			return errors.New(put.Comment)
		}
		return errors.New("reload failed")
	}
	return nil
}

// RefreshCam is `cricketstream refresh-cam`.
func RefreshCam(o RefreshOptions) int {
	if o.Source == "" {
		o.Source = "Cam"
	}
	if o.Interval <= 0 {
		o.Interval = 60
	}
	target := func() obsws.Target {
		t := obsTarget()
		if o.Host != "" {
			t.Host = o.Host
		}
		if o.Port != 0 {
			t.Port = o.Port
		}
		return t
	}
	c, err := obsws.Dial(target(), 10*time.Second)
	if err != nil {
		return fail("  Could not connect to OBS: %v", err)
	}
	defer func() {
		if c != nil {
			c.Close()
		}
	}()
	stamp(fmt.Sprintf("connected to OBS — reloading '%s' every %ss", o.Source, pyfmt.Fixed(o.Interval, 0)))
	stamp("Ctrl+C to stop")

	if o.Once {
		if err := reloadSource(c, o.Source); err != nil {
			return fail("  reload failed: %v", err)
		}
		stamp(fmt.Sprintf("reloaded '%s'", o.Source))
		return 0
	}

	stop := o.Stop
	if stop == nil {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt)
		defer signal.Stop(sig)
		ch := make(chan struct{})
		go func() { <-sig; close(ch) }()
		stop = ch
	}
	n := 0
	for {
		select {
		case <-stop:
			stamp(fmt.Sprintf("stopped after %d reload(s)", n))
			return 0
		case <-time.After(time.Duration(o.Interval * float64(time.Second))):
		}
		if c == nil {
			err = errors.New("not connected")
		} else {
			err = reloadSource(c, o.Source)
		}
		if err == nil {
			n++
			stamp(fmt.Sprintf("reloaded '%s'  (#%d)", o.Source, n))
			continue
		}
		// Most likely the socket dropped (OBS restarted). Reconnect and carry on rather
		// than dying — this runs unattended through a whole match.
		stamp(fmt.Sprintf("reload failed: %s: %v — reconnecting", errName(err), err))
		if c != nil {
			c.Close()
		}
		c, err = obsws.Dial(target(), 10*time.Second)
		if err != nil {
			c = nil
			stamp(fmt.Sprintf("reconnect failed: %v — will retry next cycle", err))
		} else {
			stamp("reconnected")
		}
	}
}
