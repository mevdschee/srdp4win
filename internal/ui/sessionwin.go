//go:build windows

package ui

// Raw Win32 for the session page and the floating toolbar. Their windows are
// created while the app runs, long after the main window, so they have their
// own window class and procedure instead of windigo's control types.

import (
	"errors"
	"syscall"
	"unsafe"
)

var (
	sessUser32   = syscall.NewLazyDLL("user32.dll")
	sessGdi32    = syscall.NewLazyDLL("gdi32.dll")
	sessKernel32 = syscall.NewLazyDLL("kernel32.dll")
	sessComctl32 = syscall.NewLazyDLL("comctl32.dll")

	sessRegisterClassEx   = sessUser32.NewProc("RegisterClassExW")
	sessCreateWindowEx    = sessUser32.NewProc("CreateWindowExW")
	sessDefWindowProc     = sessUser32.NewProc("DefWindowProcW")
	sessDestroyWindowProc = sessUser32.NewProc("DestroyWindow")
	sessShowWindow        = sessUser32.NewProc("ShowWindow")
	sessSetWindowPos      = sessUser32.NewProc("SetWindowPos")
	sessSetTimer          = sessUser32.NewProc("SetTimer")
	sessKillTimer         = sessUser32.NewProc("KillTimer")
	sessInvalidateRect    = sessUser32.NewProc("InvalidateRect")
	sessBeginPaint        = sessUser32.NewProc("BeginPaint")
	sessEndPaint          = sessUser32.NewProc("EndPaint")
	sessGetClientRect     = sessUser32.NewProc("GetClientRect")
	sessGetWindowRect     = sessUser32.NewProc("GetWindowRect")
	sessGetCursorPos      = sessUser32.NewProc("GetCursorPos")
	sessScreenToClient    = sessUser32.NewProc("ScreenToClient")
	sessClientToScreen    = sessUser32.NewProc("ClientToScreen")
	sessTrackMouseEvent   = sessUser32.NewProc("TrackMouseEvent")
	sessSetCapture        = sessUser32.NewProc("SetCapture")
	sessReleaseCapture    = sessUser32.NewProc("ReleaseCapture")
	sessGetForeground     = sessUser32.NewProc("GetForegroundWindow")
	sessSetForeground     = sessUser32.NewProc("SetForegroundWindow")
	sessGetWindowThreadID = sessUser32.NewProc("GetWindowThreadProcessId")
	sessMonitorFromWindow = sessUser32.NewProc("MonitorFromWindow")
	sessGetMonitorInfo    = sessUser32.NewProc("GetMonitorInfoW")
	sessSetWindowRgn      = sessUser32.NewProc("SetWindowRgn")
	sessSendMessage       = sessUser32.NewProc("SendMessageW")
	sessPostMessage       = sessUser32.NewProc("PostMessageW")
	sessCreatePopupMenu   = sessUser32.NewProc("CreatePopupMenu")
	sessAppendMenu        = sessUser32.NewProc("AppendMenuW")
	sessTrackPopupMenu    = sessUser32.NewProc("TrackPopupMenu")
	sessDestroyMenu       = sessUser32.NewProc("DestroyMenu")
	sessOpenClipboard     = sessUser32.NewProc("OpenClipboard")
	sessEmptyClipboard    = sessUser32.NewProc("EmptyClipboard")
	sessSetClipboardData  = sessUser32.NewProc("SetClipboardData")
	sessCloseClipboard    = sessUser32.NewProc("CloseClipboard")
	sessLoadCursor        = sessUser32.NewProc("LoadCursorW")
	sessSetFocus          = sessUser32.NewProc("SetFocus")
	sessIsWindowVisible   = sessUser32.NewProc("IsWindowVisible")
	sessFillRectProc      = sessUser32.NewProc("FillRect")
	sessDrawText          = sessUser32.NewProc("DrawTextW")

	sessCreateSolidBrush   = sessGdi32.NewProc("CreateSolidBrush")
	sessDeleteObject       = sessGdi32.NewProc("DeleteObject")
	sessSelectObject       = sessGdi32.NewProc("SelectObject")
	sessCreateCompatibleDC = sessGdi32.NewProc("CreateCompatibleDC")
	sessCreateCompatibleBm = sessGdi32.NewProc("CreateCompatibleBitmap")
	sessBitBlt             = sessGdi32.NewProc("BitBlt")
	sessDeleteDC           = sessGdi32.NewProc("DeleteDC")
	sessRoundRectProc      = sessGdi32.NewProc("RoundRect")
	sessGetStockObject     = sessGdi32.NewProc("GetStockObject")
	sessSetBkMode          = sessGdi32.NewProc("SetBkMode")
	sessSetTextColor       = sessGdi32.NewProc("SetTextColor")
	sessGetTextExtent      = sessGdi32.NewProc("GetTextExtentPoint32W")
	sessCreateRectRgn      = sessGdi32.NewProc("CreateRectRgn")
	sessCreateRoundRectRgn = sessGdi32.NewProc("CreateRoundRectRgn")
	sessCombineRgn         = sessGdi32.NewProc("CombineRgn")

	sessGetModuleHandle = sessKernel32.NewProc("GetModuleHandleW")
	sessGlobalAlloc     = sessKernel32.NewProc("GlobalAlloc")
	sessGlobalLock      = sessKernel32.NewProc("GlobalLock")
	sessGlobalUnlock    = sessKernel32.NewProc("GlobalUnlock")
	sessGlobalFree      = sessKernel32.NewProc("GlobalFree")
	sessMoveMemory      = sessKernel32.NewProc("RtlMoveMemory")
	sessGetProcessID    = sessKernel32.NewProc("GetCurrentProcessId")

	sessImageListDraw    = sessComctl32.NewProc("ImageList_Draw")
	sessImageListDestroy = sessComctl32.NewProc("ImageList_Destroy")
)

// Messages, styles and flags used below.
const (
	sessWmSize          = 0x0005
	sessWmSetFocus      = 0x0007
	sessWmPaint         = 0x000F
	sessWmEraseBkgnd    = 0x0014
	sessWmMouseActivate = 0x0021
	sessWmTimer         = 0x0113
	sessWmMouseMove     = 0x0200
	sessWmLButtonDown   = 0x0201
	sessWmLButtonUp     = 0x0202
	sessWmLButtonDblClk = 0x0203
	sessWmMouseLeave    = 0x02A3
	sessWmCaptureLost   = 0x0215
	sessWmDefer         = 0x8000 + 1 // WM_APP + 1: run the next queued function

	sessWsChild        = 0x40000000
	sessWsPopup        = 0x80000000
	sessWsClipChildren = 0x02000000
	sessWsClipSiblings = 0x04000000
	sessExTopmost      = 0x00000008
	sessExToolWindow   = 0x00000080
	sessExNoActivate   = 0x08000000

	sessSwHide   = 0
	sessSwShowNA = 8

	sessSwpNoSize     = 0x0001
	sessSwpNoMove     = 0x0002
	sessSwpNoZOrder   = 0x0004
	sessSwpNoActivate = 0x0010
	sessSwpShow       = 0x0040
	sessSwpHide       = 0x0080

	sessDtCenter      = 0x0001
	sessDtVCenter     = 0x0004
	sessDtWordBreak   = 0x0010
	sessDtSingleLine  = 0x0020
	sessDtCalcRect    = 0x0400
	sessDtNoPrefix    = 0x0800
	sessDtEndEllipsis = 0x8000
)

// sessWinRect is RECT.
type sessWinRect struct{ left, top, right, bottom int32 }

func (r sessWinRect) has(x, y int32) bool {
	return x >= r.left && x < r.right && y >= r.top && y < r.bottom
}

type sessPoint struct{ x, y int32 }

// sessWndFunc handles a message; handled false passes it to DefWindowProc.
type sessWndFunc func(msg uint32, wp, lp uintptr) (result uintptr, handled bool)

// sessWindows maps our windows to their handlers. UI thread only.
var (
	sessWindows   = map[uintptr]sessWndFunc{}
	sessClassAtom uintptr
	sessClassName = syscall.StringToUTF16Ptr("srdp4winSession")
	sessWndProcCb = syscall.NewCallback(sessWndProc)
)

func sessWndProc(hwnd, msg, wp, lp uintptr) uintptr {
	if f := sessWindows[hwnd]; f != nil {
		if r, ok := f(uint32(msg), wp, lp); ok {
			return r
		}
	}
	r, _, _ := sessDefWindowProc.Call(hwnd, msg, wp, lp)
	return r
}

func sessInstance() uintptr {
	h, _, _ := sessGetModuleHandle.Call(0)
	return h
}

// sessNewWindow creates a hidden window of our class. Parent is the parent
// of a child window or the owner of a popup.
func sessNewWindow(exStyle, style uint32, parent uintptr, f sessWndFunc) (uintptr, error) {
	if sessClassAtom == 0 {
		type wndClassEx struct {
			size, style            uint32
			wndProc                uintptr
			clsExtra, wndExtra     int32
			instance, icon, cursor uintptr
			background             uintptr
			menuName, className    *uint16
			iconSm                 uintptr
		}
		arrow, _, _ := sessLoadCursor.Call(0, 32512) // IDC_ARROW
		wc := wndClassEx{wndProc: sessWndProcCb, instance: sessInstance(), cursor: arrow, className: sessClassName}
		wc.size = uint32(unsafe.Sizeof(wc))
		atom, _, err := sessRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc)))
		if atom == 0 {
			return 0, err
		}
		sessClassAtom = atom
	}
	h, _, err := sessCreateWindowEx.Call(uintptr(exStyle), uintptr(unsafe.Pointer(sessClassName)), 0, uintptr(style),
		0, 0, 0, 0, parent, 0, sessInstance(), 0)
	if h == 0 {
		return 0, err
	}
	sessWindows[h] = f
	return h, nil
}

func sessDestroyWindow(h uintptr) {
	if h == 0 {
		return
	}
	delete(sessWindows, h)
	sessDestroyWindowProc.Call(h)
}

func sessShow(h uintptr, on bool) {
	cmd := uintptr(sessSwHide)
	if on {
		cmd = sessSwShowNA
	}
	sessShowWindow.Call(h, cmd)
}

func sessVisible(h uintptr) bool {
	r, _, _ := sessIsWindowVisible.Call(h)
	return r != 0
}

func sessInvalidate(h uintptr) { sessInvalidateRect.Call(h, 0, 0) }

func sessClientSize(h uintptr) (w, height int) {
	var rc sessWinRect
	sessGetClientRect.Call(h, uintptr(unsafe.Pointer(&rc)))
	return int(rc.right), int(rc.bottom)
}

// sessCursorIn returns the mouse position in h's client coordinates.
func sessCursorIn(h uintptr) sessPoint {
	var pt sessPoint
	sessGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	sessScreenToClient.Call(h, uintptr(unsafe.Pointer(&pt)))
	return pt
}

func sessToScreen(h uintptr, x, y int32) sessPoint {
	pt := sessPoint{x, y}
	sessClientToScreen.Call(h, uintptr(unsafe.Pointer(&pt)))
	return pt
}

// sessMousePos unpacks the position of a mouse message.
func sessMousePos(lp uintptr) (x, y int32) {
	return int32(int16(uint16(lp))), int32(int16(uint16(lp >> 16)))
}

// sessTrackLeave asks for WM_MOUSELEAVE.
func sessTrackLeave(h uintptr) {
	type trackMouseEvent struct {
		size, flags uint32
		hwnd        uintptr
		hoverTime   uint32
	}
	tme := trackMouseEvent{flags: 0x2, hwnd: h} // TME_LEAVE
	tme.size = uint32(unsafe.Sizeof(tme))
	sessTrackMouseEvent.Call(uintptr(unsafe.Pointer(&tme)))
}

// sessAppActive reports whether the foreground window is one of ours. With
// no foreground window at all (between two apps, or under a bare X server)
// it says yes.
func sessAppActive() bool {
	fg, _, _ := sessGetForeground.Call()
	if fg == 0 {
		return true
	}
	var pid uint32
	sessGetWindowThreadID.Call(fg, uintptr(unsafe.Pointer(&pid)))
	own, _, _ := sessGetProcessID.Call()
	return pid == uint32(own)
}

// sessMonitorRect is the rectangle of the monitor a window is on.
func sessMonitorRect(h uintptr) (sessWinRect, bool) {
	type monitorInfo struct {
		size          uint32
		monitor, work sessWinRect
		flags         uint32
	}
	mon, _, _ := sessMonitorFromWindow.Call(h, 2) // MONITOR_DEFAULTTONEAREST
	mi := monitorInfo{}
	mi.size = uint32(unsafe.Sizeof(mi))
	if r, _, _ := sessGetMonitorInfo.Call(mon, uintptr(unsafe.Pointer(&mi))); mon == 0 || r == 0 {
		return sessWinRect{}, false
	}
	return mi.monitor, true
}

// Drawing.

func sessFill(hdc uintptr, rc sessWinRect, color uint32) {
	brush, _, _ := sessCreateSolidBrush.Call(uintptr(color))
	sessFillRectProc.Call(hdc, uintptr(unsafe.Pointer(&rc)), brush)
	sessDeleteObject.Call(brush)
}

// sessRound fills a rounded rectangle without outline.
func sessRound(hdc uintptr, rc sessWinRect, radius int32, color uint32) {
	brush, _, _ := sessCreateSolidBrush.Call(uintptr(color))
	nullPen, _, _ := sessGetStockObject.Call(8) // NULL_PEN
	oldB, _, _ := sessSelectObject.Call(hdc, brush)
	oldP, _, _ := sessSelectObject.Call(hdc, nullPen)
	sessRoundRectProc.Call(hdc, uintptr(rc.left), uintptr(rc.top), uintptr(rc.right+1), uintptr(rc.bottom+1), uintptr(2*radius), uintptr(2*radius))
	sessSelectObject.Call(hdc, oldB)
	sessSelectObject.Call(hdc, oldP)
	sessDeleteObject.Call(brush)
}

// sessText draws text in rc with the DT_ flags and returns its height.
func sessText(hdc uintptr, s string, rc *sessWinRect, color uint32, flags uint32) int32 {
	if s == "" {
		return 0
	}
	p, _ := syscall.UTF16FromString(s)
	sessSetBkMode.Call(hdc, 1) // TRANSPARENT
	sessSetTextColor.Call(hdc, uintptr(color))
	h, _, _ := sessDrawText.Call(hdc, uintptr(unsafe.Pointer(&p[0])), ^uintptr(0), uintptr(unsafe.Pointer(rc)), uintptr(flags|sessDtNoPrefix))
	return int32(h)
}

// sessTextWidth measures one line in the font selected into hdc.
func sessTextWidth(hdc uintptr, s string) int32 {
	if s == "" {
		return 0
	}
	p, _ := syscall.UTF16FromString(s)
	var sz struct{ cx, cy int32 }
	sessGetTextExtent.Call(hdc, uintptr(unsafe.Pointer(&p[0])), uintptr(len(p)-1), uintptr(unsafe.Pointer(&sz)))
	return sz.cx
}

// sessPaintStruct is PAINTSTRUCT.
type sessPaintStruct struct {
	hdc                uintptr
	erase              int32
	rc                 sessWinRect
	restore, incUpdate int32
	reserved           [32]byte
}

// sessBuffered paints the w x h area at x, y of hdc through a memory
// bitmap: draw gets a DC whose origin is the area's top left corner.
func sessBuffered(hdc uintptr, x, y, w, h int32, draw func(mem uintptr)) {
	if w <= 0 || h <= 0 {
		return
	}
	mem, _, _ := sessCreateCompatibleDC.Call(hdc)
	bmp, _, _ := sessCreateCompatibleBm.Call(hdc, uintptr(w), uintptr(h))
	if mem == 0 || bmp == 0 {
		return
	}
	old, _, _ := sessSelectObject.Call(mem, bmp)
	draw(mem)
	sessBitBlt.Call(hdc, uintptr(x), uintptr(y), uintptr(w), uintptr(h), mem, 0, 0, 0x00CC0020) // SRCCOPY
	sessSelectObject.Call(mem, old)
	sessDeleteObject.Call(bmp)
	sessDeleteDC.Call(mem)
}

// Menus.

type sessMenuItem struct {
	id      int // 0 with an empty text is a separator
	text    string
	checked bool
	grayed  bool
}

// sessMenu shows a popup menu at a screen position and returns the chosen
// id, 0 when dismissed. The owner must be the active top-level window.
func sessMenu(owner uintptr, items []sessMenuItem, x, y int32) int {
	menu, _, _ := sessCreatePopupMenu.Call()
	if menu == 0 {
		return 0
	}
	defer sessDestroyMenu.Call(menu)
	for _, it := range items {
		if it.text == "" {
			sessAppendMenu.Call(menu, 0x0800, 0, 0) // MF_SEPARATOR
			continue
		}
		flags := uintptr(0) // MF_STRING
		if it.checked {
			flags |= 0x0008 // MF_CHECKED
		}
		if it.grayed {
			flags |= 0x0001 // MF_GRAYED
		}
		text, _ := syscall.UTF16PtrFromString(it.text)
		sessAppendMenu.Call(menu, flags, uintptr(it.id), uintptr(unsafe.Pointer(text)))
	}
	if fg, _, _ := sessGetForeground.Call(); fg != owner {
		sessSetForeground.Call(owner)
	}
	const flags = 0x0100 | 0x0002 // TPM_RETURNCMD | TPM_RIGHTBUTTON
	id, _, _ := sessTrackPopupMenu.Call(menu, flags, uintptr(x), uintptr(y), 0, owner, 0)
	sessPostMessage.Call(owner, 0, 0, 0) // WM_NULL, as the documentation asks
	return int(id)
}

// Tooltips.

// sessToolInfo is TOOLINFOW.
type sessToolInfo struct {
	size, flags uint32
	hwnd, id    uintptr
	rect        sessWinRect
	instance    uintptr
	text        *uint16
	lParam      uintptr
	reserved    uintptr
}

const (
	sessTtmAddTool = 0x0400 + 50 // TTM_ADDTOOLW
	sessTtmDelTool = 0x0400 + 51 // TTM_DELTOOLW
)

// sessNewTip creates a tooltip window for tools inside owner. It shows also
// while owner is not the active window (the floating toolbar never is).
func sessNewTip(owner uintptr) uintptr {
	class, _ := syscall.UTF16PtrFromString("tooltips_class32")
	const style = sessWsPopup | 0x01 | 0x02 // TTS_ALWAYSTIP | TTS_NOPREFIX
	const useDefault = 0x80000000           // CW_USEDEFAULT
	h, _, _ := sessCreateWindowEx.Call(sessExTopmost, uintptr(unsafe.Pointer(class)), 0, style,
		useDefault, useDefault, useDefault, useDefault, owner, 0, sessInstance(), 0)
	return h
}

// sessTip is one tooltip area.
type sessTip struct {
	rc   sessWinRect
	text []uint16 // kept alive by the caller while the tool exists
}

// sessSetTips replaces the old tools (ids 1..old) of hwnd by tips.
func sessSetTips(tip, hwnd uintptr, old int, tips []sessTip) {
	if tip == 0 {
		return
	}
	for i := 1; i <= old; i++ {
		ti := sessToolInfo{hwnd: hwnd, id: uintptr(i)}
		ti.size = uint32(unsafe.Sizeof(ti))
		sessSendMessage.Call(tip, sessTtmDelTool, 0, uintptr(unsafe.Pointer(&ti)))
	}
	for i, t := range tips {
		ti := sessToolInfo{flags: 0x10, hwnd: hwnd, id: uintptr(i + 1), rect: t.rc, text: &t.text[0]} // TTF_SUBCLASS
		ti.size = uint32(unsafe.Sizeof(ti))
		sessSendMessage.Call(tip, sessTtmAddTool, 0, uintptr(unsafe.Pointer(&ti)))
	}
}

// Clipboard.

// sessCopyDIB puts a packed DIB on the clipboard as CF_DIB.
func sessCopyDIB(owner uintptr, dib []byte) error {
	if len(dib) == 0 {
		return errors.New("empty picture")
	}
	mem, _, err := sessGlobalAlloc.Call(0x0002, uintptr(len(dib))) // GMEM_MOVEABLE
	if mem == 0 {
		return err
	}
	ptr, _, err := sessGlobalLock.Call(mem)
	if ptr == 0 {
		sessGlobalFree.Call(mem)
		return err
	}
	sessMoveMemory.Call(ptr, uintptr(unsafe.Pointer(&dib[0])), uintptr(len(dib)))
	sessGlobalUnlock.Call(mem)
	if r, _, err := sessOpenClipboard.Call(owner); r == 0 {
		sessGlobalFree.Call(mem)
		return err
	}
	defer sessCloseClipboard.Call()
	sessEmptyClipboard.Call()
	if r, _, err := sessSetClipboardData.Call(8, mem); r == 0 { // CF_DIB
		sessGlobalFree.Call(mem)
		return err
	}
	return nil // the clipboard owns the memory now
}
