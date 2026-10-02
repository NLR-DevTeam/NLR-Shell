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

// WindowButtonsHidden reports whether the titlebar leaves the window
// buttons to the platform because it draws its own decorations.
func (a *App) WindowButtonsHidden() bool { return a.decorated }

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

// SetLook changes the appearance and accent settings and applies them.
func (a *App) SetLook(appearance, accent string) {
	a.set.Appearance, a.set.Accent = appearance, accent
	a.applyTheme()
}

// OpenPermissions opens the permission dialog for a file of the active
// tab's current directory.
func (a *App) OpenPermissions(name string) {
	if sv := a.current(); sv != nil {
		for _, e := range sv.files.entries {
			if e.Name == name {
				sv.files.chmod(e)
			}
		}
	}
}

// ShowQuickCommands opens or closes the quick command panel.
func (a *App) ShowQuickCommands(on bool) {
	if sv := a.current(); sv != nil {
		sv.cmd.quickOpen = on
	}
}

// SetBackground changes the background image setting and loads it.
func (a *App) SetBackground(path string) {
	a.set.Background = path
	a.loadBackground()
}

// BackgroundLoaded reports whether a background image is showing.
func (a *App) BackgroundLoaded() bool { return a.bg.size.X > 0 }

// OpenFontList opens the Western font dropdown of an open settings dialog.
func (a *App) OpenFontList() {
	for _, d := range a.dialogs {
		if s, ok := d.(*settingsDialog); ok {
			s.fontFamily.open = true
		}
	}
}

// TabTitles returns the titles of the open tabs in order.
func (a *App) TabTitles() []string {
	out := make([]string, len(a.tabs))
	for i, t := range a.tabs {
		out[i] = t.title()
	}
	return out
}

// FileSelection returns the names selected in the active tab's file panel,
// in display order.
func (a *App) FileSelection() []string {
	var out []string
	if sv := a.current(); sv != nil {
		for _, e := range sv.files.selection() {
			out = append(out, e.Name)
		}
	}
	return out
}

// SetPrivacy switches privacy mode; it fades in over the next frames.
func (a *App) SetPrivacy(on bool) { a.set.Privacy = on }
