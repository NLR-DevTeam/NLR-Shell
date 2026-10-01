package ui

import (
	"image"
	"image/color"
	"io"
	"strings"
	"time"
	"unicode"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/transfer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"golang.org/x/image/math/fixed"

	"nlrshell/internal/term"
)

type glyphKey struct {
	r      rune
	bold   bool
	italic bool
}

type glyphInfo struct {
	g  text.Glyph
	ok bool
}

type cellPos struct {
	line int // stable line id (see term.Terminal.Evicted)
	col  int
}

func (a cellPos) before(b cellPos) bool {
	return a.line < b.line || a.line == b.line && a.col < b.col
}

const (
	selChar = iota
	selWord
	selLine
)

// imeState is what the input method was last told about the composition.
type imeState struct {
	snip  string
	caret int
	px    image.Point
}

// TermView renders a term.Terminal and translates input for it.
type TermView struct {
	th   *Theme
	term *term.Terminal
	// Send delivers input bytes to the remote side.
	Send func([]byte)
	// OnMenu is called on right click with the pointer position in window
	// coordinates being tracked by the app.
	OnMenu func()
	// OnZoom is called on Ctrl+wheel and Ctrl+plus/minus with the step.
	OnZoom func(delta int)
	// OnSize reports the grid size whenever it changes.
	OnSize func(cols, rows int)

	FontSize     unit.Sp
	CopyOnSelect bool
	RightPaste   bool

	// Font metrics in pixels for the current size.
	px      int
	face    font.Typeface
	cellW   int
	cellH   int
	ascent  int
	glyphs  map[glyphKey]glyphInfo
	cols    int
	rows    int
	padding image.Point

	// View state.
	follow    bool
	topAbs    int
	scrollRem float32

	sel       bool
	selMode   int
	anchor    cellPos
	anchorEnd cellPos // end of the anchor word or line in word/line mode
	head      cellPos
	dragging  bool
	lastClick time.Time
	lastPos   cellPos
	clicks    int
	mouseBtn  int
	lastMouse image.Point

	focused    bool
	blinkStart time.Time

	ime      []rune
	imeCaret int
	compose  key.Range
	imeSent  imeState
	caretPx  image.Point

	sbTag   int
	sbDrag  bool
	sbHover bool

	// Scratch buffers reused between frames.
	rowsBuf  [][]term.Cell
	glyphBuf []text.Glyph
}

// NewTermView creates a view for t.
func NewTermView(th *Theme, t *term.Terminal) *TermView {
	return &TermView{th: th, term: t, follow: true, FontSize: 14, compose: key.Range{Start: -1, End: -1}, mouseBtn: -1}
}

// Focus requests keyboard focus for the terminal.
func (v *TermView) Focus(gtx layout.Context) {
	gtx.Execute(key.FocusCmd{Tag: v})
}

// Focused reports whether the terminal has keyboard focus.
func (v *TermView) Focused() bool { return v.focused }

// HasSelection reports whether text is selected.
func (v *TermView) HasSelection() bool { return v.sel }

func (v *TermView) measure(gtx layout.Context) {
	px := gtx.Sp(v.FontSize)
	if px == v.px && v.face == v.th.Mono && v.glyphs != nil {
		return
	}
	v.px, v.face = px, v.th.Mono
	v.glyphs = map[glyphKey]glyphInfo{}
	g, _ := v.shape(glyphKey{r: 'M'})
	v.cellW = max(g.Advance.Round(), 1)
	v.ascent = g.Ascent.Ceil()
	v.cellH = max(v.ascent+g.Descent.Ceil()+gtx.Dp(1), 1)
	v.padding = image.Pt(gtx.Dp(8), gtx.Dp(6))
}

func (v *TermView) shape(k glyphKey) (text.Glyph, bool) {
	if gi, ok := v.glyphs[k]; ok {
		return gi.g, gi.ok
	}
	f := font.Font{Typeface: v.th.Mono}
	if k.bold {
		f.Weight = font.Bold
	}
	if k.italic {
		f.Style = font.Italic
	}
	// MaxWidth must be set or the shaper truncates everything to "…".
	v.th.Shaper.LayoutString(text.Parameters{Font: f, PxPerEm: fixed.I(v.px), MaxWidth: 1 << 24}, string(k.r))
	var first text.Glyph
	found := false
	for {
		g, ok := v.th.Shaper.NextGlyph()
		if !ok {
			break
		}
		if !found {
			first, found = g, true
		}
	}
	v.glyphs[k] = glyphInfo{g: first, ok: found}
	return first, found
}

// Copy writes the selection to the clipboard.
func (v *TermView) Copy(gtx layout.Context) {
	if s := v.SelectedText(); s != "" {
		gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(s))})
	}
}

// Paste requests the clipboard contents; they are sent when they arrive.
func (v *TermView) Paste(gtx layout.Context) {
	gtx.Execute(clipboard.ReadCmd{Tag: v})
}

// SelectAll selects the whole buffer including scrollback.
func (v *TermView) SelectAll() {
	v.term.Lock()
	ev, hist := v.term.Evicted(), v.term.HistLen()
	cols, rows := v.term.Size()
	v.term.Unlock()
	v.sel, v.selMode = true, selChar
	v.anchor = cellPos{line: ev, col: 0}
	v.head = cellPos{line: ev + hist + rows - 1, col: cols}
}

// ClearSelection drops the selection.
func (v *TermView) ClearSelection() { v.sel = false }

// SelectedText returns the selected text.
func (v *TermView) SelectedText() string {
	if !v.sel {
		return ""
	}
	a, b := v.selRange()
	return v.term.TextRange(a.line, a.col, b.line, b.col)
}

// selRange returns the selection as an ordered half-open range.
func (v *TermView) selRange() (cellPos, cellPos) {
	a, b := v.anchor, v.head
	if v.selMode != selChar {
		// The anchor is a range; extend from whichever end is farther.
		if b.before(v.anchor) {
			a, b = b, v.anchorEnd
		} else {
			a = v.anchor
			if b.before(v.anchorEnd) {
				b = v.anchorEnd
			}
		}
		return a, b
	}
	if b.before(a) {
		a, b = b, a
	}
	return a, b
}

// ScrollToBottom snaps the view to the live screen.
func (v *TermView) ScrollToBottom() { v.follow = true }

func (v *TermView) scrollLines(n int) {
	v.term.Lock()
	ev, hist := v.term.Evicted(), v.term.HistLen()
	v.term.Unlock()
	top := ev + hist
	if !v.follow {
		top = v.topAbs
	}
	top += n
	if top >= ev+hist {
		v.follow = true
		return
	}
	v.follow = false
	v.topAbs = max(top, ev)
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("_-./~:@%+#=?&", r)
}

// wordAt returns the bounds of the word at p.
func (v *TermView) wordAt(p cellPos) (cellPos, cellPos) {
	v.term.Lock()
	defer v.term.Unlock()
	l := v.term.LineAt(p.line - v.term.Evicted())
	if l == nil || p.col >= len(l.Cells) {
		return p, cellPos{p.line, p.col + 1}
	}
	at := func(i int) rune {
		if i < 0 || i >= len(l.Cells) {
			return ' '
		}
		if l.Cells[i].Attr&term.AttrWideTail != 0 && i > 0 {
			return l.Cells[i-1].R
		}
		return l.Cells[i].R
	}
	if !isWordRune(at(p.col)) {
		return p, cellPos{p.line, p.col + 1}
	}
	s, e := p.col, p.col+1
	for s > 0 && isWordRune(at(s-1)) {
		s--
	}
	for e < len(l.Cells) && isWordRune(at(e)) {
		e++
	}
	return cellPos{p.line, s}, cellPos{p.line, e}
}

func (v *TermView) posAt(pt f32.Point, top int) cellPos {
	col := int((pt.X-float32(v.padding.X))/float32(v.cellW) + 0.5)
	row := int((pt.Y - float32(v.padding.Y)) / float32(v.cellH))
	if pt.Y < float32(v.padding.Y) {
		row = -1
	}
	col = min(max(col, 0), v.cols)
	row = min(max(row, -1), v.rows)
	return cellPos{line: top + row, col: col}
}

func (v *TermView) cellAt(pt f32.Point) (x, y int) {
	x = int((pt.X - float32(v.padding.X)) / float32(v.cellW))
	y = int((pt.Y - float32(v.padding.Y)) / float32(v.cellH))
	return min(max(x, 0), v.cols-1), min(max(y, 0), v.rows-1)
}

func mods(m key.Modifiers) term.Mods {
	var out term.Mods
	if m.Contain(key.ModShift) {
		out |= term.ModShift
	}
	if m.Contain(key.ModAlt) {
		out |= term.ModAlt
	}
	if m.Contain(key.ModCtrl) {
		out |= term.ModCtrl
	}
	return out
}

var namedKeys = map[key.Name]term.Key{
	key.NameUpArrow: term.KeyUp, key.NameDownArrow: term.KeyDown, key.NameLeftArrow: term.KeyLeft, key.NameRightArrow: term.KeyRight,
	key.NameHome: term.KeyHome, key.NameEnd: term.KeyEnd, key.NameDeleteForward: term.KeyDelete,
	key.NamePageUp: term.KeyPageUp, key.NamePageDown: term.KeyPageDown,
	key.NameReturn: term.KeyEnter, key.NameEnter: term.KeyEnter, key.NameDeleteBackward: term.KeyBackspace,
	key.NameTab: term.KeyTab, key.NameEscape: term.KeyEscape,
	key.NameF1: term.KeyF1, key.NameF2: term.KeyF2, key.NameF3: term.KeyF3, key.NameF4: term.KeyF4,
	key.NameF5: term.KeyF5, key.NameF6: term.KeyF6, key.NameF7: term.KeyF7, key.NameF8: term.KeyF8,
	key.NameF9: term.KeyF9, key.NameF10: term.KeyF10, key.NameF11: term.KeyF11, key.NameF12: term.KeyF12,
}

func (v *TermView) send(b []byte) {
	if len(b) == 0 || v.Send == nil {
		return
	}
	v.Send(b)
	v.follow = true
}

func (v *TermView) handleKey(gtx layout.Context, e key.Event) {
	if e.State != key.Press {
		return
	}
	v.blinkStart = gtx.Now
	ctrl, shift, alt := e.Modifiers.Contain(key.ModCtrl), e.Modifiers.Contain(key.ModShift), e.Modifiers.Contain(key.ModAlt)

	// Local shortcuts.
	switch {
	case ctrl && shift && e.Name == "C", ctrl && !shift && !alt && e.Name == "C" && v.sel:
		v.Copy(gtx)
		v.sel = false
		return
	case ctrl && e.Name == "V" && !alt:
		v.Paste(gtx)
		return
	case ctrl && shift && e.Name == "A":
		v.SelectAll()
		return
	case shift && !ctrl && e.Name == key.NamePageUp:
		v.scrollLines(-(v.rows - 1))
		return
	case shift && !ctrl && e.Name == key.NamePageDown:
		v.scrollLines(v.rows - 1)
		return
	case shift && ctrl && e.Name == key.NameHome:
		v.scrollLines(-1 << 30)
		return
	case shift && ctrl && e.Name == key.NameEnd:
		v.follow = true
		return
	case ctrl && !shift && !alt && (e.Name == "+" || e.Name == "-" || e.Name == "0"):
		if v.OnZoom != nil {
			switch e.Name {
			case "+":
				v.OnZoom(1)
			case "-":
				v.OnZoom(-1)
			default:
				v.OnZoom(0)
			}
		}
		return
	}

	if k, ok := namedKeys[e.Name]; ok {
		v.sel = false
		v.send(v.term.EncodeKey(k, mods(e.Modifiers)))
		return
	}
	// AltGr arrives as Ctrl+Alt and produces text through an EditEvent.
	if ctrl && alt {
		return
	}
	name := string(e.Name)
	if e.Name == key.NameSpace {
		name = " "
	}
	if len(name) != 1 {
		return
	}
	c := name[0]
	switch {
	case ctrl:
		if b, ok := term.EncodeCtrl(c); ok {
			v.sel = false
			v.send([]byte{b})
		}
	case alt:
		if c >= 'A' && c <= 'Z' && !shift {
			c += 'a' - 'A'
		}
		v.send([]byte{0x1b, c})
	}
}

func (v *TermView) imeReplace(r key.Range, s string) {
	start, end := r.Start, r.End
	if start > end {
		start, end = end, start
	}
	start = min(max(start, 0), len(v.ime))
	end = min(max(end, 0), len(v.ime))
	ins := []rune(s)
	out := make([]rune, 0, len(v.ime)-(end-start)+len(ins))
	out = append(out, v.ime[:start]...)
	out = append(out, ins...)
	out = append(out, v.ime[end:]...)
	v.ime = out
	v.imeCaret = start + len(ins)
}

func (v *TermView) update(gtx layout.Context, top int) {
	mouseMode := func() term.MouseMode {
		v.term.Lock()
		defer v.term.Unlock()
		return v.term.MouseMode()
	}
	for {
		ev, ok := gtx.Event(
			key.FocusFilter{Target: v},
			pointer.Filter{Target: v, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Move | pointer.Scroll | pointer.Cancel,
				ScrollY: pointer.ScrollRange{Min: -1 << 20, Max: 1 << 20}},
			key.Filter{Focus: v, Optional: key.ModCtrl | key.ModShift | key.ModAlt},
			key.Filter{Focus: v, Name: key.NameTab, Optional: key.ModCtrl | key.ModShift | key.ModAlt},
			transfer.TargetFilter{Target: v, Type: "application/text"},
		)
		if !ok {
			break
		}
		switch e := ev.(type) {
		case key.FocusEvent:
			if e.Focus != v.focused {
				v.focused = e.Focus
				// Enable the input method while the terminal has the
				// keyboard; Gio's editor widgets do the same.
				gtx.Execute(key.SoftKeyboardCmd{Show: e.Focus})
				v.blinkStart = gtx.Now
				if v.Send != nil {
					if b := v.term.EncodeFocus(e.Focus); b != nil {
						v.Send(b)
					}
				}
			}
			if !e.Focus {
				v.ime, v.imeCaret = v.ime[:0], 0
			}
		case key.Event:
			v.handleKey(gtx, e)
		case key.EditEvent:
			v.imeReplace(e.Range, e.Text)
			v.blinkStart = gtx.Now
		case key.CompositionEvent:
			v.compose = key.Range(e)
		case key.SelectionEvent:
			v.imeCaret = min(max(e.Start, 0), len(v.ime))
		case transfer.DataEvent:
			if rc := e.Open(); rc != nil {
				b, _ := io.ReadAll(io.LimitReader(rc, 8<<20))
				rc.Close()
				if len(b) > 0 {
					v.sel = false
					v.send(v.term.EncodePaste(string(b)))
				}
			}
		case pointer.Event:
			v.pointer(gtx, e, top, mouseMode())
		}
	}
	// Text that is not part of an active composition goes to the remote.
	if len(v.ime) > 0 && (v.compose.Start < 0 || v.compose.Start == v.compose.End) {
		v.sel = false
		v.send([]byte(string(v.ime)))
		v.ime, v.imeCaret = v.ime[:0], 0
	}
}

func (v *TermView) pointer(gtx layout.Context, e pointer.Event, top int, mode term.MouseMode) {
	v.lastMouse = image.Pt(int(e.Position.X), int(e.Position.Y))
	shift := e.Modifiers.Contain(key.ModShift)
	report := mode != term.MouseOff && !shift
	cx, cy := v.cellAt(e.Position)
	switch e.Kind {
	case pointer.Press:
		v.Focus(gtx)
		v.blinkStart = gtx.Now
		btn := -1
		switch {
		case e.Buttons.Contain(pointer.ButtonPrimary):
			btn = term.MouseLeft
		case e.Buttons.Contain(pointer.ButtonTertiary):
			btn = term.MouseMiddle
		case e.Buttons.Contain(pointer.ButtonSecondary):
			btn = term.MouseRight
		}
		if report && v.follow {
			v.mouseBtn = btn
			v.send(v.term.EncodeMouse(btn, cx, cy, mods(e.Modifiers), false, false))
			return
		}
		switch btn {
		case term.MouseMiddle:
			v.Paste(gtx)
		case term.MouseRight:
			if v.RightPaste {
				if v.sel {
					v.Copy(gtx)
					v.sel = false
				} else {
					v.Paste(gtx)
				}
			} else if v.OnMenu != nil {
				v.OnMenu()
			}
		case term.MouseLeft:
			p := v.posAt(e.Position, top)
			if gtx.Now.Sub(v.lastClick) < 400*time.Millisecond && p.line == v.lastPos.line && abs(p.col-v.lastPos.col) <= 1 {
				v.clicks++
			} else {
				v.clicks = 1
			}
			v.lastClick, v.lastPos = gtx.Now, p
			v.dragging = true
			switch {
			case shift && v.sel:
				v.head = p
			case v.clicks == 2:
				v.selMode = selWord
				v.anchor, v.anchorEnd = v.wordAt(cellPos{p.line, min(p.col, v.cols-1)})
				v.head = v.anchorEnd
				v.sel = true
			case v.clicks >= 3:
				v.selMode = selLine
				v.anchor, v.anchorEnd = cellPos{p.line, 0}, cellPos{p.line, v.cols}
				v.head = v.anchorEnd
				v.sel = true
			default:
				v.selMode = selChar
				v.anchor, v.head = p, p
				v.sel = false
			}
		}
	case pointer.Drag:
		if v.mouseBtn >= 0 {
			v.send(v.term.EncodeMouse(v.mouseBtn, cx, cy, mods(e.Modifiers), true, false))
			return
		}
		if !v.dragging {
			return
		}
		p := v.posAt(e.Position, top)
		switch v.selMode {
		case selWord:
			s, en := v.wordAt(cellPos{p.line, min(p.col, v.cols-1)})
			if p.before(v.anchor) {
				p = s
			} else {
				p = en
			}
		case selLine:
			if p.before(v.anchor) {
				p.col = 0
			} else {
				p.col = v.cols
			}
		}
		v.head = p
		if v.selMode == selChar {
			v.sel = v.head != v.anchor
		}
		// Dragging past the edges scrolls.
		if e.Position.Y < 0 {
			v.scrollLines(-1)
			gtx.Execute(op.InvalidateCmd{})
		} else if int(e.Position.Y) > v.padding.Y+v.rows*v.cellH {
			v.scrollLines(1)
			gtx.Execute(op.InvalidateCmd{})
		}
	case pointer.Release, pointer.Cancel:
		if v.mouseBtn >= 0 {
			if e.Kind == pointer.Release {
				v.send(v.term.EncodeMouse(v.mouseBtn, cx, cy, mods(e.Modifiers), false, true))
			}
			v.mouseBtn = -1
			return
		}
		if v.dragging {
			v.dragging = false
			if v.sel && v.CopyOnSelect {
				v.Copy(gtx)
			}
		}
	case pointer.Move:
		if report && mode == term.MouseAny && v.follow {
			v.send(v.term.EncodeMouse(term.MouseNoButton, cx, cy, mods(e.Modifiers), true, false))
		}
	case pointer.Scroll:
		if e.Modifiers.Contain(key.ModCtrl) {
			if v.OnZoom != nil && e.Scroll.Y != 0 {
				if e.Scroll.Y < 0 {
					v.OnZoom(1)
				} else {
					v.OnZoom(-1)
				}
			}
			return
		}
		v.scrollRem += e.Scroll.Y / float32(v.cellH)
		n := int(v.scrollRem)
		v.scrollRem -= float32(n)
		if n == 0 {
			return
		}
		v.term.Lock()
		altScreen := v.term.AltScreen()
		v.term.Unlock()
		switch {
		case report:
			btn := term.MouseWheelDown
			if n < 0 {
				btn = term.MouseWheelUp
			}
			for i := 0; i < min(abs(n), 10); i++ {
				v.send(v.term.EncodeMouse(btn, cx, cy, mods(e.Modifiers), false, false))
			}
		case altScreen:
			// Full-screen programs without mouse support get arrow keys.
			k := term.KeyDown
			if n < 0 {
				k = term.KeyUp
			}
			for i := 0; i < min(abs(n), 10); i++ {
				v.send(v.term.EncodeKey(k, 0))
			}
		default:
			v.scrollLines(n)
		}
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// Layout draws the terminal filling the available space.
func (v *TermView) Layout(gtx layout.Context) layout.Dimensions {
	size := gtx.Constraints.Max
	v.measure(gtx)
	sbW := gtx.Dp(10)
	cols := max((size.X-2*v.padding.X-sbW)/v.cellW, 2)
	rows := max((size.Y-2*v.padding.Y)/v.cellH, 1)
	if cols != v.cols || rows != v.rows {
		v.cols, v.rows = cols, rows
		if v.OnSize != nil {
			v.OnSize(cols, rows)
		}
	}

	// Snapshot the visible part of the grid.
	v.term.Lock()
	tcols, trows := v.term.Size()
	ev, hist := v.term.Evicted(), v.term.HistLen()
	if !v.follow {
		if v.topAbs < ev {
			v.topAbs = ev
		}
		if v.topAbs >= ev+hist {
			v.follow = true
		}
	}
	top := ev + hist
	if !v.follow {
		top = v.topAbs
	}
	if cap(v.rowsBuf) < trows {
		v.rowsBuf = make([][]term.Cell, trows)
	}
	v.rowsBuf = v.rowsBuf[:trows]
	for r := 0; r < trows; r++ {
		l := v.term.LineAt(top - ev + r)
		v.rowsBuf[r] = v.rowsBuf[r][:0]
		if l != nil {
			v.rowsBuf[r] = append(v.rowsBuf[r], l.Cells...)
		}
	}
	curX, curY, curVisible, curShape, curBlink := v.term.Cursor()
	reverse := v.term.ReverseVideo()
	v.term.Unlock()
	curY += ev + hist - top // cursor row relative to the view

	v.update(gtx, top)

	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	bg, fg := v.th.Bg0, v.th.TermFg
	if reverse {
		bg, fg = fg, bg
	}
	paint.Fill(gtx.Ops, bg)
	event.Op(gtx.Ops, v)
	key.InputHintOp{Tag: v, Hint: key.HintAny}.Add(gtx.Ops)
	pointer.CursorText.Add(gtx.Ops)

	var selA, selB cellPos
	if v.sel {
		selA, selB = v.selRange()
	}

	origin := op.Offset(v.padding).Push(gtx.Ops)
	for r := 0; r < trows && r < rows+1; r++ {
		cells := v.rowsBuf[r]
		if len(cells) > tcols {
			cells = cells[:tcols]
		}
		y := r * v.cellH
		v.drawRowBg(gtx, cells, y, bg, fg)
		if v.sel {
			line := top + r
			if line >= selA.line && line <= selB.line {
				from, to := 0, tcols
				if line == selA.line {
					from = selA.col
				}
				if line == selB.line {
					to = selB.col
				}
				if to > from {
					fill(gtx.Ops, image.Rect(from*v.cellW, y, to*v.cellW, y+v.cellH), v.th.TermSel)
				}
			}
		}
		v.drawRowText(gtx, cells, y, bg, fg)
	}

	// The caret tracks the cursor cell even while the cursor is hidden or
	// scrolled out of view: the input method anchors its preedit and
	// candidate window to it.
	caretRow := min(max(curY, 0), max(rows-1, 0))
	caretCol := min(max(curX, 0), max(tcols-1, 0))
	v.caretPx = image.Pt(caretCol*v.cellW, caretRow*v.cellH)

	// Cursor.
	if curVisible && curY >= 0 && curY < rows && curX < tcols {
		on := true
		if v.focused && curBlink {
			const period = 600 * time.Millisecond
			dt := gtx.Now.Sub(v.blinkStart)
			on = (dt/period)%2 == 0
			gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(period - dt%period)})
		}
		x, y := curX*v.cellW, curY*v.cellH
		w := v.cellW
		var under term.Cell
		if curY < len(v.rowsBuf) && curX < len(v.rowsBuf[curY]) {
			under = v.rowsBuf[curY][curX]
			if under.Attr&term.AttrWide != 0 {
				w *= 2
			}
		}
		r := image.Rect(x, y, x+w, y+v.cellH)
		c := v.th.TermCursor
		switch {
		case !v.focused:
			strokeRR(gtx.Ops, r, 0, float32(gtx.Dp(1)), c)
		case !on:
		case curShape == term.CursorBar:
			fill(gtx.Ops, image.Rect(x, y, x+max(gtx.Dp(2), 1), y+v.cellH), c)
		case curShape == term.CursorUnderline:
			fill(gtx.Ops, image.Rect(x, y+v.cellH-max(gtx.Dp(2), 1), x+w, y+v.cellH), c)
		default:
			fill(gtx.Ops, r, c)
			if under.R != 0 && under.R != ' ' {
				v.glyphBuf = v.glyphBuf[:0]
				v.appendGlyph(under, curX)
				v.flushGlyphs(gtx, y, v.th.Bg0)
			}
		}
	}

	// IME preedit text is shown inline at the cursor.
	if len(v.ime) > 0 {
		x, y := v.caretPx.X, v.caretPx.Y
		// Measure the text alone. The widget pads its size to the minimum
		// constraint, which here is the whole terminal, and the overflow
		// check below would then pin the composition to the line start.
		mgtx := gtx
		mgtx.Constraints.Min = image.Point{}
		m := op.Record(gtx.Ops)
		d := Label{Text: string(v.ime), Size: v.FontSize, Color: v.th.Text, Mono: true}.Layout(mgtx, v.th)
		call := m.Stop()
		if x+d.Size.X > cols*v.cellW {
			x = max(cols*v.cellW-d.Size.X, 0)
		}
		// Only underline the composition: a filled background makes the
		// preedit look like a separate staging box.
		fill(gtx.Ops, image.Rect(x, y+v.cellH-max(gtx.Dp(1), 1), x+d.Size.X, y+v.cellH), v.th.Accent)
		st := op.Offset(image.Pt(x, y)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		st.Pop()
	}
	origin.Pop()

	// Keep the input method informed about the composition and caret.
	if v.focused {
		st := imeState{snip: string(v.ime), caret: v.imeCaret, px: v.caretPx}
		if st != v.imeSent {
			v.imeSent = st
			gtx.Execute(key.SnippetCmd{Tag: v, Snippet: key.Snippet{Range: key.Range{Start: 0, End: len(v.ime)}, Text: st.snip}})
			gtx.Execute(key.SelectionCmd{Tag: v, Range: key.Range{Start: v.imeCaret, End: v.imeCaret}, Caret: v.imeCaretPos()})
		}
	} else {
		v.imeSent = imeState{caret: -1}
	}

	v.layoutScrollbar(gtx, size, sbW, ev, hist, top, trows)
	return layout.Dimensions{Size: size}
}

// imeCaretPos is the caret in the terminal view's own coordinates. The input
// router adds the view transform, so the value must not include the view's
// position in the window.
func (v *TermView) imeCaretPos() key.Caret {
	return key.Caret{
		Pos:     f32.Pt(float32(v.padding.X+v.caretPx.X), float32(v.padding.Y+v.caretPx.Y+v.ascent)),
		Ascent:  float32(v.ascent),
		Descent: float32(v.cellH - v.ascent),
	}
}

func (v *TermView) cellColors(c term.Cell, bg, fg color.NRGBA) (cfg, cbg color.NRGBA, hasBg bool) {
	cfg, cbg = fg, bg
	if !c.FG.IsDefault() {
		cfg = v.resolve(c.FG)
	}
	if !c.BG.IsDefault() {
		cbg = v.resolve(c.BG)
		hasBg = true
	}
	if c.Attr&term.AttrReverse != 0 {
		cfg, cbg = cbg, cfg
		hasBg = true
	}
	if c.Attr&term.AttrFaint != 0 {
		cfg = mix(cfg, cbg, 0.45)
	}
	if c.Attr&term.AttrHidden != 0 {
		cfg = cbg
	}
	return
}

func (v *TermView) resolve(c term.Color) color.NRGBA {
	if i, ok := c.Index(); ok {
		return v.th.indexed(i)
	}
	r, g, b, _ := c.RGB()
	return color.NRGBA{R: r, G: g, B: b, A: 0xff}
}

func (v *TermView) drawRowBg(gtx layout.Context, cells []term.Cell, y int, bg, fg color.NRGBA) {
	start := -1
	var cur color.NRGBA
	flush := func(end int) {
		if start >= 0 {
			fill(gtx.Ops, image.Rect(start*v.cellW, y, end*v.cellW, y+v.cellH), cur)
			start = -1
		}
	}
	for i, c := range cells {
		_, cbg, has := v.cellColors(c, bg, fg)
		if !has {
			flush(i)
			continue
		}
		if start >= 0 && cbg != cur {
			flush(i)
		}
		if start < 0 {
			start, cur = i, cbg
		}
	}
	flush(len(cells))
}

func (v *TermView) appendGlyph(c term.Cell, col int) {
	g, ok := v.shape(glyphKey{r: c.R, bold: c.Attr&term.AttrBold != 0, italic: c.Attr&term.AttrItalic != 0})
	if !ok {
		return
	}
	w := v.cellW
	if c.Attr&term.AttrWide != 0 {
		w *= 2
	}
	g.X = fixed.I(col*v.cellW) + (fixed.I(w)-g.Advance)/2
	v.glyphBuf = append(v.glyphBuf, g)
}

func (v *TermView) flushGlyphs(gtx layout.Context, y int, c color.NRGBA) {
	gs := v.glyphBuf
	if len(gs) == 0 {
		return
	}
	off := f32.Pt(float32(gs[0].X)/64, float32(y+v.ascent))
	t := op.Affine(f32.AffineId().Offset(off)).Push(gtx.Ops)
	path := v.th.Shaper.Shape(gs)
	outline := clip.Outline{Path: path}.Op().Push(gtx.Ops)
	paint.ColorOp{Color: c}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	outline.Pop()
	if call := v.th.Shaper.Bitmaps(gs); call != (op.CallOp{}) {
		call.Add(gtx.Ops)
	}
	t.Pop()
	v.glyphBuf = v.glyphBuf[:0]
}

func (v *TermView) drawRowText(gtx layout.Context, cells []term.Cell, y int, bg, fg color.NRGBA) {
	v.glyphBuf = v.glyphBuf[:0]
	var cur color.NRGBA
	lineW := max(gtx.Dp(1), 1)
	for i, c := range cells {
		if c.Attr&term.AttrWideTail != 0 {
			continue
		}
		cfg, _, _ := v.cellColors(c, bg, fg)
		if c.Attr&(term.AttrUnderline|term.AttrStrike) != 0 {
			w := v.cellW
			if c.Attr&term.AttrWide != 0 {
				w *= 2
			}
			if c.Attr&term.AttrUnderline != 0 {
				uy := y + min(v.ascent+lineW+1, v.cellH-lineW)
				fill(gtx.Ops, image.Rect(i*v.cellW, uy, i*v.cellW+w, uy+lineW), cfg)
			}
			if c.Attr&term.AttrStrike != 0 {
				sy := y + v.ascent*2/3
				fill(gtx.Ops, image.Rect(i*v.cellW, sy, i*v.cellW+w, sy+lineW), cfg)
			}
		}
		if c.R == ' ' || c.R == 0 {
			continue
		}
		if c.R >= 0x2500 && c.R <= 0x259f && v.drawBox(gtx, c.R, image.Rect(i*v.cellW, y, (i+1)*v.cellW, y+v.cellH), cfg) {
			continue
		}
		if len(v.glyphBuf) > 0 && cfg != cur {
			v.flushGlyphs(gtx, y, cur)
		}
		cur = cfg
		v.appendGlyph(c, i)
	}
	v.flushGlyphs(gtx, y, cur)
}

// Box drawing: each entry lists the weight of the left, right, up and down
// arms (0 none, 1 light, 2 heavy).
var boxArms = map[rune][4]uint8{
	'─': {1, 1, 0, 0}, '━': {2, 2, 0, 0}, '│': {0, 0, 1, 1}, '┃': {0, 0, 2, 2},
	'┌': {0, 1, 0, 1}, '┐': {1, 0, 0, 1}, '└': {0, 1, 1, 0}, '┘': {1, 0, 1, 0},
	'├': {0, 1, 1, 1}, '┤': {1, 0, 1, 1}, '┬': {1, 1, 0, 1}, '┴': {1, 1, 1, 0}, '┼': {1, 1, 1, 1},
	'┏': {0, 2, 0, 2}, '┓': {2, 0, 0, 2}, '┗': {0, 2, 2, 0}, '┛': {2, 0, 2, 0},
	'┣': {0, 2, 2, 2}, '┫': {2, 0, 2, 2}, '┳': {2, 2, 0, 2}, '┻': {2, 2, 2, 0}, '╋': {2, 2, 2, 2},
	'╭': {0, 1, 0, 1}, '╮': {1, 0, 0, 1}, '╰': {0, 1, 1, 0}, '╯': {1, 0, 1, 0},
	'╴': {1, 0, 0, 0}, '╶': {0, 1, 0, 0}, '╵': {0, 0, 1, 0}, '╷': {0, 0, 0, 1},
}

// drawBox renders box-drawing and block characters with rectangles so that
// they join seamlessly between cells regardless of the font.
func (v *TermView) drawBox(gtx layout.Context, r rune, cell image.Rectangle, c color.NRGBA) bool {
	x0, y0, x1, y1 := cell.Min.X, cell.Min.Y, cell.Max.X, cell.Max.Y
	w, h := x1-x0, y1-y0
	if arms, ok := boxArms[r]; ok {
		light := max(v.cellH/14, 1)
		heavy := light * 2
		th := func(a uint8) int {
			if a == 2 {
				return heavy
			}
			return light
		}
		cx, cy := x0+w/2, y0+h/2
		// Arms overlap at the center by the thickness of the crossing arm.
		vth, hth := 0, 0
		if arms[2] != 0 || arms[3] != 0 {
			vth = th(max(arms[2], arms[3]))
		}
		if arms[0] != 0 || arms[1] != 0 {
			hth = th(max(arms[0], arms[1]))
		}
		if a := arms[0]; a != 0 {
			t := th(a)
			fill(gtx.Ops, image.Rect(x0, cy-t/2, cx+(vth+1)/2, cy-t/2+t), c)
		}
		if a := arms[1]; a != 0 {
			t := th(a)
			fill(gtx.Ops, image.Rect(cx-vth/2, cy-t/2, x1, cy-t/2+t), c)
		}
		if a := arms[2]; a != 0 {
			t := th(a)
			fill(gtx.Ops, image.Rect(cx-t/2, y0, cx-t/2+t, cy+(hth+1)/2), c)
		}
		if a := arms[3]; a != 0 {
			t := th(a)
			fill(gtx.Ops, image.Rect(cx-t/2, cy-hth/2, cx-t/2+t, y1), c)
		}
		return true
	}
	switch {
	case r == '█':
		fill(gtx.Ops, cell, c)
	case r == '▀':
		fill(gtx.Ops, image.Rect(x0, y0, x1, y0+h/2), c)
	case r >= '▁' && r <= '▇':
		n := int(r - '▁' + 1)
		fill(gtx.Ops, image.Rect(x0, y1-h*n/8, x1, y1), c)
	case r >= '▉' && r <= '▏':
		n := int('▏' - r + 1)
		fill(gtx.Ops, image.Rect(x0, y0, x0+w*n/8, y1), c)
	case r == '▐':
		fill(gtx.Ops, image.Rect(x0+w/2, y0, x1, y1), c)
	case r == '░':
		fill(gtx.Ops, cell, alpha(c, uint8(int(c.A)/4)))
	case r == '▒':
		fill(gtx.Ops, cell, alpha(c, uint8(int(c.A)/2)))
	case r == '▓':
		fill(gtx.Ops, cell, alpha(c, uint8(int(c.A)*3/4)))
	case r == '▔':
		fill(gtx.Ops, image.Rect(x0, y0, x1, y0+max(h/8, 1)), c)
	case r == '▕':
		fill(gtx.Ops, image.Rect(x1-max(w/8, 1), y0, x1, y1), c)
	default:
		return false
	}
	return true
}

func (v *TermView) layoutScrollbar(gtx layout.Context, size image.Point, sbW, ev, hist, top, rows int) {
	total := hist + rows
	if hist == 0 {
		return
	}
	track := image.Rect(size.X-sbW, 0, size.X, size.Y)
	// Events.
	for {
		e, ok := gtx.Event(pointer.Filter{Target: &v.sbTag, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel | pointer.Enter | pointer.Leave})
		if !ok {
			break
		}
		pe, ok := e.(pointer.Event)
		if !ok {
			continue
		}
		switch pe.Kind {
		case pointer.Enter:
			v.sbHover = true
		case pointer.Leave:
			v.sbHover = false
		case pointer.Press, pointer.Drag:
			v.sbDrag = true
			frac := pe.Position.Y / float32(size.Y)
			t := int(frac*float32(total)) - rows/2
			t = min(max(t, 0), hist)
			if t >= hist {
				v.follow = true
			} else {
				v.follow = false
				v.topAbs = ev + t
			}
		case pointer.Release, pointer.Cancel:
			v.sbDrag = false
		}
	}
	st := clip.Rect(track).Push(gtx.Ops)
	event.Op(gtx.Ops, &v.sbTag)
	pointer.CursorDefault.Add(gtx.Ops)
	st.Pop()

	h := max(size.Y*rows/total, gtx.Dp(24))
	y := 0
	if total > rows {
		y = (size.Y - h) * (top - ev) / (total - rows)
	}
	c := alpha(v.th.Text3, 0x66)
	wpx := gtx.Dp(4)
	if v.sbHover || v.sbDrag {
		c = alpha(v.th.Text2, 0xaa)
		wpx = gtx.Dp(6)
	}
	if v.follow && !v.sbHover && !v.sbDrag {
		c = alpha(v.th.Text3, 0x33)
	}
	x := size.X - wpx - gtx.Dp(2)
	fillRR(gtx.Ops, image.Rect(x, y+2, x+wpx, y+h-2), wpx/2, c)
}
