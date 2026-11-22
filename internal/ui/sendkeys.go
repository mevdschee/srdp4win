package ui

import "github.com/mevdschee/srdp4win/internal/rdp"

// sendKey is one entry of the Send keys menu.
type sendKey struct {
	text string
	keys rdp.Keys
	sep  bool // a separator line before this entry
}

// sendKeyList is the Send keys menu, in order.
var sendKeyList = []sendKey{
	{text: "Ctrl+Alt+Del", keys: rdp.KeysCtrlAltDel},
	{text: "Windows key", keys: rdp.KeysWin, sep: true},
	{text: "Alt+Tab", keys: rdp.KeysAltTab},
	{text: "Alt+F4", keys: rdp.KeysAltF4},
	{text: "Ctrl+Esc", keys: rdp.KeysCtrlEsc},
	{text: "Ctrl+Shift+Esc", keys: rdp.KeysCtrlShiftEsc},
	{text: "Print Screen", keys: rdp.KeysPrintScreen, sep: true},
	{text: "Win+L (lock)", keys: rdp.KeysWinL},
}
