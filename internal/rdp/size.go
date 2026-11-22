package rdp

// Limits of the remote desktop size (DesktopWidth and DesktopHeight of the
// control, and of the RDP display control channel).
const (
	MinSize = 200
	MaxSize = 8192
)

// FitSize turns a window size in pixels into a remote desktop size RDP
// accepts: between MinSize and MaxSize, with an even width.
func FitSize(width, height int) (int, int) {
	width = clamp(width, MinSize, MaxSize) &^ 1
	height = clamp(height, MinSize, MaxSize)
	return width, height
}

// SessionSize is the remote desktop size for a session in the given mode
// whose window is width x height: the fixed size in fixed mode, the window
// size otherwise. A window without a size yet gives 1024x768.
func SessionSize(o Options, mode ResizeMode, width, height int) (int, int) {
	if mode == ResizeFixed && o.FixedWidth > 0 && o.FixedHeight > 0 {
		return FitSize(o.FixedWidth, o.FixedHeight)
	}
	if width <= 0 || height <= 0 {
		return 1024, 768
	}
	return FitSize(width, height)
}

// ScaleFactors turns a monitor scale in percent into the two RDP scale
// factors: the desktop scale factor (100 to 500) and the device scale
// factor (100, 140 or 180).
func ScaleFactors(scale int) (desktop, device int) {
	if scale <= 0 {
		scale = 100
	}
	desktop = clamp(scale, 100, 500)
	switch {
	case desktop < 140:
		device = 100
	case desktop < 180:
		device = 140
	default:
		device = 180
	}
	return desktop, device
}

// PhysicalMM is the physical length in millimetres of a number of pixels on
// a monitor with the given scale (96 DPI at 100%), as the display control
// channel wants it.
func PhysicalMM(pixels, scale int) int {
	if scale <= 0 {
		scale = 100
	}
	mm := (pixels*2540 + 48*scale) / (96 * scale)
	return clamp(mm, 10, 10000)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
