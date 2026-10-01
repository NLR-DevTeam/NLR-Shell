//go:build !windows

package ui

import (
	"net/url"
	"os/exec"
	"path/filepath"
)

// Fonts: families are tried in order, so CJK text falls back to a font
// that has the glyphs when the preferred one does not. Names are resolved
// through fontconfig, so they are matched against whatever the desktop has
// installed.
const (
	uiFont        = "Noto Sans CJK SC, Noto Sans, Source Han Sans SC, WenQuanYi Micro Hei, DejaVu Sans, sans-serif"
	defaultMono   = "Noto Sans Mono"
	monoFallbacks = "Noto Sans Mono, DejaVu Sans Mono, Noto Sans CJK SC, Go Mono, monospace"
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
