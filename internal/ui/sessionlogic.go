package ui

// The parts of the session page that need no window: the reconnect
// schedule, the resize debounce decision, the slide of the floating toolbar,
// the status texts and the clipboard bitmap. No build tag, so they are
// tested on Linux.

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/draw"
	"strings"
	"time"

	"github.com/mevdschee/srdp4win/internal/rdp"
)

// Timings of the session page and the floating toolbar.
const (
	resizeDebounce = 250 * time.Millisecond
	slideDuration  = 150 * time.Millisecond
	slideHideDelay = 500 * time.Millisecond
	slideIntro     = 2 * time.Second
	slideStrip     = 2 // pixels of the hidden toolbar left on screen
)

// backoffDelay is the wait before SSH reconnect attempt n (from 1): 1s, 2s,
// 5s, then 10s every time.
func backoffDelay(attempt int) time.Duration {
	switch {
	case attempt <= 1:
		return time.Second
	case attempt == 2:
		return 2 * time.Second
	case attempt == 3:
		return 5 * time.Second
	}
	return 10 * time.Second
}

// sessState is where a session page is in its life.
type sessState int

const (
	stSSH          sessState = iota // dialing the SSH host
	stSSHLogin                      // SSH host key and login prompts
	stTunnel                        // opening the listener
	stRDP                           // the backend connects through the tunnel
	stRDPLogin                      // transport up, logging in to the desktop
	stConnected                     // desktop on screen
	stReconnecting                  // SSH or RDP dropped, getting it back
	stDisconnected                  // ended with a reason, nothing running
)

// busy reports a session that is up or coming up.
func (s sessState) busy() bool { return s != stDisconnected }

// statusLeft is the state text of the status line. Attempt counts the
// reconnects, reason is why the session ended, noDynamic says the server
// refused dynamic resolution.
func statusLeft(s sessState, sshHost, rdpHost string, attempt int, reason string, noDynamic bool) string {
	switch s {
	case stSSH:
		return "Connecting to " + sshHost
	case stSSHLogin, stRDPLogin:
		return "Logging in"
	case stTunnel:
		return "Opening tunnel"
	case stRDP:
		return "Connecting to " + rdpHost
	case stConnected:
		if noDynamic {
			return "Connected (the server has no dynamic resolution: scaled)"
		}
		return "Connected"
	case stReconnecting:
		if attempt > 0 {
			return fmt.Sprintf("Reconnecting (%d)", attempt)
		}
		return "Reconnecting"
	}
	if reason == "" {
		return "Disconnected"
	}
	return "Disconnected: " + reason
}

// modeName is a resize mode in the status line.
func modeName(m rdp.ResizeMode) string {
	switch m {
	case rdp.ResizeScaled:
		return "scaled"
	case rdp.ResizeFixed:
		return "fixed"
	}
	return "dynamic"
}

// statusRight is the remote size and resize mode ("1920 x 1080, dynamic"),
// empty without a remote desktop.
func statusRight(width, height int, m rdp.ResizeMode) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	return fmt.Sprintf("%d x %d, %s", width, height, modeName(m))
}

// sessRect is a rectangle in the page's client coordinates.
type sessRect struct{ x, y, w, h int }

func (r sessRect) empty() bool { return r.w <= 0 || r.h <= 0 }

// sessionRect is the part of a w x h page the session gets: everything in
// full screen, else what is below the toolbar row.
func sessionRect(w, h, barHeight int, fullScreen bool) sessRect {
	if fullScreen || barHeight <= 0 {
		return sessRect{0, 0, max(w, 0), max(h, 0)}
	}
	return sessRect{0, barHeight, max(w, 0), max(h-barHeight, 0)}
}

// resizePlan decides what to do when the session rectangle changes from
// have to want. A move is applied at once; a size change of a live session
// waits for the debounce (the remote desktop is resized, which is slow), so
// now keeps the old size and later is true. Without a live session, or
// without a size so far, the whole change is applied at once.
func resizePlan(have, want sessRect, live bool) (now sessRect, later bool) {
	if want.empty() {
		return have, false // minimized: keep what the session has
	}
	if !live || have.empty() || (have.w == want.w && have.h == want.h) {
		return want, false
	}
	return sessRect{want.x, want.y, have.w, have.h}, true
}

// scalePercent is the monitor scale for a DPI (96 is 100).
func scalePercent(dpi int) int {
	if dpi <= 0 {
		return 100
	}
	return dpi * 100 / 96
}

// slideEase maps linear progress 0..1 to an ease-out curve.
func slideEase(t float64) float64 {
	if t <= 0 {
		return 0
	}
	if t >= 1 {
		return 1
	}
	u := 1 - t
	return 1 - u*u*u
}

// slidePos is how far the toolbar is out (0 hidden, 1 shown) at elapsed
// into a slide from one position to another; done when it arrived. A slide
// over part of the way takes part of the time.
func slidePos(from, to float64, elapsed time.Duration) (pos float64, done bool) {
	dist := to - from
	if dist < 0 {
		dist = -dist
	}
	total := time.Duration(float64(slideDuration) * dist)
	if total <= 0 || elapsed >= total {
		return to, true
	}
	if elapsed < 0 {
		elapsed = 0
	}
	return from + (to-from)*slideEase(float64(elapsed)/float64(total)), false
}

// slideVisible is the number of pixel rows of a toolbar of the given height
// on screen at pos: the strip when hidden, everything when shown.
func slideVisible(pos float64, height int) int {
	if height <= slideStrip {
		return height
	}
	pos = min(1, max(0, pos))
	return slideStrip + int(pos*float64(height-slideStrip)+0.5)
}

// centred is the left edge that centres width inside [left, left+span).
func centred(left, span, width int) int { return left + (span-width)/2 }

// dibFromImage packs an image as a CF_DIB clipboard bitmap: a 40 byte
// BITMAPINFOHEADER and bottom-up 32 bit BGR rows.
func dibFromImage(img image.Image) []byte {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	rgba, ok := img.(*image.RGBA)
	if !ok || rgba.Rect.Min != (image.Point{}) {
		rgba = image.NewRGBA(image.Rect(0, 0, w, h))
		draw.Draw(rgba, rgba.Rect, img, b.Min, draw.Src)
	}
	out := make([]byte, 40+w*h*4)
	le := binary.LittleEndian
	le.PutUint32(out[0:], 40)
	le.PutUint32(out[4:], uint32(w))
	le.PutUint32(out[8:], uint32(h)) // positive: bottom-up
	le.PutUint16(out[12:], 1)
	le.PutUint16(out[14:], 32)
	le.PutUint32(out[20:], uint32(w*h*4))
	for y := 0; y < h; y++ {
		src := rgba.Pix[(h-1-y)*rgba.Stride:]
		dst := out[40+y*w*4:]
		for x := 0; x < w; x++ {
			dst[x*4+0] = src[x*4+2]
			dst[x*4+1] = src[x*4+1]
			dst[x*4+2] = src[x*4+0]
		}
	}
	return out
}

// sshErrText shortens an SSH dial error for the page: the library's own
// prefixes say nothing to the user.
func sshErrText(s string) string {
	for _, prefix := range []string{"ssh handshake: ", "ssh: handshake failed: ", "ssh: "} {
		s = strings.TrimPrefix(s, prefix)
	}
	if strings.Contains(s, "unable to authenticate") {
		return "the SSH login was refused (wrong user, key or password)"
	}
	return s
}
