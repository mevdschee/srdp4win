//go:build windows

package ui

// The connection editor: General, SSH host and RDP host in the left column,
// the session options in the right one.

import (
	"strconv"
	"strings"

	"github.com/mevdschee/srdp4win/internal/config"
	"github.com/mevdschee/srdp4win/internal/sshconn"
	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
)

var authMethods = []string{sshconn.AuthAuto, sshconn.AuthKeyFile, sshconn.AuthPageant, sshconn.AuthPassword}

// portText shows a port, the default one for 0.
func portText(port, standard int) string {
	if port == 0 {
		port = standard
	}
	return strconv.Itoa(port)
}

// parsePort reads a port; an empty field is 0 (the default).
func parsePort(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, true
	}
	n, err := strconv.Atoi(s)
	return n, err == nil && n > 0 && n <= 65535
}

// editConnection shows the form for one connection. It returns false on
// cancel. The connection is checked with Validate, so one without an SSH
// host cannot leave the form.
func editConnection(parent ui.Parent, title string, c config.Connection) (config.Connection, bool) {
	const (
		lx, fx, fw = 16, 126, 262 // left column: labels, fields, field width
		rx         = 420          // right column
		width      = rx + optionsWidth + 16
		height     = 500
	)
	wnd := newModal(parent, ui.OptsModal().Title(title).Size(ui.Dpi(width, height)))
	var headings []*ui.Static
	y := 12
	head := func(text string, x int) {
		headings = append(headings, heading(wnd, text, x, y, 300))
		y += 28
	}
	edit := func(text, value string, style co.ES) *ui.Edit {
		label(wnd, text, lx, y, fx-lx-4)
		e := ui.NewEdit(wnd, ui.OptsEdit().Text(value).Position(ui.Dpi(fx, y)).Width(ui.DpiX(fw)).
			CtrlStyle(co.ES_AUTOHSCROLL|style))
		y += 30
		return e
	}
	// hostPort is an address with its port on one row.
	hostPort := func(host string, port, standard int) (*ui.Edit, *ui.Edit) {
		label(wnd, "Address", lx, y, fx-lx-4)
		h := ui.NewEdit(wnd, ui.OptsEdit().Text(host).Position(ui.Dpi(fx, y)).Width(ui.DpiX(fw-98)))
		label(wnd, "Port", fx+fw-90, y, 30)
		p := ui.NewEdit(wnd, ui.OptsEdit().Text(portText(port, standard)).Position(ui.Dpi(fx+fw-56, y)).
			Width(ui.DpiX(56)).CtrlStyle(co.ES_AUTOHSCROLL|co.ES_NUMBER))
		y += 30
		return h, p
	}

	head("General", lx)
	name := edit("Name", c.Name, 0)
	group := edit("Group", c.Group, 0)
	y += 8

	head("SSH host", lx)
	sshHost, sshPort := hostPort(c.SSHHost, c.SSHPort, 22)
	sshUser := edit("User", c.SSHUser, 0)
	label(wnd, "Authentication", lx, y, fx-lx-4)
	selected := 0
	for i, m := range authMethods {
		if m == c.SSHAuth {
			selected = i
		}
	}
	auth := ui.NewComboBox(wnd, ui.OptsComboBox().Position(ui.Dpi(fx, y)).Width(ui.DpiX(fw)).
		Texts("Automatic (agent, Pageant, keys, password)", "Key file", "Pageant", "Password").
		Select(selected))
	y += 30
	keyLabel := label(wnd, "Key file", lx, y, fx-lx-4)
	key := ui.NewEdit(wnd, ui.OptsEdit().Text(c.SSHKeyFile).Position(ui.Dpi(fx, y)).Width(ui.DpiX(fw-76)))
	browse := ui.NewButton(wnd, ui.OptsButton().Text("&Browse...").Position(ui.Dpi(fx+fw-70, y-1)).Width(ui.DpiX(70)))
	y += 38

	head("RDP host (as seen from the SSH host)", lx)
	rdpHost, rdpPort := hostPort(c.RDPHost, c.RDPPort, 3389)
	rdpUser := edit("User", c.RDPUser, 0)
	domain := edit("Domain", c.RDPDomain, 0)
	password := edit("Password", c.RDPPassword, co.ES_PASSWORD)
	savePassword := checkBox(wnd, "&Save password", fx, y, fw, c.SavePassword)

	y = 12
	head("Options", rx)
	options := newOptionsPanel(wnd, rx, y, c.Options)

	ok, _ := okCancel(wnd, "&Save", width-16-184, height-40)

	updateKey := func() {
		isKeyFile := authMethods[max(auth.SelectedIndex(), 0)] == sshconn.AuthKeyFile
		keyLabel.Hwnd().EnableWindow(isKeyFile)
		key.Hwnd().EnableWindow(isKeyFile)
		browse.Hwnd().EnableWindow(isKeyFile)
	}
	auth.On().CbnSelChange(updateKey)
	var bold uintptr
	focusOnOpen(wnd, name, updateKey, options.update, func() { bold = headingFont(headings) })

	browse.On().BnClicked(func() {
		if f, chosen := pickKeyFile(wnd.Hwnd()); chosen {
			key.SetText(f)
		}
	})
	// A password that is typed here is meant to be kept.
	password.On().EnChange(func() {
		if wnd.Hwnd() != 0 && password.Text() != "" && password.Text() != c.RDPPassword {
			savePassword.SetCheck(true)
		}
	})

	fail := func(field *ui.Edit, text string) {
		if text != "" { // errors start in lower case, a message does not
			text = strings.ToUpper(text[:1]) + text[1:]
		}
		ui.MsgError(wnd, appName, "Cannot save the connection", text)
		if field != nil {
			field.Focus()
			field.SetSelection(0, -1)
		}
	}
	var result config.Connection
	accepted := false
	ok.On().BnClicked(func() {
		r := c
		r.Name = strings.TrimSpace(name.Text())
		r.Group = strings.TrimSpace(group.Text())
		r.SSHHost = strings.TrimSpace(sshHost.Text())
		r.SSHUser = strings.TrimSpace(sshUser.Text())
		r.SSHAuth = authMethods[max(auth.SelectedIndex(), 0)]
		r.SSHKeyFile = strings.TrimSpace(key.Text())
		r.RDPHost = strings.TrimSpace(rdpHost.Text())
		r.RDPUser = strings.TrimSpace(rdpUser.Text())
		r.RDPDomain = strings.TrimSpace(domain.Text())
		r.SavePassword = savePassword.IsChecked()
		r.RDPPassword = ""
		if r.SavePassword {
			r.RDPPassword = password.Text()
		}
		var valid bool
		if r.SSHPort, valid = parsePort(sshPort.Text()); !valid {
			fail(sshPort, "Enter an SSH port between 1 and 65535.")
			return
		}
		if r.RDPPort, valid = parsePort(rdpPort.Text()); !valid {
			fail(rdpPort, "Enter an RDP port between 1 and 65535.")
			return
		}
		var field *ui.Edit
		var text string
		if r.Options, field, text = options.read(); text != "" {
			fail(field, text)
			return
		}
		if err := r.Validate(); err != nil {
			// Validate names no field. The field is the one that, filled
			// in, makes this error go away.
			probes := []struct {
				field *ui.Edit
				fix   func(c *config.Connection)
			}{
				{sshHost, func(c *config.Connection) { c.SSHHost = "host" }},
				{sshUser, func(c *config.Connection) { c.SSHUser = "user" }},
				{rdpHost, func(c *config.Connection) { c.RDPHost = "host" }},
				{key, func(c *config.Connection) { c.SSHKeyFile = "key" }},
				{sshPort, func(c *config.Connection) { c.SSHPort = 22 }},
				{rdpPort, func(c *config.Connection) { c.RDPPort = 3389 }},
				{name, func(c *config.Connection) { c.Name = "name" }},
				{options.letters, func(c *config.Connection) { c.Options.DriveLetters = "" }},
				{options.fixedW, func(c *config.Connection) { c.Options.FixedWidth = 1920 }},
				{options.fixedH, func(c *config.Connection) { c.Options.FixedHeight = 1080 }},
			}
			var offending *ui.Edit
			for _, p := range probes {
				fixed := r
				p.fix(&fixed)
				if e := fixed.Validate(); e == nil || e.Error() != err.Error() {
					offending = p.field
					break
				}
			}
			fail(offending, err.Error())
			return
		}
		result, accepted = r, true
		closeModal(wnd)
	})

	wnd.ShowModal()
	if bold != 0 {
		procDeleteObject.Call(bold)
	}
	return result, accepted
}
