//go:build !windows

package main

import (
	"gioui.org/io/event"

	"nlrshell/internal/ui"
)

// platformEvent handles the window events that only exist on platforms
// without native window handles. X11 and Wayland do not expose one, so
// there is nothing to do here; file drops from a file manager are not
// delivered by Gio on those backends.
func platformEvent(*host, *ui.App, event.Event) {}
