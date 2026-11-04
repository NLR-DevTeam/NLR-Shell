package main

import (
	"gioui.org/app"
	"gioui.org/io/event"

	"nlrshell/internal/droid"
	"nlrshell/internal/ui"
)

// platformEvent hands the Android view to the droid package, which needs
// the activity behind it for permission requests and the like. The view
// goes away (and comes back) when Android recreates the activity; the Go
// side and its sessions live on.
func platformEvent(h *host, a *ui.App, e event.Event) {
	if v, ok := e.(app.AndroidViewEvent); ok {
		droid.SetView(v.View)
		if v.Valid() {
			a.ViewAttached()
		}
	}
}
