package rdp

import (
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestPerformanceFlags(t *testing.T) {
	o := DefaultOptions()
	cases := []struct {
		q     Quality
		fonts bool
		want  uint32
	}{
		{QualityBest, true, 0x180},
		{QualityBest, false, 0x100},
		{QualityMedium, true, 0x1 | 0x2 | 0x4 | 0x20 | 0x80},
		{QualityMedium, false, 0x1 | 0x2 | 0x4 | 0x20},
		{QualityPoor, true, 0x1 | 0x2 | 0x4 | 0x8 | 0x20},
		{QualityPoor, false, 0x1 | 0x2 | 0x4 | 0x8 | 0x20},
	}
	for _, c := range cases {
		o.Quality, o.FontSmoothing = c.q, c.fonts
		if got := PerformanceFlags(o); got != c.want {
			t.Errorf("%v fonts=%v: got %#x, want %#x", c.q, c.fonts, got, c.want)
		}
	}
}

func TestEffectiveColorDepth(t *testing.T) {
	o := DefaultOptions()
	if got := EffectiveColorDepth(o); got != 32 {
		t.Errorf("default: %d", got)
	}
	o.ColorDepth = 24
	if got := EffectiveColorDepth(o); got != 24 {
		t.Errorf("24: %d", got)
	}
	o.ColorDepth = 7
	if got := EffectiveColorDepth(o); got != 32 {
		t.Errorf("unknown: %d", got)
	}
	o.Quality = QualityPoor
	if got := EffectiveColorDepth(o); got != 16 {
		t.Errorf("poor: %d", got)
	}
}

func TestFitSize(t *testing.T) {
	cases := [][4]int{
		{1920, 1080, 1920, 1080},
		{1921, 1081, 1920, 1081},
		{0, 0, 200, 200},
		{201, 150, 200, 200},
		{10000, 9000, 8192, 8192},
		{8193, 300, 8192, 300},
	}
	for _, c := range cases {
		if w, h := FitSize(c[0], c[1]); w != c[2] || h != c[3] {
			t.Errorf("FitSize(%d, %d) = %d, %d, want %d, %d", c[0], c[1], w, h, c[2], c[3])
		}
	}
}

func TestSessionSize(t *testing.T) {
	o := DefaultOptions()
	if w, h := SessionSize(o, ResizeDynamic, 1281, 801); w != 1280 || h != 801 {
		t.Errorf("dynamic: %d, %d", w, h)
	}
	if w, h := SessionSize(o, ResizeScaled, 0, 0); w != 1024 || h != 768 {
		t.Errorf("no size: %d, %d", w, h)
	}
	if w, h := SessionSize(o, ResizeFixed, 640, 480); w != 1920 || h != 1080 {
		t.Errorf("fixed: %d, %d", w, h)
	}
	o.FixedWidth = 0
	if w, h := SessionSize(o, ResizeFixed, 640, 480); w != 640 || h != 480 {
		t.Errorf("fixed without size: %d, %d", w, h)
	}
}

func TestScaleFactors(t *testing.T) {
	cases := [][3]int{
		{0, 100, 100}, {50, 100, 100}, {100, 100, 100}, {125, 125, 100}, {150, 150, 140},
		{175, 175, 140}, {200, 200, 180}, {350, 350, 180}, {900, 500, 180},
	}
	for _, c := range cases {
		if d, v := ScaleFactors(c[0]); d != c[1] || v != c[2] {
			t.Errorf("ScaleFactors(%d) = %d, %d, want %d, %d", c[0], d, v, c[1], c[2])
		}
	}
}

func TestPhysicalMM(t *testing.T) {
	if got := PhysicalMM(1920, 100); got != 508 {
		t.Errorf("1920 at 100%%: %d", got)
	}
	if got := PhysicalMM(3840, 200); got != 508 {
		t.Errorf("3840 at 200%%: %d", got)
	}
	if got := PhysicalMM(1, 100); got != 10 {
		t.Errorf("minimum: %d", got)
	}
}

func TestDisconnectText(t *testing.T) {
	cases := []struct {
		reason, ext int
		want        string
	}{
		{1, 0, "The session was disconnected."},
		{3, 12, "The user logged off."},
		{3, 5, "Another connection took over the session."},
		{2055, 0, "wrong user name or password"},
		{2308, 0, "was lost"},
		{516, 0, "Could not connect"},
		{3, 0x1005, "protocol error occurred (code 0x1005)"},
		{99999, 0, "reason 99999"},
		{99999, 77, "reason 99999, extended reason 77"},
		{99999, 26, "restarting"},
		{0, 768, "wrong user name or password"},
	}
	for _, c := range cases {
		if got := DisconnectText(c.reason, c.ext); !strings.Contains(got, c.want) {
			t.Errorf("DisconnectText(%d, %d) = %q, want it to contain %q", c.reason, c.ext, got, c.want)
		}
	}
}

func TestUserEnded(t *testing.T) {
	cases := []struct {
		reason, ext int
		want        bool
	}{
		{1, 0, true}, {2, 0, true}, {3, 0, false}, {3, 11, true}, {3, 12, true},
		{3, 5, false}, {3, 1, false}, {2308, 0, false}, {2055, 0, false}, {0, 0, false},
	}
	for _, c := range cases {
		if got := UserEnded(c.reason, c.ext); got != c.want {
			t.Errorf("UserEnded(%d, %d) = %v", c.reason, c.ext, got)
		}
	}
}

func TestAuthFailure(t *testing.T) {
	if !AuthFailure(2055, 0) || !AuthFailure(2567, 0) || !AuthFailure(3, 768) {
		t.Error("login failures not recognised")
	}
	if AuthFailure(1, 0) || AuthFailure(2308, 0) || AuthFailure(3, 12) {
		t.Error("other reasons taken for a login failure")
	}
}

func TestScancodes(t *testing.T) {
	for k := KeysCtrlAltDel; k <= KeysWinL; k++ {
		if len(Scancodes(k)) == 0 {
			t.Errorf("%v has no scancodes", k)
		}
		if strings.HasPrefix(k.String(), "Keys(") {
			t.Errorf("%d has no name", int(k))
		}
	}
	if Scancodes(Keys(99)) != nil {
		t.Error("unknown combination has scancodes")
	}
	cad := Scancodes(KeysCtrlAltDel)
	if cad[0].Code != 0x1D || cad[1].Code != 0x38 || cad[2].Code != 0x53 {
		t.Errorf("Ctrl+Alt+Del: %v", cad)
	}
	if w := Scancodes(KeysWin); !w[0].Extended || w[0].Code != 0x5B {
		t.Errorf("Win: %v", w)
	}
}

// fakeServer answers one X.224 connection request with answer and returns
// the request it got.
func fakeServer(t *testing.T, answer []byte) (net.Conn, <-chan []byte) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan []byte, 1)
	go func() {
		defer ln.Close()
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		var hdr [4]byte
		if _, err := io.ReadFull(c, hdr[:]); err != nil {
			got <- nil
			return
		}
		req := make([]byte, binary.BigEndian.Uint16(hdr[2:]))
		copy(req, hdr[:])
		if _, err := io.ReadFull(c, req[4:]); err != nil {
			got <- nil
			return
		}
		got <- req
		if answer != nil {
			c.Write(answer)
			// Keep the connection until the client is done.
			io.Copy(io.Discard, c)
		}
	}()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn, got
}

// confirm builds a connection confirm with a negotiation answer of the
// given type, or without one when typ is 0.
func confirm(typ byte, value uint32) []byte {
	b := []byte{0x03, 0x00, 0x00, 0x0B, 0x06, 0xD0, 0x00, 0x00, 0x12, 0x34, 0x00}
	if typ != 0 {
		b = append(b, typ, 0x00, 0x08, 0x00, 0, 0, 0, 0)
		binary.LittleEndian.PutUint32(b[15:], value)
		b[3], b[4] = 0x13, 0x0E
	}
	return b
}

func TestProbeX224(t *testing.T) {
	cases := []struct {
		name   string
		answer []byte
		want   string
	}{
		{"nla", confirm(2, 2), "NLA (CredSSP)"},
		{"tls", confirm(2, 1), "TLS"},
		{"rdp", confirm(2, 0), "RDP"},
		{"hybrid ex", confirm(2, 8), "NLA (CredSSP, early user authorization)"},
		{"old server", confirm(0, 0), "RDP (no negotiation)"},
		{"failure", confirm(3, 2), "negotiation failure 2 (the server does not allow TLS)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			conn, got := fakeServer(t, c.answer)
			answer, err := ProbeX224(conn, 2*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if answer != c.want {
				t.Errorf("got %q, want %q", answer, c.want)
			}
			req := <-got
			if len(req) != 19 || req[5] != 0xE0 || req[11] != 0x01 || req[15] != 0x03 {
				t.Errorf("request: % x", req)
			}
		})
	}
}

func TestProbeX224Errors(t *testing.T) {
	t.Run("not rdp", func(t *testing.T) {
		conn, _ := fakeServer(t, []byte("SSH-2.0-OpenSSH_9.6\r\n"))
		if _, err := ProbeX224(conn, 2*time.Second); err == nil || !strings.Contains(err.Error(), "not an RDP server") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("wrong tpdu", func(t *testing.T) {
		b := confirm(2, 2)
		b[5] = 0x80
		conn, _ := fakeServer(t, b)
		if _, err := ProbeX224(conn, 2*time.Second); err == nil || !strings.Contains(err.Error(), "TPDU") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("closed", func(t *testing.T) {
		conn, _ := fakeServer(t, nil)
		if _, err := ProbeX224(conn, 2*time.Second); err == nil {
			t.Error("no error from a server that hangs up")
		}
	})
	t.Run("timeout", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		conn, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		start := time.Now()
		if _, err := ProbeX224(conn, 100*time.Millisecond); err == nil {
			t.Error("no error from a silent server")
		}
		if time.Since(start) > time.Second {
			t.Error("timeout not applied")
		}
	})
}
