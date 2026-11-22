//go:build windows

package ui

// The session page: one remote desktop in a tab. A container window, child
// of the main window, holds a toolbar row and below it the session backend's
// window (Microsoft's RDP control, or the fake one under Wine). In full
// screen the toolbar row is gone and the floating toolbar takes its place.
// The connect flow (SSH, tunnel, RDP, reconnect) is in sessionconn.go.

import (
	"context"
	"log/slog"
	"strings"
	"time"
	"unsafe"

	"github.com/mevdschee/srdp4win/internal/config"
	"github.com/mevdschee/srdp4win/internal/rdp"
	"github.com/mevdschee/srdp4win/internal/sshconn"
	"github.com/mevdschee/srdp4win/internal/tunnel"
	"github.com/rodrigocfd/windigo/win"
)

// Button ids of the toolbar row, the floating toolbar and the buttons under
// a "disconnected" message.
const (
	sbFull = iota + 1
	sbMode
	sbKeys
	sbShot
	sbDisc
	sbPin
	sbName
	sbPrev
	sbNext
	sbMin
	sbReconnect
	sbClose
)

// Timers of the container window.
const (
	sessTimerResize = 1 // the resize debounce
	sessTimerNote   = 2 // a passing note in the status line
)

type sessionPage struct {
	sh      shell
	c       config.Connection
	useFake bool

	hwnd  uintptr   // the container
	bar   *sessBar  // toolbar row
	over  *sessBar  // Reconnect and Close under a "disconnected" message
	float *floatBar // only in full screen, while this is the visible tab

	ctx    context.Context // ends when the tab closes
	cancel context.CancelFunc
	dead   bool // destroyed
	ending bool // the user disconnected; the tab is closing

	state     sessState
	attempt   int       // reconnect attempt
	reason    string    // why the session ended
	lostAt    time.Time // when it ended
	noDynamic bool      // the server refused dynamic resolution
	note      string    // passing text for the status line

	client     *sshconn.Client // nil while SSH is down
	tun        *tunnel.Tunnel
	sess       rdp.Session
	gen        int       // counts backends, to drop events of an old one
	dialing    bool      // an SSH dial is running
	rdpLost    bool      // the session ended while SSH was down: connect again
	rdpTrouble bool      // the backend reported that it is reconnecting
	before     sessState // the state when SSH dropped
	asking     bool      // a rejected RDP password is being asked again
	queue      []func()  // backend events waiting for their posted message

	password     string // RDP password of this tab
	havePassword bool
	askFresh     bool // the server rejected the password we had

	width, height int      // of the container
	visible       bool     // the visible tab
	wentFull      bool     // the connection's full screen option was applied
	full          bool     // full screen as last seen in Layout
	dpi           int      // DPI the bars were made for
	have          sessRect // what the session was last resized to
	want          sessRect // what it should get after the debounce
}

// newSessionPage creates the page and starts connecting.
func newSessionPage(sh shell, c config.Connection, useFake bool) tabPage {
	p := &sessionPage{sh: sh, c: c, useFake: useFake}
	p.ctx, p.cancel = context.WithCancel(context.Background())
	h, err := sessNewWindow(0, sessWsChild|sessWsClipChildren|sessWsClipSiblings, uintptr(sh.Hwnd()), p.wndProc)
	if err != nil {
		slog.Error("session window", "err", err)
	}
	p.hwnd = h
	p.bar = newSessBar(h, func() uint32 { return colors.nav }, []*sessItem{
		{id: sbFull, icons: []string{"full_screen_maximize", "arrow_maximize"}, label: "Full screen", tip: "Full screen (F11)"},
		{id: sbMode, icons: []string{"resize", "scale_fit"}, label: "Resize", tip: "Resize mode"},
		{id: sbKeys, icons: []string{"keyboard"}, label: "Keys", tip: "Send keys"},
		{id: sbShot, icons: []string{"camera"}, label: "Screenshot", tip: "Copy a screenshot to the clipboard"},
		{kind: sessSep},
		{id: sbDisc, icons: []string{"plug_disconnected", "dismiss_circle"}, label: "Disconnect", tip: "Disconnect"},
	}, func(id int) { p.barClick(p.bar, id) })
	p.over = newSessBar(h, func() uint32 { return colors.window }, []*sessItem{
		{id: sbReconnect, kind: sessPush, label: "Reconnect"},
		{id: sbClose, kind: sessPush, label: "Close"},
	}, func(id int) { p.barClick(p.over, id) })
	p.over.shown = false
	p.setupBars()
	if h == 0 {
		p.state, p.reason = stDisconnected, "the session window could not be created"
		return p
	}
	p.syncButtons()
	p.start()
	return p
}

func (p *sessionPage) setupBars() {
	p.dpi = p.sh.Dpi()
	p.bar.setup(p.dpi)
	p.over.setup(p.dpi)
}

// tabPage.

func (p *sessionPage) Title() string { return p.c.DisplayName() }

func (p *sessionPage) Status() (left, right string) {
	left = p.note
	if left == "" {
		left = statusLeft(p.state, p.c.SSHAddr(), p.c.RDPHost, p.attempt, p.reason, p.noDynamic)
	}
	if p.sess != nil && p.sessionShown() {
		w, h := p.sess.RemoteSize()
		right = statusRight(w, h, p.sess.Mode())
	}
	return left, right
}

func (p *sessionPage) Busy() bool { return !p.dead && p.state.busy() }

func (p *sessionPage) CanClose(quiet bool) bool {
	if quiet || !p.Busy() {
		return true
	}
	return p.sh.Confirm("Close session", "This ends the session with "+p.c.DisplayName()+". Close the tab?")
}

func (p *sessionPage) Layout(r win.RECT, visible bool) {
	if p.dead || p.hwnd == 0 {
		return
	}
	p.width, p.height = int(r.Right-r.Left), int(r.Bottom-r.Top)
	p.visible = visible
	flags := uintptr(sessSwpNoZOrder | sessSwpNoActivate | sessSwpHide)
	if visible {
		flags ^= sessSwpHide | sessSwpShow
	}
	sessSetWindowPos.Call(p.hwnd, 0, uintptr(r.Left), uintptr(r.Top), uintptr(p.width), uintptr(p.height), flags)
	if !visible {
		p.syncFloat()
		return
	}
	if dpi := p.sh.Dpi(); dpi != p.dpi {
		p.setupBars()
		p.have = sessRect{} // the scale changed: resize at once
	}
	if full := p.sh.FullScreen(); full != p.full {
		p.full = full
		if p.sess != nil {
			p.sess.SetFullScreen(full)
		}
	}
	p.bar.setShown(!p.full)
	p.bar.layout(0, 0)
	p.layoutOverlay()
	p.applyRect(false)
	p.syncFloat()
}

func (p *sessionPage) Activated() {
	if p.dead {
		return
	}
	if p.sessionShown() {
		p.sess.Focus()
	} else if p.hwnd != 0 {
		sessSetFocus.Call(p.hwnd)
	}
}

func (p *sessionPage) ThemeChanged() {
	if p.dead {
		return
	}
	p.setupBars()
	sessInvalidate(p.hwnd)
	if p.float != nil {
		p.float.themeChanged()
	}
}

func (p *sessionPage) Command(id int) bool {
	if p.dead {
		return false
	}
	switch id {
	case cmdFullScreen:
		p.sh.SetFullScreen(!p.sh.FullScreen())
	case cmdDisconnect:
		p.disconnect()
	case cmdScreenshot:
		p.screenshot()
	default:
		return false
	}
	return true
}

func (p *sessionPage) Destroy() {
	if p.dead {
		return
	}
	p.dead = true
	p.cancel()
	sessKillTimer.Call(p.hwnd, sessTimerResize)
	sessKillTimer.Call(p.hwnd, sessTimerNote)
	if p.float != nil {
		p.float.destroy()
		p.float = nil
	}
	if p.sess != nil {
		p.sess.Close()
		p.sess = nil
	}
	p.dropLink()
	p.bar.destroy()
	p.over.destroy()
	sessDestroyWindow(p.hwnd)
}

// deferUI runs f on the UI thread after the current message, in order.
// UI thread only.
func (p *sessionPage) deferUI(f func()) {
	p.queue = append(p.queue, f)
	sessPostMessage.Call(p.hwnd, sessWmDefer, 0, 0)
}

// State.

// changed tells the shell that the status line is different.
func (p *sessionPage) changed() {
	if !p.dead {
		p.sh.PageChanged(p)
	}
}

func (p *sessionPage) setState(s sessState) {
	if p.dead {
		return
	}
	if s != p.state {
		slog.Info("session state", "connection", p.c.DisplayName(), "state", statusLeft(s, p.c.SSHAddr(), p.c.RDPHost, p.attempt, p.reason, false))
	}
	p.state = s
	if s != stReconnecting && s != stDisconnected {
		p.attempt = 0
	}
	p.over.setShown(s == stDisconnected)
	if p.sess != nil {
		sessShow(p.sess.Hwnd(), p.sessionShown())
	}
	p.syncButtons()
	sessInvalidate(p.hwnd)
	p.changed()
}

// fail ends the flow with a reason, shown in the page and the status line.
func (p *sessionPage) fail(reason string) {
	if p.dead {
		return
	}
	p.reason, p.lostAt = reason, time.Now()
	p.setState(stDisconnected)
	p.layoutOverlay()
}

// sessionShown reports whether the backend's window is what the page shows.
func (p *sessionPage) sessionShown() bool {
	if p.sess == nil {
		return false
	}
	switch p.state {
	case stRDP, stRDPLogin, stConnected, stReconnecting:
		return true
	}
	return false
}

// syncButtons greys the buttons that need a remote desktop.
func (p *sessionPage) syncButtons() {
	live := p.sess != nil && p.state == stConnected
	bars := []*sessBar{p.bar}
	if p.float != nil && p.float.bar != nil {
		bars = append(bars, p.float.bar)
	}
	for _, b := range bars {
		for _, id := range []int{sbKeys, sbShot} {
			if it := b.item(id); it != nil && it.disabled == live {
				it.disabled = !live
				sessInvalidate(b.hwnd)
			}
		}
	}
}

func (p *sessionPage) setNote(text string) {
	p.note = text
	sessSetTimer.Call(p.hwnd, sessTimerNote, 3000, 0)
	p.changed()
}

// Layout.

func (p *sessionPage) barHeight() int {
	if p.full {
		return 0
	}
	return int(p.bar.height())
}

// applyRect gives the session its rectangle: a move at once, a new size
// after the debounce while the session is live. Force skips the debounce.
func (p *sessionPage) applyRect(force bool) {
	p.want = sessionRect(p.width, p.height, p.barHeight(), p.full)
	if p.sess == nil {
		return
	}
	// Only a dynamic session resizes the remote desktop, which is what the
	// debounce is for; a scaled or fixed one just follows the window.
	live := !force && p.sess.Connected() && p.sess.Mode() == rdp.ResizeDynamic
	now, later := resizePlan(p.have, p.want, live)
	if now != p.have || (force && !now.empty()) {
		p.resizeTo(now)
	}
	if later {
		sessSetTimer.Call(p.hwnd, sessTimerResize, uintptr(resizeDebounce.Milliseconds()), 0)
	} else {
		sessKillTimer.Call(p.hwnd, sessTimerResize)
	}
}

func (p *sessionPage) resizeTo(r sessRect) {
	p.have = r
	p.sess.Resize(r.x, r.y, r.w, r.h, scalePercent(p.sh.Dpi()))
	p.changed() // the remote size in the status line may follow
}

// layoutOverlay centres the Reconnect and Close buttons in the session area.
func (p *sessionPage) layoutOverlay() {
	area := sessionRect(p.width, p.height, p.barHeight(), p.full)
	w := p.over.measure()
	p.over.layout(int32(centred(area.x, area.w, int(w))), int32(area.y+area.h/2))
}

// syncFloat creates, places or removes the floating toolbar.
func (p *sessionPage) syncFloat() {
	want := p.visible && p.full && !p.dead
	switch {
	case want && p.float == nil:
		p.float = newFloatBar(p)
		p.syncButtons()
	case !want && p.float != nil:
		p.float.destroy()
		p.float = nil
	case want:
		p.float.place()
	}
}

// Window procedure of the container.

func (p *sessionPage) wndProc(msg uint32, wp, lp uintptr) (uintptr, bool) {
	switch msg {
	case sessWmPaint:
		p.paint()
		return 0, true
	case sessWmEraseBkgnd:
		return 1, true
	case sessWmMouseMove:
		x, y := sessMousePos(lp)
		p.bar.mouseMove(x, y)
		p.over.mouseMove(x, y)
		return 0, true
	case sessWmMouseLeave:
		p.bar.mouseLeave()
		p.over.mouseLeave()
		return 0, true
	case sessWmLButtonDown:
		x, y := sessMousePos(lp)
		if !p.bar.mouseDown(x, y) {
			p.over.mouseDown(x, y)
		}
		return 0, true
	case sessWmLButtonUp:
		x, y := sessMousePos(lp)
		p.bar.mouseUp(x, y)
		if !p.dead {
			p.over.mouseUp(x, y)
		}
		return 0, true
	case sessWmCaptureLost:
		p.bar.cancelPress()
		p.over.cancelPress()
		return 0, true
	case sessWmSetFocus:
		if p.sessionShown() {
			p.sess.Focus()
		}
		return 0, true
	case sessWmDefer:
		if len(p.queue) > 0 {
			f := p.queue[0]
			p.queue = p.queue[1:]
			if !p.dead {
				f()
			}
		}
		return 0, true
	case sessWmTimer:
		switch wp {
		case sessTimerResize:
			sessKillTimer.Call(p.hwnd, sessTimerResize)
			if p.sess != nil && !p.want.empty() && p.want != p.have {
				p.resizeTo(p.want)
			}
		case sessTimerNote:
			sessKillTimer.Call(p.hwnd, sessTimerNote)
			p.note = ""
			p.changed()
		}
		return 0, true
	}
	return 0, false
}

func (p *sessionPage) paint() {
	var ps sessPaintStruct
	hdc, _, _ := sessBeginPaint.Call(p.hwnd, uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 {
		return
	}
	defer sessEndPaint.Call(p.hwnd, uintptr(unsafe.Pointer(&ps)))
	w, h := int32(p.width), int32(p.height)
	barH := int32(p.barHeight())
	if barH > 0 {
		sessBuffered(hdc, 0, 0, w, barH, func(mem uintptr) {
			sessFill(mem, sessWinRect{0, 0, w, barH}, colors.nav)
			sessFill(mem, sessWinRect{0, barH - 1, w, barH}, colors.divider)
			p.bar.paint(mem, 0, 0)
		})
	}
	if p.sessionShown() {
		// The backend's window covers this, except while a resize waits.
		sessFill(hdc, sessWinRect{0, barH, w, h}, colors.window)
		return
	}
	sessBuffered(hdc, 0, barH, w, h-barH, func(mem uintptr) {
		sessFill(mem, sessWinRect{0, 0, w, h - barH}, colors.window)
		old, _, _ := sessSelectObject.Call(mem, p.bar.font)
		defer sessSelectObject.Call(mem, old)
		if p.state != stDisconnected {
			rc := sessWinRect{0, 0, w, h - barH}
			text := statusLeft(p.state, p.c.SSHAddr(), p.c.RDPHost, p.attempt, "", false)
			sessText(mem, text+"...", &rc, colors.text, sessDtSingleLine|sessDtVCenter|sessDtCenter)
			return
		}
		// The reason, above the buttons.
		text := "Disconnected"
		if p.reason != "" {
			text += "\n" + strings.ToUpper(p.reason[:1]) + p.reason[1:]
		}
		side := max((w-p.bar.px(560))/2, p.bar.px(16))
		rc := sessWinRect{side, 0, w - side, 0}
		height := sessText(mem, text, &rc, colors.text, sessDtCenter|sessDtWordBreak|sessDtCalcRect)
		bottom := p.over.y - barH - p.bar.px(8)
		rc = sessWinRect{side, bottom - height, w - side, bottom}
		sessText(mem, text, &rc, colors.text, sessDtCenter|sessDtWordBreak)
		p.over.paint(mem, 0, -barH)
	})
}

// Commands.

// barClick runs a button of the toolbar row, the floating toolbar or the
// "disconnected" message. Menus open under the button in bar b.
func (p *sessionPage) barClick(b *sessBar, id int) {
	switch id {
	case sbFull:
		p.sh.SetFullScreen(!p.sh.FullScreen())
	case sbMode:
		p.modeMenu(b)
	case sbKeys:
		p.keysMenu(b)
	case sbShot:
		p.screenshot()
	case sbDisc:
		p.disconnect()
	case sbReconnect:
		p.reconnect()
	case sbClose:
		p.sh.ClosePage(p)
	}
}

// focusSession gives the keys back to the remote desktop, after a menu.
func (p *sessionPage) focusSession() {
	if p.dead || !p.visible || !p.sessionShown() {
		return
	}
	// Not while a dialog of ours or another app has the keyboard.
	if fg, _, _ := sessGetForeground.Call(); fg != 0 && fg != uintptr(p.sh.Hwnd()) {
		return
	}
	p.sess.Focus()
}

// mode is the resize mode in effect, or the one the next connect will use.
func (p *sessionPage) mode() rdp.ResizeMode {
	if p.sess != nil {
		return p.sess.Mode()
	}
	return p.c.Options.Resize
}

func (p *sessionPage) modeMenu(b *sessBar) {
	cur := p.mode()
	modes := []rdp.ResizeMode{rdp.ResizeDynamic, rdp.ResizeScaled, rdp.ResizeFixed}
	names := []string{"Dynamic resolution", "Scaled", "Fixed size"}
	var items []sessMenuItem
	for i, m := range modes {
		items = append(items, sessMenuItem{id: i + 1, text: names[i], checked: m == cur})
	}
	at := b.anchor(sbMode)
	choice := sessMenu(uintptr(p.sh.Hwnd()), items, at.x, at.y)
	if p.dead {
		return
	}
	if choice > 0 && modes[choice-1] != cur {
		m := modes[choice-1]
		p.c.Options.Resize = m // for a reconnect in this tab; not saved
		p.noDynamic = false
		if p.sess != nil {
			p.sess.SetMode(m)
		}
		p.changed()
	}
	p.focusSession()
}

func (p *sessionPage) keysMenu(b *sessBar) {
	var items []sessMenuItem
	for i, k := range sendKeyList {
		if k.sep {
			items = append(items, sessMenuItem{})
		}
		items = append(items, sessMenuItem{id: i + 1, text: k.text})
	}
	at := b.anchor(sbKeys)
	choice := sessMenu(uintptr(p.sh.Hwnd()), items, at.x, at.y)
	if p.dead {
		return
	}
	p.focusSession()
	if choice > 0 && p.sess != nil && p.state == stConnected {
		p.sess.SendKeys(sendKeyList[choice-1].keys)
	}
}

// screenshot copies the remote desktop to the clipboard.
func (p *sessionPage) screenshot() {
	if p.sess == nil || p.state != stConnected {
		return
	}
	img, err := p.sess.Screenshot()
	if err == nil {
		err = sessCopyDIB(uintptr(p.sh.Hwnd()), dibFromImage(img))
	}
	if err != nil {
		slog.Error("screenshot", "err", err)
		p.setNote("Screenshot failed: " + err.Error())
		return
	}
	p.setNote("Screenshot copied")
}

// disconnect ends the session and closes the tab, after asking.
func (p *sessionPage) disconnect() {
	if p.Busy() && !p.sh.Confirm("Disconnect", "Disconnect from "+p.c.DisplayName()+"?") {
		p.focusSession()
		return
	}
	if p.dead {
		return
	}
	p.ending = true
	if p.sess != nil {
		p.sess.Disconnect()
	}
	p.sh.ClosePage(p)
}
