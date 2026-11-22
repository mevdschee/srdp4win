//go:build windows

package ui

// sessBar is a row of flat buttons painted with GDI: Fluent icons, hover and
// pressed looks, tooltips. The session page uses it for its toolbar row and
// for the buttons under a "disconnected" message, the floating full screen
// toolbar is one in a window of its own. The bar has no window: its owner
// passes it the paint and mouse messages.

import (
	"syscall"
	"unsafe"

	"github.com/mevdschee/srdp4win/internal/icons"
)

var (
	sessGetDC     = sessUser32.NewProc("GetDC")
	sessReleaseDC = sessUser32.NewProc("ReleaseDC")
)

// Kinds of bar items.
const (
	sessButton = iota // icon button (text when the icon is missing)
	sessLabel         // plain text, not clickable
	sessSep           // thin vertical line
	sessPush          // outlined text button
)

type sessItem struct {
	id    int
	kind  int
	icons []string // candidates; the first one that exists is used
	label string   // text of a label or push button, fallback of an icon button
	tip   string

	hidden, disabled, checked bool

	rc  sessWinRect // in the window's client coordinates
	img int         // index in the image lists, -1 without icon
}

type sessBar struct {
	hwnd    uintptr
	items   []*sessItem
	bg      func() uint32
	onClick func(id int)

	shown          bool // false: not painted, no mouse, no tooltips
	dpi            int
	font           uintptr
	imgs, imgsOff  uintptr // image lists: normal and disabled
	iconPx         int32
	x, y, w, h     int32 // where the bar is in its window
	hover, pressed int   // item index, -1 for none
	tracking       bool

	tip      uintptr
	tipCount int
	tipTexts [][]uint16
}

// sessIconSet is the set of icon names that exist.
var sessIconSet map[string]bool

func sessHasIcon(name string) bool {
	if sessIconSet == nil {
		sessIconSet = map[string]bool{}
		for _, n := range icons.Names() {
			sessIconSet[n] = true
		}
	}
	return sessIconSet[name]
}

func newSessBar(hwnd uintptr, bg func() uint32, items []*sessItem, onClick func(id int)) *sessBar {
	return &sessBar{hwnd: hwnd, items: items, bg: bg, onClick: onClick, shown: true, hover: -1, pressed: -1, dpi: 96}
}

func (b *sessBar) px(n int) int32 { return int32(n * b.dpi / 96) }

// height is the height of the row for the bar's DPI.
func (b *sessBar) height() int32 { return b.px(40) }

// setup renders the font and icons for a DPI in the current palette. Call
// it before the first layout and after a DPI or theme change.
func (b *sessBar) setup(dpi int) {
	if dpi <= 0 {
		dpi = 96
	}
	b.dpi = dpi
	b.iconPx = max(20, b.px(20))
	b.freeGdi()
	b.font = uiFont(0)
	var names []string
	for _, it := range b.items {
		it.img = -1
		for _, name := range it.icons {
			if sessHasIcon(name) {
				it.img = len(names)
				names = append(names, name)
				break
			}
		}
	}
	if len(names) > 0 {
		b.imgs = uintptr(fluentImageList(names, int(b.iconPx), colors.icon))
		b.imgsOff = uintptr(fluentImageList(names, int(b.iconPx), colors.iconDisabled))
	}
}

func (b *sessBar) freeGdi() {
	for _, l := range []uintptr{b.imgs, b.imgsOff} {
		if l != 0 {
			sessImageListDestroy.Call(l)
		}
	}
	b.imgs, b.imgsOff = 0, 0
	if b.font != 0 {
		sessDeleteObject.Call(b.font)
		b.font = 0
	}
}

func (b *sessBar) destroy() {
	if b.tip != 0 {
		sessDestroyWindowProc.Call(b.tip)
		b.tip = 0
	}
	b.freeGdi()
}

func (b *sessBar) item(id int) *sessItem {
	for _, it := range b.items {
		if it.id == id {
			return it
		}
	}
	return nil
}

// measure returns the width the bar needs.
func (b *sessBar) measure() int32 { return b.place(0, 0, false) }

// layout places the bar at x, y in its window and returns its width.
func (b *sessBar) layout(x, y int32) int32 {
	b.x, b.y, b.h = x, y, b.height()
	b.w = b.place(x, y, true)
	b.setTips()
	sessInvalidate(b.hwnd)
	return b.w
}

func (b *sessBar) place(x0, y0 int32, store bool) int32 {
	hdc, _, _ := sessGetDC.Call(b.hwnd)
	old, _, _ := sessSelectObject.Call(hdc, b.font)
	defer func() {
		sessSelectObject.Call(hdc, old)
		sessReleaseDC.Call(b.hwnd, hdc)
	}()
	height := b.height()
	btnH := b.iconPx + b.px(12)
	x := x0 + b.px(6)
	first := true
	for _, it := range b.items {
		if it.hidden {
			if store {
				it.rc = sessWinRect{}
			}
			continue
		}
		if !first {
			x += b.px(2)
			if it.kind == sessPush {
				x += b.px(6)
			}
		}
		first = false
		w, h := b.iconPx+b.px(16), btnH
		switch it.kind {
		case sessSep:
			w = b.px(9)
		case sessLabel:
			w = min(sessTextWidth(hdc, it.label), b.px(220)) + b.px(12)
		case sessPush:
			w = max(sessTextWidth(hdc, it.label)+b.px(28), b.px(96))
		default:
			if it.img < 0 {
				w = sessTextWidth(hdc, it.label) + b.px(16)
			}
		}
		if store {
			top := y0 + (height-h)/2
			it.rc = sessWinRect{x, top, x + w, top + h}
		}
		x += w
	}
	return x + b.px(6) - x0
}

func (b *sessBar) setTips() {
	if b.tip == 0 {
		b.tip = sessNewTip(b.hwnd)
	}
	var tips []sessTip
	b.tipTexts = b.tipTexts[:0]
	if b.shown {
		for _, it := range b.items {
			if it.hidden || it.tip == "" {
				continue
			}
			text := syscall.StringToUTF16(it.tip)
			b.tipTexts = append(b.tipTexts, text)
			tips = append(tips, sessTip{rc: it.rc, text: text})
		}
	}
	sessSetTips(b.tip, b.hwnd, b.tipCount, tips)
	b.tipCount = len(tips)
}

// setShown turns the bar on or off.
func (b *sessBar) setShown(on bool) {
	if b.shown == on {
		return
	}
	b.shown = on
	b.hover, b.pressed = -1, -1
	b.setTips()
	sessInvalidate(b.hwnd)
}

// hoverColors are the fills of a hovered and a pressed button on bg.
func sessHoverColors(bg uint32) (hover, pressed uint32) {
	if darkMode {
		return shade(bg, 20), shade(bg, 34)
	}
	return shade(bg, -16), shade(bg, -30)
}

// paint draws the items on hdc, moved by dx, dy (for a memory DC whose
// origin is not the window's). The caller fills the background.
func (b *sessBar) paint(hdc uintptr, dx, dy int32) {
	if !b.shown {
		return
	}
	old, _, _ := sessSelectObject.Call(hdc, b.font)
	defer sessSelectObject.Call(hdc, old)
	hoverFill, pressedFill := sessHoverColors(b.bg())
	radius := b.px(4)
	for i, it := range b.items {
		if it.hidden {
			continue
		}
		rc := sessWinRect{it.rc.left + dx, it.rc.top + dy, it.rc.right + dx, it.rc.bottom + dy}
		fg := colors.text
		if it.disabled {
			fg = colors.textDisabled
		}
		switch it.kind {
		case sessSep:
			x := (rc.left + rc.right) / 2
			pad := (rc.bottom - rc.top) / 4
			sessFill(hdc, sessWinRect{x, rc.top + pad, x + 1, rc.bottom - pad}, colors.tabSeparator)
			continue
		case sessLabel:
			in := sessWinRect{rc.left + b.px(6), rc.top, rc.right - b.px(6), rc.bottom}
			sessText(hdc, it.label, &in, fg, sessDtSingleLine|sessDtVCenter|sessDtEndEllipsis)
			continue
		case sessPush:
			sessRound(hdc, rc, radius, colors.addressBorder)
			fill := b.bg()
			switch {
			case i == b.pressed && i == b.hover:
				fill = pressedFill
			case i == b.hover:
				fill = hoverFill
			}
			sessRound(hdc, sessWinRect{rc.left + 1, rc.top + 1, rc.right - 1, rc.bottom - 1}, radius, fill)
			sessText(hdc, it.label, &rc, fg, sessDtSingleLine|sessDtVCenter|sessDtCenter)
			continue
		}
		switch {
		case it.disabled:
		case i == b.pressed && i == b.hover:
			sessRound(hdc, rc, radius, pressedFill)
		case i == b.hover || i == b.pressed || it.checked:
			sessRound(hdc, rc, radius, hoverFill)
		}
		if it.img < 0 {
			sessText(hdc, it.label, &rc, fg, sessDtSingleLine|sessDtVCenter|sessDtCenter)
			continue
		}
		list := b.imgs
		if it.disabled {
			list = b.imgsOff
		}
		x := rc.left + (rc.right-rc.left-b.iconPx)/2
		y := rc.top + (rc.bottom-rc.top-b.iconPx)/2
		sessImageListDraw.Call(list, uintptr(it.img), hdc, uintptr(x), uintptr(y), 1) // ILD_TRANSPARENT
	}
}

// hit returns the index of the clickable item at x, y, or -1.
func (b *sessBar) hit(x, y int32) int {
	if !b.shown {
		return -1
	}
	for i, it := range b.items {
		if it.hidden || it.disabled || it.kind == sessSep || it.kind == sessLabel {
			continue
		}
		if it.rc.has(x, y) {
			return i
		}
	}
	return -1
}

func (b *sessBar) setHover(i int) {
	if i != b.hover {
		b.hover = i
		sessInvalidate(b.hwnd)
	}
}

func (b *sessBar) mouseMove(x, y int32) {
	if !b.tracking {
		sessTrackLeave(b.hwnd)
		b.tracking = true
	}
	b.setHover(b.hit(x, y))
}

func (b *sessBar) mouseLeave() {
	b.tracking = false
	b.setHover(-1)
}

// mouseDown reports whether the click was on one of the bar's buttons.
func (b *sessBar) mouseDown(x, y int32) bool {
	i := b.hit(x, y)
	if i < 0 {
		return false
	}
	b.pressed, b.hover = i, i
	if b.tip != 0 {
		sessSendMessage.Call(b.tip, 0x0400+28, 0, 0) // TTM_POP: no tooltip over a menu
	}
	sessSetCapture.Call(b.hwnd)
	sessInvalidate(b.hwnd)
	return true
}

// mouseUp ends a press and runs the click when the mouse is still on the
// pressed button.
func (b *sessBar) mouseUp(x, y int32) {
	if b.pressed < 0 {
		return
	}
	i := b.pressed
	b.pressed = -1
	sessReleaseCapture.Call()
	sessInvalidate(b.hwnd)
	if b.hit(x, y) != i || b.onClick == nil {
		return
	}
	id := b.items[i].id
	b.onClick(id) // may show a menu, or destroy the bar's window
	if sessWindows[b.hwnd] == nil {
		return
	}
	pt := sessCursorIn(b.hwnd)
	b.setHover(b.hit(pt.x, pt.y))
}

// cancelPress drops a press whose capture was taken away.
func (b *sessBar) cancelPress() {
	if b.pressed >= 0 {
		b.pressed = -1
		sessInvalidate(b.hwnd)
	}
}

// anchor is the screen position for a menu under the item.
func (b *sessBar) anchor(id int) sessPoint {
	it := b.item(id)
	if it == nil {
		pt := sessPoint{}
		sessGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
		return pt
	}
	return sessToScreen(b.hwnd, it.rc.left, it.rc.bottom+b.px(2))
}
