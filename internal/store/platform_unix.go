//go:build !windows

package store

import (
	"os"
	"path/filepath"
	"strings"
)

// appDir is the per-user directory that holds the config file, the
// known_hosts file and the secret key. It follows the XDG convention of a
// lowercase name without spaces.
const appDir = "nlrshell"

// defaultFont is the terminal font used until the user picks another one.
// fontconfig resolves the name and falls back to the desktop's own default
// when it is missing.
const defaultFont = "Noto Sans Mono"

// defaultCJKFont is the Chinese font: the UI font, and the terminal
// fallback for characters the Western font lacks.
const defaultCJKFont = "Noto Sans CJK SC"

// defaultDownloadDir returns the directory downloads go to. It follows the
// XDG_DOWNLOAD_DIR entry of user-dirs.dirs, which desktop environments
// localize (for example to ~/下载), and falls back to ~/Downloads.
func defaultDownloadDir(home string) string {
	fallback := filepath.Join(home, "Downloads")
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	b, err := os.ReadFile(filepath.Join(cfg, "user-dirs.dirs"))
	if err != nil {
		return fallback
	}
	for line := range strings.Lines(string(b)) {
		v, ok := strings.CutPrefix(strings.TrimSpace(line), "XDG_DOWNLOAD_DIR=")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"`)
		switch {
		case v == "$HOME" || v == "$HOME/":
			return home
		case strings.HasPrefix(v, "$HOME/"):
			return filepath.Join(home, v[len("$HOME/"):])
		case strings.HasPrefix(v, "/"):
			return v
		}
	}
	return fallback
}

// dataDir is the per-user configuration directory.
func dataDir() string { return userDir() }
