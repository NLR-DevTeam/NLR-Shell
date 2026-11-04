package ui

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/font/opentype"

	"nlrshell/internal/droid"
)

// Fonts: families are tried in order, so CJK text falls back to a font
// that has the glyphs when the preferred one does not. Android ships Noto
// Sans CJK and Droid Sans Mono; vendor ROMs that replace them still match
// the generic names at the end.
const (
	uiFont      = "Noto Sans CJK SC, Roboto, sans-serif"
	defaultMono = "Droid Sans Mono"
	// monoFallbacks are monospace Latin fonts tried after the chosen Western
	// font; cjkFallbacks follow the chosen Chinese font. Go Mono ships with
	// the program, so Latin text always has a monospace font before any CJK
	// (proportional) font is reached.
	monoFallbacks  = "Droid Sans Mono, Go Mono"
	cjkFallbacks   = "Noto Sans CJK SC, sans-serif, monospace"
	emojiFallbacks = "Noto Color Emoji, emoji"
	defaultEmoji   = "Noto Color Emoji"
)

// shellOpen would open a file with another app; Android needs a content
// URI for that, so files open in the built-in viewers only.
func shellOpen(string) error {
	return errors.New("Android 上暂不支持用其他应用打开")
}

// revealInExplorer has no file manager to ask on Android.
func revealInExplorer(string) {}

// systemDark reports whether Android uses its dark theme.
func systemDark() (dark, ok bool) { return droid.NightMode() }

// systemFontFamilies lists the families of the fonts in /system/fonts.
func systemFontFamilies() []string {
	var names []string
	for _, dir := range []string{"/system/fonts", "/product/fonts"} {
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			f, err := os.Open(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			lds, err := opentype.NewLoaders(f)
			if err == nil {
				for _, ld := range lds {
					d, _ := font.Describe(ld, nil)
					names = append(names, d.Family)
				}
			}
			f.Close()
		}
	}
	return cleanFamilies(names)
}
