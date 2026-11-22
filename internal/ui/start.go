//go:build windows

package ui

// The start tab: the connection manager. A toolbar (Connect, New, Edit,
// Copy, Delete and the "..." menu), a search box and the list of saved
// connections. Its controls are children of the main window, placed by
// Layout; the main window paints the toolbar row and the search box frame.

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/mevdschee/srdp4win/internal/config"
	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"
)

// Toolbar button ids of the start tab.
const (
	tbConnConnect uint16 = 3001 + iota
	tbConnNew
	tbConnEdit
	tbConnCopy
	tbConnDelete
	tbConnMore // "..." with the overflow menu
)

// Menu ids of the start tab.
const (
	mnuConnect uint16 = 2001 + iota
	mnuEdit
	mnuCopy
	mnuDelete
	mnuNew
	mnuSettings
	mnuOpenLog
	mnuCheckUpdates
	mnuAbout
)

const emSetCueBanner = 0x1501

type startPage struct {
	me     *mainWindow
	bar    *ui.Toolbar
	search *ui.Edit
	list   *ui.ListView
	hint   *ui.Static // shown instead of the list when there are no connections

	visible     bool
	rect        win.RECT
	rowBottom   int      // y of the divider under the toolbar row
	searchBox   win.RECT // the frame around the search box
	searchFocus bool
	searchIcon  uintptr // image list with the magnifier, 0 without
	rowIcons    uintptr // image list with the list's icon, 0 without

	sortCol  int
	sortDesc bool
	shown    int // rows in the list
}

func newStartPage(me *mainWindow) *startPage {
	s := &startPage{me: me}
	s.bar = newToolbar(me.wnd, func() uint32 { return colors.nav })
	// The search box is a borderless edit centred in a frame drawn by paint().
	s.search = ui.NewEdit(me.wnd, ui.OptsEdit().Position(ui.Dpi(600, 52)).Width(ui.DpiX(220)).
		WndExStyle(co.WS_EX_LEFT))
	s.list = ui.NewListView(me.wnd, ui.OptsListView().
		Position(ui.Dpi(0, 96)).
		Size(ui.Dpi(1000, 530)).
		CtrlStyle(co.LVS_REPORT|co.LVS_SHOWSELALWAYS|co.LVS_SINGLESEL).
		CtrlExStyle(co.LVS_EX_FULLROWSELECT|co.LVS_EX_DOUBLEBUFFER).
		WndExStyle(co.WS_EX_LEFT).
		Column("Name", ui.DpiX(240)).
		Column("Group", ui.DpiX(130)).
		Column("SSH host", ui.DpiX(220)).
		Column("RDP host", ui.DpiX(180)).
		Column("Last used", ui.DpiX(140)))
	s.hint = ui.NewStatic(me.wnd, ui.OptsStatic().
		Text("No connections yet.\n\nChoose New to add one: an SSH host to log in to and the RDP host to reach from there.").
		Position(ui.Dpi(0, 160)).Size(ui.Dpi(1000, 80)).CtrlStyle(co.SS_CENTER|co.SS_NOPREFIX).
		WndStyle(co.WS_CHILD))
	return s
}

// style fills the toolbar and themes the list; called in WM_CREATE.
func (s *startPage) style() {
	size := iconSize(s.me.wnd.Hwnd())
	fillToolbar(s.bar, []tbButton{
		{id: tbConnConnect, icon: pickIcon("plug_connected", "desktop", "arrow_right", "add"), text: "Connect", showText: true},
		{},
		{id: tbConnNew, icon: "add", text: "New", showText: true},
		{id: tbConnEdit, icon: pickIcon("edit", "rename", "add"), text: "Edit (F2)", width: ui.DpiX(40)},
		{id: tbConnCopy, icon: pickIcon("copy", "add"), text: "Copy", width: ui.DpiX(40)},
		{id: tbConnDelete, icon: pickIcon("delete", "dismiss_16"), text: "Delete (Del)", width: ui.DpiX(40)},
		{},
		{id: tbConnMore, icon: pickIcon("more_horizontal", "add"), text: "See more", width: ui.DpiX(40)},
	}, size, ui.DpiX(20), ui.DpiY(14), ui.DpiX(8))
	cue, _ := syscall.UTF16PtrFromString("Search")
	s.search.Hwnd().SendMessage(emSetCueBanner, 1, win.LPARAM(unsafe.Pointer(cue)))
	s.themeIcons()
	themeListView(s.list.Hwnd())
	s.updateSortArrows()
	s.fill()
}

// themeIcons renders the search and list icons in the current palette.
func (s *startPage) themeIcons() {
	small := max(16, 16*s.me.Dpi()/96)
	if s.searchIcon != 0 {
		win.HIMAGELIST(s.searchIcon).Destroy()
		s.searchIcon = 0
	}
	if hasIcon("search") {
		s.searchIcon = uintptr(fluentImageList([]string{"search"}, small, colors.iconDisabled))
	}
	if name := pickIcon("desktop", ""); name != "" {
		// The list destroys its last image list itself, a replaced one
		// is destroyed here.
		icons := uintptr(fluentImageList([]string{name}, small, colors.icon))
		s.list.Hwnd().SendMessage(co.LVM_SETIMAGELIST, win.WPARAM(co.LVSIL_SMALL), win.LPARAM(icons))
		if s.rowIcons != 0 {
			win.HIMAGELIST(s.rowIcons).Destroy()
		}
		s.rowIcons = icons
	}
}

func (s *startPage) events() {
	on := s.me.wnd.On().WmCommandAccelMenu
	on(tbConnConnect, s.connect)
	on(tbConnNew, s.newConnection)
	on(tbConnEdit, s.edit)
	on(tbConnCopy, s.copy)
	on(tbConnDelete, s.delete)
	on(tbConnMore, s.moreMenu)

	invalidateBox := func() {
		b := s.searchBox
		procInvalidateRect.Call(uintptr(s.me.wnd.Hwnd()), uintptr(unsafe.Pointer(&b)), 1)
	}
	s.search.On().EnSetFocus(func() { s.searchFocus = true; invalidateBox() })
	s.search.On().EnKillFocus(func() { s.searchFocus = false; invalidateBox() })
	s.search.On().EnChange(func() {
		if s.me.styled {
			s.fill()
		}
	})

	s.list.On().LvnItemActivate(func(_ *win.NMITEMACTIVATE) { s.connect() }) // double-click
	s.list.On().LvnItemChanged(func(_ *win.NMLISTVIEW) { s.selectionChanged() })
	s.list.On().LvnColumnClick(func(p *win.NMLISTVIEW) { s.sortBy(int(p.ISubItem)) })
	s.list.On().NmRClick(func(_ *win.NMITEMACTIVATE) { s.contextMenu() })
}

// owns reports whether a window is one of the start tab's controls.
func (s *startPage) owns(h win.HWND) bool {
	return h == s.bar.Hwnd() || h == s.search.Hwnd() || h == s.list.Hwnd() || h == s.hint.Hwnd()
}

// --- tabPage ---

func (s *startPage) Title() string { return "Connections" }

func (s *startPage) Layout(r win.RECT, visible bool) {
	s.visible, s.rect = visible, r
	if !visible {
		for _, h := range []win.HWND{s.bar.Hwnd(), s.search.Hwnd(), s.list.Hwnd(), s.hint.Hwnd()} {
			show(h, false)
		}
		return
	}
	x, y, w := int(r.Left), int(r.Top), int(r.Right-r.Left)
	m := ui.DpiX(8)
	bar := toolbarSize(s.bar)
	rowY, rowH := y+ui.DpiY(6), max(int(bar.Cy), ui.DpiY(40))
	// The search box at the right; the edit inside is one text line high
	// and centred, with room for the magnifier before it.
	boxW, boxH := min(ui.DpiX(280), max(ui.DpiX(120), w-int(bar.Cx)-3*m)), ui.DpiY(32)
	boxX, boxY := x+w-ui.DpiX(12)-boxW, rowY+(rowH-boxH)/2
	s.searchBox = win.RECT{Left: int32(boxX), Top: int32(boxY), Right: int32(boxX + boxW), Bottom: int32(boxY + boxH)}
	move(s.bar.Hwnd(), x+m, rowY+(rowH-int(bar.Cy))/2, min(int(bar.Cx), boxX-x-2*m), int(bar.Cy))
	editH := fontHeight(s.me.fontNormal) + 2
	pad := ui.DpiX(10)
	if s.searchIcon != 0 {
		pad = ui.DpiX(14) + max(16, 16*s.me.Dpi()/96)
	}
	move(s.search.Hwnd(), boxX+pad, boxY+(boxH-editH)/2, boxW-pad-ui.DpiX(10), editH)
	s.rowBottom = rowY + rowH + ui.DpiY(6)

	top := s.rowBottom + 1
	h := int(r.Bottom) - top
	move(s.list.Hwnd(), x, top, w, h)
	s.fitColumns(w)
	move(s.hint.Hwnd(), x+ui.DpiX(40), top+min(ui.DpiY(80), h/3), w-ui.DpiX(80), max(0, min(ui.DpiY(120), h-h/3)))
	show(s.bar.Hwnd(), true)
	show(s.search.Hwnd(), true)
	s.showList()
}

// showList shows the list, or the hint when there is nothing to list.
func (s *startPage) showList() {
	if !s.visible {
		return
	}
	empty := len(s.me.store.Connections) == 0
	show(s.list.Hwnd(), !empty)
	show(s.hint.Hwnd(), empty)
}

func (s *startPage) Activated() {
	if shown(s.list.Hwnd()) {
		s.focusList()
	} else {
		s.search.Hwnd().SetFocus()
	}
}

func (s *startPage) Status() (string, string) {
	total := len(s.me.store.Connections)
	left := fmt.Sprintf("%d connections", total)
	switch {
	case total == 1:
		left = "1 connection"
	case s.shown != total:
		left = fmt.Sprintf("%d of %d connections", s.shown, total)
	}
	right := ""
	if c, ok := s.selected(); ok {
		right = userAt(c.SSHUser, c.SSHAddr()) + " > " + c.RDPTarget()
	}
	return left, right
}

func (s *startPage) CanClose(quiet bool) bool { return false }
func (s *startPage) Busy() bool               { return false }
func (s *startPage) Destroy()                 {}
func (s *startPage) Command(id int) bool      { return false }

func (s *startPage) ThemeChanged() {
	setToolbarIcons(s.bar)
	s.themeIcons()
	themeListView(s.list.Hwnd())
}

// paint draws the toolbar row, the divider under it and the search box: a
// 1px border with rounded corners, filled inside, and an accent underline
// while the search box has the focus.
func (s *startPage) paint(hdc uintptr) {
	if !s.visible {
		return
	}
	r := s.rect
	fillRect(hdc, win.RECT{Left: r.Left, Top: r.Top, Right: r.Right, Bottom: int32(s.rowBottom)}, colors.nav)
	fillRect(hdc, win.RECT{Left: r.Left, Top: int32(s.rowBottom), Right: r.Right, Bottom: int32(s.rowBottom) + 1}, colors.divider)
	if b := s.searchBox; b.Right > b.Left {
		radius := int32(ui.DpiX(4))
		roundRect(hdc, win.RECT{Left: b.Left, Top: b.Top, Right: b.Right - 1, Bottom: b.Bottom - 1}, radius, colors.addressBorder)
		roundRect(hdc, win.RECT{Left: b.Left + 1, Top: b.Top + 1, Right: b.Right - 2, Bottom: b.Bottom - 2}, radius-1, colors.addressFill)
		if s.searchFocus {
			fillRect(hdc, win.RECT{Left: b.Left + radius, Top: b.Bottom - 2, Right: b.Right - radius, Bottom: b.Bottom}, colors.accent)
		}
		if s.searchIcon != 0 {
			size := int32(max(16, 16*s.me.Dpi()/96))
			procImageListDraw.Call(s.searchIcon, 0, hdc, uintptr(b.Left+int32(ui.DpiX(9))), uintptr(b.Top+(b.Bottom-b.Top-size)/2), 1) // ILD_TRANSPARENT
		}
	}
}

// fitColumns sizes the columns like Explorer: the others keep their width
// and Name takes the rest, so there is no horizontal scrollbar until the
// list gets really narrow.
func (s *startPage) fitColumns(listW int) {
	fixed := []int{ui.DpiX(130), ui.DpiX(220), ui.DpiX(180), ui.DpiX(140)}
	rest := listW - ui.DpiX(22) // room for the vertical scrollbar
	for i, w := range fixed {
		s.list.Col(i + 1).SetWidth(w)
		rest -= w
	}
	s.list.Col(0).SetWidth(max(rest, ui.DpiX(180)))
}

// --- the list ---

// userAt is "user@host", or the host alone without a user.
func userAt(user, host string) string {
	if user == "" {
		return host
	}
	return user + "@" + host
}

func lastUsedText(t time.Time) string {
	if t.IsZero() {
		return "Never"
	}
	return t.Local().Format("2006-01-02 15:04")
}

// matches reports whether a connection has every word of the search text in
// its name, group, SSH host or RDP host.
func matches(c config.Connection, words []string) bool {
	hay := strings.ToLower(strings.Join([]string{c.DisplayName(), c.Group, c.SSHUser, c.SSHHost, c.RDPHost}, "\n"))
	for _, w := range words {
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

// fill shows the connections that match the search box, sorted, and keeps
// the selection.
func (s *startPage) fill() {
	if s.list.Hwnd() == 0 {
		return
	}
	keep := ""
	if c, ok := s.selected(); ok {
		keep = c.ID
	}
	s.fillSelect(keep)
}

// fillSelect is fill with the connection to select; without it the first
// row is selected, so Enter connects to the best match.
func (s *startPage) fillSelect(id string) {
	words := strings.Fields(strings.ToLower(s.search.Text()))
	var rows []config.Connection
	for _, c := range s.me.store.Connections {
		if matches(c, words) {
			rows = append(rows, c)
		}
	}
	slices.SortStableFunc(rows, func(a, b config.Connection) int {
		r := 0
		switch s.sortCol {
		case 1:
			r = cmp.Compare(strings.ToLower(a.Group), strings.ToLower(b.Group))
		case 2:
			r = cmp.Compare(strings.ToLower(a.SSHHost), strings.ToLower(b.SSHHost))
		case 3:
			r = cmp.Compare(strings.ToLower(a.RDPHost), strings.ToLower(b.RDPHost))
		case 4:
			r = a.LastUsed.Compare(b.LastUsed)
		}
		if r == 0 {
			r = cmp.Compare(strings.ToLower(a.DisplayName()), strings.ToLower(b.DisplayName()))
			if s.sortCol != 0 {
				return r // the name always ascends as the second key
			}
		}
		if s.sortDesc {
			r = -r
		}
		return r
	})

	s.list.SetRedraw(false)
	s.list.DeleteAllItems()
	selected := false
	for _, c := range rows {
		item := s.list.AddItem(c.DisplayName(), c.Group, userAt(c.SSHUser, c.SSHAddr()), c.RDPTarget(), lastUsedText(c.LastUsed))
		item.SetData(c.ID)
		if c.ID == id && id != "" {
			item.Select(true).Focus()
			selected = true
		}
	}
	if !selected && len(rows) > 0 {
		s.list.Item(0).Select(true).Focus()
	}
	s.list.SetRedraw(true)
	// Not windigo's EnsureVisible: it needs a message Wine does not have.
	if items := s.list.SelectedItems(); len(items) == 1 {
		s.list.Hwnd().SendMessage(co.LVM_ENSUREVISIBLE, win.WPARAM(items[0].Index()), 0)
	}
	s.shown = len(rows)
	s.showList()
	s.selectionChanged()
}

// selected returns the selected connection.
func (s *startPage) selected() (config.Connection, bool) {
	if s.list.Hwnd() == 0 {
		return config.Connection{}, false
	}
	items := s.list.SelectedItems()
	if len(items) != 1 {
		return config.Connection{}, false
	}
	id, _ := items[0].Data().(string)
	return s.me.store.Get(id)
}

// selectionChanged enables the buttons that need a connection and updates
// the status line.
func (s *startPage) selectionChanged() {
	_, ok := s.selected()
	for _, id := range []uint16{tbConnConnect, tbConnEdit, tbConnCopy, tbConnDelete} {
		enableButton(s.bar, id, ok)
	}
	s.me.PageChanged(s)
}

// sortBy sorts by a column; clicking the same column again reverses.
func (s *startPage) sortBy(col int) {
	if col == s.sortCol {
		s.sortDesc = !s.sortDesc
	} else {
		s.sortCol, s.sortDesc = col, col == 4 // last used: newest first
	}
	s.updateSortArrows()
	s.fill()
}

func (s *startPage) updateSortArrows() {
	for i := 0; i < s.list.ColCount(); i++ {
		arrow := co.HDF_NONE
		if i == s.sortCol {
			arrow = co.HDF_SORTUP
			if s.sortDesc {
				arrow = co.HDF_SORTDOWN
			}
		}
		s.list.Col(i).SetSortArrow(arrow)
	}
}

func (s *startPage) focusList() {
	if shown(s.list.Hwnd()) {
		s.list.Hwnd().SetFocus()
	}
}

func (s *startPage) focusSearch() {
	s.search.Hwnd().SetFocus()
	s.search.SetSelection(0, -1)
}

// --- actions ---

// connect opens a session tab for the selected connection.
func (s *startPage) connect() {
	if c, ok := s.selected(); ok {
		s.me.openSession(c)
	}
}

// save stores an edited connection, writes the file and selects it.
func (s *startPage) save(c config.Connection) {
	c = s.me.store.Put(c)
	s.me.SaveStore()
	s.fillSelect(c.ID)
	s.focusList()
}

func (s *startPage) newConnection() {
	c := config.NewConnection(s.me.prefs.Defaults)
	if c, ok := editConnection(s.me.wnd, "New connection", c); ok {
		// A new connection may not match the search text.
		if !matches(c, strings.Fields(strings.ToLower(s.search.Text()))) {
			s.search.SetText("")
		}
		s.save(c)
	}
}

func (s *startPage) edit() {
	c, ok := s.selected()
	if !ok {
		return
	}
	if c, ok = editConnection(s.me.wnd, "Edit connection", c); ok {
		s.save(c)
	}
}

// copy duplicates the selected connection under a new ID.
func (s *startPage) copy() {
	c, ok := s.selected()
	if !ok {
		return
	}
	c.Name = c.DisplayName() + " (copy)"
	c.ID = ""
	c.LastUsed = time.Time{}
	s.save(c)
}

func (s *startPage) delete() {
	c, ok := s.selected()
	if !ok {
		return
	}
	if !ui.MsgOkCancel(s.me.wnd, appName, "Delete \""+c.DisplayName()+"\"?",
		"The connection is removed from this list. Nothing on the hosts is changed.", "Delete") {
		return
	}
	s.me.store.Delete(c.ID)
	s.me.SaveStore()
	s.focusList()
}

func (s *startPage) contextMenu() {
	_, ok := s.selected()
	items := []menuItem{
		{id: mnuConnect, text: "&Connect\tEnter", enabled: ok, bold: true},
		{},
		{id: mnuEdit, text: "&Edit...\tF2", enabled: ok},
		{id: mnuCopy, text: "C&opy", enabled: ok},
		{id: mnuDelete, text: "&Delete\tDel", enabled: ok},
		{},
		{id: mnuNew, text: "&New...\tCtrl+N", enabled: true},
	}
	switch popupMenu(s.me.wnd.Hwnd(), items) {
	case mnuConnect:
		s.connect()
	case mnuEdit:
		s.edit()
	case mnuCopy:
		s.copy()
	case mnuDelete:
		s.delete()
	case mnuNew:
		s.newConnection()
	}
}

// moreMenu shows the overflow ("...") menu below its button.
func (s *startPage) moreMenu() {
	me := s.me
	rc := buttonScreenRect(s.bar, tbConnMore)
	items := []menuItem{
		{id: mnuSettings, text: "&Settings...", enabled: true},
		{id: mnuOpenLog, text: "Open &log", enabled: true},
		{},
		{id: mnuCheckUpdates, text: "Check for &updates", enabled: !me.checking},
		{},
		{id: mnuAbout, text: "&About ...", enabled: true},
	}
	switch popupMenuAt(me.wnd.Hwnd(), items, win.POINT{X: rc.Left, Y: rc.Bottom}) {
	case mnuSettings:
		me.showSettings()
	case mnuOpenLog:
		if err := shellOpen(me.wnd.Hwnd(), me.logFile()); err != nil {
			me.ShowError("Could not open the log", me.logFile()+"\n\n"+err.Error())
		}
	case mnuCheckUpdates:
		me.checkForUpdates()
	case mnuAbout:
		showAbout(me.wnd, me.version)
	}
}
