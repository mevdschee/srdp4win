package rdp

import "strconv"

// Key is one key of a combination: a set 1 scancode, with Extended for the
// keys that have an E0 prefix.
type Key struct {
	Code     int
	Extended bool
}

// Set 1 scancodes used by the combinations.
const (
	scEsc    = 0x01
	scTab    = 0x0F
	scCtrl   = 0x1D
	scL      = 0x26
	scShift  = 0x2A
	scAlt    = 0x38
	scF4     = 0x3E
	scDelete = 0x53
	scPrtSc  = 0x37 // extended
	scLWin   = 0x5B // extended
)

// Scancodes returns the keys of a combination in the order they go down;
// they come up in reverse order. Nil for an unknown combination.
func Scancodes(k Keys) []Key {
	switch k {
	case KeysCtrlAltDel:
		return []Key{{scCtrl, false}, {scAlt, false}, {scDelete, false}}
	case KeysWin:
		return []Key{{scLWin, true}}
	case KeysAltTab:
		return []Key{{scAlt, false}, {scTab, false}}
	case KeysAltF4:
		return []Key{{scAlt, false}, {scF4, false}}
	case KeysCtrlEsc:
		return []Key{{scCtrl, false}, {scEsc, false}}
	case KeysPrintScreen:
		return []Key{{scPrtSc, true}}
	case KeysCtrlShiftEsc:
		return []Key{{scCtrl, false}, {scShift, false}, {scEsc, false}}
	case KeysWinL:
		return []Key{{scLWin, true}, {scL, false}}
	}
	return nil
}

func (k Keys) String() string {
	switch k {
	case KeysCtrlAltDel:
		return "Ctrl+Alt+Del"
	case KeysWin:
		return "Win"
	case KeysAltTab:
		return "Alt+Tab"
	case KeysAltF4:
		return "Alt+F4"
	case KeysCtrlEsc:
		return "Ctrl+Esc"
	case KeysPrintScreen:
		return "Print Screen"
	case KeysCtrlShiftEsc:
		return "Ctrl+Shift+Esc"
	case KeysWinL:
		return "Win+L"
	}
	return "Keys(" + strconv.Itoa(int(k)) + ")"
}

func (m ResizeMode) String() string {
	switch m {
	case ResizeDynamic:
		return "dynamic"
	case ResizeScaled:
		return "scaled"
	case ResizeFixed:
		return "fixed"
	}
	return "ResizeMode(" + strconv.Itoa(int(m)) + ")"
}

func (q Quality) String() string {
	switch q {
	case QualityBest:
		return "best"
	case QualityMedium:
		return "medium"
	case QualityPoor:
		return "poor"
	}
	return "Quality(" + strconv.Itoa(int(q)) + ")"
}
