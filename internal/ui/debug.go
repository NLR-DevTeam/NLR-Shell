package ui

import (
	"image"

	"nlrshell/internal/sshx"
)

// The helpers in this file exist for the off-screen renderer (cmd/shot) and
// tests; the application itself does not use them.

// DialogCount returns the number of open dialogs.
func (a *App) DialogCount() int { return len(a.dialogs) }

// MenuOpen reports whether a popup menu is showing.
func (a *App) MenuOpen() bool { return a.menu != nil }

// Sessions returns the sessions of all open tabs.
func (a *App) Sessions() []*sshx.Session {
	var out []*sshx.Session
	for _, t := range a.sessionViews() {
		out = append(out, t.sess)
	}
	return out
}

// Mouse returns the last pointer position seen by the app.
func (a *App) Mouse() image.Point { return a.mouse }

// FilesPath returns the directory shown in the active tab's file panel.
func (a *App) FilesPath() string {
	if sv := a.current(); sv != nil {
		return sv.files.path
	}
	return ""
}

// ShowTab activates a tab by index; -1 is the home page.
func (a *App) ShowTab(i int) { a.activate(i) }

// OpenTunnels opens the port forwarding dialog for the active tab.
func (a *App) OpenTunnels() {
	if sv := a.current(); sv != nil {
		a.Open(newTunnelDialog(sv))
	}
}

// OpenSettings opens the settings dialog.
func (a *App) OpenSettings() { a.Open(newSettingsDialog(a)) }

// ShowTasks opens or closes the task list of the active tab's file panel.
func (a *App) ShowTasks(on bool) {
	if sv := a.current(); sv != nil {
		sv.files.showTasks = on
	}
}

// OpenRemote opens a remote file of the active session the way a double
// click in the file panel does.
func (a *App) OpenRemote(p string, size int64) {
	if sv := a.current(); sv != nil {
		a.openRemote(sv.sess, p, size)
	}
}

// TabCount returns the number of open tabs.
func (a *App) TabCount() int { return len(a.tabs) }
