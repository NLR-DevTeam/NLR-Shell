package ui

import (
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// parseLocalPaths extracts local file paths from clipboard text. File
// managers put either a text/uri-list or plain paths on the clipboard, one
// entry per line; anything else may be mixed in and is skipped.
func parseLocalPaths(s string) []string {
	home, _ := os.UserHomeDir()
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Some applications quote entries that contain spaces.
		if len(line) >= 2 && strings.HasPrefix(line, `"`) && strings.HasSuffix(line, `"`) {
			line = strings.TrimSuffix(strings.TrimPrefix(line, `"`), `"`)
		}
		switch {
		case strings.HasPrefix(line, "file://"):
			if p, ok := fileURIPath(line); ok {
				add(p)
			}
		case line == "~" && home != "":
			add(home)
		case home != "" && (strings.HasPrefix(line, "~/") || strings.HasPrefix(line, `~\`)):
			add(filepath.Join(home, strings.TrimLeft(line[1:], `/\`)))
		case filepath.IsAbs(line):
			add(line)
		}
	}
	return out
}

// fileURIPath converts a file:// URI into a path, rejecting hosts other than
// the local machine.
func fileURIPath(s string) (string, bool) {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "file" || u.User != nil {
		return "", false
	}
	if u.Host != "" && u.Host != "localhost" {
		return "", false
	}
	p := u.Path
	if p == "" {
		return "", false
	}
	if runtime.GOOS == "windows" {
		// file:///C:/dir parses into /C:/dir.
		p = filepath.FromSlash(strings.TrimPrefix(p, "/"))
	}
	// A URI without a drive on Windows does not name a local file.
	if !filepath.IsAbs(p) {
		return "", false
	}
	return p, true
}
