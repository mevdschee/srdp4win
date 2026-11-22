//go:build windows

package ui

// floatBar is the toolbar of a full screen session: a small popup owned by
// the main window, centred at the top of the monitor (a child window cannot
// sit on top of the RDP control reliably). Hidden it leaves a strip of two
// pixels; the mouse on the strip slides it down, half a second after the
// mouse left it slides back. A pin keeps it down. It never takes the
// keyboard focus, so the keys stay with the remote desktop.
//
// The window always has the full size of the bar. Sliding moves it up past
// the monitor's edge, and a window region cuts off what is above the edge
// (so nothing shows on a monitor above this one) and rounds the bottom
// corners.

import (
	"time"
	"unsafe"
)

// Timers of the floating toolbar.
const (
	floatTimerAnim  = 1 // one frame of the slide
	floatTimerHide  = 2 // the mouse left: slide back
	floatTimerIntro = 3 // shown after entering full screen: slide back
	floatTimerPoll  = 4 // is the app still in front, tab count, lost mouse

	floatFrame = 15 // ms per frame
	floatPoll  = 250
)

type floatBar struct {
	p    *sessionPage
	hwnd uintptr
	bar  *sessBar

	w, h    int32
	mon     sessWinRect // the monitor of the main window
	visible int32       // pixel rows on screen, as last applied

	pos, from, to float64 // 0 hidden .. 1 shown
	animStart     time.Time
	animating     bool

	intro     bool // just entered full screen: stay down for a moment
	menuOpen  bool // one of its menus is open: stay down
	hideArmed bool // the hide timer runs
	active    bool // the app is in front (else the bar is hidden)
	tabs      int  // session tabs when last placed
}

func newFloatBar(p *sessionPage) *floatBar {
	f := &floatBar{p: p, pos: 1, to: 1, intro: true, active: true}
	h, err := sessNewWindow(sessExToolWindow|sessExNoActivate, sessWsPopup, uintptr(p.sh.Hwnd()), f.wndProc)
	if err != nil {
		return f // no toolbar; F11 and Ctrl+Alt+Break still leave full screen
	}
	f.hwnd = h
	f.bar = newSessBar(h, func() uint32 { return colors.band }, []*sessItem{
		{id: sbPin, icons: []string{"pin"}, label: "Pin"},
		{id: sbName, kind: sessLabel, label: p.c.DisplayName()},
		{kind: sessSep},
		{id: sbFull, icons: []string{"full_screen_minimize"}, label: "Window", tip: "Leave full screen"},
		{id: sbMode, icons: []string{"resize", "scale_fit"}, label: "Resize", tip: "Resize mode"},
		{id: sbPrev, icons: []string{"chevron_left", "arrow_left"}, label: "<", tip: "Previous tab"},
		{id: sbNext, icons: []string{"chevron_right", "arrow_right"}, label: ">", tip: "Next tab"},
		{id: sbKeys, icons: []string{"keyboard"}, label: "Keys", tip: "Send keys"},
		{id: sbShot, icons: []string{"camera"}, label: "Screenshot", tip: "Copy a screenshot to the clipboard"},
		{id: sbMin, icons: []string{"subtract"}, label: "Minimize", tip: "Minimize"},
		{id: sbDisc, icons: []string{"plug_disconnected", "dismiss_circle"}, label: "Disconnect", tip: "Disconnect"},
	}, f.click)
	f.setup()
	f.place()
	f.setTopmost(true)
	sessShow(h, true)
	sessSetTimer.Call(h, floatTimerIntro, uintptr(slideIntro.Milliseconds()), 0)
	sessSetTimer.Call(h, floatTimerPoll, floatPoll, 0)
	return f
}

func (f *floatBar) pinned() bool { return f.p.sh.Prefs().ToolbarPinned }

// setup renders the bar for the DPI, palette and pin state.
func (f *floatBar) setup() {
	pin := f.bar.item(sbPin)
	if f.pinned() {
		pin.icons, pin.tip, pin.checked = []string{"pin_off", "pin"}, "Hide the toolbar automatically", true
	} else {
		pin.icons, pin.tip, pin.checked = []string{"pin"}, "Keep the toolbar visible", false
	}
	f.bar.setup(f.p.sh.Dpi())
}

func (f *floatBar) themeChanged() {
	if f.hwnd == 0 {
		return
	}
	f.setup()
	f.place()
}

// place sizes the bar and puts it on the main window's monitor. Called on
// every layout of the page: the monitor, the DPI or the tabs may differ.
func (f *floatBar) place() {
	if f.hwnd == 0 {
		return
	}
	if f.p.sh.Dpi() != f.bar.dpi {
		f.setup()
	}
	f.tabs = f.p.sh.TabCount()
	for _, id := range []int{sbPrev, sbNext} {
		f.bar.item(id).hidden = f.tabs <= 1
	}
	f.w = f.bar.layout(0, 0)
	f.h = f.bar.height()
	if mon, ok := sessMonitorRect(uintptr(f.p.sh.Hwnd())); ok {
		f.mon = mon
	}
	f.visible = -1
	f.apply()
}

// apply moves and clips the window for the current slide position.
func (f *floatBar) apply() {
	vis := int32(slideVisible(f.pos, int(f.h)))
	if vis == f.visible {
		return
	}
	growing := vis > f.visible
	f.visible = vis
	x := int32(centred(int(f.mon.left), int(f.mon.right-f.mon.left), int(f.w)))
	y := f.mon.top - (f.h - vis)
	move := func() {
		sessSetWindowPos.Call(f.hwnd, 0, uintptr(x), uintptr(y), uintptr(f.w), uintptr(f.h), sessSwpNoZOrder|sessSwpNoActivate)
	}
	clip := func() {
		// The rows below the monitor's edge, with round bottom corners once
		// there is room for them.
		rgn, _, _ := sessCreateRectRgn.Call(0, uintptr(f.h-vis), uintptr(f.w), uintptr(f.h))
		if r := f.bar.px(8); vis > r {
			round, _, _ := sessCreateRoundRectRgn.Call(0, uintptr(f.h-vis-2*r), uintptr(f.w+1), uintptr(f.h+1), uintptr(2*r), uintptr(2*r))
			sessCombineRgn.Call(rgn, rgn, round, 1) // RGN_AND
			sessDeleteObject.Call(round)
		}
		sessSetWindowRgn.Call(f.hwnd, rgn, 1) // the window owns the region now
	}
	// Never show rows above the edge: going down move first, going up clip
	// first.
	if growing {
		move()
		clip()
	} else {
		clip()
		move()
	}
}

// slideTo starts sliding to 0 (hidden) or 1 (shown).
func (f *floatBar) slideTo(to float64) {
	if f.to == to && (f.animating || f.pos == to) {
		return
	}
	f.from, f.to, f.animStart, f.animating = f.pos, to, time.Now(), true
	sessSetTimer.Call(f.hwnd, floatTimerAnim, floatFrame, 0)
}

// mouseInside reports whether the mouse is on the visible part of the bar.
func (f *floatBar) mouseInside() bool {
	var pt sessPoint
	var wr sessWinRect
	sessGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	sessGetWindowRect.Call(f.hwnd, uintptr(unsafe.Pointer(&wr)))
	wr.top = wr.bottom - f.visible
	return wr.has(pt.x, pt.y)
}

// armHide starts the half second after which the bar slides back.
func (f *floatBar) armHide() {
	if f.pinned() || f.menuOpen || f.intro || f.hideArmed {
		return
	}
	f.hideArmed = true
	sessSetTimer.Call(f.hwnd, floatTimerHide, uintptr(slideHideDelay.Milliseconds()), 0)
}

func (f *floatBar) disarmHide() {
	if f.hideArmed {
		f.hideArmed = false
		sessKillTimer.Call(f.hwnd, floatTimerHide)
	}
}

// setTopmost keeps the bar above everything only while the app is in front.
func (f *floatBar) setTopmost(on bool) {
	after := ^uintptr(1) // HWND_NOTOPMOST (-2)
	if on {
		after = ^uintptr(0) // HWND_TOPMOST (-1)
	}
	sessSetWindowPos.Call(f.hwnd, after, 0, 0, 0, 0, sessSwpNoMove|sessSwpNoSize|sessSwpNoActivate)
}

func (f *floatBar) wndProc(msg uint32, wp, lp uintptr) (uintptr, bool) {
	switch msg {
	case sessWmMouseActivate:
		return 3, true // MA_NOACTIVATE
	case sessWmPaint:
		f.paint()
		return 0, true
	case sessWmEraseBkgnd:
		return 1, true
	case sessWmMouseMove:
		x, y := sessMousePos(lp)
		f.bar.mouseMove(x, y)
		f.disarmHide()
		f.slideTo(1)
		return 0, true
	case sessWmMouseLeave:
		f.bar.mouseLeave()
		f.armHide()
		return 0, true
	case sessWmLButtonDown:
		x, y := sessMousePos(lp)
		f.bar.mouseDown(x, y)
		return 0, true
	case sessWmLButtonUp:
		x, y := sessMousePos(lp)
		f.bar.mouseUp(x, y) // may destroy the bar
		return 0, true
	case sessWmCaptureLost:
		f.bar.cancelPress()
		return 0, true
	case sessWmTimer:
		f.timer(wp)
		return 0, true
	}
	return 0, false
}

func (f *floatBar) timer(id uintptr) {
	switch id {
	case floatTimerAnim:
		pos, done := slidePos(f.from, f.to, time.Since(f.animStart))
		f.pos = pos
		if done {
			f.animating = false
			sessKillTimer.Call(f.hwnd, floatTimerAnim)
		}
		f.apply()
	case floatTimerHide:
		f.hideArmed = false
		sessKillTimer.Call(f.hwnd, floatTimerHide)
		if !f.pinned() && !f.menuOpen && !f.mouseInside() {
			f.slideTo(0)
		}
	case floatTimerIntro:
		f.intro = false
		sessKillTimer.Call(f.hwnd, floatTimerIntro)
		if !f.pinned() && !f.menuOpen && !f.mouseInside() {
			f.slideTo(0)
		}
	case floatTimerPoll:
		// Do not float over other apps.
		if active := sessAppActive(); active != f.active {
			f.active = active
			f.setTopmost(active)
			sessShow(f.hwnd, active)
		}
		if f.p.sh.TabCount() != f.tabs {
			f.place()
		}
		// A mouse that left without WM_MOUSELEAVE (the bar moved away
		// under it, or a menu was open).
		if f.to == 1 && !f.intro && !f.mouseInside() {
			f.armHide()
		}
	}
}

func (f *floatBar) paint() {
	var ps sessPaintStruct
	hdc, _, _ := sessBeginPaint.Call(f.hwnd, uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 {
		return
	}
	defer sessEndPaint.Call(f.hwnd, uintptr(unsafe.Pointer(&ps)))
	sessBuffered(hdc, 0, 0, f.w, f.h, func(mem uintptr) {
		// A one pixel border left, right and below; the top is the screen edge.
		r := f.bar.px(8)
		sessFill(mem, sessWinRect{0, 0, f.w, f.h}, colors.addressBorder)
		sessRound(mem, sessWinRect{1, -r, f.w - 2, f.h - 2}, r-1, colors.band)
		f.bar.paint(mem, 0, 0)
	})
}

func (f *floatBar) click(id int) {
	p := f.p
	switch id {
	case sbPin:
		prefs := p.sh.Prefs()
		prefs.ToolbarPinned = !prefs.ToolbarPinned
		p.sh.SavePrefs()
		f.disarmHide()
		f.setup()
		f.place()
		p.syncButtons()
	case sbPrev:
		p.sh.SwitchTab(-1) // hides this page, which removes the bar
	case sbNext:
		p.sh.SwitchTab(1)
	case sbMin:
		p.sh.Minimize()
	case sbMode, sbKeys:
		f.menuOpen = true
		p.barClick(f.bar, id)
		if p.float != f || f.hwnd == 0 {
			return // gone while the menu was open
		}
		f.menuOpen = false
		if !f.mouseInside() {
			f.armHide()
		}
	default:
		p.barClick(f.bar, id) // may leave full screen, which removes the bar
	}
}

func (f *floatBar) destroy() {
	if f.hwnd == 0 {
		return
	}
	for _, id := range []uintptr{floatTimerAnim, floatTimerHide, floatTimerIntro, floatTimerPoll} {
		sessKillTimer.Call(f.hwnd, id)
	}
	f.bar.destroy()
	sessDestroyWindow(f.hwnd)
	f.hwnd = 0
}
