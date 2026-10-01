//go:build !windows

package ui

import (
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Fonts: families are tried in order, so CJK text falls back to a font
// that has the glyphs when the preferred one does not. Names are resolved
// through fontconfig, so they are matched against whatever the desktop has
// installed.
const (
	uiFont      = "Noto Sans CJK SC, Noto Sans, Source Han Sans SC, WenQuanYi Micro Hei, DejaVu Sans, sans-serif"
	defaultMono = "Noto Sans Mono"
	// monoFallbacks are monospace Latin fonts tried after the chosen Western
	// font; cjkFallbacks follow the chosen Chinese font. Go Mono ships with
	// the program, so Latin text always has a monospace font before any CJK
	// (proportional) font is reached.
	monoFallbacks = "Noto Sans Mono, DejaVu Sans Mono, Go Mono"
	cjkFallbacks  = "Noto Sans CJK SC, Source Han Sans SC, WenQuanYi Micro Hei, monospace"
)

// shellOpen opens a file with its default application.
func shellOpen(path string) error {
	if err := exec.Command("xdg-open", path).Start(); err == nil {
		return nil
	}
	return exec.Command("gio", "open", path).Start()
}

// revealInExplorer asks the desktop's file manager to show path, falling
// back to opening its directory.
func revealInExplorer(path string) {
	// The freedesktop FileManager1 interface selects the file itself; only
	// some file managers (Nautilus, Dolphin, Nemo, PCManFM) implement it.
	uri := (&url.URL{Scheme: "file", Path: path}).String()
	cmd := exec.Command("dbus-send", "--session", "--type=method_call",
		"--dest=org.freedesktop.FileManager1", "/org/freedesktop/FileManager1",
		"org.freedesktop.FileManager1.ShowItems",
		"array:string:"+uri, "string:")
	if err := cmd.Run(); err == nil {
		return
	}
	exec.Command("xdg-open", filepath.Dir(path)).Start()
}

// systemDark reports whether the desktop prefers a dark appearance, using
// the freedesktop color-scheme setting where GNOME-compatible desktops
// publish it, then GTK_THEME.
func systemDark() (dark, ok bool) {
	if out, err := exec.Command("gsettings", "get", "org.gnome.desktop.interface", "color-scheme").Output(); err == nil {
		s := strings.TrimSpace(string(out))
		switch {
		case strings.Contains(s, "dark"):
			return true, true
		case strings.Contains(s, "light"), s == "'default'":
			return false, true
		}
	}
	if t := os.Getenv("GTK_THEME"); t != "" {
		return strings.Contains(strings.ToLower(t), "dark"), true
	}
	return false, false
}

// systemFontFamilies lists the installed font families through fontconfig.
func systemFontFamilies() []string {
	out, err := exec.Command("fc-list", ":", "family").Output()
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		// Localized names follow the primary one after commas.
		if i := strings.IndexByte(line, ','); i >= 0 {
			line = line[:i]
		}
		names = append(names, line)
	}
	return cleanFamilies(names)
}
