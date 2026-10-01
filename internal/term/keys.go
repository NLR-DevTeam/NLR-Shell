package term

import (
	"fmt"
	"strings"
)

// Key identifies a non-text key.
type Key int

const (
	KeyNone Key = iota
	KeyUp
	KeyDown
	KeyRight
	KeyLeft
	KeyHome
	KeyEnd
	KeyInsert
	KeyDelete
	KeyPageUp
	KeyPageDown
	KeyEnter
	KeyBackspace
	KeyTab
	KeyEscape
	KeyF1
	KeyF2
	KeyF3
	KeyF4
	KeyF5
	KeyF6
	KeyF7
	KeyF8
	KeyF9
	KeyF10
	KeyF11
	KeyF12
)

// Mods is a set of keyboard modifiers.
type Mods uint8

const (
	ModShift Mods = 1 << iota
	ModAlt
	ModCtrl
)

func (m Mods) code() int { return 1 + int(m) }

// EncodeKey returns the byte sequence for a special key, honoring the
// application cursor mode.
func (t *Terminal) EncodeKey(k Key, m Mods) []byte {
	t.mu.Lock()
	app := t.appCursor
	t.mu.Unlock()

	esc := func(s string) []byte {
		if m&ModAlt != 0 {
			return []byte("\x1b" + s)
		}
		return []byte(s)
	}
	switch k {
	case KeyEnter:
		return esc("\r")
	case KeyBackspace:
		if m&ModCtrl != 0 {
			return esc("\x08")
		}
		return esc("\x7f")
	case KeyTab:
		if m&ModShift != 0 {
			return []byte("\x1b[Z")
		}
		return esc("\t")
	case KeyEscape:
		return esc("\x1b")
	}

	letter := func(c byte) []byte {
		if m != 0 {
			return []byte(fmt.Sprintf("\x1b[1;%d%c", m.code(), c))
		}
		if app {
			return []byte{0x1b, 'O', c}
		}
		return []byte{0x1b, '[', c}
	}
	tilde := func(n int) []byte {
		if m != 0 {
			return []byte(fmt.Sprintf("\x1b[%d;%d~", n, m.code()))
		}
		return []byte(fmt.Sprintf("\x1b[%d~", n))
	}
	ss3 := func(c byte) []byte {
		if m != 0 {
			return []byte(fmt.Sprintf("\x1b[1;%d%c", m.code(), c))
		}
		return []byte{0x1b, 'O', c}
	}
	switch k {
	case KeyUp:
		return letter('A')
	case KeyDown:
		return letter('B')
	case KeyRight:
		return letter('C')
	case KeyLeft:
		return letter('D')
	case KeyHome:
		return letter('H')
	case KeyEnd:
		return letter('F')
	case KeyInsert:
		return tilde(2)
	case KeyDelete:
		return tilde(3)
	case KeyPageUp:
		return tilde(5)
	case KeyPageDown:
		return tilde(6)
	case KeyF1:
		return ss3('P')
	case KeyF2:
		return ss3('Q')
	case KeyF3:
		return ss3('R')
	case KeyF4:
		return ss3('S')
	case KeyF5:
		return tilde(15)
	case KeyF6:
		return tilde(17)
	case KeyF7:
		return tilde(18)
	case KeyF8:
		return tilde(19)
	case KeyF9:
		return tilde(20)
	case KeyF10:
		return tilde(21)
	case KeyF11:
		return tilde(23)
	case KeyF12:
		return tilde(24)
	}
	return nil
}

// EncodeCtrl returns the control code produced by Ctrl plus the given
// printable ASCII character, or false if there is none.
func EncodeCtrl(c byte) (byte, bool) {
	switch {
	case c >= 'a' && c <= 'z':
		return c - 'a' + 1, true
	case c >= 'A' && c <= 'Z':
		return c - 'A' + 1, true
	case c == ' ' || c == '@' || c == '2':
		return 0, true
	case c == '[' || c == '3':
		return 0x1b, true
	case c == '\\' || c == '4':
		return 0x1c, true
	case c == ']' || c == '5':
		return 0x1d, true
	case c == '^' || c == '6':
		return 0x1e, true
	case c == '_' || c == '-' || c == '/' || c == '7':
		return 0x1f, true
	case c == '?' || c == '8':
		return 0x7f, true
	}
	return 0, false
}

// EncodePaste prepares pasted text for the remote side: line endings become
// carriage returns and the text is bracketed if the application asked for it.
func (t *Terminal) EncodePaste(s string) []byte {
	t.mu.Lock()
	bracket := t.bracketedPaste
	t.mu.Unlock()
	s = strings.ReplaceAll(s, "\r\n", "\r")
	s = strings.ReplaceAll(s, "\n", "\r")
	if bracket {
		s = strings.ReplaceAll(s, "\x1b[201~", "")
		return []byte("\x1b[200~" + s + "\x1b[201~")
	}
	return []byte(s)
}

// Mouse button codes for EncodeMouse.
const (
	MouseLeft      = 0
	MouseMiddle    = 1
	MouseRight     = 2
	MouseNoButton  = 3
	MouseWheelUp   = 64
	MouseWheelDown = 65
)

// EncodeMouse returns the report for a mouse event at cell (x, y), or nil if
// the application has not asked for it. motion marks pointer movement and
// release a button release.
func (t *Terminal) EncodeMouse(button, x, y int, m Mods, motion, release bool) []byte {
	t.mu.Lock()
	mode, sgr := t.mouseMode, t.mouseSGR
	cols, rows := t.cols, t.rows
	t.mu.Unlock()

	switch mode {
	case MouseOff:
		return nil
	case MouseX10:
		if motion || release || button >= MouseWheelUp {
			return nil
		}
	case MouseNormal:
		if motion {
			return nil
		}
	case MouseButton:
		if motion && button == MouseNoButton {
			return nil
		}
	}
	x, y = clamp(x, 0, cols-1), clamp(y, 0, rows-1)
	cb := button
	if m&ModShift != 0 {
		cb |= 4
	}
	if m&ModAlt != 0 {
		cb |= 8
	}
	if m&ModCtrl != 0 {
		cb |= 16
	}
	if motion {
		cb |= 32
	}
	if sgr {
		final := 'M'
		if release {
			final = 'm'
		}
		return []byte(fmt.Sprintf("\x1b[<%d;%d;%d%c", cb, x+1, y+1, final))
	}
	if release {
		cb = cb&^3 | 3
	}
	if x > 222 || y > 222 {
		return nil
	}
	return []byte{0x1b, '[', 'M', byte(32 + cb), byte(32 + x + 1), byte(32 + y + 1)}
}

// EncodeFocus returns the focus report, or nil if not requested.
func (t *Terminal) EncodeFocus(focused bool) []byte {
	t.mu.Lock()
	on := t.focusEvents
	t.mu.Unlock()
	if !on {
		return nil
	}
	if focused {
		return []byte("\x1b[I")
	}
	return []byte("\x1b[O")
}

// AppCursor reports whether application cursor keys are enabled.
func (t *Terminal) AppCursor() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.appCursor
}
