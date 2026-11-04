package store

import (
	"os"
	"path/filepath"
)

// appDir is the directory under the app's private files directory (which
// Gio exposes as XDG_CONFIG_HOME) that holds the config file, the
// known_hosts file and the secret key.
const appDir = "nlrshell"

// defaultFont is the terminal font used until the user picks another one.
const defaultFont = "Droid Sans Mono"

// defaultCJKFont is the Chinese font: the UI font, and the terminal
// fallback for characters the Western font lacks.
const defaultCJKFont = "Noto Sans CJK SC"

const defaultEmojiFont = "Noto Color Emoji"

// defaultDownloadDir returns the Download folder of the shared storage.
func defaultDownloadDir(string) string {
	root := os.Getenv("EXTERNAL_STORAGE")
	if root == "" {
		root = "/storage/emulated/0"
	}
	return filepath.Join(root, "Download")
}

// dataDir is the per-user configuration directory.
func dataDir() string { return userDir() }
