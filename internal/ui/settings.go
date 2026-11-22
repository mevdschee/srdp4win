//go:build windows

package ui

import (
	"log/slog"
	"slices"

	"github.com/rodrigocfd/windigo/ui"
)

var themeValues = []string{"", "light", "dark"}

// showSettings shows the settings: the theme and the options a new
// connection starts with.
func (me *mainWindow) showSettings() {
	const width, height = optionsWidth + 32, 96 + optionsHeight + 52
	wnd := newModal(me.wnd, ui.OptsModal().Title("Settings").Size(ui.Dpi(width, height)))
	headings := []*ui.Static{heading(wnd, "Look", 16, 12, 300)}
	label(wnd, "&Theme", 16, 40, 136)
	theme := ui.NewComboBox(wnd, ui.OptsComboBox().Position(ui.Dpi(156, 40)).Width(ui.DpiX(optionsWidth-140)).
		Texts("System", "Light", "Dark").Select(max(0, slices.Index(themeValues, me.prefs.Theme))))
	headings = append(headings, heading(wnd, "Options of a new connection", 16, 80, 300))
	options := newOptionsPanel(wnd, 16, 108, me.prefs.Defaults)
	ok, _ := okCancel(wnd, "&Save", width-16-184, height-40)

	var bold uintptr
	wnd.On().WmCreate(func(_ ui.WmCreate) int {
		options.update()
		bold = headingFont(headings)
		wnd.UiThread(func() { theme.Focus() })
		return 0
	})
	accepted := false
	ok.On().BnClicked(func() {
		defaults, field, text := options.read()
		if text != "" {
			ui.MsgError(wnd, appName, "Cannot save the settings", text)
			field.Focus()
			return
		}
		me.prefs.Defaults = defaults
		me.prefs.Theme = themeValues[max(0, min(theme.SelectedIndex(), len(themeValues)-1))]
		accepted = true
		closeModal(wnd)
	})
	wnd.ShowModal()
	if bold != 0 {
		procDeleteObject.Call(bold)
	}
	if !accepted {
		return
	}
	slog.Info("settings saved", "theme", me.prefs.Theme)
	me.SavePrefs()
	setThemePref(me.prefs.Theme)
	me.themeChanged()
}
