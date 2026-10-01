// Package term implements an xterm-compatible terminal emulator: an escape
// sequence parser driving a cell grid with scrollback. It has no UI or I/O
// dependencies; the owner feeds it bytes from the remote side and reads the
// grid back for rendering.
package term

import "unicode"

// Color is a terminal color. The zero value means "default" (the theme's
// foreground or background, depending on where it is used).
type Color uint32

const (
	colorKindMask Color = 0xff000000
	colorIndexed  Color = 0x01000000
	colorRGB      Color = 0x02000000
)

// ColorDefault is the theme default color.
const ColorDefault Color = 0

// Indexed returns a color from the 256-color palette.
func Indexed(i uint8) Color { return colorIndexed | Color(i) }

// RGB returns a 24-bit color.
func RGB(r, g, b uint8) Color {
	return colorRGB | Color(r)<<16 | Color(g)<<8 | Color(b)
}

// IsDefault reports whether c is the theme default.
func (c Color) IsDefault() bool { return c&colorKindMask == 0 }

// Index returns the palette index if c is an indexed color.
func (c Color) Index() (uint8, bool) {
	if c&colorKindMask == colorIndexed {
		return uint8(c), true
	}
	return 0, false
}

// RGB returns the components if c is a 24-bit color.
func (c Color) RGB() (r, g, b uint8, ok bool) {
	if c&colorKindMask == colorRGB {
		return uint8(c >> 16), uint8(c >> 8), uint8(c), true
	}
	return 0, 0, 0, false
}

// Attr is a set of cell attributes.
type Attr uint16

const (
	AttrBold Attr = 1 << iota
	AttrFaint
	AttrItalic
	AttrUnderline
	AttrBlink
	AttrReverse
	AttrHidden
	AttrStrike
	// AttrWide marks the first cell of a double-width character.
	AttrWide
	// AttrWideTail marks the second (empty) cell of a double-width character.
	AttrWideTail
)

// Cell is one character position in the grid.
type Cell struct {
	R    rune
	FG   Color
	BG   Color
	Attr Attr
}

// Line is one row of cells. Wrapped is set when the line was terminated by
// auto-wrap rather than a newline, so that copying text joins it with the
// following line.
type Line struct {
	Cells   []Cell
	Wrapped bool
}

// Text returns the line contents with trailing blanks removed.
func (l *Line) Text() string {
	n := len(l.Cells)
	for n > 0 && (l.Cells[n-1].R == 0 || l.Cells[n-1].R == ' ') && l.Cells[n-1].Attr&AttrWideTail == 0 {
		n--
	}
	buf := make([]rune, 0, n)
	for i := 0; i < n; i++ {
		c := &l.Cells[i]
		if c.Attr&AttrWideTail != 0 {
			continue
		}
		if c.R == 0 {
			buf = append(buf, ' ')
		} else {
			buf = append(buf, c.R)
		}
	}
	return string(buf)
}

// RuneWidth reports how many cells r occupies: 0 for combining and
// formatting characters, 2 for East Asian wide characters and emoji, 1
// otherwise.
func RuneWidth(r rune) int {
	if r < 0x300 {
		return 1
	}
	if r >= 0x1100 && isWide(r) {
		return 2
	}
	if r == 0x200b || r == 0x200c || r == 0x200d || r == 0xfeff ||
		(r >= 0xfe00 && r <= 0xfe0f) || unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf) {
		return 0
	}
	return 1
}

func isWide(r rune) bool {
	switch {
	case r <= 0x115f, // Hangul Jamo
		r >= 0x2e80 && r <= 0x303e,   // CJK radicals, punctuation
		r >= 0x3041 && r <= 0x33ff,   // Kana, CJK symbols
		r >= 0x3400 && r <= 0x4dbf,   // CJK ext A
		r >= 0x4e00 && r <= 0x9fff,   // CJK unified
		r >= 0xa000 && r <= 0xa4cf,   // Yi
		r >= 0xa960 && r <= 0xa97f,   // Hangul Jamo ext A
		r >= 0xac00 && r <= 0xd7a3,   // Hangul syllables
		r >= 0xf900 && r <= 0xfaff,   // CJK compatibility
		r >= 0xfe30 && r <= 0xfe6f,   // CJK compatibility forms
		r >= 0xff01 && r <= 0xff60,   // Fullwidth forms
		r >= 0xffe0 && r <= 0xffe6,   // Fullwidth signs
		r >= 0x1f300 && r <= 0x1f64f, // Emoji
		r >= 0x1f680 && r <= 0x1f6ff, // Transport symbols
		r >= 0x1f900 && r <= 0x1f9ff, // Supplemental symbols
		r >= 0x20000 && r <= 0x3fffd: // CJK ext B..
		return true
	}
	return false
}
