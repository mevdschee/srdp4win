//go:build windows

package ui

// Keyboard shortcuts of the chrome. They are not an accelerator table: that
// would also take the keys while the focus is in a session, where every key
// belongs to the remote desktop. A keyboard hook on the UI thread sees each
// key first and only acts while the focus is in the tab strip or the start
// tab.

import (
	"log/slog"
	"syscall"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/win"
)

var (
	procSetWindowsHookExW   = user32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx = user32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx      = user32.NewProc("CallNextHookEx")
	procGetKeyState         = user32.NewProc("GetKeyState")

	keyHookWindow *mainWindow // the window the hook works for
	keyHookCb     = syscall.NewCallback(keyHookProc)
)

const whKeyboard = 2

func (me *mainWindow) installKeyHook() {
	keyHookWindow = me
	h, _, err := procSetWindowsHookExW.Call(whKeyboard, keyHookCb, 0, uintptr(win.GetCurrentThreadId()))
	if h == 0 {
		slog.Error("keyboard hook", "err", err)
	}
	me.keyHook = h
}

func (me *mainWindow) removeKeyHook() {
	if me.keyHook != 0 {
		procUnhookWindowsHookEx.Call(me.keyHook)
		me.keyHook = 0
	}
	keyHookWindow = nil
}

// keyHookProc is the WH_KEYBOARD hook: wParam is the virtual key, bit 31 of
// lParam is set for a key release. Returning 1 swallows the key.
func keyHookProc(code, wParam, lParam uintptr) uintptr {
	me := keyHookWindow
	if me != nil && int32(code) == 0 && lParam&(1<<31) == 0 { // HC_ACTION, key down
		if me.handleKey(co.VK(wParam), lParam) {
			return 1
		}
	}
	r, _, _ := procCallNextHookEx.Call(0, code, wParam, lParam)
	return r
}

func keyDown(vk co.VK) bool {
	r, _, _ := procGetKeyState.Call(uintptr(vk))
	return r&0x8000 != 0
}

// chromeFocus reports whether the keyboard focus is in the chrome or the
// start tab, not in a session.
func (me *mainWindow) chromeFocus(focus win.HWND) bool {
	if me.active == 0 || focus == 0 {
		return true
	}
	return focus == me.wnd.Hwnd() || focus == me.tabs.ctl.Hwnd() || me.start.owns(focus)
}

// printable reports whether a key types a character.
func printable(vk co.VK) bool {
	switch {
	case vk >= '0' && vk <= '9', vk >= 'A' && vk <= 'Z':
		return true
	case vk >= co.VK_NUMPAD0 && vk <= co.VK_NUMPAD9:
		return true
	case vk >= 0xBA && vk <= 0xC0, vk >= 0xDB && vk <= 0xDF: // VK_OEM_*
		return true
	}
	return false
}

// handleKey acts on a key press; true when the key was a shortcut. The
// action runs after the hook returned (it may open a dialog).
func (me *mainWindow) handleKey(vk co.VK, lParam uintptr) bool {
	if !me.styled || me.closing || me.menuDepth > 0 {
		return false
	}
	if active, _, _ := procGetActiveWindow.Call(); win.HWND(active) != me.wnd.Hwnd() {
		return false // a dialog is open
	}
	focus := win.GetFocus()
	if !me.chromeFocus(focus) {
		return false
	}
	ctrl, shift, alt := keyDown(co.VK_CONTROL), keyDown(co.VK_SHIFT), keyDown(co.VK_MENU)
	if alt {
		return false
	}
	plain := !ctrl && !shift
	do := func(f func()) bool {
		me.Post(f)
		return true
	}
	switch {
	case ctrl && !shift && vk == 'T':
		return do(func() { me.activate(0) })
	case ctrl && !shift && vk == 'W':
		return do(func() { me.closeTab(me.active) })
	case ctrl && vk == co.VK_TAB:
		delta := 1
		if shift {
			delta = -1
		}
		return do(func() { me.SwitchTab(delta) })
	case plain && vk == co.VK_F11:
		return do(func() { me.pages[me.active].Command(cmdFullScreen) })
	}
	if me.active != 0 {
		return false
	}
	s := me.start
	inSearch, inList := focus == s.search.Hwnd(), focus == s.list.Hwnd()
	// repost sends the key again, to the control that has the focus now.
	repost := func(to win.HWND) {
		to.SetFocus()
		to.PostMessage(co.WM_KEYDOWN, win.WPARAM(vk), win.LPARAM(lParam))
	}
	switch {
	case ctrl && !shift && vk == 'F':
		return do(s.focusSearch)
	case ctrl && !shift && vk == 'N', plain && vk == co.VK_INSERT && !inSearch:
		return do(s.newConnection)
	case plain && vk == co.VK_F2:
		return do(s.edit)
	case plain && vk == co.VK_DELETE && inList:
		return do(s.delete)
	case plain && vk == co.VK_RETURN && (inSearch || inList):
		return do(s.connect)
	case plain && vk == co.VK_ESCAPE && inSearch:
		return do(func() {
			s.search.SetText("")
			s.focusList()
		})
	case plain && inSearch && (vk == co.VK_DOWN || vk == co.VK_UP) && shown(s.list.Hwnd()):
		repost(s.list.Hwnd())
		return true
	case !ctrl && inList && printable(vk):
		// Typing in the list searches: the character goes to the end of
		// the search box.
		s.search.SetSelection(1<<15, 1<<15)
		repost(s.search.Hwnd())
		return true
	}
	return false
}
