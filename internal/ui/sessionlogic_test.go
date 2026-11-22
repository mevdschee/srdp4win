package ui

import (
	"image"
	"image/color"
	"testing"
	"time"

	"github.com/mevdschee/srdp4win/internal/rdp"
)

func TestBackoffDelay(t *testing.T) {
	want := []time.Duration{time.Second, time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 10 * time.Second}
	for n, w := range want {
		if got := backoffDelay(n); got != w {
			t.Errorf("backoffDelay(%d) = %v, want %v", n, got, w)
		}
	}
	if got := backoffDelay(1000); got != 10*time.Second {
		t.Errorf("backoffDelay(1000) = %v", got)
	}
}

func TestStatusLeft(t *testing.T) {
	cases := []struct {
		s         sessState
		attempt   int
		reason    string
		noDynamic bool
		want      string
	}{
		{stSSH, 0, "", false, "Connecting to gate:22"},
		{stSSHLogin, 0, "", false, "Logging in"},
		{stTunnel, 0, "", false, "Opening tunnel"},
		{stRDP, 0, "", false, "Connecting to desk"},
		{stRDPLogin, 0, "", false, "Logging in"},
		{stConnected, 0, "", false, "Connected"},
		{stConnected, 0, "", true, "Connected (the server has no dynamic resolution: scaled)"},
		{stReconnecting, 0, "", false, "Reconnecting"},
		{stReconnecting, 3, "", false, "Reconnecting (3)"},
		{stDisconnected, 0, "", false, "Disconnected"},
		{stDisconnected, 0, "the server ended the session", false, "Disconnected: the server ended the session"},
	}
	for _, c := range cases {
		if got := statusLeft(c.s, "gate:22", "desk", c.attempt, c.reason, c.noDynamic); got != c.want {
			t.Errorf("statusLeft(%d) = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestBusy(t *testing.T) {
	for s := stSSH; s <= stDisconnected; s++ {
		if got, want := s.busy(), s != stDisconnected; got != want {
			t.Errorf("state %d busy = %v", s, got)
		}
	}
}

func TestStatusRight(t *testing.T) {
	if got := statusRight(1920, 1080, rdp.ResizeDynamic); got != "1920 x 1080, dynamic" {
		t.Error(got)
	}
	if got := statusRight(1280, 720, rdp.ResizeScaled); got != "1280 x 720, scaled" {
		t.Error(got)
	}
	if got := statusRight(800, 600, rdp.ResizeFixed); got != "800 x 600, fixed" {
		t.Error(got)
	}
	if got := statusRight(0, 0, rdp.ResizeDynamic); got != "" {
		t.Error(got)
	}
}

func TestSessionRect(t *testing.T) {
	if got := sessionRect(1000, 700, 40, false); got != (sessRect{0, 40, 1000, 660}) {
		t.Error(got)
	}
	if got := sessionRect(1000, 700, 40, true); got != (sessRect{0, 0, 1000, 700}) {
		t.Error(got)
	}
	if got := sessionRect(1000, 30, 40, false); got != (sessRect{0, 40, 1000, 0}) {
		t.Error(got)
	}
}

func TestResizePlan(t *testing.T) {
	a := sessRect{0, 40, 1000, 660}
	// First placement and placements before the session is live: at once.
	if now, later := resizePlan(sessRect{}, a, true); now != a || later {
		t.Error("first", now, later)
	}
	b := sessRect{0, 40, 1200, 800}
	if now, later := resizePlan(a, b, false); now != b || later {
		t.Error("not live", now, later)
	}
	// A live session: the size waits.
	if now, later := resizePlan(a, b, true); now != a || !later {
		t.Error("live size", now, later)
	}
	// Full screen: moved at once with the old size, then resized.
	full := sessRect{0, 0, 1920, 1080}
	if now, later := resizePlan(a, full, true); now != (sessRect{0, 0, 1000, 660}) || !later {
		t.Error("live move and size", now, later)
	}
	// Only a move.
	moved := sessRect{0, 0, 1000, 660}
	if now, later := resizePlan(a, moved, true); now != moved || later {
		t.Error("move", now, later)
	}
	// Minimized: nothing.
	if now, later := resizePlan(a, sessRect{0, 40, 0, 0}, true); now != a || later {
		t.Error("empty", now, later)
	}
}

func TestScalePercent(t *testing.T) {
	for dpi, want := range map[int]int{96: 100, 120: 125, 144: 150, 192: 200, 0: 100} {
		if got := scalePercent(dpi); got != want {
			t.Errorf("scalePercent(%d) = %d, want %d", dpi, got, want)
		}
	}
}

func TestSlidePos(t *testing.T) {
	if pos, done := slidePos(0, 1, 0); pos != 0 || done {
		t.Error("start", pos, done)
	}
	if pos, done := slidePos(0, 1, slideDuration); pos != 1 || !done {
		t.Error("end", pos, done)
	}
	if pos, done := slidePos(1, 0, slideDuration*2); pos != 0 || !done {
		t.Error("past the end", pos, done)
	}
	// Ease-out: more than half way at half time, and always moving forward.
	if pos, _ := slidePos(0, 1, slideDuration/2); pos <= 0.5 || pos >= 1 {
		t.Error("half", pos)
	}
	last := -1.0
	for e := time.Duration(0); e <= slideDuration; e += slideDuration / 10 {
		pos, _ := slidePos(0, 1, e)
		if pos < last || pos < 0 || pos > 1 {
			t.Errorf("down at %v: %v after %v", e, pos, last)
		}
		last = pos
	}
	last = 2
	for e := time.Duration(0); e <= slideDuration; e += slideDuration / 10 {
		pos, _ := slidePos(1, 0, e)
		if pos > last || pos < 0 || pos > 1 {
			t.Errorf("up at %v: %v after %v", e, pos, last)
		}
		last = pos
	}
	// Half the way takes half the time.
	if _, done := slidePos(0.5, 1, slideDuration/2); !done {
		t.Error("half way not done at half time")
	}
	if pos, done := slidePos(0.5, 0.5, 0); pos != 0.5 || !done {
		t.Error("no distance", pos, done)
	}
}

func TestSlideVisible(t *testing.T) {
	if got := slideVisible(0, 40); got != slideStrip {
		t.Error("hidden", got)
	}
	if got := slideVisible(1, 40); got != 40 {
		t.Error("shown", got)
	}
	if got := slideVisible(0.5, 40); got != slideStrip+19 {
		t.Error("half", got)
	}
	if got := slideVisible(-1, 40); got != slideStrip {
		t.Error("below", got)
	}
	if got := slideVisible(2, 40); got != 40 {
		t.Error("above", got)
	}
}

func TestCentred(t *testing.T) {
	if got := centred(1920, 1920, 400); got != 1920+760 {
		t.Error(got)
	}
	if got := centred(-1280, 1280, 300); got != -790 {
		t.Error(got)
	}
}

func TestDibFromImage(t *testing.T) {
	img := image.NewNRGBA(image.Rect(5, 5, 7, 8))        // 2 x 3, not at the origin
	img.Set(5, 5, color.NRGBA{R: 1, G: 2, B: 3, A: 255}) // top left
	img.Set(6, 7, color.NRGBA{R: 4, G: 5, B: 6, A: 255}) // bottom right
	dib := dibFromImage(img)
	if len(dib) != 40+2*3*4 {
		t.Fatal(len(dib))
	}
	if dib[0] != 40 || dib[4] != 2 || dib[8] != 3 || dib[12] != 1 || dib[14] != 32 || dib[16] != 0 {
		t.Error("header", dib[:40])
	}
	// Bottom-up: the last source row comes first.
	first := dib[40:]
	if got := first[4:7]; got[0] != 6 || got[1] != 5 || got[2] != 4 {
		t.Error("bottom right", got)
	}
	top := dib[40+2*2*4:]
	if got := top[0:3]; got[0] != 3 || got[1] != 2 || got[2] != 1 {
		t.Error("top left", got)
	}
}

func TestSendKeyList(t *testing.T) {
	seen := map[rdp.Keys]bool{}
	for _, k := range sendKeyList {
		if k.text == "" || seen[k.keys] {
			t.Errorf("bad entry %+v", k)
		}
		seen[k.keys] = true
	}
	for k := rdp.KeysCtrlAltDel; k <= rdp.KeysWinL; k++ {
		if !seen[k] {
			t.Errorf("key %d is not in the menu", k)
		}
	}
}

func TestSSHErrText(t *testing.T) {
	for in, want := range map[string]string{
		"ssh handshake: ssh: handshake failed: host key mismatch: the server key differs from known_hosts":                                  "host key mismatch: the server key differs from known_hosts",
		"ssh handshake: ssh: handshake failed: ssh: unable to authenticate, attempted methods [none password], no supported methods remain": "the SSH login was refused (wrong user, key or password)",
		"connect 10.0.0.1:22: i/o timeout": "connect 10.0.0.1:22: i/o timeout",
	} {
		if got := sshErrText(in); got != want {
			t.Errorf("sshErrText(%q) = %q, want %q", in, got, want)
		}
	}
}
