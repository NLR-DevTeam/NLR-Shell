//go:build !windows

package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultDownloadDir(t *testing.T) {
	home := t.TempDir()
	cfg := filepath.Join(home, ".config")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", cfg)
	write := func(s string) {
		if err := os.WriteFile(filepath.Join(cfg, "user-dirs.dirs"), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Desktop environments localize the entry, so the name is not fixed.
	write("# comment\nXDG_DOWNLOAD_DIR=\"$HOME/下载\"\n")
	if got := defaultDownloadDir(home); got != filepath.Join(home, "下载") {
		t.Fatalf("localized: %q", got)
	}
	write("XDG_DOWNLOAD_DIR=\"/media/data/dl\"\n")
	if got := defaultDownloadDir(home); got != "/media/data/dl" {
		t.Fatalf("absolute: %q", got)
	}
	// An unrelated entry must not be mistaken for the download directory.
	write("XDG_DESKTOP_DIR=\"$HOME/Desktop\"\n")
	if got := defaultDownloadDir(home); got != filepath.Join(home, "Downloads") {
		t.Fatalf("fallback: %q", got)
	}
	os.Remove(filepath.Join(cfg, "user-dirs.dirs"))
	if got := defaultDownloadDir(home); got != filepath.Join(home, "Downloads") {
		t.Fatalf("missing file: %q", got)
	}
}
