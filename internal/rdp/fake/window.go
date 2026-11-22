//go:build windows

package fake

// The window of the fake session: a registered class, a window procedure
// and GDI text.

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"syscall"
	"unsafe"
)

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procMoveWindow       = user32.NewProc("MoveWindow")
	procPostMessageW     = user32.NewProc("PostMessageW")
	procInvalidateRect   = user32.NewProc("InvalidateRect")
	procBeginPaint       = user32.NewProc("BeginPaint")
	procEndPaint         = user32.NewProc("EndPaint")
	procGetClientRect    = user32.NewProc("GetClientRect")
	procFillRect         = user32.NewProc("FillRect")
	procSetFocus         = user32.NewProc("SetFocus")
	procSetTimer         = user32.NewProc("SetTimer")
	procKillTimer        = user32.NewProc("KillTimer")
	procLoadCursorW      = user32.NewProc("LoadCursorW")

	gdi32                = syscall.NewLazyDLL("gdi32.dll")
	procCreateSolidBrush = gdi32.NewProc("CreateSolidBrush")
	procCreateFontW      = gdi32.NewProc("CreateFontW")
	procSelectObject     = gdi32.NewProc("SelectObject")
	procDeleteObject     = gdi32.NewProc("DeleteObject")
	procSetBkMode        = gdi32.NewProc("SetBkMode")
	procSetTextColor     = gdi32.NewProc("SetTextColor")
	procTextOutW         = gdi32.NewProc("TextOutW")

	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
)

const (
	wsChild        = 0x40000000
	wsVisible      = 0x10000000
	wsClipSiblings = 0x04000000

	csDblClks = 0x0008

	wmDestroy       = 0x0002
	wmPaint         = 0x000F
	wmEraseBkgnd    = 0x0014
	wmTimer         = 0x0113
	wmLButtonDown   = 0x0201
	wmLButtonDblClk = 0x0203
	wmRun           = 0x8000 + 0x51 // WM_APP + n: run the posted functions

	timerPoll  = 1
	timerClock = 2

	idcArrow    = 32512
	transparent = 1
	fwNormal    = 400
	fwSemibold  = 600

	// Colours as COLORREF (0x00BBGGRR).
	colorBack  = 0x00382A22
	colorTitle = 0x00FFFFFF
	colorText  = 0x00E0D6CE
)

type rect struct{ left, top, right, bottom int32 }

// wndClassEx is WNDCLASSEXW.
type wndClassEx struct {
	size, style            uint32
	wndProc                uintptr
	clsExtra, wndExtra     int32
	instance, icon, cursor uintptr
	background             uintptr
	menuName, className    *uint16
	iconSm                 uintptr
}

// paintStruct is PAINTSTRUCT.
type paintStruct struct {
	hdc         uintptr
	erase       int32
	rc          rect
	restore     int32
	incUpdate   int32
	rgbReserved [32]byte
}

var (
	className, _ = syscall.UTF16PtrFromString("srdp4winFakeSession")
	classOnce    sync.Once
	classErr     error

	// windows maps the window handles to their sessions (UI thread only).
	windows = map[uintptr]*session{}
)

func registerClass() error {
	classOnce.Do(func() {
		hinst, _, _ := procGetModuleHandleW.Call(0)
		cursor, _, _ := procLoadCursorW.Call(0, idcArrow)
		wc := wndClassEx{
			style:     csDblClks,
			wndProc:   syscall.NewCallback(wndProc),
			instance:  hinst,
			cursor:    cursor,
			className: className,
		}
		wc.size = uint32(unsafe.Sizeof(wc))
		if atom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
			classErr = errors.Join(errors.New("register the fake session window class"), err)
		}
	})
	return classErr
}

func wndProc(hwnd, msg, wparam, lparam uintptr) (result uintptr) {
	s := windows[hwnd]
	if s == nil {
		r, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
		return r
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Error("fake session: panic in the window procedure", "message", msg, "panic", fmt.Sprint(r))
			result = 0
		}
	}()
	switch msg {
	case wmRun:
		s.run()
		return 0
	case wmPaint:
		var ps paintStruct
		hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		if hdc != 0 {
			var rc rect
			procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
			s.paint(hdc, int(rc.right), int(rc.bottom))
			procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		}
		return 0
	case wmEraseBkgnd:
		return 1 // paint fills everything
	case wmTimer:
		switch wparam {
		case timerPoll:
			s.poll()
		case timerClock:
			s.repaint()
		}
		return 0
	case wmLButtonDown:
		procSetFocus.Call(hwnd)
		return 0
	case wmLButtonDblClk:
		s.doubleClick()
		return 0
	case wmDestroy:
		// Also when the parent is destroyed without Close.
		s.closed = true
		procKillTimer.Call(hwnd, timerPoll)
		procKillTimer.Call(hwnd, timerClock)
		delete(windows, hwnd)
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return r
}

// paint draws the panel, width x height pixels, into a device context.
func (s *session) paint(hdc uintptr, width, height int) {
	scale := s.scale
	if scale <= 0 {
		scale = 100
	}
	px := func(v int) int { return v * scale / 100 }

	rc := rect{0, 0, int32(width), int32(height)}
	brush, _, _ := procCreateSolidBrush.Call(colorBack)
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(&rc)), brush)
	procDeleteObject.Call(brush)

	face := utf16("Segoe UI")
	font := func(size, weight int) uintptr {
		f, _, _ := procCreateFontW.Call(uintptr(-px(size)), 0, 0, 0, uintptr(weight), 0, 0, 0,
			1 /* DEFAULT_CHARSET */, 0, 0, 5 /* CLEARTYPE_QUALITY */, 0, uintptr(unsafe.Pointer(&face[0])))
		return f
	}
	text := func(x, y int, t string) {
		if t == "" {
			return
		}
		u := utf16(t)
		procTextOutW.Call(hdc, uintptr(x), uintptr(y), uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1))
	}
	procSetBkMode.Call(hdc, transparent)

	x, y := px(24), px(20)
	title := font(24, fwSemibold)
	old, _, _ := procSelectObject.Call(hdc, title)
	procSetTextColor.Call(hdc, colorTitle)
	text(x, y, "Fake session")
	y += px(44)

	body := font(14, fwNormal)
	procSelectObject.Call(hdc, body)
	procSetTextColor.Call(hdc, colorText)
	for _, line := range s.lines() {
		text(x, y, line)
		y += px(22)
	}

	procSelectObject.Call(hdc, old)
	procDeleteObject.Call(title)
	procDeleteObject.Call(body)
}
