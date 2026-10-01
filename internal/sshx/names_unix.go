//go:build !windows

package sshx

import "strings"

// safeName replaces characters that are not allowed in POSIX file names.
// Everything else, including backslashes and colons, is legal there and is
// left alone.
func safeName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r == 0 || r == '/' {
			return '_'
		}
		return r
	}, name)
	if name == "" || name == "." || name == ".." {
		name = "_"
	}
	return name
}
