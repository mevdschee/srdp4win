//go:build windows

package ui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/mevdschee/srdp4win/internal/logging"
	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"
)

// progressDialog runs work on a goroutine and shows a progress window with a
// Cancel button until it finishes; closing the window also cancels. It
// blocks (running the window's message loop, so the app stays responsive)
// and returns work's error.
func (me *mainWindow) progressDialog(title, file string, work func(ctx context.Context, progress func(done, total int64)) error) error {
	ctx, cancel := context.WithCancel(me.ctx)
	defer cancel()
	slog.Debug("operation start", "op", title)

	wnd := newModal(me.wnd, ui.OptsModal().Title(title).Size(ui.Dpi(460, 158)))
	action := ui.NewStatic(wnd, ui.OptsStatic().Text("Preparing...").Position(ui.Dpi(14, 14)).Size(ui.Dpi(432, 20)))
	ui.NewStatic(wnd, ui.OptsStatic().Text(file).Position(ui.Dpi(14, 36)).Size(ui.Dpi(432, 20)).
		CtrlStyle(co.SS_LEFT|co.SS_NOPREFIX|co.SS_PATHELLIPSIS))
	bar := ui.NewProgressBar(wnd, ui.OptsProgressBar().Position(ui.Dpi(14, 62)).Size(ui.Dpi(432, 18)))
	detail := ui.NewStatic(wnd, ui.OptsStatic().Text("").Position(ui.Dpi(14, 88)).Size(ui.Dpi(432, 20)))
	btnCancel := ui.NewButton(wnd, ui.OptsButton().Text("Cancel").Position(ui.Dpi(358, 120)).
		CtrlId(uint16(co.ID_CANCEL)))

	closed := false
	start := time.Now()
	type state struct{ done, total int64 }
	show := func(p state) {
		if closed {
			return
		}
		setStaticText(action, "Downloading...")
		if p.total <= 0 {
			setStaticText(detail, humanSize(p.done))
			return
		}
		bar.SetPos(int(p.done * 1000 / p.total))
		text := fmt.Sprintf("%s of %s", humanSize(p.done), humanSize(p.total))
		if secs := time.Since(start).Seconds(); secs > 1 && p.done > 0 {
			rate := float64(p.done) / secs
			left := time.Duration(float64(p.total-p.done)/rate) * time.Second
			text += fmt.Sprintf("  (%s/s, %s left)", humanSize(int64(rate)), left.Round(time.Second))
		}
		setStaticText(detail, text)
	}

	// Progress arrives on the worker goroutine, possibly very often; show
	// the latest state at most every 100 ms.
	var latest atomic.Pointer[state]
	var scheduled atomic.Bool
	progress := func(done, total int64) {
		latest.Store(&state{done, total})
		if scheduled.CompareAndSwap(false, true) {
			go func() {
				time.Sleep(100 * time.Millisecond)
				me.Post(func() {
					scheduled.Store(false)
					show(*latest.Load())
				})
			}()
		}
	}

	btnCancel.On().BnClicked(func() {
		slog.Info("operation cancelled by user", "op", title)
		cancel()
		setStaticText(action, "Cancelling...")
		btnCancel.Hwnd().EnableWindow(false)
	})
	wnd.On().WmDestroy(func() {
		closed = true
		cancel() // closing the window cancels
	})

	finished := make(chan error, 1)
	wnd.On().WmCreate(func(_ ui.WmCreate) int {
		bar.SetRange(0, 1000)
		go func() {
			err := errors.New("internal error, see the log")
			func() {
				defer logging.Recover(title)
				err = work(ctx, progress)
			}()
			if err != nil {
				slog.Error("operation failed", "op", title, "err", err)
			}
			finished <- err
			me.Post(func() {
				if !closed {
					closeModal(wnd)
				}
			})
		}()
		return 0
	})
	wnd.ShowModal()
	// Normally the work is done when the window closes. If the user closed
	// it, the work was cancelled and ends shortly; keep dispatching messages
	// meanwhile, so a worker waiting on the UI thread cannot deadlock. The
	// worker posts a message after finishing, which ends the wait.
	for {
		select {
		case err := <-finished:
			return err
		default:
		}
		var msg win.MSG
		if r, err := win.GetMessage(&msg, 0, 0, 0); err != nil || r == 0 {
			if r == 0 { // WM_QUIT: hand it back to the main loop
				win.PostQuitMessage(int(msg.WParam))
			}
			return <-finished
		}
		win.TranslateMessage(&msg)
		win.DispatchMessage(&msg)
	}
}
