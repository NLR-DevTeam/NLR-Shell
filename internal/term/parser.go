package term

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

var decTable = []rune("◆▒␉␌␍␊°±␤␋┘┐┌└┼⎺⎻─⎼⎽├┤┴┬│≤≥π≠£·")

const (
	stGround = iota
	stEsc
	stEscSkip // consume one byte after ESC SP / ESC %
	stEscHash
	stCharset
	stCSI
	stOSC
	stOSCEsc
	stStr // DCS, SOS, PM, APC: ignored until ST
	stStrEsc
)

const (
	maxParams = 32
	maxOSC    = 1 << 20
)

type parser struct {
	state   int
	u8      rune
	u8need  int
	params  [maxParams]int
	sub     [maxParams]bool
	np      int
	pseen   bool
	private byte
	inter   byte
	csTgt   int
	osc     []byte
}

// Write feeds output from the remote side into the terminal.
func (t *Terminal) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.gen++
	t.parse(p)
	pend := t.pend
	t.pend = pending{}
	t.mu.Unlock()
	t.fire(pend)
	return len(p), nil
}

func (t *Terminal) fire(p pending) {
	if len(p.reply) > 0 && t.h.Reply != nil {
		t.h.Reply(p.reply)
	}
	if p.hasTitle && t.h.Title != nil {
		t.h.Title(p.title)
	}
	if p.hasCwd && t.h.Cwd != nil {
		t.h.Cwd(p.cwd)
	}
	if p.bell && t.h.Bell != nil {
		t.h.Bell()
	}
	if p.hasClip && t.h.Clipboard != nil {
		t.h.Clipboard(p.clip)
	}
}

func (t *Terminal) reply(s string) { t.pend.reply = append(t.pend.reply, s...) }

func (t *Terminal) parse(p []byte) {
	for i := 0; i < len(p); i++ {
		b := p[i]
		switch t.state {
		case stGround:
			if t.u8need > 0 {
				if b&0xc0 == 0x80 {
					t.u8 = t.u8<<6 | rune(b&0x3f)
					t.u8need--
					if t.u8need == 0 {
						t.put(t.u8)
					}
					continue
				}
				t.u8need = 0
				t.put(0xfffd)
			}
			switch {
			case b >= 0x20 && b < 0x7f:
				// Fast path for runs of ASCII.
				t.put(rune(b))
			case b < 0x20:
				t.control(b)
			case b == 0x7f:
			case b >= 0xc2 && b <= 0xdf:
				t.u8, t.u8need = rune(b&0x1f), 1
			case b >= 0xe0 && b <= 0xef:
				t.u8, t.u8need = rune(b&0x0f), 2
			case b >= 0xf0 && b <= 0xf4:
				t.u8, t.u8need = rune(b&0x07), 3
			default:
				t.put(0xfffd)
			}
		case stEsc:
			t.state = stGround
			switch b {
			case '[':
				t.state = stCSI
				t.np, t.pseen, t.private, t.inter = 0, false, 0, 0
				t.params[0], t.sub[0] = -1, false
			case ']':
				t.state = stOSC
				t.osc = t.osc[:0]
			case 'P', 'X', '^', '_':
				t.state = stStr
			case '(', ')', '*', '+':
				t.csTgt = int(b - '(')
				t.state = stCharset
			case '#':
				t.state = stEscHash
			case ' ', '%':
				t.state = stEscSkip
			default:
				if b < 0x20 {
					t.state = stEsc
					t.control(b)
				} else {
					t.escDispatch(b)
				}
			}
		case stEscSkip:
			t.state = stGround
		case stEscHash:
			t.state = stGround
			if b == '8' {
				for y := 0; y < t.rows; y++ {
					for x := range t.lines[y].Cells {
						t.lines[y].Cells[x] = Cell{R: 'E'}
					}
				}
			}
		case stCharset:
			t.state = stGround
			if t.csTgt < 2 {
				if b == '0' {
					t.charset[t.csTgt] = '0'
				} else {
					t.charset[t.csTgt] = 'B'
				}
			}
		case stCSI:
			switch {
			case b >= '0' && b <= '9':
				v := t.params[t.np]
				if v < 0 {
					v = 0
				}
				v = v*10 + int(b-'0')
				if v > 99999 {
					v = 99999
				}
				t.params[t.np] = v
				t.pseen = true
			case b == ';' || b == ':':
				t.pseen = true
				if t.np < maxParams-1 {
					t.np++
					t.params[t.np] = -1
					t.sub[t.np] = b == ':'
				}
			case b >= 0x3c && b <= 0x3f:
				t.private = b
			case b >= 0x20 && b <= 0x2f:
				t.inter = b
			case b >= 0x40 && b <= 0x7e:
				t.state = stGround
				t.csiDispatch(b)
			case b < 0x20:
				t.control(b)
			}
		case stOSC:
			switch b {
			case 0x07:
				t.state = stGround
				t.oscDispatch()
			case 0x1b:
				t.state = stOSCEsc
			case 0x18, 0x1a:
				t.state = stGround
			default:
				if len(t.osc) < maxOSC {
					t.osc = append(t.osc, b)
				}
			}
		case stOSCEsc:
			t.oscDispatch()
			if b == '\\' {
				t.state = stGround
			} else {
				t.state = stEsc
				i--
			}
		case stStr:
			switch b {
			case 0x1b:
				t.state = stStrEsc
			case 0x18, 0x1a:
				t.state = stGround
			}
		case stStrEsc:
			if b == '\\' {
				t.state = stGround
			} else {
				t.state = stEsc
				i--
			}
		}
	}
}

func (t *Terminal) control(b byte) {
	switch b {
	case 0x07:
		t.pend.bell = true
	case 0x08:
		if t.x > 0 {
			t.x--
		}
		t.wrapNext = false
	case 0x09:
		t.tabForward(1)
	case 0x0a, 0x0b, 0x0c:
		t.lineFeed()
		if t.lnm {
			t.x = 0
		}
		t.wrapNext = false
	case 0x0d:
		t.x = 0
		t.wrapNext = false
	case 0x0e:
		t.gl = 1
	case 0x0f:
		t.gl = 0
	case 0x18, 0x1a:
		t.state = stGround
	case 0x1b:
		t.state = stEsc
	}
}

func (t *Terminal) escDispatch(b byte) {
	switch b {
	case '7':
		t.saveCursor()
	case '8':
		t.restoreCursor()
	case 'D':
		t.lineFeed()
		t.wrapNext = false
	case 'E':
		t.lineFeed()
		t.x = 0
		t.wrapNext = false
	case 'H':
		t.tabs[t.x] = true
	case 'M':
		t.reverseIndex()
		t.wrapNext = false
	case 'Z':
		t.reply("\x1b[?62;22c")
	case 'c':
		t.setAlt(false, false)
		t.reset()
		t.lines = t.newScreen()
	case '=':
		t.appKeypad = true
	case '>':
		t.appKeypad = false
	}
}

// param returns parameter i, or def if it is missing or zero.
func (t *Terminal) param(i, def int) int {
	if i >= t.count() || t.params[i] <= 0 {
		return def
	}
	return t.params[i]
}

func (t *Terminal) count() int {
	if !t.pseen {
		return 0
	}
	return t.np + 1
}

func (t *Terminal) csiDispatch(b byte) {
	if t.private != 0 {
		t.csiPrivate(b)
		return
	}
	if t.inter != 0 {
		switch {
		case t.inter == ' ' && b == 'q':
			n := t.param(0, 1)
			t.cursorBlink = n%2 == 1
			switch n {
			case 3, 4:
				t.cursorShape = CursorUnderline
			case 5, 6:
				t.cursorShape = CursorBar
			default:
				t.cursorShape = CursorBlock
			}
		case t.inter == '!' && b == 'p':
			t.softReset()
		case t.inter == '$' && b == 'p':
			t.reply(fmt.Sprintf("\x1b[%d;0$y", t.param(0, 0)))
		}
		return
	}
	n := t.param(0, 1)
	switch b {
	case '@':
		t.insertCells(&t.lines[t.y], t.x, n)
	case 'A':
		lim := 0
		if t.y >= t.top {
			lim = t.top
		}
		t.y = max(t.y-n, lim)
		t.wrapNext = false
	case 'B', 'e':
		lim := t.rows - 1
		if t.y <= t.bot {
			lim = t.bot
		}
		t.y = min(t.y+n, lim)
		t.wrapNext = false
	case 'C', 'a':
		t.x = min(t.x+n, t.cols-1)
		t.wrapNext = false
	case 'D':
		t.x = max(t.x-n, 0)
		t.wrapNext = false
	case 'E':
		t.y = min(t.y+n, t.bot)
		t.x = 0
		t.wrapNext = false
	case 'F':
		t.y = max(t.y-n, t.top)
		t.x = 0
		t.wrapNext = false
	case 'G', '`':
		t.x = clamp(n-1, 0, t.cols-1)
		t.wrapNext = false
	case 'H', 'f':
		y := n - 1
		if t.origin {
			y += t.top
		}
		t.setCursor(t.param(1, 1)-1, y)
	case 'I':
		t.tabForward(n)
	case 'J':
		t.eraseDisplay(t.param(0, 0))
	case 'K':
		switch t.param(0, 0) {
		case 0:
			t.erase(t.y, t.x, t.cols)
		case 1:
			t.erase(t.y, 0, t.x+1)
		case 2:
			t.erase(t.y, 0, t.cols)
		}
		t.wrapNext = false
	case 'L':
		if t.y >= t.top && t.y <= t.bot {
			t.scrollDown(t.y, t.bot, n)
			t.x = 0
		}
	case 'M':
		if t.y >= t.top && t.y <= t.bot {
			t.scrollUp(t.y, t.bot, n, false)
			t.x = 0
		}
	case 'P':
		t.deleteCells(&t.lines[t.y], t.x, n)
	case 'S':
		t.scrollUp(t.top, t.bot, n, true)
	case 'T':
		if t.count() <= 1 {
			t.scrollDown(t.top, t.bot, n)
		}
	case 'X':
		t.erase(t.y, t.x, t.x+n)
	case 'Z':
		t.tabBackward(n)
	case 'b':
		if t.lastRune != 0 {
			for i := 0; i < n && i < t.cols*t.rows; i++ {
				t.put(t.lastRune)
			}
		}
	case 'c':
		if t.param(0, 0) == 0 {
			t.reply("\x1b[?62;22c")
		}
	case 'd':
		y := n - 1
		if t.origin {
			y += t.top
		}
		t.setCursor(t.x, y)
	case 'g':
		switch t.param(0, 0) {
		case 0:
			t.tabs[t.x] = false
		case 3:
			for i := range t.tabs {
				t.tabs[i] = false
			}
		}
	case 'h', 'l':
		on := b == 'h'
		for i := 0; i < t.count(); i++ {
			switch t.params[i] {
			case 4:
				t.insert = on
			case 20:
				t.lnm = on
			}
		}
	case 'm':
		t.sgr()
	case 'n':
		switch t.param(0, 0) {
		case 5:
			t.reply("\x1b[0n")
		case 6:
			y := t.y
			if t.origin {
				y -= t.top
			}
			t.reply(fmt.Sprintf("\x1b[%d;%dR", y+1, t.x+1))
		}
	case 'r':
		top, bot := t.param(0, 1)-1, t.param(1, t.rows)-1
		if bot >= t.rows {
			bot = t.rows - 1
		}
		if top < bot {
			t.top, t.bot = top, bot
			if t.origin {
				t.setCursor(0, t.top)
			} else {
				t.setCursor(0, 0)
			}
		}
	case 's':
		t.saveCursor()
	case 't':
		if t.param(0, 0) == 18 {
			t.reply(fmt.Sprintf("\x1b[8;%d;%dt", t.rows, t.cols))
		}
	case 'u':
		t.restoreCursor()
	}
}

func (t *Terminal) csiPrivate(b byte) {
	switch t.private {
	case '?':
		switch b {
		case 'h', 'l':
			if t.inter != 0 {
				return
			}
			for i := 0; i < t.count(); i++ {
				t.setPrivateMode(t.params[i], b == 'h')
			}
		case 'J':
			t.eraseDisplay(t.param(0, 0))
		case 'K':
			t.private = 0
			t.csiDispatch('K')
		case 'n':
			if t.param(0, 0) == 6 {
				t.reply(fmt.Sprintf("\x1b[?%d;%dR", t.y+1, t.x+1))
			}
		case 'p':
			if t.inter == '$' {
				t.reply(fmt.Sprintf("\x1b[?%d;%d$y", t.param(0, 0), t.queryPrivateMode(t.param(0, 0))))
			}
		}
	case '>':
		if b == 'c' && t.param(0, 0) == 0 {
			t.reply("\x1b[>0;10;1c")
		}
	}
}

func (t *Terminal) queryPrivateMode(m int) int {
	b := func(v bool) int {
		if v {
			return 1
		}
		return 2
	}
	switch m {
	case 1:
		return b(t.appCursor)
	case 6:
		return b(t.origin)
	case 7:
		return b(t.autoWrap)
	case 25:
		return b(t.cursorVisible)
	case 1000:
		return b(t.mouseMode == MouseNormal)
	case 1002:
		return b(t.mouseMode == MouseButton)
	case 1003:
		return b(t.mouseMode == MouseAny)
	case 1004:
		return b(t.focusEvents)
	case 1006:
		return b(t.mouseSGR)
	case 1049:
		return b(t.alt)
	case 2004:
		return b(t.bracketedPaste)
	}
	return 0
}

func (t *Terminal) setPrivateMode(m int, on bool) {
	switch m {
	case 1:
		t.appCursor = on
	case 5:
		t.reverseVideo = on
	case 6:
		t.origin = on
		if on {
			t.setCursor(0, t.top)
		} else {
			t.setCursor(0, 0)
		}
	case 7:
		t.autoWrap = on
		if !on {
			t.wrapNext = false
		}
	case 12:
		t.cursorBlink = on
	case 25:
		t.cursorVisible = on
	case 9:
		t.setMouse(MouseX10, on)
	case 1000:
		t.setMouse(MouseNormal, on)
	case 1002:
		t.setMouse(MouseButton, on)
	case 1003:
		t.setMouse(MouseAny, on)
	case 1004:
		t.focusEvents = on
	case 1006:
		t.mouseSGR = on
	case 47, 1047:
		t.setAlt(on, on)
	case 1048:
		if on {
			t.saveCursor()
		} else {
			t.restoreCursor()
		}
	case 1049:
		if on {
			t.saveCursor()
			t.setAlt(true, true)
			t.x, t.y = 0, 0
		} else {
			t.setAlt(false, false)
			t.restoreCursor()
		}
	case 2004:
		t.bracketedPaste = on
	}
}

func (t *Terminal) setMouse(m MouseMode, on bool) {
	if on {
		t.mouseMode = m
	} else if t.mouseMode == m {
		t.mouseMode = MouseOff
	}
}

func (t *Terminal) softReset() {
	t.pen = Cell{}
	t.origin, t.insert, t.appCursor, t.appKeypad = false, false, false, false
	t.autoWrap, t.cursorVisible = true, true
	t.top, t.bot = 0, t.rows-1
	t.charset = [2]byte{'B', 'B'}
	t.gl = 0
	t.saved = [2]savedCursor{}
}

func (t *Terminal) eraseDisplay(mode int) {
	switch mode {
	case 0:
		t.erase(t.y, t.x, t.cols)
		for y := t.y + 1; y < t.rows; y++ {
			t.erase(y, 0, t.cols)
		}
	case 1:
		for y := 0; y < t.y; y++ {
			t.erase(y, 0, t.cols)
		}
		t.erase(t.y, 0, t.x+1)
	case 2:
		for y := 0; y < t.rows; y++ {
			t.erase(y, 0, t.cols)
		}
	case 3:
		t.evicted += len(t.hist)
		t.hist = nil
	}
	t.wrapNext = false
}

func (t *Terminal) sgr() {
	n := t.count()
	if n == 0 {
		t.pen = Cell{}
		return
	}
	for i := 0; i < n; i++ {
		p := t.params[i]
		if p < 0 {
			p = 0
		}
		switch {
		case p == 0:
			t.pen = Cell{}
		case p == 1:
			t.pen.Attr |= AttrBold
		case p == 2:
			t.pen.Attr |= AttrFaint
		case p == 3:
			t.pen.Attr |= AttrItalic
		case p == 4:
			t.pen.Attr |= AttrUnderline
			// 4:0 turns underline off; other sub-styles are plain underline.
			for i+1 < n && t.sub[i+1] {
				i++
				if t.params[i] == 0 {
					t.pen.Attr &^= AttrUnderline
				}
			}
		case p == 5 || p == 6:
			t.pen.Attr |= AttrBlink
		case p == 7:
			t.pen.Attr |= AttrReverse
		case p == 8:
			t.pen.Attr |= AttrHidden
		case p == 9:
			t.pen.Attr |= AttrStrike
		case p == 21:
			t.pen.Attr |= AttrUnderline
		case p == 22:
			t.pen.Attr &^= AttrBold | AttrFaint
		case p == 23:
			t.pen.Attr &^= AttrItalic
		case p == 24:
			t.pen.Attr &^= AttrUnderline
		case p == 25:
			t.pen.Attr &^= AttrBlink
		case p == 27:
			t.pen.Attr &^= AttrReverse
		case p == 28:
			t.pen.Attr &^= AttrHidden
		case p == 29:
			t.pen.Attr &^= AttrStrike
		case p >= 30 && p <= 37:
			t.pen.FG = Indexed(uint8(p - 30))
		case p == 38:
			if c, j, ok := t.extColor(i); ok {
				t.pen.FG = c
				i = j
			} else {
				i = j
			}
		case p == 39:
			t.pen.FG = ColorDefault
		case p >= 40 && p <= 47:
			t.pen.BG = Indexed(uint8(p - 40))
		case p == 48:
			if c, j, ok := t.extColor(i); ok {
				t.pen.BG = c
				i = j
			} else {
				i = j
			}
		case p == 49:
			t.pen.BG = ColorDefault
		case p == 58:
			_, i, _ = t.extColor(i)
		case p >= 90 && p <= 97:
			t.pen.FG = Indexed(uint8(p - 90 + 8))
		case p >= 100 && p <= 107:
			t.pen.BG = Indexed(uint8(p - 100 + 8))
		}
	}
}

// extColor parses an extended color (38/48/58) starting at parameter i and
// returns the color and the index of the last parameter consumed.
func (t *Terminal) extColor(i int) (Color, int, bool) {
	n := t.count()
	v := func(j int) int {
		if t.params[j] < 0 {
			return 0
		}
		return t.params[j]
	}
	if i+1 < n && t.sub[i+1] {
		j := i + 1
		for j < n && t.sub[j] {
			j++
		}
		s := j - (i + 1)
		last := j - 1
		switch v(i + 1) {
		case 5:
			if s >= 2 {
				return Indexed(uint8(v(i + 2))), last, true
			}
		case 2:
			if s >= 5 {
				return RGB(uint8(v(i+3)), uint8(v(i+4)), uint8(v(i+5))), last, true
			}
			if s >= 4 {
				return RGB(uint8(v(i+2)), uint8(v(i+3)), uint8(v(i+4))), last, true
			}
		}
		return 0, last, false
	}
	if i+1 >= n {
		return 0, i, false
	}
	switch v(i + 1) {
	case 5:
		if i+2 < n {
			return Indexed(uint8(v(i + 2))), i + 2, true
		}
		return 0, n - 1, false
	case 2:
		if i+4 < n {
			return RGB(uint8(v(i+2)), uint8(v(i+3)), uint8(v(i+4))), i + 4, true
		}
		return 0, n - 1, false
	}
	return 0, i + 1, false
}

func (t *Terminal) oscDispatch() {
	s := string(t.osc)
	t.osc = t.osc[:0]
	code, rest, _ := strings.Cut(s, ";")
	n, err := strconv.Atoi(code)
	if err != nil {
		return
	}
	switch n {
	case 0, 2:
		t.title = rest
		t.pend.title, t.pend.hasTitle = rest, true
	case 7:
		if u, err := url.Parse(rest); err == nil && u.Path != "" {
			t.pend.cwd, t.pend.hasCwd = u.Path, true
		}
	case 10, 11:
		if rest == "?" {
			c := t.defaultFG
			if n == 11 {
				c = t.defaultBG
			}
			r, g, b := (c>>16)&0xff, (c>>8)&0xff, c&0xff
			t.reply(fmt.Sprintf("\x1b]%d;rgb:%02x%02x/%02x%02x/%02x%02x\x1b\\", n, r, r, g, g, b, b))
		}
	case 52:
		_, data, ok := strings.Cut(rest, ";")
		if !ok || data == "?" {
			return
		}
		if b, err := base64.StdEncoding.DecodeString(data); err == nil {
			t.pend.clip, t.pend.hasClip = string(b), true
		}
	}
}
