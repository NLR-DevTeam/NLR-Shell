package store

import (
	"io/fs"
	"os"
	"path/filepath"
)

// appDir is the per-user directory that holds the config file, the
// known_hosts file and the secret key.
const appDir = "NLR Shell"

// defaultFont is the terminal font used until the user picks another one.
const defaultFont = "Cascadia Mono"

// defaultCJKFont is the Chinese font: the UI font, and the terminal
// fallback for characters the Western font lacks.
const defaultCJKFont = "Microsoft YaHei UI"

// defaultDownloadDir returns the directory downloads go to.
func defaultDownloadDir(home string) string { return filepath.Join(home, "Downloads") }

// dataDir returns "data" next to the executable, so that copying the
// program's folder takes the saved connections along. Data from older
// versions, kept in %APPDATA%\NLR Shell, is moved there on the first run.
// When the program's folder cannot be written (installed under Program
// Files, for one), the per-user directory is used as before.
func dataDir() string {
	legacy := userDir()
	exe, err := os.Executable()
	if err != nil {
		return legacy
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	dir := filepath.Join(filepath.Dir(exe), "data")
	if err := os.MkdirAll(dir, 0o700); err != nil || !writable(dir) {
		return legacy
	}
	migrateDir(legacy, dir)
	return dir
}

// writable reports whether files can be created in dir.
func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

// migrateDir copies the old per-user data into dir when dir holds no data
// yet, then deletes the old directory. Nothing is deleted unless every
// file was copied.
func migrateDir(old, dir string) {
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err == nil {
		return
	}
	if _, err := os.Stat(filepath.Join(old, "config.json")); err != nil {
		return
	}
	if err := copyTree(old, dir); err != nil {
		return
	}
	os.RemoveAll(old)
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o600)
	})
}
