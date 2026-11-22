//go:build windows

// Package ui is the windigo (Win32) user interface of srdp4win: the main
// window with its tab strip, the connection manager in the start tab, the
// dialogs, and one session page per remote desktop.
package ui

import (
	"context"
	"log/slog"
	"path/filepath"
	"syscall"
	"unsafe"

	"github.com/mevdschee/srdp4win/internal/config"
	"github.com/mevdschee/srdp4win/internal/rdp"
	"github.com/mevdschee/srdp4win/internal/rdp/mstsc"
	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"
)

const (
	appName = "srdp4win"
	logName = "srdp4win.log"

	// maxTabs limits the number of open sessions.
	maxTabs = 32

	wmDpiChanged = 0x02E0
)

var (
	procBeginPaint       = user32.NewProc("BeginPaint")
	procEndPaint         = user32.NewProc("EndPaint")
	procFillRect         = user32.NewProc("FillRect")
	procInvalidateRect   = user32.NewProc("InvalidateRect")
	procGetActiveWindow  = user32.NewProc("GetActiveWindow")
	procCreateSolidBrush = gdi32.NewProc("CreateSolidBrush")
	procFindResourceW    = syscall.NewLazyDLL("kernel32.dll").NewProc("FindResourceW")
)

type paintStruct struct {
	hdc         uintptr
	erase       int32
	rc          win.RECT
	restore     int32
	incUpdate   int32
	rgbReserved [32]byte
}

// mainWindow is the shell: the window, the tab strip, the status line and
// the pages. pages[0] is always the start tab.
type mainWindow struct {
	wnd                     *ui.Main
	tabs                    *tabStrip
	statusLeft, statusRight *ui.Static
	start                   *startPage
	pages                   []tabPage
	active                  int

	dir     string
	store   *config.Store
	prefs   *config.Preferences
	version string
	useFake bool

	ctx    context.Context
	cancel context.CancelFunc

	styled     bool
	fontNormal uintptr
	bandHeight int
	statusTop  int // y of the divider above the status line

	full      bool                // full screen
	fullPrev  win.WINDOWPLACEMENT // placement and styles to go back to
	fullStyle uintptr
	fullEx    uintptr

	keyHook   uintptr
	menuDepth int  // open menus: the shortcuts are off
	checking  bool // an update check runs
	closing   bool
}

// Run shows the main window and blocks until it is closed. version is the
// release version ("dev" for development builds, which skip update checks).
// With useFake, or when the RDP control is not registered (Wine), sessions
// use the fake backend.
func Run(dir string, store *config.Store, prefs *config.Preferences, version string, useFake bool) int {
	if store == nil {
		store = &config.Store{}
	}
	if prefs == nil {
		prefs = &config.Preferences{Defaults: rdp.DefaultOptions()}
	}
	if !mstsc.Available() {
		useFake = true
	}
	me := &mainWindow{dir: dir, store: store, prefs: prefs, version: version, useFake: useFake}
	me.ctx, me.cancel = context.WithCancel(context.Background())
	setThemePref(prefs.Theme)
	loadTheme()
	me.build()
	me.events()
	return me.wnd.RunAsMain()
}

func (me *mainWindow) build() {
	opts := ui.OptsMain().
		Title(appName).
		Size(ui.Dpi(1000, 660)).
		Style(co.WS_CAPTION | co.WS_SYSMENU | co.WS_CLIPCHILDREN | co.WS_BORDER |
			co.WS_MINIMIZEBOX | co.WS_MAXIMIZEBOX | co.WS_SIZEBOX).
		ExStyle(co.WS_EX_LEFT).
		Center(true) // paint() and WM_ERASEBKGND draw the background
	// The app icon (winres/winres.json). A build without the resources has
	// none; windigo would panic on it.
	if r, _, _ := procFindResourceW.Call(0, 1, 14); r != 0 { // RT_GROUP_ICON
		opts.ClassIconId(1)
	}
	if me.prefs.Maximized {
		opts.CmdShow(co.SW_SHOWMAXIMIZED)
	}
	me.wnd = ui.NewMain(opts)
	me.tabs = newTabStrip(me.wnd)
	me.tabs.fixed = 1
	me.start = newStartPage(me)
	me.pages = []tabPage{me.start}
	me.statusLeft = ui.NewStatic(me.wnd, ui.OptsStatic().Text("").
		Position(ui.Dpi(12, 630)).Size(ui.Dpi(500, 20)).CtrlStyle(co.SS_LEFT|co.SS_NOPREFIX|co.SS_ENDELLIPSIS|co.SS_CENTERIMAGE))
	me.statusRight = ui.NewStatic(me.wnd, ui.OptsStatic().Text("").
		Position(ui.Dpi(600, 630)).Size(ui.Dpi(380, 20)).CtrlStyle(co.SS_RIGHT|co.SS_NOPREFIX|co.SS_CENTERIMAGE))
}

func (me *mainWindow) events() {
	on := me.wnd.On()
	on.WmCreate(func(_ ui.WmCreate) int {
		me.styleWindow()
		me.restorePlacement()
		me.layout()
		me.installKeyHook()
		me.wnd.UiThread(func() { // after the window is shown
			me.updateStatus()
			me.start.Activated()
		})
		return 0
	})
	on.WmDestroy(func() {
		me.removeKeyHook()
		me.cancel()
	})
	on.WmClose(me.close)
	// The window itself has no use for the focus (windigo hands it to the
	// first child, the tab strip): it goes to the active page.
	on.WmSetFocus(func(_ ui.WmSetFocus) {
		if me.styled && !me.closing {
			me.pages[me.active].Activated()
		}
	})
	on.WmSize(func(p ui.WmSize) {
		if p.Request() != co.SIZE_REQ_MINIMIZED {
			me.layout()
		}
	})
	on.WmGetMinMaxInfo(func(p ui.WmGetMinMaxInfo) {
		if !me.full {
			p.Info().PtMinTrackSize = win.POINT{X: int32(ui.DpiX(680)), Y: int32(ui.DpiY(420))}
		}
	})
	on.WmDisplayChange(func(_ ui.WmDisplayChange) {
		me.fitFullScreen()
		me.layout()
	})
	on.Wm(wmDpiChanged, func(p ui.Wm) uintptr {
		// lParam points to the suggested window rectangle (the cast keeps go
		// vet quiet).
		lp := uintptr(p.LParam)
		rc := *(**win.RECT)(unsafe.Pointer(&lp))
		if me.full {
			me.fitFullScreen()
		} else if rc != nil {
			me.wnd.Hwnd().SetWindowPos(0, win.POINT{X: rc.Left, Y: rc.Top},
				win.SIZE{Cx: rc.Right - rc.Left, Cy: rc.Bottom - rc.Top}, co.SWP_NOZORDER|co.SWP_NOACTIVATE)
		}
		if me.styled {
			me.tabs.setup(me.fontNormal, me.Dpi())
		}
		me.layout()
		return 0
	})
	on.Wm(co.WM_WININICHANGE, func(p ui.Wm) uintptr { // WM_SETTINGCHANGE
		if isThemeChange(p) {
			me.themeChanged()
		}
		return 0
	})
	on.Wm(co.WM_ENTERMENULOOP, func(_ ui.Wm) uintptr { me.menuDepth++; return 0 })
	on.Wm(co.WM_EXITMENULOOP, func(_ ui.Wm) uintptr { me.menuDepth = max(0, me.menuDepth-1); return 0 })

	on.WmPaint(me.paint)
	on.WmEraseBkgnd(func(p ui.WmEraseBkgnd) int {
		if rc, err := me.wnd.Hwnd().GetClientRect(); err == nil {
			fillRect(uintptr(p.Hdc()), rc, colors.window)
		}
		return 1
	})
	// Labels on the window (the status line, the empty list hint) get the
	// window background, the search box the colours of its frame.
	on.WmCtlColorStatic(func(p ui.WmCtlColor) win.HBRUSH {
		procSetTextColor.Call(uintptr(p.Hdc()), uintptr(colors.text))
		p.Hdc().SetBkColor(win.COLORREF(colors.window))
		return solidBrush(colors.window)
	})
	on.WmCtlColorEdit(func(p ui.WmCtlColor) win.HBRUSH {
		procSetTextColor.Call(uintptr(p.Hdc()), uintptr(colors.text))
		p.Hdc().SetBkColor(win.COLORREF(colors.addressFill))
		return solidBrush(colors.addressFill)
	})

	// Enter and Esc as the dialog manager sends them, in case the keyboard
	// hook did not see the key first.
	on.WmCommandAccelMenu(uint16(co.ID_OK), func() {
		if me.active == 0 && !me.full {
			me.start.connect()
		}
	})
	on.WmCommandAccelMenu(uint16(co.ID_CANCEL), func() {})

	me.tabEvents()
	me.start.events()
}

// styleWindow applies the Windows 11 look; called in WM_CREATE, when all
// controls exist.
func (me *mainWindow) styleWindow() {
	h := me.wnd.Hwnd()
	setWin11Frame(h)
	themeFrame(h)
	me.fontNormal = uiFont(0)
	applyFont(h, me.fontNormal)
	me.tabs.setup(me.fontNormal, me.Dpi())
	me.start.style()
	me.styled = true
	me.rebuildTabs()
	slog.Debug("window styled", "dpi", me.Dpi(), "dark", darkMode, "fake", me.useFake)
}

// themeChanged follows a switch between light and dark, in Windows or in
// the settings.
func (me *mainWindow) themeChanged() {
	if !me.styled || !loadTheme() {
		return
	}
	slog.Info("theme changed", "dark", darkMode)
	themeFrame(me.wnd.Hwnd())
	me.tabs.setup(me.fontNormal, me.Dpi())
	for _, p := range me.pages {
		p.ThemeChanged()
	}
	redrawAll(me.wnd.Hwnd())
}

// move places a child window.
func move(h win.HWND, x, y, cx, cy int) {
	if cx < 0 {
		cx = 0
	}
	if cy < 0 {
		cy = 0
	}
	// SWP_NOCOPYBITS and the invalidate: a moved or resized control must be
	// redrawn, not shown with its old pixels (a right-aligned label would
	// otherwise keep its text at the old position, cut off).
	h.SetWindowPos(0, win.POINT{X: int32(x), Y: int32(y)}, win.SIZE{Cx: int32(cx), Cy: int32(cy)},
		co.SWP_NOZORDER|co.SWP_NOACTIVATE|co.SWP_NOCOPYBITS)
	procInvalidateRect.Call(uintptr(h), 0, 1)
}

// shown reports whether a child window is set to be visible (also while
// the main window itself is hidden or minimized).
func shown(h win.HWND) bool {
	style, _ := h.GetWindowLongPtr(co.GWLP_STYLE)
	return style&uintptr(co.WS_VISIBLE) != 0
}

// show shows or hides a child window without activating it.
func show(h win.HWND, on bool) {
	if on == shown(h) {
		return
	}
	if on {
		h.ShowWindow(co.SW_SHOWNA)
	} else {
		h.ShowWindow(co.SW_HIDE)
	}
}

// contentRect is the area of the active page: below the tab band and above
// the status line, or the whole window in full screen.
func (me *mainWindow) contentRect() (win.RECT, bool) {
	rc, err := me.wnd.Hwnd().GetClientRect()
	if err != nil || rc.Right == 0 || rc.Bottom == 0 {
		return win.RECT{}, false
	}
	if me.full {
		return rc, true
	}
	return win.RECT{Left: 0, Top: int32(me.bandHeight), Right: rc.Right, Bottom: int32(me.statusTop)}, true
}

// layout positions everything, top to bottom: the grey tab band, the active
// page and the status line. In full screen the page has the whole window.
func (me *mainWindow) layout() {
	if !me.styled {
		return
	}
	rc, err := me.wnd.Hwnd().GetClientRect()
	if err != nil || rc.Right == 0 || rc.Bottom == 0 {
		return
	}
	cw, ch := int(rc.Right), int(rc.Bottom)
	chrome := !me.full
	show(me.tabs.ctl.Hwnd(), chrome)
	show(me.statusLeft.Hwnd(), chrome)
	show(me.statusRight.Hwnd(), chrome)
	if chrome {
		me.bandHeight = ui.DpiY(42)
		move(me.tabs.ctl.Hwnd(), 0, 0, cw, me.bandHeight)
		// The status labels fill the footer; SS_CENTERIMAGE centres the text.
		statusH := ui.DpiY(30)
		me.statusTop = max(me.bandHeight, ch-statusH-1)
		move(me.statusLeft.Hwnd(), ui.DpiX(12), ch-statusH, cw/2-ui.DpiX(12), statusH)
		move(me.statusRight.Hwnd(), cw/2, ch-statusH, cw/2-ui.DpiX(16), statusH)
	}
	content, _ := me.contentRect()
	for i, p := range me.pages {
		if i != me.active {
			p.Layout(content, false)
		}
	}
	me.pages[me.active].Layout(content, true)
	procInvalidateRect.Call(uintptr(me.wnd.Hwnd()), 0, 1)
}

// paint draws what is not a control: the divider above the status line and
// the start tab's toolbar row and search box (the rest is the window
// background).
func (me *mainWindow) paint() {
	var ps paintStruct
	hdc, _, _ := procBeginPaint.Call(uintptr(me.wnd.Hwnd()), uintptr(unsafe.Pointer(&ps)))
	defer procEndPaint.Call(uintptr(me.wnd.Hwnd()), uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 || !me.styled || me.full {
		return
	}
	rc, _ := me.wnd.Hwnd().GetClientRect()
	fillRect(hdc, win.RECT{Top: int32(me.statusTop), Right: rc.Right, Bottom: int32(me.statusTop) + 1}, colors.divider)
	if me.active == 0 {
		me.start.paint(hdc)
	}
}

// setStaticText sets a label's text, skipping unchanged text (no flicker).
func setStaticText(s *ui.Static, text string) {
	if s.Text() != text {
		s.Hwnd().SetWindowText(text)
	}
}

// updateStatus shows the active page's status in the status line.
func (me *mainWindow) updateStatus() {
	if !me.styled || me.active >= len(me.pages) {
		return
	}
	left, right := me.pages[me.active].Status()
	setStaticText(me.statusLeft, left)
	setStaticText(me.statusRight, right)
}

// restorePlacement puts the window where it was last time, when that is
// still on a screen.
func (me *mainWindow) restorePlacement() {
	p := me.prefs
	if p.WindowW < 200 || p.WindowH < 150 {
		return
	}
	rc := win.RECT{Left: int32(p.WindowX), Top: int32(p.WindowY), Right: int32(p.WindowX + p.WindowW), Bottom: int32(p.WindowY + p.WindowH)}
	if win.MonitorFromRect(&rc, co.MONITOR_DEFAULTTONULL) == 0 {
		return
	}
	me.wnd.Hwnd().SetWindowPos(0, win.POINT{X: rc.Left, Y: rc.Top},
		win.SIZE{Cx: rc.Right - rc.Left, Cy: rc.Bottom - rc.Top}, co.SWP_NOZORDER|co.SWP_NOACTIVATE)
}

// savePlacement stores the window's normal position and whether it is
// maximized in the preferences.
func (me *mainWindow) savePlacement() {
	h := me.wnd.Hwnd()
	wp, err := h.GetWindowPlacement()
	if err != nil {
		return
	}
	if me.full {
		wp = me.fullPrev
	}
	rc := wp.RcNormalPosition
	if h.IsIconic() && !me.full {
		me.prefs.Maximized = wp.Flags&co.WPF_RESTORETOMAXIMIZED != 0
	} else {
		me.prefs.Maximized = wp.ShowCmd == co.SW_SHOWMAXIMIZED
	}
	if rc.Right-rc.Left >= 200 && rc.Bottom-rc.Top >= 150 {
		me.prefs.WindowX, me.prefs.WindowY = int(rc.Left), int(rc.Top)
		me.prefs.WindowW, me.prefs.WindowH = int(rc.Right-rc.Left), int(rc.Bottom-rc.Top)
	}
}

// close handles WM_CLOSE: with sessions open it asks once, then every page
// is closed and the window destroyed.
func (me *mainWindow) close() {
	if me.closing {
		return
	}
	busy := 0
	for _, p := range me.pages[1:] {
		if p.Busy() {
			busy++
		}
	}
	if busy > 0 {
		text := "One session is still open. Closing the window disconnects it."
		if busy > 1 {
			text = "Several sessions are still open. Closing the window disconnects them."
		}
		if !me.Confirm("Close "+appName+"?", text) {
			return
		}
	}
	me.closing = true
	for _, p := range me.pages[1:] {
		p.CanClose(true)
	}
	me.SetFullScreen(false)
	me.savePlacement()
	me.SavePrefs()
	pages := me.pages[1:]
	me.pages, me.active = me.pages[:1], 0
	for _, p := range pages {
		p.Layout(win.RECT{}, false)
		p.Destroy()
	}
	slog.Info("window closed")
	me.wnd.Hwnd().DestroyWindow()
}

// --- shell ---

func (me *mainWindow) Parent() ui.Parent { return me.wnd }
func (me *mainWindow) Hwnd() win.HWND    { return me.wnd.Hwnd() }

// Post runs f on the UI thread, from any goroutine.
func (me *mainWindow) Post(f func()) { me.wnd.UiThread(f) }

func (me *mainWindow) Minimize() { me.wnd.Hwnd().ShowWindow(co.SW_MINIMIZE) }

func (me *mainWindow) Dpi() int {
	if dpi := me.wnd.Hwnd().GetDpiForWindow(); dpi > 0 {
		return int(dpi)
	}
	return 96
}

func (me *mainWindow) Prefs() *config.Preferences { return me.prefs }

func (me *mainWindow) SavePrefs() {
	if err := me.prefs.Save(me.dir); err != nil {
		slog.Error("save settings", "err", err)
	}
}

func (me *mainWindow) Store() *config.Store { return me.store }

func (me *mainWindow) SaveStore() {
	if err := me.store.Save(me.dir); err != nil {
		slog.Error("save connections", "err", err)
		if !me.closing {
			ui.MsgError(me.wnd, appName, "Could not save the connections", err.Error())
		}
	}
	me.start.fill()
}

func (me *mainWindow) AskPassword(title, prompt string, offerSave bool) (string, bool, bool) {
	return askPassword(me.wnd, title, prompt, offerSave)
}

func (me *mainWindow) AskTrustHost(hostport, keyType, fingerprint string) bool {
	return askTrustHost(me.wnd, hostport, keyType, fingerprint)
}

func (me *mainWindow) ShowError(title, text string) { ui.MsgError(me.wnd, appName, title, text) }

func (me *mainWindow) Confirm(title, text string) bool {
	return ui.MsgOkCancel(me.wnd, appName, title, text, "Yes")
}

// logFile is the log's path (written by package logging).
func (me *mainWindow) logFile() string { return filepath.Join(me.dir, logName) }
