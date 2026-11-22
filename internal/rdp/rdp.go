// Package rdp defines the remote desktop session as the UI sees it, and the
// plain Go parts shared by its two backends: rdp/mstsc (Microsoft's RDP
// control) and rdp/fake (a stand-in for Wine and tests).
//
// This file is the contract between the UI and the backends. Change it only
// in agreement with both sides.
package rdp

import "image"

// ResizeMode says what happens when the tab area changes size.
type ResizeMode int

const (
	ResizeDynamic ResizeMode = iota // the remote desktop is resized to the tab
	ResizeScaled                    // the picture is stretched to the tab
	ResizeFixed                     // a chosen resolution, shown 1:1
)

// Quality is a preset of visual effects.
type Quality int

const (
	QualityBest Quality = iota
	QualityMedium
	QualityPoor
)

// KeyboardMode says where Windows key combinations (Alt+Tab, Win) go.
type KeyboardMode int

const (
	KeysFullScreen KeyboardMode = iota // to the remote desktop in full screen only
	KeysRemote                         // always to the remote desktop
	KeysLocal                          // always to this computer
)

// AudioMode says where the remote sound plays.
type AudioMode int

const (
	AudioLocal  AudioMode = iota // on this computer
	AudioRemote                  // on the remote computer
	AudioOff
)

// Options are the per-connection session settings. The zero value is not the
// default: use DefaultOptions.
type Options struct {
	Clipboard     bool         `json:"clipboard"`
	Printers      bool         `json:"printers"`
	FontSmoothing bool         `json:"fontSmoothing"`
	Drives        bool         `json:"drives"`
	DriveLetters  string       `json:"driveLetters,omitempty"` // "CD" redirects C: and D:; empty is all
	Audio         AudioMode    `json:"audio"`
	Microphone    bool         `json:"microphone"`
	SmartCards    bool         `json:"smartCards"`
	Ports         bool         `json:"ports"`
	Keyboard      KeyboardMode `json:"keyboard"`
	Resize        ResizeMode   `json:"resize"`
	FixedWidth    int          `json:"fixedWidth,omitempty"`  // for ResizeFixed
	FixedHeight   int          `json:"fixedHeight,omitempty"` // for ResizeFixed
	Quality       Quality      `json:"quality"`
	ColorDepth    int          `json:"colorDepth"` // 32, 24, 16 or 15
	Admin         bool         `json:"admin"`      // connect to the administrative session
	VerifyCert    bool         `json:"verifyCert"` // warn when the RDP certificate cannot be verified
}

// DefaultOptions returns the settings of a new connection.
func DefaultOptions() Options {
	return Options{
		Clipboard:     true,
		Printers:      true,
		FontSmoothing: true,
		Audio:         AudioLocal,
		Keyboard:      KeysFullScreen,
		Resize:        ResizeDynamic,
		FixedWidth:    1920,
		FixedHeight:   1080,
		Quality:       QualityBest,
		ColorDepth:    32,
	}
}

// Credentials is the RDP login. An empty password leaves the login to the
// server's own logon screen.
type Credentials struct {
	User     string
	Domain   string
	Password string
}

// Keys is a key combination sent to the remote desktop.
type Keys int

const (
	KeysCtrlAltDel Keys = iota
	KeysWin
	KeysAltTab
	KeysAltF4
	KeysCtrlEsc
	KeysPrintScreen
	KeysCtrlShiftEsc
	KeysWinL
)

// Events are the callbacks of a session. They are always called on the UI
// thread. Any of them may be nil.
type Events struct {
	// Connected: the transport is up (before the login is complete).
	Connected func()
	// LoginComplete: the remote desktop is on screen.
	LoginComplete func()
	// Disconnected: the session ended. Text is a readable reason; userEnded
	// is true when the user logged off or disconnected on purpose, so no
	// reconnect or error is wanted.
	Disconnected func(reason int, text string, userEnded bool)
	// Reconnecting: the control tries to get the session back.
	Reconnecting func(attempt int)
	// RequestFullScreen: the session wants the container to enter or leave
	// full screen (Ctrl+Alt+Break).
	RequestFullScreen func(on bool)
	// RequestMinimize: the session wants the container minimized.
	RequestMinimize func()
	// RemoteSize: the remote desktop size changed.
	RemoteSize func(width, height int)
	// DynamicUnsupported: ResizeDynamic was asked but the server cannot do
	// it; the backend fell back to ResizeScaled.
	DynamicUnsupported func()
	// AuthFailed: the server rejected the password (ask again).
	AuthFailed func()
}

// Session is one remote desktop in one child window. All methods must be
// called on the UI thread.
type Session interface {
	// Hwnd is the session's child window (a HWND), created by the backend
	// inside the parent given to New. The UI positions it with Resize only.
	Hwnd() uintptr
	// Connect starts connecting to host:port, which is always the tunnel's
	// loopback address. The result comes through the events.
	Connect(host string, port int, cred Credentials, o Options) error
	// Disconnect ends the session; Events.Disconnected follows.
	Disconnect()
	// Connected reports whether a session is up or coming up.
	Connected() bool
	// Resize places the session window at x, y (parent client coordinates)
	// with the given size in physical pixels. Scale is the monitor's scale
	// in percent (100, 125, 150 ...). The backend applies the resize mode;
	// the UI debounces.
	Resize(x, y, width, height, scale int)
	// SetMode switches the resize mode while connected.
	SetMode(m ResizeMode)
	// Mode is the resize mode in effect (after a fallback).
	Mode() ResizeMode
	// SetFullScreen tells the session the container entered or left full
	// screen (for the keyboard mode).
	SetFullScreen(on bool)
	// RemoteSize is the remote desktop size, 0, 0 when not connected.
	RemoteSize() (width, height int)
	// SendKeys sends a key combination to the remote desktop.
	SendKeys(k Keys)
	// Screenshot returns the remote desktop as it is on screen.
	Screenshot() (image.Image, error)
	// Focus gives the keyboard to the session.
	Focus()
	// Close disconnects without events and destroys the window.
	Close()
}

// Each backend has:
//
//	func New(parent uintptr, ev rdp.Events) (rdp.Session, error)
//
// and rdp/mstsc also has
//
//	func Available() bool // the control is registered (false under Wine)
