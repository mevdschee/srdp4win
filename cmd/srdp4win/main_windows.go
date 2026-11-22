// Command srdp4win is a remote desktop client for Windows whose sessions
// always go through an SSH tunnel.
//
//	srdp4win [--fake]
//
// --fake uses the fake session instead of the Windows RDP control (as happens
// by itself under Wine, where the control does not exist).
package main

import (
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/mevdschee/srdp4win/internal/config"
	"github.com/mevdschee/srdp4win/internal/logging"
	"github.com/mevdschee/srdp4win/internal/ui"
	wui "github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"
)

const appName = "srdp4win"

// version is set at build time by release.sh (-ldflags -X main.version=...).
var version = "dev"

func main() {
	runtime.LockOSThread() // the Win32 GUI is single-threaded

	useFake := false
	for _, arg := range os.Args[1:] {
		switch strings.ToLower(arg) {
		case "--fake", "-fake", "/fake":
			useFake = true
		default:
			wui.MsgError(nil, appName, "Unknown argument", arg+"\n\nUsage: srdp4win [--fake]")
			os.Exit(1)
		}
	}

	dir, err := config.Dir()
	if err != nil {
		wui.MsgError(nil, appName, "No settings folder", err.Error())
		os.Exit(1)
	}
	logFile, closeLog, err := logging.Setup(dir)
	if err != nil {
		wui.MsgError(nil, appName, "Could not open the log file", err.Error())
	} else {
		defer closeLog()
	}
	commit := ""
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" {
				commit = s.Value
			}
		}
	}
	slog.Info("start", "version", version, "commit", commit, "go", runtime.Version(), "settings", dir, "log", logFile, "fake", useFake)
	defer func() {
		if r := recover(); r != nil {
			slog.Error("crash", "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
			wui.MsgError(nil, appName, "srdp4win crashed", fmt.Sprintf("%v\n\nDetails are in %s", r, logFile))
			os.Exit(2)
		}
	}()

	// OLE (not only COM) is needed to host the RDP control.
	if err := win.OleInitialize(); err != nil {
		slog.Error("OleInitialize", "err", err)
	}
	defer win.OleUninitialize()

	// A connections file that cannot be read is kept: the first save moves
	// it to connections.dat.bad.
	store, err := config.LoadStore(dir)
	if err != nil {
		slog.Error("read connections", "err", err)
		wui.MsgError(nil, appName, "Could not read the connections",
			err.Error()+"\n\nsrdp4win starts without connections. The file is kept as connections.dat.bad when you save a new one.")
		store = &config.Store{}
	}
	prefs := config.LoadPreferences(dir)

	code := ui.Run(dir, store, &prefs, version, useFake)
	slog.Info("exit", "code", code)
	os.Exit(code)
}
