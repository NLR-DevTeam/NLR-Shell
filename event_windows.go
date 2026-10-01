package main

import (
	"gioui.org/app"
	"gioui.org/io/event"

	"nlrshell/internal/ui"
)

// platformEvent handles the window events that only exist on Windows.
func platformEvent(h *host, a *ui.App, e event.Event) {
	v, ok := e.(app.Win32ViewEvent)
	if !ok {
		return
	}
	h.hwnd.Store(v.HWND)
	if !v.Valid() {
		return
	}
	// Window-affecting Win32 calls must run on the window's own thread: it
	// is parked while we handle this event, so a cross-thread call that
	// sends it a message would deadlock.
	hwnd := v.HWND
	h.w.Run(func() { ui.InstallDropHandler(hwnd, a.DropFiles) })
}
