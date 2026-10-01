package sshx

import "strings"

// safeName replaces characters that are not allowed in Windows file names.
func safeName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.TrimRight(name, " .")
	if name == "" {
		name = "_"
	}
	return name
}
