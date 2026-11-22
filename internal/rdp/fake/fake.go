//go:build windows

// Package fake is a stand-in session for Wine and tests. It has no remote
// desktop: its window shows what a real session would be doing. It does use
// the network like the real one: Connect dials the tunnel and asks the RDP
// server behind it for an X.224 connection confirm, which proves the whole
// SSH path, and it keeps probing to notice a dead tunnel.
package fake

import (
	"errors"
	"fmt"
	"image"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/mevdschee/srdp4win/internal/rdp"
	"github.com/mevdschee/srdp4win/internal/rdp/wincap"
)

const (
	dialTimeout  = 10 * time.Second
	probeTimeout = 5 * time.Second
	pollEvery    = 10 * time.Second

	// WrongPassword is the password the fake server rejects.
	WrongPassword = "wrong"
)

type state int

const (
	stateIdle state = iota
	stateConnecting
	stateConnected
)

func (s state) String() string {
	switch s {
	case stateConnecting:
		return "connecting"
	case stateConnected:
		return "connected"
	}
	return "not connected"
}

type session struct {
	hwnd uintptr
	ev   rdp.Events

	// Functions posted by goroutines, run on the UI thread.
	mu    sync.Mutex
	queue []func()

	// Everything below belongs to the UI thread.
	state  state
	gen    int // changes with every connect and disconnect; old results are dropped
	closed bool

	host string
	port int
	cred rdp.Credentials // the password is only compared, never shown
	opts rdp.Options
	mode rdp.ResizeMode

	w, h, scale      int // the window, from the last Resize
	remoteW, remoteH int
	fullScreen       bool

	answer      string // what ProbeX224 said
	note        string // why the last session ended
	connectedAt time.Time
	probes      int
	probeTook   time.Duration
	probing     bool
	resizes     int
	lastKeys    string
	keysSent    int
}

// New creates the fake session in a child window of parent (a HWND).
func New(parent uintptr, ev rdp.Events) (rdp.Session, error) {
	if err := registerClass(); err != nil {
		return nil, err
	}
	hinst, _, _ := procGetModuleHandleW.Call(0)
	hwnd, _, lastErr := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(className)), 0,
		wsChild|wsVisible|wsClipSiblings, 0, 0, 0, 0, parent, 0, hinst, 0)
	if hwnd == 0 {
		return nil, fmt.Errorf("create the fake session window: %w", lastErr)
	}
	s := &session{hwnd: hwnd, ev: ev, scale: 100, mode: rdp.ResizeDynamic, opts: rdp.DefaultOptions()}
	windows[hwnd] = s
	return s, nil
}

func (s *session) Hwnd() uintptr { return s.hwnd }

func (s *session) Connected() bool { return !s.closed && s.state != stateIdle }

func (s *session) Mode() rdp.ResizeMode { return s.mode }

func (s *session) RemoteSize() (int, int) {
	if s.closed || s.state != stateConnected {
		return 0, 0
	}
	return s.remoteW, s.remoteH
}

// post runs f on the UI thread, from any goroutine.
func (s *session) post(f func()) {
	s.mu.Lock()
	s.queue = append(s.queue, f)
	s.mu.Unlock()
	procPostMessageW.Call(s.hwnd, wmRun, 0, 0)
}

// run runs the posted functions (on the UI thread).
func (s *session) run() {
	s.mu.Lock()
	queue := s.queue
	s.queue = nil
	s.mu.Unlock()
	for _, f := range queue {
		if s.closed {
			return
		}
		f()
	}
}

func (s *session) Connect(host string, port int, cred rdp.Credentials, o rdp.Options) error {
	if s.closed {
		return errors.New("the session is closed")
	}
	if s.state != stateIdle {
		return errors.New("the session is connected already")
	}
	if host == "" || port <= 0 || port > 65535 {
		return fmt.Errorf("no valid address: %q port %d", host, port)
	}
	s.host, s.port, s.cred, s.opts, s.mode = host, port, cred, o, o.Resize
	s.answer, s.note, s.probes, s.probeTook = "", "", 0, 0
	s.state = stateConnecting
	s.gen++
	gen := s.gen
	slog.Info("fake session: connecting", "host", host, "port", port, "user", cred.User, "password", cred.Password != "")
	s.probe(func(answer string, took time.Duration, err error) {
		if gen != s.gen {
			return
		}
		s.connected(answer, took, err)
	})
	s.repaint()
	return nil
}

// probe dials the target on a goroutine, asks for a connection confirm and
// posts the result to done on the UI thread.
func (s *session) probe(done func(answer string, took time.Duration, err error)) {
	addr := net.JoinHostPort(s.host, strconv.Itoa(s.port))
	s.probing = true
	go func() {
		start := time.Now()
		answer, err := "", error(nil)
		conn, err := net.DialTimeout("tcp", addr, dialTimeout)
		if err == nil {
			answer, err = rdp.ProbeX224(conn, probeTimeout)
			conn.Close()
		}
		took := time.Since(start)
		s.post(func() {
			s.probing = false
			done(answer, took, err)
		})
	}()
}

// connected handles the result of the first probe.
func (s *session) connected(answer string, took time.Duration, err error) {
	s.probes, s.probeTook = 1, took
	if err != nil {
		slog.Info("fake session: connect failed", "err", err)
		s.end(rdp.ReasonSocketConnectFailed, rdp.DisconnectText(rdp.ReasonSocketConnectFailed, 0)+" ("+err.Error()+")", false)
		return
	}
	slog.Info("fake session: connected", "answer", answer, "took", took.String())
	gen := s.gen
	s.answer = answer
	s.state = stateConnected
	s.connectedAt = time.Now()
	s.remoteW, s.remoteH = rdp.SessionSize(s.opts, s.mode, s.w, s.h)
	s.repaint()
	if s.ev.Connected != nil {
		s.ev.Connected()
	}
	if s.closed || gen != s.gen {
		return
	}
	if s.cred.Password == WrongPassword {
		if s.ev.AuthFailed != nil {
			s.ev.AuthFailed()
		}
		if s.closed || gen != s.gen {
			return
		}
		s.end(rdp.ReasonLogonFailure, rdp.DisconnectText(rdp.ReasonLogonFailure, 0), false)
		return
	}
	procSetTimer.Call(s.hwnd, timerPoll, uintptr(pollEvery/time.Millisecond), 0)
	procSetTimer.Call(s.hwnd, timerClock, 1000, 0)
	if s.ev.RemoteSize != nil {
		s.ev.RemoteSize(s.remoteW, s.remoteH)
	}
	if s.closed || gen != s.gen {
		return
	}
	if s.ev.LoginComplete != nil {
		s.ev.LoginComplete()
	}
}

// poll probes the target again to notice a dead tunnel.
func (s *session) poll() {
	if s.state != stateConnected || s.probing {
		return
	}
	gen := s.gen
	s.probe(func(answer string, took time.Duration, err error) {
		if gen != s.gen || s.state != stateConnected {
			return
		}
		s.probes++
		s.probeTook = took
		if err != nil {
			slog.Info("fake session: probe failed", "err", err)
			s.end(rdp.ReasonSocketClosed, rdp.DisconnectText(rdp.ReasonSocketClosed, 0)+" ("+err.Error()+")", false)
			return
		}
		s.answer = answer
		s.repaint()
	})
}

// end stops the session and reports it.
func (s *session) end(reason int, text string, userEnded bool) {
	s.stop()
	s.note = text
	s.repaint()
	slog.Info("fake session: disconnected", "reason", reason, "text", text)
	if s.ev.Disconnected != nil {
		s.ev.Disconnected(reason, text, userEnded)
	}
}

// stop goes back to the state before Connect.
func (s *session) stop() {
	s.state = stateIdle
	s.gen++
	s.remoteW, s.remoteH = 0, 0
	procKillTimer.Call(s.hwnd, timerPoll)
	procKillTimer.Call(s.hwnd, timerClock)
}

func (s *session) Disconnect() {
	if s.closed || s.state == stateIdle {
		return
	}
	s.stop()
	s.repaint()
	// Like the control, the event comes after Disconnect has returned.
	gen := s.gen
	s.post(func() {
		if gen == s.gen {
			s.end(rdp.ReasonLocal, rdp.DisconnectText(rdp.ReasonLocal, 0), true)
		}
	})
}

func (s *session) Resize(x, y, width, height, scale int) {
	if s.closed {
		return
	}
	procMoveWindow.Call(s.hwnd, uintptr(int32(x)), uintptr(int32(y)), uintptr(int32(max(width, 0))), uintptr(int32(max(height, 0))), 1)
	s.w, s.h = width, height
	if scale > 0 {
		s.scale = scale
	}
	s.resizes++
	s.applySize()
	s.repaint()
}

func (s *session) SetMode(m rdp.ResizeMode) {
	if s.closed {
		return
	}
	s.mode = m
	s.applySize()
	s.repaint()
}

// applySize simulates the resize modes: dynamic follows the window, fixed
// has the fixed size, scaled keeps the size it had.
func (s *session) applySize() {
	if s.state != stateConnected {
		return
	}
	w, h := s.remoteW, s.remoteH
	switch s.mode {
	case rdp.ResizeDynamic:
		if s.w <= 0 || s.h <= 0 {
			return
		}
		w, h = rdp.FitSize(s.w, s.h)
	case rdp.ResizeFixed:
		w, h = rdp.SessionSize(s.opts, rdp.ResizeFixed, s.remoteW, s.remoteH)
	}
	if w == s.remoteW && h == s.remoteH {
		return
	}
	s.remoteW, s.remoteH = w, h
	if s.ev.RemoteSize != nil {
		s.ev.RemoteSize(w, h)
	}
}

func (s *session) SetFullScreen(on bool) {
	if s.closed {
		return
	}
	s.fullScreen = on
	s.repaint()
}

func (s *session) SendKeys(k rdp.Keys) {
	if s.closed || s.state != stateConnected {
		return
	}
	s.lastKeys = k.String()
	s.keysSent++
	slog.Info("fake session: keys", "keys", s.lastKeys)
	s.repaint()
}

func (s *session) Screenshot() (image.Image, error) {
	if s.closed {
		return nil, errors.New("the session is closed")
	}
	if s.state != stateConnected {
		return nil, errors.New("the session is not connected")
	}
	return wincap.Draw(s.w, s.h, func(hdc uintptr) { s.paint(hdc, s.w, s.h) })
}

func (s *session) Focus() {
	if !s.closed {
		procSetFocus.Call(s.hwnd)
	}
}

func (s *session) Close() {
	if s.closed {
		return
	}
	s.stop()
	s.closed = true
	procDestroyWindow.Call(s.hwnd)
}

func (s *session) repaint() {
	if !s.closed {
		procInvalidateRect.Call(s.hwnd, 0, 0)
	}
}

// doubleClick asks for full screen, as Ctrl+Alt+Break does in a real session.
func (s *session) doubleClick() {
	if s.ev.RequestFullScreen != nil {
		s.ev.RequestFullScreen(!s.fullScreen)
	}
}

// lines is the text of the panel.
func (s *session) lines() []string {
	onOff := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}
	o := s.opts
	st := "State: " + s.state.String()
	if s.state == stateConnected {
		st += fmt.Sprintf(" for %d s", int(time.Since(s.connectedAt).Seconds()))
	}
	l := []string{st}
	if s.note != "" && s.state == stateIdle {
		l = append(l, "Ended: "+s.note)
	}
	if s.host != "" {
		target := "Target: " + net.JoinHostPort(s.host, strconv.Itoa(s.port))
		if s.cred.User != "" {
			user := s.cred.User
			if s.cred.Domain != "" {
				user = s.cred.Domain + `\` + user
			}
			target += "   user: " + user
		}
		pw := "none"
		if s.cred.Password != "" {
			pw = "given"
		}
		l = append(l, target+"   password: "+pw)
	}
	answer := s.answer
	if answer == "" {
		answer = "-"
	}
	l = append(l, "Server answered: "+answer)
	_, device := rdp.ScaleFactors(s.scale)
	l = append(l, fmt.Sprintf("Window: %d x %d at %d%% (device scale %d)   full screen: %s", s.w, s.h, s.scale, device, onOff(s.fullScreen)))
	remote := "-"
	if s.state == stateConnected {
		remote = fmt.Sprintf("%d x %d", s.remoteW, s.remoteH)
	}
	l = append(l, fmt.Sprintf("Resize mode: %s   remote desktop: %s", s.mode, remote))
	if s.mode == rdp.ResizeFixed || o.Resize == rdp.ResizeFixed {
		l = append(l, fmt.Sprintf("Fixed size: %d x %d", o.FixedWidth, o.FixedHeight))
	}
	l = append(l, fmt.Sprintf("Options: clipboard %s, printers %s, font smoothing %s, quality %s, performance flags %#x, %d bit",
		onOff(o.Clipboard), onOff(o.Printers), onOff(o.FontSmoothing), o.Quality, rdp.PerformanceFlags(o), rdp.EffectiveColorDepth(o)))
	drives := onOff(o.Drives)
	if o.Drives && o.DriveLetters != "" {
		drives = o.DriveLetters
	}
	l = append(l, fmt.Sprintf("Options: drives %s, audio %s, microphone %s, smart cards %s, ports %s",
		drives, [...]string{"local", "remote", "off"}[clampIndex(int(o.Audio), 3)], onOff(o.Microphone), onOff(o.SmartCards), onOff(o.Ports)))
	l = append(l, fmt.Sprintf("Options: keyboard %s, admin %s, verify certificate %s",
		[...]string{"full screen", "remote", "local"}[clampIndex(int(o.Keyboard), 3)], onOff(o.Admin), onOff(o.VerifyCert)))
	keys := "-"
	if s.lastKeys != "" {
		keys = fmt.Sprintf("%s (%d sent)", s.lastKeys, s.keysSent)
	}
	l = append(l, "Last keys: "+keys)
	l = append(l, fmt.Sprintf("Stats: %d probes, last took %d ms", s.probes, s.probeTook.Milliseconds()))
	l = append(l, fmt.Sprintf("Stats: %d resizes", s.resizes))
	l = append(l, "", "Double-click asks for full screen.")
	return l
}

func clampIndex(i, n int) int {
	if i < 0 || i >= n {
		return 0
	}
	return i
}

// utf16 converts text for a Windows call; it drops text with a NUL.
func utf16(s string) []uint16 {
	u, err := syscall.UTF16FromString(s)
	if err != nil {
		return []uint16{0}
	}
	return u
}
