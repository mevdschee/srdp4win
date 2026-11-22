//go:build windows

package ui

import (
	"fmt"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"
)

func closeModal(wnd *ui.Modal) { wnd.Hwnd().SendMessage(co.WM_CLOSE, 0, 0) }

// focusOnOpen puts the keyboard focus in an edit when the dialog opens, with
// its text selected so typing replaces it. The init functions run first, in
// WM_CREATE, when all controls exist.
func focusOnOpen(wnd *ui.Modal, edit *ui.Edit, init ...func()) {
	wnd.On().WmCreate(func(_ ui.WmCreate) int {
		for _, fn := range init {
			fn()
		}
		wnd.UiThread(func() {
			edit.Focus()
			edit.SetSelection(0, -1)
		})
		return 0
	})
}

func label(parent ui.Parent, text string, x, y, cx int) *ui.Static {
	return ui.NewStatic(parent, ui.OptsStatic().Text(text).Position(ui.Dpi(x, y+3)).Size(ui.Dpi(cx, 20)))
}

func okCancel(wnd *ui.Modal, okText string, x, y int) (*ui.Button, *ui.Button) {
	ok := ui.NewButton(wnd, ui.OptsButton().Text(okText).Position(ui.Dpi(x, y)).
		CtrlId(uint16(co.ID_OK)).CtrlStyle(co.BS_DEFPUSHBUTTON))
	cancel := ui.NewButton(wnd, ui.OptsButton().Text("Cancel").Position(ui.Dpi(x+96, y)).
		CtrlId(uint16(co.ID_CANCEL)))
	cancel.On().BnClicked(func() { closeModal(wnd) })
	return ok, cancel
}

// heading is a section title in a dialog; headingFont makes it semibold
// (group boxes are avoided: they ignore the text colour in dark mode).
func heading(parent ui.Parent, text string, x, y, cx int) *ui.Static {
	return ui.NewStatic(parent, ui.OptsStatic().Text(text).Position(ui.Dpi(x, y)).Size(ui.Dpi(cx, 20)).
		CtrlStyle(co.SS_LEFT|co.SS_NOPREFIX))
}

// headingFont sets a semibold font on the headings; call it in WM_CREATE and
// delete the returned font when the dialog has closed.
func headingFont(headings []*ui.Static) uintptr {
	font := uiFontWeight(0, 600)
	if font != 0 {
		for _, h := range headings {
			h.Hwnd().SendMessage(co.WM_SETFONT, win.WPARAM(font), 1)
		}
	}
	return font
}

// checkBox makes a check box with a label. In dark mode the label is a
// separate text (a themed check box draws its text black, also in dark
// mode); clicking it toggles the box.
func checkBox(parent ui.Parent, text string, x, y, cx int, checked bool) *ui.CheckBox {
	state := co.BST_UNCHECKED
	if checked {
		state = co.BST_CHECKED
	}
	if !darkMode {
		return ui.NewCheckBox(parent, ui.OptsCheckBox().Text(text).Position(ui.Dpi(x, y+2)).Size(ui.Dpi(cx, 20)).State(state))
	}
	box := ui.NewCheckBox(parent, ui.OptsCheckBox().Position(ui.Dpi(x, y+2)).Size(ui.Dpi(18, 20)).State(state))
	lbl := ui.NewStatic(parent, ui.OptsStatic().Text(text).Position(ui.Dpi(x+19, y+4)).Size(ui.Dpi(cx-19, 18)).
		CtrlStyle(co.SS_LEFT|co.SS_NOTIFY))
	lbl.On().StnClicked(func() {
		if r, _, _ := procIsWindowEnabled.Call(uintptr(box.Hwnd())); r != 0 {
			box.SetCheckAndTrigger(!box.IsChecked())
		}
	})
	return box
}

// showInput asks for one line of text. Password hides the input.
func showInput(parent ui.Parent, title, prompt, value string, password bool) (string, bool) {
	wnd := newModal(parent, ui.OptsModal().Title(title).Size(ui.Dpi(400, 120)))
	label(wnd, prompt, 14, 12, 370)
	style := co.ES_AUTOHSCROLL
	if password {
		style |= co.ES_PASSWORD
	}
	edit := ui.NewEdit(wnd, ui.OptsEdit().Text(value).Position(ui.Dpi(14, 40)).Width(ui.DpiX(372)).CtrlStyle(style))
	ok, _ := okCancel(wnd, "OK", 194, 80)
	focusOnOpen(wnd, edit)
	result, accepted := "", false
	ok.On().BnClicked(func() {
		result, accepted = edit.Text(), true
		closeModal(wnd)
	})
	wnd.ShowModal()
	return result, accepted
}

// askPassword asks for a password, with a "Save password" box when
// offerSave. ok is false when cancelled.
func askPassword(parent ui.Parent, title, prompt string, offerSave bool) (password string, save, ok bool) {
	h := 132
	if offerSave {
		h = 160
	}
	wnd := newModal(parent, ui.OptsModal().Title(title).Size(ui.Dpi(400, h)))
	ui.NewStatic(wnd, ui.OptsStatic().Text(prompt).Position(ui.Dpi(14, 12)).Size(ui.Dpi(372, 36)).
		CtrlStyle(co.SS_LEFT|co.SS_NOPREFIX))
	edit := ui.NewEdit(wnd, ui.OptsEdit().Position(ui.Dpi(14, 52)).Width(ui.DpiX(372)).
		CtrlStyle(co.ES_AUTOHSCROLL|co.ES_PASSWORD))
	var saveBox *ui.CheckBox
	if offerSave {
		saveBox = checkBox(wnd, "&Save password", 14, 82, 240, false)
	}
	okBtn, _ := okCancel(wnd, "OK", 194, h-40)
	focusOnOpen(wnd, edit)
	okBtn.On().BnClicked(func() {
		password, ok = edit.Text(), true
		save = saveBox != nil && saveBox.IsChecked()
		closeModal(wnd)
	})
	wnd.ShowModal()
	return password, save, ok
}

// askTrustHost shows the key of an SSH server that is not known yet. The
// focus starts on Cancel and Enter only trusts while the Trust button has
// the focus, so the key is not trusted by accident.
func askTrustHost(parent ui.Parent, hostport, keyType, fingerprint string) bool {
	wnd := newModal(parent, ui.OptsModal().Title("Unknown SSH host").Size(ui.Dpi(520, 232)))
	text := func(t string, x, y, cx, cy int) *ui.Static {
		return ui.NewStatic(wnd, ui.OptsStatic().Text(t).Position(ui.Dpi(x, y)).Size(ui.Dpi(cx, cy)).
			CtrlStyle(co.SS_LEFT|co.SS_NOPREFIX))
	}
	title := text("Trust this SSH host?", 14, 12, 492, 20)
	text("This host is not known yet. Only continue when the fingerprint matches the host's real key.", 14, 38, 492, 36)
	text("Host", 14, 84, 90, 20)
	text(hostport, 110, 84, 396, 20)
	text("Key type", 14, 110, 90, 20)
	text(keyType, 110, 110, 396, 20)
	text("Fingerprint", 14, 139, 90, 20)
	// A read-only edit, so the fingerprint can be selected and copied.
	ui.NewEdit(wnd, ui.OptsEdit().Text(fingerprint).Position(ui.Dpi(110, 136)).Width(ui.DpiX(396)).
		CtrlStyle(co.ES_AUTOHSCROLL|co.ES_READONLY))
	const idTrust = 1001
	trust := ui.NewButton(wnd, ui.OptsButton().Text("&Trust").Position(ui.Dpi(314, 190)).CtrlId(idTrust))
	cancel := ui.NewButton(wnd, ui.OptsButton().Text("Cancel").Position(ui.Dpi(410, 190)).
		CtrlId(uint16(co.ID_CANCEL)).CtrlStyle(co.BS_DEFPUSHBUTTON))
	trusted := false
	trust.On().BnClicked(func() {
		trusted = true
		closeModal(wnd)
	})
	cancel.On().BnClicked(func() { closeModal(wnd) })
	wnd.On().WmCommandAccelMenu(uint16(co.ID_OK), func() { // Enter
		if win.GetFocus() == trust.Hwnd() {
			trusted = true
		}
		closeModal(wnd)
	})
	var bold uintptr
	wnd.On().WmCreate(func(_ ui.WmCreate) int {
		bold = headingFont([]*ui.Static{title})
		wnd.UiThread(func() { cancel.Focus() })
		return 0
	})
	wnd.ShowModal()
	if bold != 0 {
		procDeleteObject.Call(bold)
	}
	return trusted
}

func humanSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d bytes", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(1024*1024*1024))
}
