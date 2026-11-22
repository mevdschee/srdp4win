//go:build windows

package ui

// The contract between the two halves of this package: the shell (main
// window, tab strip, connection manager, dialogs) and the session page (one
// remote desktop tab with its toolbar, the floating full screen toolbar and
// the connect flow). Change it only in agreement with both sides.

import (
	"github.com/mevdschee/srdp4win/internal/config"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"
)

// tabPage is the content of one tab. All methods are called on the UI thread.
type tabPage interface {
	// Title is the tab's text.
	Title() string
	// Layout places the page in r (shell client coordinates, physical
	// pixels) and shows it, or hides it when visible is false. In full
	// screen r is the whole window.
	Layout(r win.RECT, visible bool)
	// Activated is called when the page becomes the visible tab (give the
	// keyboard focus to the right child).
	Activated()
	// Status is the text for the status line: state on the left, details on
	// the right.
	Status() (left, right string)
	// CanClose asks the user when closing would end a session; false keeps
	// the tab. Quiet skips the question (the shell asked already).
	CanClose(quiet bool) bool
	// Busy reports a connected or connecting session (the shell asks once
	// before closing the window with busy pages).
	Busy() bool
	// Destroy releases everything. The page is not used afterwards.
	Destroy()
	// ThemeChanged is called after the light/dark palette changed.
	ThemeChanged()
	// Command handles an accelerator the shell saw (cmdFullScreen ...);
	// false when the page has no use for it.
	Command(id int) bool
}

// Commands passed to tabPage.Command.
const (
	cmdFullScreen = iota + 1 // toggle full screen (F11)
	cmdDisconnect
	cmdScreenshot
)

// shell is what a page may ask of the main window. All methods must be
// called on the UI thread, except Post.
type shell interface {
	// Parent is the main window, the parent of every page's windows.
	Parent() ui.Parent
	Hwnd() win.HWND
	// Post runs f on the UI thread, from any goroutine.
	Post(f func())
	// PageChanged tells the shell that p's title or status changed.
	PageChanged(p tabPage)
	// ClosePage closes p's tab without asking.
	ClosePage(p tabPage)
	// TabCount is the number of session tabs; SwitchTab moves by delta
	// among all tabs.
	TabCount() int
	SwitchTab(delta int)
	// SetFullScreen makes the window cover its monitor without frame, tab
	// strip and status line (the active page gets the whole window), or
	// restores it. FullScreen reports the state.
	SetFullScreen(on bool)
	FullScreen() bool
	// Minimize minimizes the main window.
	Minimize()
	// Dpi is the window's DPI (96 is 100%).
	Dpi() int
	// Prefs returns the preferences to read and change; SavePrefs writes
	// them.
	Prefs() *config.Preferences
	SavePrefs()
	// Store returns the connections; SaveStore writes them (for a password
	// the user chose to save).
	Store() *config.Store
	SaveStore()
	// AskPassword asks for a password in a modal dialog. With offerSave it
	// has a "Save password" box. ok is false when cancelled.
	AskPassword(title, prompt string, offerSave bool) (password string, save, ok bool)
	// AskTrustHost shows an unknown SSH host key fingerprint; true trusts it.
	AskTrustHost(hostport, keyType, fingerprint string) bool
	// ShowError shows a modal error.
	ShowError(title, text string)
	// Confirm asks a yes/no question.
	Confirm(title, text string) bool
}

// The session half provides:
//
//	func newSessionPage(sh shell, c config.Connection, useFake bool) tabPage
//
// which creates the page and starts connecting at once. The shell half
// provides Run, the shell and the start tab (the connection manager, itself
// a tabPage).
//
// Shared helpers keep the names they have in ssh-cloud: the palette and
// theme functions in theme.go, the icon and toolbar helpers in icons.go and
// fluent.go, the modal helpers in dialogs.go (newModal, label, okCancel,
// showInput, focusOnOpen, closeModal). The shell half owns those files; the
// session half only calls them.
