//go:build windows

// Package mstsc hosts Microsoft's RDP control (mstscax.dll, the engine of
// mstsc.exe) in a child window and drives it as an rdp.Session.
//
// The control is created by atl.dll (host.go), its scriptable settings are
// set by name through IDispatch (com.go), the few settings without
// IDispatch go through the vtable slots in ids.go, and its events arrive in
// a hand-written IDispatch sink (sink.go). Everything runs on the UI thread.
package mstsc

import (
	"errors"
	"fmt"
	"image"
	"log/slog"
	"strings"
	"syscall"
	"unsafe"

	"github.com/mevdschee/srdp4win/internal/rdp"
	"github.com/mevdschee/srdp4win/internal/rdp/wincap"
)

type state int

const (
	stateIdle       state = iota
	stateConnecting       // Connect was called
	stateConnected        // the transport is up
	stateLoggedIn         // the remote desktop is on screen
)

const (
	// The display channel opens a moment after the login, so the first
	// resize is tried a few times before dynamic resolution is given up.
	dynRetries = 5
	dynRetryMs = 600
	// How long Close waits for the control to disconnect before it destroys
	// the control anyway.
	closeWaitMs = 3000
	// The control gives up after this many reconnect attempts.
	maxReconnects = 20
)

type session struct {
	hwnd uintptr
	ev   rdp.Events

	unk     *iface    // the control
	client  *dispatch // IMsRdpClient<version>
	version int
	adv     *dispatch // IMsRdpClientAdvancedSettings*
	sec     *dispatch // IMsRdpClientSecuredSettings*
	nsPass  *iface    // IMsTscNonScriptable (or one that inherits it)
	nsKeys  *iface    // IMsRdpClientNonScriptable, nil when missing
	ns3     *iface    // IMsRdpClientNonScriptable3, nil when missing
	ns5     *iface    // IMsRdpClientNonScriptable5, nil when missing
	ext     *iface    // IMsRdpExtendedSettings, nil when missing
	cp      *iface    // IConnectionPoint of IMsTscAxEvents
	cookie  uint32
	sink    *sink

	state state
	opts  rdp.Options
	mode  rdp.ResizeMode

	w, h, scale      int // the window, from the last Resize
	remoteW, remoteH int
	sentScale        int // the scale the remote desktop has

	dynUnsupported bool // the server cannot resize the remote desktop
	dynWorked      bool // a resize worked in this session
	dynTries       int
	dynForce       bool // resize even when the size looks right
	cancelRetry    func()

	fullScreen   bool
	settingFS    bool // we are setting FullScreen ourselves
	reconnecting bool
	lastAttempt  int

	depth       int // > 0 inside an event of the control
	closed      bool
	destroyed   bool
	cancelClose func()
}

// New creates the control in a child window of parent (a HWND).
func New(parent uintptr, ev rdp.Events) (rdp.Session, error) {
	hwnd, unk, class, err := createControl(parent)
	if err != nil {
		return nil, err
	}
	s := &session{hwnd: hwnd, ev: ev, unk: unk, scale: 100, sentScale: 100}
	if err := s.bind(); err != nil {
		s.destroy()
		return nil, err
	}
	slog.Info("rdp control: created", "class", class, "interface", s.version, "version", s.client.getString("Version"),
		"extendedSettings", s.ext != nil, "sendKeys", s.nsKeys != nil)
	return s, nil
}

// advancedName is the property that gives the newest advanced settings of
// each client interface version.
var advancedName = map[int]string{
	1: "AdvancedSettings2", 2: "AdvancedSettings3", 3: "AdvancedSettings4", 4: "AdvancedSettings5",
	5: "AdvancedSettings6", 6: "AdvancedSettings7", 7: "AdvancedSettings8", 8: "AdvancedSettings9",
}

// bind gets the interfaces of the control and connects the event sink.
func (s *session) bind() error {
	for _, c := range clientInterfaces {
		if p := s.unk.query(&c.iid); p != nil {
			s.client, s.version = newDispatch(p, "client"), c.version
			break
		}
	}
	if s.client == nil {
		return errors.New("the remote desktop control has no IMsRdpClient interface")
	}
	s.adv = s.client.getObject(advancedName[min(s.version, 8)], "advanced settings")
	if s.adv == nil {
		return errors.New("the remote desktop control has no advanced settings")
	}
	secured := "SecuredSettings2"
	if s.version >= 7 {
		secured = "SecuredSettings3"
	}
	s.sec = s.client.getObject(secured, "secured settings")

	s.nsKeys = s.unk.query(&iidIMsRdpClientNonScriptable)
	if s.nsKeys != nil {
		s.nsPass = s.nsKeys
	} else {
		s.nsPass = s.unk.query(&iidIMsTscNonScriptable)
	}
	s.ns3 = s.unk.query(&iidIMsRdpClientNonScriptable3)
	s.ns5 = s.unk.query(&iidIMsRdpClientNonScriptable5)
	s.ext = s.unk.query(&iidIMsRdpExtendedSettings)

	// The events.
	cpc := s.unk.query(&iidIConnectionPointContainer)
	if cpc == nil {
		return errors.New("the remote desktop control has no connection points")
	}
	defer cpc.release()
	r, _, _ := syscall.SyscallN(cpc.fn(slotFindConnectionPoint), uintptr(unsafe.Pointer(cpc)),
		uintptr(unsafe.Pointer(&diidIMsTscAxEvents)), uintptr(unsafe.Pointer(&s.cp)))
	if err := check(r); err != nil || s.cp == nil {
		return fmt.Errorf("the remote desktop control has no event source: %v", err)
	}
	s.sink = newSink(s)
	r, _, _ = syscall.SyscallN(s.cp.fn(slotAdvise), uintptr(unsafe.Pointer(s.cp)),
		uintptr(unsafe.Pointer(s.sink)), uintptr(unsafe.Pointer(&s.cookie)))
	if err := check(r); err != nil {
		s.cp.release()
		s.cp = nil
		return fmt.Errorf("connect the events of the remote desktop control: %w", err)
	}
	return nil
}

func (s *session) Hwnd() uintptr { return s.hwnd }

func (s *session) Connected() bool { return !s.closed && s.state != stateIdle }

func (s *session) Mode() rdp.ResizeMode { return s.mode }

func (s *session) RemoteSize() (int, int) {
	if s.closed || s.state == stateIdle {
		return 0, 0
	}
	return s.remoteW, s.remoteH
}

func (s *session) Connect(host string, port int, cred rdp.Credentials, o rdp.Options) error {
	if s.closed {
		return errors.New("the session is closed")
	}
	if s.state != stateIdle {
		return errors.New("the session is connected already")
	}
	s.opts, s.mode = o, o.Resize
	s.dynUnsupported, s.dynWorked, s.dynTries, s.dynForce = false, false, 0, false
	s.reconnecting, s.lastAttempt = false, 0
	c, adv := s.client, s.adv

	// Where to connect. Without these two the control would go to the
	// default port, so they must work.
	if !c.putString("Server", host) || !adv.putInt("RDPPort", port) {
		return errors.New("the remote desktop control did not accept the address")
	}

	// Login.
	c.putString("UserName", cred.User)
	c.putString("Domain", cred.Domain)
	s.setPassword(cred.Password)
	adv.putBool("EnableCredSspSupport", true)
	level := authNone
	if o.VerifyCert {
		level = authWarn
	}
	adv.putInt("AuthenticationLevel", level)
	// With a password we ask again ourselves when it is wrong; without one
	// the control may show the Windows credential prompt.
	s.putNS5(slotPutAllowPromptingForCredentials, "AllowPromptingForCredentials", cred.Password == "")

	// Size and scale.
	w, h := rdp.SessionSize(o, s.mode, s.w, s.h)
	c.putInt("DesktopWidth", w)
	c.putInt("DesktopHeight", h)
	c.putInt("ColorDepth", rdp.EffectiveColorDepth(o))
	desktop, device := rdp.ScaleFactors(s.scale)
	s.putExtended("DesktopScaleFactor", varUint(uint32(desktop)))
	s.putExtended("DeviceScaleFactor", varUint(uint32(device)))
	s.sentScale = s.scale
	adv.putBool("SmartSizing", s.mode == rdp.ResizeScaled)

	// Full screen is ours: the control only asks for it.
	adv.putInt("ContainerHandledFullScreen", 1)
	adv.putBool("DisplayConnectionBar", false)
	adv.putBool("PinConnectionBar", false)
	adv.putBool("GrabFocusOnConnect", false)
	if s.fullScreen {
		s.putFullScreen(true)
	}

	// Redirection.
	adv.putBool("RedirectClipboard", o.Clipboard)
	adv.putBool("RedirectPrinters", o.Printers)
	adv.putBool("RedirectSmartCards", o.SmartCards)
	adv.putBool("RedirectPorts", o.Ports)
	s.setDrives(o)
	audio := audioLocal
	switch o.Audio {
	case rdp.AudioRemote:
		audio = audioRemote
	case rdp.AudioOff:
		audio = audioNone
	}
	if s.sec == nil || !s.sec.putInt("AudioRedirectionMode", audio) {
		adv.putInt("AudioRedirectionMode", audio)
	}
	adv.putBool("AudioCaptureRedirectionMode", o.Microphone)

	// Keyboard.
	hook := keyboardHookFullScreen
	switch o.Keyboard {
	case rdp.KeysRemote:
		hook = keyboardHookRemote
	case rdp.KeysLocal:
		hook = keyboardHookLocal
	}
	if s.sec != nil {
		s.sec.putInt("KeyboardHookMode", hook)
	}
	adv.putInt("EnableWindowsKey", 1)

	// Performance: let the server pick the codec for a fast link, keep the
	// caches and the compression on.
	adv.putInt("PerformanceFlags", int(rdp.PerformanceFlags(o)))
	adv.putInt("NetworkConnectionType", networkLAN)
	adv.putBool("BandwidthDetection", true)
	adv.putInt("BitmapPersistence", 1)
	adv.putInt("CachePersistenceActive", 1)
	adv.putInt("Compress", 1)
	// UDP cannot pass the SSH tunnel; do not let the control try it. The
	// property is not in Microsoft's list, so it may be refused.
	s.putExtended("DisableUDPTransport", varBool(true))

	// Session.
	adv.putBool("ConnectToAdministerServer", o.Admin)
	adv.putBool("EnableAutoReconnect", true)
	adv.putInt("MaxReconnectAttempts", maxReconnects)

	slog.Info("rdp control: connecting", "host", host, "port", port, "user", cred.User, "domain", cred.Domain,
		"password", cred.Password != "", "width", w, "height", h, "scale", desktop, "mode", s.mode.String(),
		"performanceFlags", fmt.Sprintf("%#x", rdp.PerformanceFlags(o)))
	s.remoteW, s.remoteH = w, h
	s.state = stateConnecting
	if err := c.method("Connect"); err != nil {
		s.reset()
		return fmt.Errorf("the remote desktop control did not connect: %w", err)
	}
	return nil
}

// setPassword hands the password to the control, or clears an earlier one.
// The password itself is never logged.
func (s *session) setPassword(password string) {
	if s.nsPass == nil {
		if password != "" {
			b := newBSTR(password)
			s.adv.put("ClearTextPassword", varBSTR(b))
			b.wipe()
		}
		return
	}
	if password == "" {
		if err := s.nsPass.call(slotResetPassword); err != nil {
			slog.Debug("rdp control: ResetPassword failed", "err", err)
		}
		return
	}
	b := newBSTR(password)
	defer b.wipe()
	if err := s.nsPass.call(slotPutClearTextPassword, uintptr(b)); err != nil {
		slog.Warn("rdp control: set failed", "object", "non-scriptable", "property", "ClearTextPassword", "err", err)
	}
}

// putNS5 sets a boolean property of IMsRdpClientNonScriptable5.
func (s *session) putNS5(slot int, name string, v bool) {
	if s.ns5 == nil {
		return
	}
	if err := s.ns5.call(slot, variantBool(v)); err != nil {
		slog.Warn("rdp control: set failed", "object", "non-scriptable", "property", name, "err", err)
	}
}

// putExtended sets a property of IMsRdpExtendedSettings.
func (s *session) putExtended(name string, v variant) bool {
	if s.ext == nil {
		return false
	}
	b := newBSTR(name)
	defer b.free()
	r, _, _ := syscall.SyscallN(s.ext.fn(slotPutProperty), uintptr(unsafe.Pointer(s.ext)), uintptr(b), uintptr(unsafe.Pointer(&v)))
	if err := check(r); err != nil {
		slog.Warn("rdp control: set failed", "object", "extended settings", "property", name, "err", err)
		return false
	}
	return true
}

// setDrives redirects all drives, none, or the ones in o.DriveLetters.
func (s *session) setDrives(o rdp.Options) {
	letters := strings.ToUpper(strings.TrimSpace(o.DriveLetters))
	s.adv.putBool("RedirectDrives", o.Drives && letters == "")
	if !o.Drives || letters == "" {
		return
	}
	if s.ns3 == nil {
		slog.Warn("rdp control: cannot choose drives, none are redirected")
		return
	}
	var coll *iface
	r, _, _ := syscall.SyscallN(s.ns3.fn(slotGetDriveCollection), uintptr(unsafe.Pointer(s.ns3)), uintptr(unsafe.Pointer(&coll)))
	if err := check(r); err != nil || coll == nil {
		slog.Warn("rdp control: get failed", "object", "non-scriptable", "property", "DriveCollection", "err", err)
		return
	}
	defer coll.release()
	var count uint32
	r, _, _ = syscall.SyscallN(coll.fn(slotGetDriveCount), uintptr(unsafe.Pointer(coll)), uintptr(unsafe.Pointer(&count)))
	if err := check(r); err != nil {
		slog.Warn("rdp control: get failed", "object", "drive collection", "property", "DriveCount", "err", err)
		return
	}
	for i := uint32(0); i < count && i < 64; i++ {
		var drive *iface
		r, _, _ = syscall.SyscallN(coll.fn(slotGetDriveByIndex), uintptr(unsafe.Pointer(coll)), uintptr(i), uintptr(unsafe.Pointer(&drive)))
		if check(r) != nil || drive == nil {
			continue
		}
		var name *uint16
		r, _, _ = syscall.SyscallN(drive.fn(slotDriveGetName), uintptr(unsafe.Pointer(drive)), uintptr(unsafe.Pointer(&name)))
		if check(r) == nil {
			text := strings.ToUpper(takeString(name))
			on := text != "" && strings.ContainsRune(letters, rune(text[0]))
			if err := drive.call(slotDrivePutRedirectionState, variantBool(on)); err != nil {
				slog.Warn("rdp control: set failed", "object", "drive "+text, "property", "RedirectionState", "err", err)
			}
		}
		drive.release()
	}
}

func (s *session) Disconnect() {
	if s.closed || s.state == stateIdle {
		return
	}
	if err := s.client.method("Disconnect"); err != nil {
		// No event will follow, so end the session ourselves.
		slog.Warn("rdp control: Disconnect failed", "err", err)
		after(0, func() {
			if !s.closed && s.state != stateIdle {
				s.disconnected(rdp.ReasonLocal)
			}
		})
	}
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
	s.applySize()
}

func (s *session) SetMode(m rdp.ResizeMode) {
	if s.closed {
		return
	}
	s.mode = m
	if s.state == stateIdle {
		return
	}
	switch m {
	case rdp.ResizeScaled:
		s.adv.putBool("SmartSizing", true)
	case rdp.ResizeDynamic:
		if s.dynUnsupported {
			s.fallBack()
			return
		}
		s.adv.putBool("SmartSizing", false)
		s.applySize()
	case rdp.ResizeFixed:
		s.adv.putBool("SmartSizing", false)
		s.applySize()
	}
}

// applySize makes the remote desktop the size the mode asks for: the window
// in dynamic mode, the fixed size in fixed mode. Scaled mode leaves it.
func (s *session) applySize() {
	if s.closed || s.state != stateLoggedIn {
		return
	}
	if s.cancelRetry != nil {
		s.cancelRetry()
		s.cancelRetry = nil
	}
	var w, h int
	switch s.mode {
	case rdp.ResizeDynamic:
		if s.w <= 0 || s.h <= 0 {
			return
		}
		w, h = rdp.FitSize(s.w, s.h)
	case rdp.ResizeFixed:
		if s.dynUnsupported {
			return // the remote desktop keeps the size it has
		}
		w, h = rdp.SessionSize(s.opts, rdp.ResizeFixed, s.remoteW, s.remoteH)
	default:
		return
	}
	if w == s.remoteW && h == s.remoteH && s.scale == s.sentScale && !s.dynForce {
		return
	}
	err := s.updateDisplay(w, h)
	if err == nil {
		s.dynWorked, s.dynTries, s.dynForce, s.sentScale = true, 0, false, s.scale
		s.setRemote(w, h)
		return
	}
	slog.Warn("rdp control: UpdateSessionDisplaySettings failed", "width", w, "height", h, "try", s.dynTries, "err", err)
	if s.dynWorked {
		return // it worked before: not a missing feature
	}
	if s.version >= 9 && s.dynTries < dynRetries {
		s.dynTries++
		s.cancelRetry = after(dynRetryMs, func() {
			s.cancelRetry = nil
			s.applySize()
		})
		return
	}
	s.dynUnsupported = true
	if s.mode == rdp.ResizeDynamic {
		s.fallBack()
	}
}

// updateDisplay asks the server for another desktop size (IMsRdpClient9).
func (s *session) updateDisplay(w, h int) error {
	if s.version < 9 {
		return errors.New("the remote desktop control is too old")
	}
	desktop, device := rdp.ScaleFactors(s.scale)
	return s.client.method("UpdateSessionDisplaySettings",
		varUint(uint32(w)), varUint(uint32(h)),
		varUint(uint32(rdp.PhysicalMM(w, s.scale))), varUint(uint32(rdp.PhysicalMM(h, s.scale))),
		varUint(0), // orientation: landscape
		varUint(uint32(desktop)), varUint(uint32(device)))
}

// fallBack switches from dynamic to scaled because the server cannot
// resize the remote desktop.
func (s *session) fallBack() {
	slog.Info("rdp control: no dynamic resolution, falling back to scaled")
	s.mode = rdp.ResizeScaled
	s.adv.putBool("SmartSizing", true)
	if s.ev.DynamicUnsupported != nil {
		s.ev.DynamicUnsupported()
	}
}

// setRemote records the remote desktop size and reports a change.
func (s *session) setRemote(w, h int) {
	if w == s.remoteW && h == s.remoteH {
		return
	}
	s.remoteW, s.remoteH = w, h
	if s.ev.RemoteSize != nil {
		s.ev.RemoteSize(w, h)
	}
}

func (s *session) SetFullScreen(on bool) {
	if s.closed || s.fullScreen == on {
		return
	}
	s.fullScreen = on
	if s.state != stateIdle {
		s.putFullScreen(on)
	}
}

// putFullScreen tells the control the container is in full screen. With
// ContainerHandledFullScreen the control changes nothing on screen; it only
// needs to know for the keyboard mode.
func (s *session) putFullScreen(on bool) {
	s.settingFS = true
	s.client.putBool("FullScreen", on)
	s.settingFS = false
	if on {
		hideConnectionBar(s.hwnd)
	}
}

func (s *session) SendKeys(k rdp.Keys) {
	if s.closed || s.state != stateLoggedIn {
		return
	}
	s.Focus()
	if k == rdp.KeysWin && s.version >= 8 {
		err := s.client.method("SendRemoteAction", varInt(actionStartScreen))
		if err == nil {
			return
		}
		slog.Warn("rdp control: SendRemoteAction failed", "err", err)
	}
	keys := rdp.Scancodes(k)
	if len(keys) == 0 || s.nsKeys == nil {
		slog.Warn("rdp control: cannot send keys", "keys", k.String())
		return
	}
	// All keys down in order, then up in reverse order. An extended key has
	// bit 8 set, as in the RDP keyboard flags.
	n := len(keys)
	up := make([]int16, 2*n)
	data := make([]int32, 2*n)
	for i, key := range keys {
		code := int32(key.Code)
		if key.Extended {
			code |= 0x100
		}
		data[i], data[2*n-1-i] = code, code
		up[2*n-1-i] = -1 // VARIANT_TRUE
	}
	r, _, _ := syscall.SyscallN(s.nsKeys.fn(slotSendKeys), uintptr(unsafe.Pointer(s.nsKeys)),
		uintptr(2*n), uintptr(unsafe.Pointer(&up[0])), uintptr(unsafe.Pointer(&data[0])))
	if err := check(r); err != nil {
		slog.Warn("rdp control: SendKeys failed", "keys", k.String(), "err", err)
	}
}

func (s *session) Screenshot() (image.Image, error) {
	if s.closed {
		return nil, errors.New("the session is closed")
	}
	if s.state != stateLoggedIn {
		return nil, errors.New("the session is not connected")
	}
	return wincap.Window(s.hwnd)
}

func (s *session) Focus() {
	if !s.closed {
		procSetFocus.Call(s.hwnd)
	}
}

// Close disconnects without events and destroys the control. The control
// must not be destroyed inside one of its own events or while it is still
// connected, so the destruction may happen a little later; the window is
// hidden at once.
func (s *session) Close() {
	if s.closed {
		return
	}
	s.closed = true
	if s.cancelRetry != nil {
		s.cancelRetry()
		s.cancelRetry = nil
	}
	procShowWindow.Call(s.hwnd, swHide)
	if s.state != stateIdle {
		if err := s.client.method("Disconnect"); err == nil {
			s.cancelClose = after(closeWaitMs, s.destroy)
			return
		}
	}
	if s.depth > 0 {
		after(0, s.destroy)
		return
	}
	s.destroy()
}

// destroy releases everything the session holds.
func (s *session) destroy() {
	if s.destroyed {
		return
	}
	s.destroyed, s.closed = true, true
	if s.cancelClose != nil {
		s.cancelClose()
		s.cancelClose = nil
	}
	if s.cp != nil {
		if err := s.cp.call(slotUnadvise, uintptr(s.cookie)); err != nil {
			slog.Warn("rdp control: Unadvise failed", "err", err)
		}
		s.cp.release()
		s.cp = nil
	}
	if s.sink != nil {
		s.sink.s = nil
		s.sink.release()
		s.sink = nil
	}
	s.adv.release()
	s.sec.release()
	s.ext.release()
	s.ns5.release()
	s.ns3.release()
	if s.nsPass != s.nsKeys {
		s.nsPass.release()
	}
	s.nsKeys.release()
	s.client.release()
	s.unk.release()
	s.adv, s.sec, s.ext, s.ns5, s.ns3, s.nsPass, s.nsKeys, s.client, s.unk = nil, nil, nil, nil, nil, nil, nil, nil, nil
	if s.hwnd != 0 {
		procDestroyWindow.Call(s.hwnd)
	}
	slog.Info("rdp control: destroyed")
}

// reset goes back to the state before Connect.
func (s *session) reset() {
	s.state = stateIdle
	s.remoteW, s.remoteH = 0, 0
	s.reconnecting, s.lastAttempt = false, 0
	if s.cancelRetry != nil {
		s.cancelRetry()
		s.cancelRetry = nil
	}
}

// event handles an event of the control. Args are its numeric arguments.
func (s *session) event(id int32, args []int) {
	arg := func(i int) int {
		if i < len(args) {
			return args[i]
		}
		return 0
	}
	switch id {
	case evConnecting:
		slog.Info("rdp event: connecting")
	case evConnected:
		slog.Info("rdp event: connected")
		if s.closed || s.state == stateIdle {
			return
		}
		s.state = stateConnected
		if s.fullScreen {
			hideConnectionBar(s.hwnd) // the control shows it when it connects
		}
		if s.ev.Connected != nil {
			s.ev.Connected()
		}
	case evLoginComplete:
		slog.Info("rdp event: login complete")
		s.loggedIn()
	case evDisconnected:
		s.disconnected(arg(0))
	case evRequestGoFullScreen, evRequestLeaveFullScreen:
		on := id == evRequestGoFullScreen
		slog.Info("rdp event: full screen requested", "on", on)
		if !s.closed && !s.settingFS && s.ev.RequestFullScreen != nil {
			s.ev.RequestFullScreen(on)
		}
	case evRequestContainerMinimize:
		slog.Info("rdp event: minimize requested")
		if !s.closed && s.ev.RequestMinimize != nil {
			s.ev.RequestMinimize()
		}
	case evRemoteDesktopSizeChange:
		slog.Info("rdp event: remote desktop size", "width", arg(0), "height", arg(1))
		if !s.closed && s.state != stateIdle && arg(0) > 0 && arg(1) > 0 {
			s.setRemote(arg(0), arg(1))
		}
	case evAutoReconnecting:
		s.autoReconnecting(arg(0), arg(1))
	case evAutoReconnecting2:
		s.autoReconnecting(arg(0), arg(2))
	case evAutoReconnected:
		slog.Info("rdp event: reconnected")
		if s.reconnecting {
			s.loggedIn()
		}
	case evFatalError:
		slog.Error("rdp event: fatal error", "code", arg(0))
		if s.closed || s.state == stateIdle {
			return
		}
		s.reset()
		if s.ev.Disconnected != nil {
			s.ev.Disconnected(arg(0), fmt.Sprintf("The remote desktop control reported a fatal error (code %d).", arg(0)), false)
		}
	case evWarning:
		slog.Warn("rdp event: warning", "code", arg(0))
	case evLogonError:
		slog.Warn("rdp event: logon error", "code", arg(0))
	case evAuthenticationWarningDisplayed:
		slog.Info("rdp event: authentication warning displayed")
	case evAuthenticationWarningDismissed:
		slog.Info("rdp event: authentication warning dismissed")
	case evIdleTimeoutNotification:
		slog.Info("rdp event: idle timeout")
	case evEnterFullScreenMode, evLeaveFullScreenMode, evConfirmClose, evReceivedTSPublicKey,
		evFocusReleased, evNetworkStatusChanged:
		slog.Debug("rdp event", "id", id, "args", args)
	default:
		slog.Debug("rdp event: not handled", "id", id, "args", args)
	}
}

// loggedIn: the remote desktop is on screen, after a login or a reconnect.
func (s *session) loggedIn() {
	if s.closed || s.state == stateIdle {
		return
	}
	// After a reconnect the size of the session is not known for sure.
	s.dynForce = s.reconnecting && s.dynWorked
	s.state = stateLoggedIn
	s.reconnecting, s.lastAttempt, s.dynTries = false, 0, 0
	if s.ev.RemoteSize != nil {
		s.ev.RemoteSize(s.remoteW, s.remoteH)
	}
	if s.ev.LoginComplete != nil {
		s.ev.LoginComplete()
	}
	if s.fullScreen {
		s.putFullScreen(true)
	}
	s.applySize()
}

// autoReconnecting: the control lost the connection and tries again. Newer
// controls report every attempt twice (two events); it is passed on once.
func (s *session) autoReconnecting(reason, attempt int) {
	if s.closed || s.state == stateIdle {
		return
	}
	if s.reconnecting && attempt == s.lastAttempt {
		return
	}
	slog.Info("rdp event: reconnecting", "reason", reason, "attempt", attempt)
	s.reconnecting, s.lastAttempt = true, attempt
	if s.ev.Reconnecting != nil {
		s.ev.Reconnecting(attempt)
	}
}

// disconnected: the session ended, or the connection failed.
func (s *session) disconnected(reason int) {
	extended := 0
	if s.client != nil {
		if v, ok := s.client.getInt("ExtendedDisconnectReason"); ok {
			extended = v
		}
	}
	slog.Info("rdp event: disconnected", "reason", reason, "extended", extended, "text", rdp.DisconnectText(reason, extended))
	was := s.state
	s.reset()
	if s.closed {
		// Close was waiting for this. We are inside an event of the control,
		// so destroy it from the message loop.
		if s.cancelClose != nil {
			s.cancelClose()
			s.cancelClose = nil
		}
		after(0, s.destroy)
		return
	}
	if was == stateIdle {
		return
	}
	if rdp.AuthFailure(reason, extended) && s.ev.AuthFailed != nil {
		s.ev.AuthFailed()
	}
	if !s.closed && s.ev.Disconnected != nil {
		s.ev.Disconnected(reason, rdp.DisconnectText(reason, extended), rdp.UserEnded(reason, extended))
	}
}
