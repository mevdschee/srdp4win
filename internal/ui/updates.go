//go:build windows

package ui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mevdschee/srdp4win/internal/logging"
	"github.com/mevdschee/srdp4win/internal/update"
	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
)

// checkForUpdates runs when the user chooses Check for updates: it asks
// GitHub for the latest release and offers to install it when it is newer.
// Nothing is checked automatically.
func (me *mainWindow) checkForUpdates() {
	if me.checking {
		return
	}
	if !update.IsRelease(me.version) {
		ui.MsgOk(me.wnd, "Update", "Development build",
			"This is a development build ("+me.version+"). Updates are only for installed releases.")
		return
	}
	me.checking = true
	setStaticText(me.statusLeft, "Checking for updates...")
	go func() {
		var r update.Release
		var ok bool
		err := errors.New("internal error, see the log")
		func() {
			defer logging.Recover("update check")
			r, ok, err = update.Latest(me.ctx, me.version)
		}()
		me.Post(func() {
			me.checking = false
			me.updateStatus()
			switch {
			case err != nil:
				slog.Error("update check failed", "err", err)
				ui.MsgError(me.wnd, "Update", "Could not check for updates", err.Error())
			case !ok || !update.Newer(r.Version, me.version):
				slog.Info("no update", "latest", r.Version, "current", me.version)
				ui.MsgOk(me.wnd, "Update", appName+" is up to date", "You have the latest version ("+me.version+").")
			default:
				slog.Info("update available", "latest", r.Version, "current", me.version)
				me.offerUpdate(r)
			}
		})
	}()
}

// installed reports whether this is the copy the MSI installed, in
// %LOCALAPPDATA%\Programs\srdp4win, and not the portable exe. A portable copy
// cannot update itself: the MSI would install a second copy next to it.
func installed() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "srdp4win")
	return strings.EqualFold(filepath.Clean(filepath.Dir(exe)), filepath.Clean(dir))
}

func (me *mainWindow) offerUpdate(r update.Release) {
	if !installed() {
		if ui.MsgOkCancel(me.wnd, "Update", "srdp4win "+r.Version+" is available",
			fmt.Sprintf("You have %s. This copy is not installed, so it cannot update itself. Open the download page?",
				me.version), "Open") {
			openURL(me.wnd.Hwnd(), repoURL+"/releases/latest")
		}
		return
	}
	text := fmt.Sprintf("You have %s. Download and install %s now? %s closes during the installation.",
		me.version, r.Version, appName)
	if me.TabCount() > 0 {
		text += " Open sessions are disconnected."
	}
	if !ui.MsgOkCancel(me.wnd, "Update", appName+" "+r.Version+" is available", text, "Install") {
		return
	}
	var path string
	err := me.progressDialog("Downloading update", r.MsiName, func(ctx context.Context, progress func(done, total int64)) error {
		var err error
		path, err = update.Download(ctx, r, os.TempDir(), progress)
		return err
	})
	if errors.Is(err, context.Canceled) {
		return
	}
	if err == nil {
		// msiexec shows the installer; the MSI replaces the running version,
		// so close to release srdp4win.exe.
		log := filepath.Join(os.TempDir(), appName+"-update.log")
		err = exec.Command("msiexec", "/i", path, "/l*v", log).Start()
	}
	if err != nil {
		slog.Error("update failed", "err", err)
		ui.MsgError(me.wnd, "Update", "Could not install the update", err.Error())
		return
	}
	slog.Info("installing update, closing", "msi", path)
	// The user agreed to end the sessions: close without asking again.
	for _, p := range me.pages[1:] {
		me.ClosePage(p)
	}
	me.wnd.Hwnd().PostMessage(co.WM_CLOSE, 0, 0)
}
