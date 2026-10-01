package term

import (
	"strings"
	"sync"
)

// MouseMode is the mouse reporting mode requested by the application.
type MouseMode uint8

const (
	MouseOff    MouseMode = iota
	MouseX10              // press only (mode 9)
	MouseNormal           // press and release (mode 1000)
	MouseButton           // plus motion while a button is held (mode 1002)
	MouseAny              // plus all motion (mode 1003)
)

// CursorShape is the shape requested through DECSCUSR.
type CursorShape uint8

const (
	CursorBlock CursorShape = iota
	CursorUnderline
	CursorBar
)

// Handler receives out-of-band notifications from the terminal. Callbacks are
// invoked after the terminal lock has been released, from the goroutine that
// called Write.
type Handler struct {
	// Reply sends bytes back to the remote side (status reports etc).
	Reply func([]byte)
	// Title is called when the window title changes.
	Title func(string)
	// Cwd is called when the shell reports its working directory (OSC 7).
	Cwd func(string)
	// Bell is called on BEL.
	Bell func()
	// Clipboard is called when the application sets the clipboard (OSC 52).
	Clipboard func(string)
}

type savedCursor struct {
	x, y     int
	pen      Cell
	origin   bool
	wrapNext bool
	charset  [2]byte
	gl       int
}

type pending struct {
	reply    []byte
	title    string
	hasTitle bool
	cwd      string
	hasCwd   bool
	bell     bool
	clip     string
	hasClip  bool
}

// Terminal is the emulator state. All exported methods are safe for
// concurrent use unless noted; renderers bracket their reads with Lock and
// Unlock and use the *Locked accessors.
type Terminal struct {
	mu sync.Mutex
	h  Handler

	cols, rows int
	lines      []Line // active screen
	other      []Line // main screen contents while the alternate screen is active
	alt        bool

	hist    []Line
	histMax int
	evicted int

	x, y     int
	wrapNext bool
	pen      Cell
	saved    [2]savedCursor
	top, bot int
	tabs     []bool

	appCursor      bool
	appKeypad      bool
	origin         bool
	autoWrap       bool
	insert         bool
	lnm            bool
	reverseVideo   bool
	cursorVisible  bool
	cursorBlink    bool
	cursorShape    CursorShape
	bracketedPaste bool
	focusEvents    bool
	mouseMode      MouseMode
	mouseSGR       bool

	charset [2]byte
	gl      int

	title    string
	lastRune rune

	// DefaultFG and DefaultBG are reported to applications that query the
	// terminal colors (OSC 10/11). They are 0xRRGGBB.
	defaultFG, defaultBG uint32

	parser
	pend pending
	gen  uint64
}

// New returns a terminal with the given size and scrollback capacity.
func New(cols, rows, scrollback int, h Handler) *Terminal {
	if cols < 2 {
		cols = 2
	}
	if rows < 1 {
		rows = 1
	}
	t := &Terminal{h: h, cols: cols, rows: rows, histMax: scrollback, defaultFG: 0xd7dce4, defaultBG: 0x0b0d10}
	t.lines = t.newScreen()
	t.reset()
	return t
}

// SetDefaultColors sets the colors reported for OSC 10/11 queries.
func (t *Terminal) SetDefaultColors(fg, bg uint32) {
	t.mu.Lock()
	t.defaultFG, t.defaultBG = fg, bg
	t.mu.Unlock()
}

// SetScrollback changes the scrollback capacity.
func (t *Terminal) SetScrollback(n int) {
	t.mu.Lock()
	t.histMax = n
	t.trimHist(true)
	t.mu.Unlock()
}

func (t *Terminal) reset() {
	t.x, t.y, t.wrapNext = 0, 0, false
	t.pen = Cell{}
	t.saved = [2]savedCursor{}
	t.top, t.bot = 0, t.rows-1
	t.appCursor, t.appKeypad, t.origin, t.insert, t.lnm, t.reverseVideo = false, false, false, false, false, false
	t.autoWrap, t.cursorVisible, t.cursorBlink = true, true, true
	t.cursorShape = CursorBlock
	t.bracketedPaste, t.focusEvents = false, false
	t.mouseMode, t.mouseSGR = MouseOff, false
	t.charset = [2]byte{'B', 'B'}
	t.gl = 0
	t.resetTabs()
}

func (t *Terminal) resetTabs() {
	t.tabs = make([]bool, t.cols)
	for i := 8; i < t.cols; i += 8 {
		t.tabs[i] = true
	}
}

func (t *Terminal) blank() Cell { return Cell{R: ' ', BG: t.pen.BG} }

func (t *Terminal) newLine() Line {
	cells := make([]Cell, t.cols)
	b := t.blank()
	for i := range cells {
		cells[i] = b
	}
	return Line{Cells: cells}
}

func (t *Terminal) newScreen() []Line {
	lines := make([]Line, t.rows)
	for i := range lines {
		lines[i] = t.newLine()
	}
	return lines
}

// ---- Locked accessors -------------------------------------------------

// Lock acquires the terminal lock for a consistent read of the grid.
func (t *Terminal) Lock() { t.mu.Lock() }

// Unlock releases the lock taken by Lock.
func (t *Terminal) Unlock() { t.mu.Unlock() }

// Size returns the grid size. Caller must hold the lock.
func (t *Terminal) Size() (cols, rows int) { return t.cols, t.rows }

// HistLen returns the number of scrollback lines. Caller must hold the lock.
func (t *Terminal) HistLen() int { return len(t.hist) }

// Evicted returns how many lines have been dropped from the scrollback since
// the terminal was created. Evicted()+i is a stable identifier for line i.
// Caller must hold the lock.
func (t *Terminal) Evicted() int { return t.evicted }

// LineAt returns line i, where 0 is the oldest scrollback line and
// HistLen() is the first screen row. Caller must hold the lock.
func (t *Terminal) LineAt(i int) *Line {
	if i < 0 {
		return nil
	}
	if i < len(t.hist) {
		return &t.hist[i]
	}
	i -= len(t.hist)
	if i < len(t.lines) {
		return &t.lines[i]
	}
	return nil
}

// Cursor returns the cursor position on the screen. Caller must hold the lock.
func (t *Terminal) Cursor() (x, y int, visible bool, shape CursorShape, blink bool) {
	return t.x, t.y, t.cursorVisible, t.cursorShape, t.cursorBlink
}

// Modes. Caller must hold the lock.

func (t *Terminal) AltScreen() bool      { return t.alt }
func (t *Terminal) MouseMode() MouseMode { return t.mouseMode }
func (t *Terminal) ReverseVideo() bool   { return t.reverseVideo }
func (t *Terminal) FocusEvents() bool    { return t.focusEvents }
func (t *Terminal) BracketedPaste() bool { return t.bracketedPaste }

// Gen returns a counter that changes whenever the terminal contents change.
// Caller must hold the lock.
func (t *Terminal) Gen() uint64 { return t.gen }

// Title returns the current window title.
func (t *Terminal) Title() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.title
}

// TextRange returns the text between two positions given as stable line
// identifiers (see Evicted) and columns. The end position is exclusive.
func (t *Terminal) TextRange(startLine, startCol, endLine, endCol int) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var sb strings.Builder
	for abs := startLine; abs <= endLine; abs++ {
		l := t.LineAt(abs - t.evicted)
		if l == nil {
			continue
		}
		from, to := 0, len(l.Cells)
		if abs == startLine {
			from = startCol
		}
		if abs == endLine {
			to = endCol
		}
		if to > len(l.Cells) {
			to = len(l.Cells)
		}
		if from < 0 {
			from = 0
		}
		// Trim trailing blanks unless the line continues on the next row.
		last := to
		if !l.Wrapped || abs == endLine {
			for last > from && (l.Cells[last-1].R == ' ' || l.Cells[last-1].R == 0) && l.Cells[last-1].Attr&AttrWideTail == 0 {
				last--
			}
		}
		for i := from; i < last; i++ {
			c := &l.Cells[i]
			if c.Attr&AttrWideTail != 0 {
				continue
			}
			if c.R == 0 {
				sb.WriteByte(' ')
			} else {
				sb.WriteRune(c.R)
			}
		}
		if abs != endLine && !l.Wrapped {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// ---- Resize -----------------------------------------------------------

// Resize changes the grid size.
func (t *Terminal) Resize(cols, rows int) {
	if cols < 2 {
		cols = 2
	}
	if rows < 1 {
		rows = 1
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if cols == t.cols && rows == t.rows {
		return
	}
	t.gen++
	oldRows := t.rows
	t.cols = cols

	main, altLines := t.lines, []Line(nil)
	if t.alt {
		main, altLines = t.other, t.lines
	}

	// Main screen: keep the cursor line visible by moving lines to and from
	// the scrollback.
	cy := t.y
	if t.alt {
		cy = t.saved[0].y
	}
	if rows < oldRows {
		// Drop blank lines below the cursor first.
		for len(main) > rows && len(main)-1 > cy && lineBlank(&main[len(main)-1]) {
			main = main[:len(main)-1]
		}
		for len(main) > rows {
			t.pushHist(main[0])
			main = main[1:]
			cy--
		}
	} else {
		for len(main) < rows && len(t.hist) > 0 {
			l := t.hist[len(t.hist)-1]
			t.hist = t.hist[:len(t.hist)-1]
			main = append([]Line{l}, main...)
			cy++
		}
	}
	t.rows = rows
	main = t.fitLines(main)
	if t.alt {
		t.saved[0].y = clamp(cy, 0, rows-1)
		t.saved[0].x = clamp(t.saved[0].x, 0, cols-1)
		t.other = main
		t.lines = t.fitLines(altLines)
		t.y = clamp(t.y, 0, rows-1)
	} else {
		t.lines = main
		t.y = clamp(cy, 0, rows-1)
	}
	t.x = clamp(t.x, 0, cols-1)
	t.wrapNext = false
	t.top, t.bot = 0, rows-1
	old := t.tabs
	t.resetTabs()
	copy(t.tabs, old)
}

// fitLines pads or truncates lines to the current grid size.
func (t *Terminal) fitLines(lines []Line) []Line {
	if len(lines) > t.rows {
		lines = lines[:t.rows]
	}
	for len(lines) < t.rows {
		lines = append(lines, t.newLine())
	}
	out := make([]Line, t.rows)
	copy(out, lines)
	for i := range out {
		l := &out[i]
		switch {
		case len(l.Cells) > t.cols:
			l.Cells = l.Cells[:t.cols:t.cols]
			if l.Cells[t.cols-1].Attr&AttrWide != 0 {
				l.Cells[t.cols-1] = Cell{R: ' ', BG: l.Cells[t.cols-1].BG}
			}
		case len(l.Cells) < t.cols:
			cells := make([]Cell, t.cols)
			n := copy(cells, l.Cells)
			for j := n; j < t.cols; j++ {
				cells[j] = Cell{R: ' '}
			}
			l.Cells = cells
		}
	}
	return out
}

func lineBlank(l *Line) bool {
	for i := range l.Cells {
		c := &l.Cells[i]
		if (c.R != ' ' && c.R != 0) || !c.BG.IsDefault() {
			return false
		}
	}
	return true
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ---- Scrollback -------------------------------------------------------

func (t *Terminal) pushHist(l Line) {
	if t.histMax <= 0 {
		t.evicted++
		return
	}
	n := len(l.Cells)
	for n > 0 {
		c := &l.Cells[n-1]
		if (c.R != ' ' && c.R != 0) || !c.BG.IsDefault() || c.Attr != 0 {
			break
		}
		n--
	}
	if n*2 < len(l.Cells) {
		cells := make([]Cell, n)
		copy(cells, l.Cells)
		l.Cells = cells
	} else {
		l.Cells = l.Cells[:n]
	}
	t.hist = append(t.hist, l)
	t.trimHist(false)
}

func (t *Terminal) trimHist(force bool) {
	slack := 512
	if force {
		slack = 0
	}
	if len(t.hist) > t.histMax+slack {
		drop := len(t.hist) - t.histMax
		n := copy(t.hist, t.hist[drop:])
		for i := n; i < len(t.hist); i++ {
			t.hist[i] = Line{}
		}
		t.hist = t.hist[:n]
		t.evicted += drop
	}
}

// ClearScrollback drops all scrollback lines.
func (t *Terminal) ClearScrollback() {
	t.mu.Lock()
	t.evicted += len(t.hist)
	t.hist = nil
	t.gen++
	t.mu.Unlock()
}

// ---- Grid operations (lock held) ---------------------------------------

func (t *Terminal) scrollUp(top, bot, n int, save bool) {
	if n <= 0 || top > bot {
		return
	}
	if n > bot-top+1 {
		n = bot - top + 1
	}
	if save && !t.alt && top == 0 {
		for i := 0; i < n; i++ {
			t.pushHist(t.lines[i])
		}
	}
	copy(t.lines[top:bot+1], t.lines[top+n:bot+1])
	for i := bot - n + 1; i <= bot; i++ {
		t.lines[i] = t.newLine()
	}
}

func (t *Terminal) scrollDown(top, bot, n int) {
	if n <= 0 || top > bot {
		return
	}
	if n > bot-top+1 {
		n = bot - top + 1
	}
	copy(t.lines[top+n:bot+1], t.lines[top:bot+1-n])
	for i := top; i < top+n; i++ {
		t.lines[i] = t.newLine()
	}
}

func (t *Terminal) lineFeed() {
	if t.y == t.bot {
		t.scrollUp(t.top, t.bot, 1, true)
	} else if t.y < t.rows-1 {
		t.y++
	}
}

func (t *Terminal) reverseIndex() {
	if t.y == t.top {
		t.scrollDown(t.top, t.bot, 1)
	} else if t.y > 0 {
		t.y--
	}
}

// fixWide blanks the other half of a wide character that is about to be
// partially overwritten at column x.
func (t *Terminal) fixWide(l *Line, x int) {
	if x < 0 || x >= len(l.Cells) {
		return
	}
	c := &l.Cells[x]
	if c.Attr&AttrWideTail != 0 && x > 0 {
		l.Cells[x-1].R = ' '
		l.Cells[x-1].Attr &^= AttrWide
	}
	if c.Attr&AttrWide != 0 && x+1 < len(l.Cells) {
		l.Cells[x+1].R = ' '
		l.Cells[x+1].Attr &^= AttrWideTail
	}
}

func (t *Terminal) put(r rune) {
	if r < 0x7f && t.charset[t.gl] == '0' {
		r = decSpecial(r)
	}
	w := 1
	if r >= 0x300 {
		w = RuneWidth(r)
		if w == 0 {
			return
		}
	}
	if t.wrapNext {
		t.wrapNext = false
		if t.autoWrap {
			t.lines[t.y].Wrapped = true
			t.x = 0
			t.lineFeed()
		}
	}
	if w == 2 && t.x >= t.cols-1 {
		if !t.autoWrap {
			return
		}
		l := &t.lines[t.y]
		t.fixWide(l, t.x)
		l.Cells[t.x] = t.blank()
		l.Wrapped = true
		t.x = 0
		t.lineFeed()
	}
	l := &t.lines[t.y]
	if t.insert {
		t.insertCells(l, t.x, w)
	}
	t.fixWide(l, t.x)
	attr := t.pen.Attr
	if w == 2 {
		t.fixWide(l, t.x+1)
		l.Cells[t.x] = Cell{R: r, FG: t.pen.FG, BG: t.pen.BG, Attr: attr | AttrWide}
		l.Cells[t.x+1] = Cell{R: ' ', FG: t.pen.FG, BG: t.pen.BG, Attr: attr | AttrWideTail}
	} else {
		l.Cells[t.x] = Cell{R: r, FG: t.pen.FG, BG: t.pen.BG, Attr: attr}
	}
	t.lastRune = r
	t.x += w
	if t.x >= t.cols {
		t.x = t.cols - 1
		t.wrapNext = t.autoWrap
	}
}

func (t *Terminal) insertCells(l *Line, x, n int) {
	if n > t.cols-x {
		n = t.cols - x
	}
	t.fixWide(l, x)
	copy(l.Cells[x+n:], l.Cells[x:t.cols-n])
	b := t.blank()
	for i := x; i < x+n; i++ {
		l.Cells[i] = b
	}
	if l.Cells[t.cols-1].Attr&AttrWide != 0 {
		l.Cells[t.cols-1] = b
	}
}

func (t *Terminal) deleteCells(l *Line, x, n int) {
	if n > t.cols-x {
		n = t.cols - x
	}
	t.fixWide(l, x)
	t.fixWide(l, x+n)
	copy(l.Cells[x:], l.Cells[x+n:])
	b := t.blank()
	for i := t.cols - n; i < t.cols; i++ {
		l.Cells[i] = b
	}
}

// erase blanks cells [x0, x1) on row y using the current background.
func (t *Terminal) erase(y, x0, x1 int) {
	if y < 0 || y >= t.rows {
		return
	}
	x0, x1 = clamp(x0, 0, t.cols), clamp(x1, 0, t.cols)
	if x0 >= x1 {
		return
	}
	l := &t.lines[y]
	t.fixWide(l, x0)
	t.fixWide(l, x1-1)
	b := t.blank()
	for i := x0; i < x1; i++ {
		l.Cells[i] = b
	}
	if x1 == t.cols {
		l.Wrapped = false
	}
}

func (t *Terminal) setCursor(x, y int) {
	t.x = clamp(x, 0, t.cols-1)
	if t.origin {
		t.y = clamp(y, t.top, t.bot)
	} else {
		t.y = clamp(y, 0, t.rows-1)
	}
	t.wrapNext = false
}

func (t *Terminal) saveCursor() {
	i := 0
	if t.alt {
		i = 1
	}
	t.saved[i] = savedCursor{x: t.x, y: t.y, pen: t.pen, origin: t.origin, wrapNext: t.wrapNext, charset: t.charset, gl: t.gl}
}

func (t *Terminal) restoreCursor() {
	i := 0
	if t.alt {
		i = 1
	}
	s := t.saved[i]
	if s.charset == [2]byte{} {
		s.charset = [2]byte{'B', 'B'}
	}
	t.x, t.y = clamp(s.x, 0, t.cols-1), clamp(s.y, 0, t.rows-1)
	t.pen, t.origin, t.wrapNext, t.charset, t.gl = s.pen, s.origin, s.wrapNext, s.charset, s.gl
}

func (t *Terminal) setAlt(on, clear bool) {
	if on == t.alt {
		if on && clear {
			t.lines = t.newScreen()
		}
		return
	}
	if on {
		t.other = t.lines
		t.lines = t.newScreen()
	} else {
		t.lines = t.other
		t.other = nil
	}
	t.alt = on
	t.top, t.bot = 0, t.rows-1
	t.wrapNext = false
}

func (t *Terminal) tabForward(n int) {
	for ; n > 0; n-- {
		x := t.x + 1
		for x < t.cols-1 && !t.tabs[x] {
			x++
		}
		t.x = clamp(x, 0, t.cols-1)
	}
	t.wrapNext = false
}

func (t *Terminal) tabBackward(n int) {
	for ; n > 0; n-- {
		x := t.x - 1
		for x > 0 && !t.tabs[x] {
			x--
		}
		t.x = clamp(x, 0, t.cols-1)
	}
	t.wrapNext = false
}

// decSpecial maps the DEC Special Graphics character set to Unicode.
func decSpecial(r rune) rune {
	if r >= 0x60 && r <= 0x7e {
		return decTable[r-0x60]
	}
	if r == 0x5f {
		return ' '
	}
	return r
}
