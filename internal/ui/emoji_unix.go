//go:build !windows

package ui

import "gioui.org/font"

// Linux emoji fonts are resolved through the system font map. Noto Color
// Emoji uses bitmap glyphs, which Gio can already paint.
func emojiFontCollection() []font.FontFace { return nil }
