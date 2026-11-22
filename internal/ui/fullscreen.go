//go:build windows

package ui

// Full screen: the window drops its frame, tab strip and status line and
// covers the monitor it is on; the active page gets the whole window.
// Leaving restores the frame and the placement (also a maximized one).

import (
	"log/slog"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/win"
)

// Styles a framed window has and a full screen one has not. WS_MAXIMIZE
// goes too: a maximized window is full screen as a plain popup and is
// maximized again from its placement on leaving (Wine would otherwise keep
// the full screen size).
const (
	frameStyles = uintptr(co.WS_CAPTION | co.WS_SYSMENU | co.WS_BORDER | co.WS_SIZEBOX | co.WS_MINIMIZEBOX |
		co.WS_MAXIMIZEBOX | co.WS_MAXIMIZE)
	frameExStyles = uintptr(co.WS_EX_WINDOWEDGE | co.WS_EX_CLIENTEDGE | co.WS_EX_STATICEDGE | co.WS_EX_DLGMODALFRAME)
)

func (me *mainWindow) FullScreen() bool { return me.full }

// SetFullScreen enters or leaves full screen. The start tab cannot be full
// screen.
func (me *mainWindow) SetFullScreen(on bool) {
	if on == me.full || (on && (me.active == 0 || me.closing)) {
		return
	}
	h := me.wnd.Hwnd()
	if on {
		wp, err := h.GetWindowPlacement()
		if err != nil {
			slog.Error("full screen", "err", err)
			return
		}
		if h.IsIconic() {
			h.ShowWindow(co.SW_RESTORE)
		}
		me.fullPrev = wp
		me.fullStyle, _ = h.GetWindowLongPtr(co.GWLP_STYLE)
		me.fullEx, _ = h.GetWindowLongPtr(co.GWLP_EXSTYLE)
		me.full = true
		h.SetWindowLongPtr(co.GWLP_STYLE, me.fullStyle&^frameStyles|uintptr(co.WS_POPUP))
		h.SetWindowLongPtr(co.GWLP_EXSTYLE, me.fullEx&^frameExStyles)
		me.fitFullScreen()
		slog.Info("full screen", "on", true)
	} else {
		me.full = false
		h.SetWindowLongPtr(co.GWLP_STYLE, me.fullStyle&^uintptr(co.WS_MAXIMIZE|co.WS_MINIMIZE)|uintptr(co.WS_VISIBLE))
		h.SetWindowLongPtr(co.GWLP_EXSTYLE, me.fullEx)
		wp := me.fullPrev
		wp.SetLength()
		if wp.ShowCmd != co.SW_SHOWMAXIMIZED {
			wp.ShowCmd = co.SW_SHOWNORMAL
		}
		h.SetWindowPlacement(&wp)
		h.SetWindowPos(0, win.POINT{}, win.SIZE{},
			co.SWP_NOMOVE|co.SWP_NOSIZE|co.SWP_NOZORDER|co.SWP_NOOWNERZORDER|co.SWP_FRAMECHANGED)
		setWin11Frame(h)
		themeFrame(h)
		slog.Info("full screen", "on", false)
	}
	me.layout()
	redrawAll(h)
}

// fitFullScreen makes the full screen window cover the monitor it is on
// (again, after the screen changed).
func (me *mainWindow) fitFullScreen() {
	if !me.full {
		return
	}
	h := me.wnd.Hwnd()
	rc := win.RECT{Right: win.GetSystemMetrics(co.SM_CXSCREEN), Bottom: win.GetSystemMetrics(co.SM_CYSCREEN)}
	// Before the frame went, the window's rectangle says which monitor it
	// was on; Wine without monitor support keeps the primary screen.
	if mon := h.MonitorFromWindow(co.MONITOR_DEFAULTTONEAREST); mon != 0 {
		if mi, err := mon.GetMonitorInfo(); err == nil && mi.RcMonitor.Right > mi.RcMonitor.Left {
			rc = mi.RcMonitor
		}
	}
	h.SetWindowPos(win.HWND(0), win.POINT{X: rc.Left, Y: rc.Top},
		win.SIZE{Cx: rc.Right - rc.Left, Cy: rc.Bottom - rc.Top},
		co.SWP_NOOWNERZORDER|co.SWP_FRAMECHANGED|co.SWP_NOZORDER)
}
