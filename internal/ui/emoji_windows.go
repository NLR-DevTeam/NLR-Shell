package ui

import (
	"os"
	"path/filepath"
	"sync"

	"gioui.org/font"
	"gioui.org/font/opentype"
	"golang.org/x/sys/windows"
)

var (
	emojiOnce  sync.Once
	emojiFaces []font.FontFace
)

// Load the Windows color emoji face explicitly so the default stays
// available even when system font discovery is unavailable.
func emojiFontCollection() []font.FontFace {
	emojiOnce.Do(func() {
		dir, err := windows.GetWindowsDirectory()
		if err != nil {
			return
		}
		data, err := os.ReadFile(filepath.Join(dir, "Fonts", "seguiemj.ttf"))
		if err != nil {
			return
		}
		face, err := opentype.Parse(data)
		if err != nil {
			return
		}
		emojiFaces = []font.FontFace{{Font: face.Font(), Face: face}}
	})
	return emojiFaces
}
