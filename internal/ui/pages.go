//go:build windows

package ui

// Tabs: the start tab (the connection manager) first, then one per session.
// The window shows one page at a time; the others are laid out hidden.

import (
	"log/slog"

	"github.com/mevdschee/srdp4win/internal/config"
	"github.com/rodrigocfd/windigo/win"
)

// Tab context menu ids.
const (
	mnuTabClose uint16 = 2101 + iota
	mnuTabCloseOthers
	mnuTabNew
)

// rebuildTabs shows the page titles in the tab strip.
func (me *mainWindow) rebuildTabs() {
	if !me.styled {
		return
	}
	names := make([]string, len(me.pages))
	for i, p := range me.pages {
		names[i] = p.Title()
	}
	me.tabs.set(names, me.active)
}

func (me *mainWindow) pageIndex(p tabPage) int {
	for i, q := range me.pages {
		if q == p {
			return i
		}
	}
	return -1
}

// activate shows page i. The start tab ends full screen: it has no use for
// it.
func (me *mainWindow) activate(i int) {
	if i < 0 || i >= len(me.pages) {
		return
	}
	me.active = i
	if i == 0 && me.full {
		me.SetFullScreen(false) // lays out
	} else {
		me.layout()
	}
	me.rebuildTabs()
	me.updateStatus()
	me.pages[i].Activated()
}

// openSession opens a session tab for a connection.
func (me *mainWindow) openSession(c config.Connection) {
	if len(me.pages)-1 >= maxTabs {
		me.ShowError("Too many tabs", "Close a session before opening another one.")
		return
	}
	me.store.Touch(c.ID)
	if fresh, ok := me.store.Get(c.ID); ok {
		c = fresh
	}
	me.SaveStore()
	slog.Info("open session", "connection", c.DisplayName(), "ssh", c.SSHAddr(), "rdp", c.RDPTarget(), "fake", me.useFake)
	p := newSessionPage(me, c, me.useFake)
	if p == nil {
		return
	}
	me.pages = append(me.pages, p)
	me.activate(len(me.pages) - 1)
}

// closeTab closes a session tab after asking its page.
func (me *mainWindow) closeTab(i int) {
	if i <= 0 || i >= len(me.pages) || me.closing {
		return
	}
	p := me.pages[i]
	if !p.CanClose(false) {
		return
	}
	me.removePage(p)
}

// removePage takes a page out of the tabs and destroys it. Closing the
// active tab activates its neighbour; without session tabs full screen ends.
func (me *mainWindow) removePage(p tabPage) {
	i := me.pageIndex(p)
	if i <= 0 {
		return
	}
	slog.Info("tab closed", "tab", i, "title", p.Title())
	wasActive := i == me.active
	me.pages = append(me.pages[:i], me.pages[i+1:]...)
	if i < me.active {
		me.active--
	}
	p.Layout(win.RECT{}, false)
	p.Destroy()
	switch {
	case len(me.pages) == 1:
		me.activate(0)
	case wasActive:
		me.activate(min(i, len(me.pages)-1))
	default:
		me.rebuildTabs()
	}
}

// PageChanged tells the shell that p's title or status changed.
func (me *mainWindow) PageChanged(p tabPage) {
	i := me.pageIndex(p)
	if i < 0 {
		return
	}
	if me.styled && (i >= len(me.tabs.names) || me.tabs.names[i] != p.Title()) {
		me.rebuildTabs()
	}
	if i == me.active {
		me.updateStatus()
	}
}

// ClosePage closes p's tab without asking.
func (me *mainWindow) ClosePage(p tabPage) {
	if !me.closing {
		me.removePage(p)
	}
}

// TabCount is the number of session tabs.
func (me *mainWindow) TabCount() int { return len(me.pages) - 1 }

// SwitchTab moves by delta among all tabs.
func (me *mainWindow) SwitchTab(delta int) {
	n := len(me.pages)
	if n < 2 {
		return
	}
	next := ((me.active+delta)%n + n) % n
	// In full screen only the sessions take part: the start tab would end
	// the full screen.
	if me.FullScreen() && next == 0 {
		if n < 3 {
			return
		}
		next = ((next+delta)%n + n) % n
	}
	me.activate(next)
}

// tabEvents connects the tab strip: click selects, the close button and a
// middle-click close, + shows the start tab, right-click shows a menu.
func (me *mainWindow) tabEvents() {
	me.tabs.onSelect = func(i int) {
		if i != me.active {
			me.activate(i)
		}
	}
	me.tabs.onClose = me.closeTab
	me.tabs.onNew = func() { me.activate(0) }
	me.tabs.onMenu = func(i int) {
		isSession := i > 0 && i < len(me.pages)
		items := []menuItem{
			{id: mnuTabClose, text: "&Close tab\tCtrl+W", enabled: isSession},
			{id: mnuTabCloseOthers, text: "Close &other tabs", enabled: isSession && len(me.pages) > 2},
			{},
			{id: mnuTabNew, text: "&New tab\tCtrl+T", enabled: true},
		}
		switch popupMenu(me.wnd.Hwnd(), items) {
		case mnuTabClose:
			me.closeTab(i)
		case mnuTabCloseOthers:
			keep := me.pages[i]
			for j := len(me.pages) - 1; j > 0; j-- {
				if j < len(me.pages) && me.pages[j] != keep {
					me.closeTab(j)
				}
			}
		case mnuTabNew:
			me.activate(0)
		}
	}
}
