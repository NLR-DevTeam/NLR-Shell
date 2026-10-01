// NLR Shell is a native Windows SSH client: terminal, system monitor, SFTP
// file manager and port forwarding in one window.
package main

import (
	"os"
	"sync/atomic"

	"gioui.org/app"
	"gioui.org/io/system"
	"gioui.org/op"
	"gioui.org/unit"

	"nlrshell/internal/store"
	"nlrshell/internal/ui"
)

type host struct {
	w    *app.Window
	hwnd atomic.Uintptr
}

func (h *host) Invalidate()             { h.w.Invalidate() }
func (h *host) Perform(a system.Action) { h.w.Perform(a) }
func (h *host) HWND() uintptr           { return h.hwnd.Load() }

func main() {
	st := store.Open(store.Dir())
	set := st.Settings()

	w := new(app.Window)
	opts := []app.Option{
		app.Title("NLR Shell"),
		app.Size(unit.Dp(set.WindowWidth), unit.Dp(set.WindowHeight)),
		app.MinSize(760, 480),
		app.Decorated(false),
	}
	if set.Maximized {
		opts = append(opts, app.Maximized.Option())
	}
	w.Option(opts...)

	h := &host{w: w}
	a := ui.New(h, st)
	go func() {
		run(w, h, a)
		os.Exit(0)
	}()
	app.Main()
}

func run(w *app.Window, h *host, a *ui.App) {
	var ops op.Ops
	var metric unit.Metric
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			a.Shutdown()
			return
		case app.Win32ViewEvent:
			h.hwnd.Store(e.HWND)
			if e.Valid() {
				// Window-affecting Win32 calls must run on the window's own
				// thread: it is parked while we handle this event, so a
				// cross-thread call that sends it a message would deadlock.
				hwnd := e.HWND
				w.Run(func() { ui.InstallDropHandler(hwnd, a.DropFiles) })
			}
		case app.ConfigEvent:
			a.SetMaximized(e.Config.Mode == app.Maximized)
			switch {
			case e.Config.Mode == app.Maximized:
				a.RememberWindow(0, 0, true)
			case e.Config.Mode == app.Windowed && metric.PxPerDp > 0:
				a.RememberWindow(int(metric.PxToDp(e.Config.Size.X)), int(metric.PxToDp(e.Config.Size.Y)), false)
			}
		case app.FrameEvent:
			metric = e.Metric
			gtx := app.NewContext(&ops, e)
			a.Layout(gtx)
			e.Frame(gtx.Ops)
		}
	}
}
