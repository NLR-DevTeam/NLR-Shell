//go:build !windows && !android

package ui

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gioui.org/widget"
)

// knownPlaces are the special folders the file picker names and pins by
// default, in their default order: home, then the XDG user directories,
// which are localized ("~/桌面") on many desktops.
func knownPlaces() []place {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	dirs := xdgUserDirs(home)
	out := []place{{path: home, name: "主目录", icon: icHome}}
	for _, k := range []struct {
		key, fallback, name string
		ic                  *widget.Icon
	}{
		{"XDG_DESKTOP_DIR", "Desktop", "桌面", icDesktop},
		{"XDG_DOWNLOAD_DIR", "Downloads", "下载", icDownload},
		{"XDG_DOCUMENTS_DIR", "Documents", "文档", icDocument},
	} {
		p := dirs[k.key]
		if p == "" {
			p = filepath.Join(home, k.fallback)
		}
		// XDG points unused directories at the home directory itself.
		if samePath(p, home) {
			continue
		}
		out = append(out, place{path: p, name: k.name, icon: k.ic})
	}
	return out
}

// xdgUserDirs reads user-dirs.dirs, whose lines look like
// XDG_DESKTOP_DIR="$HOME/Desktop".
func xdgUserDirs(home string) map[string]string {
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	out := map[string]string{}
	f, err := os.Open(filepath.Join(cfg, "user-dirs.dirs"))
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(line, "#") {
			continue
		}
		v = strings.Trim(v, `"`)
		switch {
		case v == "$HOME":
			v = home
		case strings.HasPrefix(v, "$HOME/"):
			v = filepath.Join(home, v[len("$HOME/"):])
		case !strings.HasPrefix(v, "/"):
			continue
		}
		out[k] = v
	}
	return out
}

// listDrives returns the root file system and the volumes mounted where
// desktops mount removable media and users mount extra disks.
func listDrives() []place {
	out := []place{{path: "/", name: "文件系统", icon: icDrive}}
	f, err := os.Open("/proc/self/mounts")
	if err != nil {
		return out
	}
	defer f.Close()
	seen := map[string]bool{"/": true}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		mp := unescapeMount(fields[1])
		if seen[mp] {
			continue
		}
		ic := icDrive
		switch {
		case strings.HasPrefix(mp, "/media/"), strings.HasPrefix(mp, "/run/media/"):
			ic = icUSB
		case strings.HasPrefix(mp, "/mnt/"):
		default:
			continue
		}
		seen[mp] = true
		out = append(out, place{path: mp, name: filepath.Base(mp), icon: ic})
	}
	return out
}

// unescapeMount decodes the octal escapes (\040 for a space) the kernel
// writes into mount points.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}
