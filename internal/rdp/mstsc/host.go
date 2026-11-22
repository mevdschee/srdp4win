//go:build windows

package mstsc

// Hosting: atl.dll is the OLE container. A window of its class "AtlAxWin"
// creates the control named by the window text and does the whole
// IOleClientSite side.

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

var (
	atl                 = syscall.NewLazyDLL("atl.dll")
	procAtlAxWinInit    = atl.NewProc("AtlAxWinInit")
	procAtlAxGetControl = atl.NewProc("AtlAxGetControl")

	user32              = syscall.NewLazyDLL("user32.dll")
	procCreateWindowExW = user32.NewProc("CreateWindowExW")
	procDestroyWindow   = user32.NewProc("DestroyWindow")
	procMoveWindow      = user32.NewProc("MoveWindow")
	procShowWindow      = user32.NewProc("ShowWindow")
	procSetFocus        = user32.NewProc("SetFocus")
	procSetTimer        = user32.NewProc("SetTimer")
	procKillTimer       = user32.NewProc("KillTimer")

	procEnumChildWindows   = user32.NewProc("EnumChildWindows")
	procEnumThreadWindows  = user32.NewProc("EnumThreadWindows")
	procGetClassNameW      = user32.NewProc("GetClassNameW")
	procIsWindowVisible    = user32.NewProc("IsWindowVisible")
	procGetCurrentThreadId = kernel32.NewProc("GetCurrentThreadId")
)

const (
	wsChild        = 0x40000000
	wsVisible      = 0x10000000
	wsClipSiblings = 0x04000000
	wsClipChildren = 0x02000000
	swHide         = 0
)

// Available reports whether the control can be created: atl.dll loads and
// one of the control classes is registered. False under Wine, which has no
// mstscax.dll. It creates nothing.
func Available() bool {
	if atl.Load() != nil || procAtlAxWinInit.Find() != nil || procAtlAxGetControl.Find() != nil {
		return false
	}
	for _, c := range classes {
		k, err := registry.OpenKey(registry.CLASSES_ROOT, `CLSID\{`+c.clsid+`}\InprocServer32`, registry.QUERY_VALUE)
		if err == nil {
			k.Close()
			return true
		}
	}
	return false
}

var (
	hostOnce sync.Once
	hostErr  error
)

// initHost prepares the UI thread for OLE and registers the AtlAxWin class.
func initHost() error {
	hostOnce.Do(func() {
		if err := atl.Load(); err != nil {
			hostErr = err
			return
		}
		// S_FALSE when the program did it already.
		if r, _, _ := procOleInitialize.Call(0); check(r) != nil {
			hostErr = fmt.Errorf("OleInitialize: %w", check(r))
			return
		}
		if r, _, _ := procAtlAxWinInit.Call(); r == 0 {
			hostErr = errors.New("AtlAxWinInit failed")
		}
	})
	return hostErr
}

// createControl makes the host window in parent with the newest control
// class that works. It returns the window, the control's IUnknown and the
// class name.
func createControl(parent uintptr) (uintptr, *iface, string, error) {
	if err := initHost(); err != nil {
		return 0, nil, "", err
	}
	class, _ := syscall.UTF16PtrFromString("AtlAxWin")
	hinst, _, _ := procGetModuleHandleW.Call(0)
	for _, c := range classes {
		name, _ := syscall.UTF16PtrFromString("{" + c.clsid + "}")
		hwnd, _, lastErr := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(name)),
			wsChild|wsVisible|wsClipSiblings|wsClipChildren, 0, 0, 0, 0, parent, 0, hinst, 0)
		if hwnd == 0 {
			slog.Warn("rdp control: class not created", "class", c.name, "err", lastErr)
			continue
		}
		var unk *iface
		r, _, _ := procAtlAxGetControl.Call(hwnd, uintptr(unsafe.Pointer(&unk)))
		if err := check(r); err != nil || unk == nil {
			slog.Warn("rdp control: no control in the host window", "class", c.name, "err", err)
			procDestroyWindow.Call(hwnd)
			continue
		}
		return hwnd, unk, c.name, nil
	}
	return 0, nil, "", errors.New("the Windows remote desktop control (mstscax.dll) could not be created")
}

// Timers without a window: SetTimer with a callback runs on the thread's
// message loop, which is the UI thread.

var (
	timers     = map[uintptr]func(){}
	timerProc  uintptr
	timerSetup sync.Once
)

// after runs f once on the UI thread after ms milliseconds. The returned
// function cancels it.
func after(ms int, f func()) (cancel func()) {
	timerSetup.Do(func() {
		timerProc = syscall.NewCallback(func(hwnd, msg, id, tick uintptr) uintptr {
			procKillTimer.Call(0, id)
			if f := timers[id]; f != nil {
				delete(timers, id)
				defer func() {
					if r := recover(); r != nil {
						slog.Error("rdp control: timer panic", "panic", fmt.Sprint(r))
					}
				}()
				f()
			}
			return 0
		})
	})
	id, _, _ := procSetTimer.Call(0, 0, uintptr(ms), timerProc)
	if id == 0 {
		slog.Error("rdp control: SetTimer failed")
		return func() {}
	}
	timers[id] = f
	return func() {
		if timers[id] != nil {
			delete(timers, id)
			procKillTimer.Call(0, id)
		}
	}
}

// barClass is the window class of the control's own connection bar, the blue
// bar at the top of a full screen session.
const barClass = "BBarWindowClass"

// hideBarProc hides a window when it is a connection bar. One callback for
// all sessions: Go can only make a limited number of them.
var hideBarProc = sync.OnceValue(func() uintptr {
	return syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		var name [32]uint16
		n, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)))
		if syscall.UTF16ToString(name[:n]) == barClass {
			if visible, _, _ := procIsWindowVisible.Call(hwnd); visible != 0 {
				slog.Info("rdp control: connection bar hidden")
			}
			procShowWindow.Call(hwnd, swHide)
		}
		return 1 // go on
	})
})

// hideConnectionBar hides the control's connection bar: newer controls show
// it in full screen whatever DisplayConnectionBar says. It is a window of
// the UI thread, inside the control or on its own, and we never want one,
// so every one found is hidden.
func hideConnectionBar(control uintptr) {
	proc := hideBarProc()
	procEnumChildWindows.Call(control, proc, 0)
	thread, _, _ := procGetCurrentThreadId.Call()
	procEnumThreadWindows.Call(thread, proc, 0)
}
