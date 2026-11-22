//go:build windows

package ui

import (
	"unicode/utf16"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/win"
	"github.com/rodrigocfd/windigo/x/cosh"
	"github.com/rodrigocfd/windigo/x/winsh"
)

// pickKeyFile shows a file open dialog for a private key.
func pickKeyFile(owner win.HWND) (string, bool) {
	rel := win.NewOleReleaser()
	defer rel.Release()

	var fod *winsh.IFileOpenDialog
	if err := win.CoCreateInstance(rel, &cosh.CLSID_FileOpenDialog, nil, co.CLSCTX_INPROC_SERVER, &fod); err != nil {
		return "", false
	}
	opts, _ := fod.GetOptions()
	_ = fod.SetOptions(opts | cosh.FOS_FORCEFILESYSTEM | cosh.FOS_FILEMUSTEXIST)
	_ = fod.SetTitle("Choose private key")
	if ok, _ := fod.Show(owner); !ok {
		return "", false
	}
	item, err := fod.GetResult(rel)
	if err != nil {
		return "", false
	}
	path, err := item.GetDisplayName(cosh.SIGDN_FILESYSPATH)
	return path, err == nil
}

// menuItem is one entry of a popup menu; an empty text is a separator.
type menuItem struct {
	id      uint16
	text    string
	enabled bool
	bold    bool // default action
	checked bool // shows a check mark
}

// popupMenu shows a popup menu at the cursor and returns the chosen id, or 0.
func popupMenu(owner win.HWND, items []menuItem) uint16 {
	pos, _ := win.GetCursorPos()
	return popupMenuAt(owner, items, pos)
}

// popupMenuAt shows a popup menu at a screen position.
func popupMenuAt(owner win.HWND, items []menuItem, pos win.POINT) uint16 {
	hMenu, err := win.CreatePopupMenu()
	if err != nil {
		return 0
	}
	defer hMenu.DestroyMenu()
	for i, it := range items {
		var mii win.MENUITEMINFO
		mii.SetCbSize()
		if it.text == "" {
			mii.FMask = co.MIIM_FTYPE
			mii.FType = co.MFT_SEPARATOR
		} else {
			mii.FMask = co.MIIM_FTYPE | co.MIIM_ID | co.MIIM_STRING | co.MIIM_STATE
			mii.FType = co.MFT_STRING
			mii.WId = uint32(it.id)
			text := utf16.Encode([]rune(it.text + "\x00"))
			mii.DwTypeData = &text[0]
			if !it.enabled {
				mii.FState |= co.MFS_DISABLED
			}
			if it.bold {
				mii.FState |= co.MFS_DEFAULT
			}
			if it.checked {
				mii.FState |= co.MFS_CHECKED
			}
		}
		_ = hMenu.InsertMenuItemByPos(i, &mii)
	}
	owner.SetForegroundWindow()
	id, _ := hMenu.TrackPopupMenu(co.TPM_RETURNCMD|co.TPM_LEFTBUTTON|co.TPM_RIGHTBUTTON, int(pos.X), int(pos.Y), owner)
	owner.PostMessage(co.WM_NULL, 0, 0)
	return uint16(id)
}
