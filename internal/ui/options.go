//go:build windows

package ui

// The session options as a block of controls, used by the connection editor
// and, for the defaults of new connections, by the settings dialog.

import (
	"strconv"
	"strings"

	"github.com/mevdschee/srdp4win/internal/rdp"
	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
)

// optionsWidth and optionsHeight are the size of the block (at 96 DPI).
const (
	optionsWidth  = 392
	optionsHeight = 12 * 30
)

var colorDepths = []int{32, 24, 16, 15}

type optionsPanel struct {
	base rdp.Options // values without a control are kept

	clipboard, printers, fontSmoothing, smartCards *ui.CheckBox
	ports, microphone, drives, admin, verify       *ui.CheckBox
	letters, fixedW, fixedH                        *ui.Edit
	audio, keys, resize, quality, depth            *ui.ComboBox
	lettersLabel, sizeLabel, byLabel               *ui.Static
}

// newOptionsPanel creates the controls at x, y in a dialog.
func newOptionsPanel(wnd ui.Parent, x, y int, o rdp.Options) *optionsPanel {
	p := &optionsPanel{base: o}
	row := func(i int) int { return y + i*30 }
	half := optionsWidth / 2
	lx, fx, fw := x, x+140, optionsWidth-140
	combo := func(i int, text string, selected int, texts ...string) *ui.ComboBox {
		label(wnd, text, lx, row(i), 136)
		return ui.NewComboBox(wnd, ui.OptsComboBox().Position(ui.Dpi(fx, row(i))).Width(ui.DpiX(fw)).
			Texts(texts...).Select(max(0, min(selected, len(texts)-1))))
	}
	number := func(xx, i, w int, value int) *ui.Edit {
		return ui.NewEdit(wnd, ui.OptsEdit().Text(strconv.Itoa(value)).Position(ui.Dpi(xx, row(i))).Width(ui.DpiX(w)).
			CtrlStyle(co.ES_AUTOHSCROLL|co.ES_NUMBER))
	}

	p.clipboard = checkBox(wnd, "&Clipboard", lx, row(0), half-8, o.Clipboard)
	p.printers = checkBox(wnd, "&Printers", lx+half, row(0), half, o.Printers)
	p.fontSmoothing = checkBox(wnd, "&Font smoothing", lx, row(1), half-8, o.FontSmoothing)
	p.smartCards = checkBox(wnd, "S&mart cards", lx+half, row(1), half, o.SmartCards)
	p.ports = checkBox(wnd, "Serial p&orts", lx, row(2), half-8, o.Ports)
	p.microphone = checkBox(wnd, "M&icrophone", lx+half, row(2), half, o.Microphone)
	p.drives = checkBox(wnd, "&Drives", lx, row(3), 110, o.Drives)
	p.lettersLabel = label(wnd, "Letters (empty: all)", lx+half-56, row(3), 132)
	p.letters = ui.NewEdit(wnd, ui.OptsEdit().Text(o.DriveLetters).Position(ui.Dpi(lx+half+80, row(3))).
		Width(ui.DpiX(half-80)).CtrlStyle(co.ES_AUTOHSCROLL|co.ES_UPPERCASE))
	p.audio = combo(4, "Sound", int(o.Audio), "Play on this computer", "Play on the remote computer", "Off")
	p.keys = combo(5, "Windows key combos", int(o.Keyboard), "In full screen only", "Always to the remote desktop", "Always to this computer")
	p.resize = combo(6, "Resize mode", resizeIndex(o), resizeTexts...)
	p.sizeLabel = label(wnd, "Fixed size", lx, row(7), 136)
	p.fixedW = number(fx, 7, 70, o.FixedWidth)
	p.byLabel = label(wnd, "x", fx+78, row(7), 12)
	p.fixedH = number(fx+94, 7, 70, o.FixedHeight)
	p.quality = combo(8, "Quality", int(o.Quality), "Best (slowest)", "Medium", "Poor (fastest)")
	depth := 0
	for i, d := range colorDepths {
		if d == o.ColorDepth {
			depth = i
		}
	}
	p.depth = combo(9, "Colour depth", depth, "32 bit", "24 bit", "16 bit", "15 bit")
	p.admin = checkBox(wnd, "&Admin session", lx, row(10), optionsWidth, o.Admin)
	p.verify = checkBox(wnd, "&Verify the RDP certificate", lx, row(11), optionsWidth, o.VerifyCert)

	p.drives.On().BnClicked(p.update)
	p.resize.On().CbnSelChange(p.update)
	return p
}

// update enables the fields that depend on another one. Call it in
// WM_CREATE too.
func (p *optionsPanel) update() {
	drives := p.drives.IsChecked()
	p.letters.Hwnd().EnableWindow(drives)
	p.lettersLabel.Hwnd().EnableWindow(drives)
	fixed := resizeChoices[max(p.resize.SelectedIndex(), 0)].mode == rdp.ResizeFixed
	p.fixedW.Hwnd().EnableWindow(fixed)
	p.fixedH.Hwnd().EnableWindow(fixed)
	p.sizeLabel.Hwnd().EnableWindow(fixed)
	p.byLabel.Hwnd().EnableWindow(fixed)
}

// read returns the options as entered. With a mistake it returns the field
// to put the focus in and what is wrong with it.
func (p *optionsPanel) read() (rdp.Options, *ui.Edit, string) {
	o := p.base
	o.Clipboard = p.clipboard.IsChecked()
	o.Printers = p.printers.IsChecked()
	o.FontSmoothing = p.fontSmoothing.IsChecked()
	o.SmartCards = p.smartCards.IsChecked()
	o.Ports = p.ports.IsChecked()
	o.Microphone = p.microphone.IsChecked()
	o.Drives = p.drives.IsChecked()
	o.Admin = p.admin.IsChecked()
	o.VerifyCert = p.verify.IsChecked()
	o.Audio = rdp.AudioMode(max(p.audio.SelectedIndex(), 0))
	o.Keyboard = rdp.KeyboardMode(max(p.keys.SelectedIndex(), 0))
	choice := resizeChoices[max(p.resize.SelectedIndex(), 0)]
	o.Resize, o.FullScreen = choice.mode, choice.full
	o.Quality = rdp.Quality(max(p.quality.SelectedIndex(), 0))
	o.ColorDepth = colorDepths[max(0, min(p.depth.SelectedIndex(), len(colorDepths)-1))]

	// "c: d:" and "C,D" are fine too.
	letters := strings.ToUpper(strings.NewReplacer(" ", "", ",", "", ";", "", ":", "").Replace(p.letters.Text()))
	for _, r := range letters {
		if r < 'A' || r > 'Z' {
			return o, p.letters, "The drive letters must be letters, like CD for C: and D:."
		}
	}
	o.DriveLetters = letters

	w, errW := strconv.Atoi(strings.TrimSpace(p.fixedW.Text()))
	h, errH := strconv.Atoi(strings.TrimSpace(p.fixedH.Text()))
	if o.Resize == rdp.ResizeFixed {
		const text = "Enter a fixed size between 200 x 200 and 8192 x 8192."
		if errW != nil || w < 200 || w > 8192 {
			return o, p.fixedW, text
		}
		if errH != nil || h < 200 || h > 8192 {
			return o, p.fixedH, text
		}
	}
	if errW == nil && w > 0 {
		o.FixedWidth = w
	}
	if errH == nil && h > 0 {
		o.FixedHeight = h
	}
	return o, nil, ""
}

// resizeChoices are the entries of the resize mode list: the three modes,
// and dynamic resolution that goes to full screen as soon as it connects.
var resizeChoices = []struct {
	text string
	mode rdp.ResizeMode
	full bool
}{
	{"Dynamic resolution", rdp.ResizeDynamic, false},
	{"Dynamic full screen", rdp.ResizeDynamic, true},
	{"Scaled", rdp.ResizeScaled, false},
	{"Fixed size", rdp.ResizeFixed, false},
}

var resizeTexts = func() []string {
	texts := make([]string, len(resizeChoices))
	for i, c := range resizeChoices {
		texts[i] = c.text
	}
	return texts
}()

// resizeIndex is the list entry for the options.
func resizeIndex(o rdp.Options) int {
	for i, c := range resizeChoices {
		if c.mode == o.Resize && c.full == (o.FullScreen && o.Resize == rdp.ResizeDynamic) {
			return i
		}
	}
	return 0
}
