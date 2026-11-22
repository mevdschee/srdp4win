package rdp

// RDP performance flags (MS-RDPBCGR 2.2.1.11.1.1.1, TS_PERF_*). The control
// takes them as AdvancedSettings.PerformanceFlags.
const (
	PerfDisableWallpaper      = 0x001
	PerfDisableFullWindowDrag = 0x002
	PerfDisableMenuAnimations = 0x004
	PerfDisableThemes         = 0x008
	PerfDisableCursorShadow   = 0x020
	PerfFontSmoothing         = 0x080
	PerfDesktopComposition    = 0x100
)

// PerformanceFlags maps the quality preset and the font smoothing option to
// the RDP performance flags. Best keeps every effect, medium keeps font
// smoothing and themes, poor turns everything off (font smoothing too).
func PerformanceFlags(o Options) uint32 {
	var f uint32
	switch o.Quality {
	case QualityMedium:
		f = PerfDisableWallpaper | PerfDisableFullWindowDrag | PerfDisableMenuAnimations | PerfDisableCursorShadow
	case QualityPoor:
		return PerfDisableWallpaper | PerfDisableFullWindowDrag | PerfDisableMenuAnimations |
			PerfDisableThemes | PerfDisableCursorShadow
	default:
		f = PerfDesktopComposition
	}
	if o.FontSmoothing {
		f |= PerfFontSmoothing
	}
	return f
}

// EffectiveColorDepth is the colour depth to ask for: the option, or 16 bit
// with the poor preset. Unknown values become 32.
func EffectiveColorDepth(o Options) int {
	if o.Quality == QualityPoor {
		return 16
	}
	switch o.ColorDepth {
	case 15, 16, 24, 32:
		return o.ColorDepth
	}
	return 32
}
