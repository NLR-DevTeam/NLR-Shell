package ui

import (
	"image"
	"image/color"
	"time"

	"gioui.org/font"
	"gioui.org/gesture"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

type btnKind int

const (
	btnDefault btnKind = iota
	btnPrimary
	btnGhost
	btnDanger
)

// button draws a text button, optionally with a leading icon.
func (th *Theme) button(gtx layout.Context, clk *widget.Clickable, label string, ic *widget.Icon, kind btnKind) layout.Dimensions {
	bg, fg, border := th.Bg2, th.Text, th.Border
	switch kind {
	case btnPrimary:
		bg, fg, border = th.Accent, th.AccentFg, th.Accent
	case btnGhost:
		bg, border = color.NRGBA{}, color.NRGBA{}
	case btnDanger:
		bg, fg, border = alpha(th.Danger, 0x22), th.Danger, alpha(th.Danger, 0x55)
	}
	disabled := !gtx.Enabled()
	switch {
	case disabled:
		fg = alpha(fg, 0x66)
		bg = alpha(bg, uint8(int(bg.A)/2))
	case clk.Pressed():
		if kind == btnPrimary {
			bg = mix(bg, th.Bg0, 0.25)
		} else {
			bg = th.Bg4
		}
	case clk.Hovered():
		if kind == btnPrimary {
			bg = mix(bg, color.NRGBA{R: 255, G: 255, B: 255, A: 255}, 0.15)
		} else if kind == btnDanger {
			bg = alpha(th.Danger, 0x33)
		} else {
			bg = th.Bg3
		}
	}
	h := gtx.Dp(32)
	return clk.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		m := op.Record(gtx.Ops)
		gtx.Constraints.Min = image.Pt(0, 0)
		d := layout.Inset{Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if ic == nil {
						return layout.Dimensions{}
					}
					d := drawIcon(gtx, ic, 16, fg)
					d.Size.X += gtx.Dp(6)
					return d
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					w := font.Normal
					if kind == btnPrimary {
						w = font.Medium
					}
					return th.txtW(gtx, label, 13, fg, w)
				}),
			)
		})
		call := m.Stop()
		size := image.Pt(d.Size.X, h)
		r := image.Rectangle{Max: size}
		rad := gtx.Dp(6)
		fillRR(gtx.Ops, r, rad, bg)
		if border.A != 0 && kind != btnPrimary {
			strokeRR(gtx.Ops, r, rad, float32(gtx.Dp(1)), border)
		}
		st := op.Offset(image.Pt(0, (h-d.Size.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		st.Pop()
		if !disabled {
			a := clip.Rect(r).Push(gtx.Ops)
			pointer.CursorPointer.Add(gtx.Ops)
			a.Pop()
		}
		return layout.Dimensions{Size: size}
	})
}

// tooltip tracks which control the pointer rests on so that the app can
// show its title after a short delay.
type tooltip struct {
	key   any
	text  string
	rows  []tipRow
	since time.Time
	seen  bool
	shown bool
	pos   image.Point
}

// tipRow is one line of a hover card: an icon and a value.
type tipRow struct {
	icon *widget.Icon
	text string
}

// hover reports that the pointer is over the control identified by key.
func (t *tooltip) hover(gtx layout.Context, key any, text string) {
	if t.key != key {
		t.key, t.since, t.shown = key, gtx.Now, false
	}
	t.text, t.rows, t.seen = text, nil, true
}

// hoverCard is hover for a card of several icon rows.
func (t *tooltip) hoverCard(gtx layout.Context, key any, rows []tipRow) {
	if t.key != key {
		t.key, t.since, t.shown = key, gtx.Now, false
	}
	t.text, t.rows, t.seen = "", rows, true
}

// iconButton draws a square button containing only an icon. title is shown
// as a tooltip when the pointer rests on the button.
func (th *Theme) iconButton(gtx layout.Context, clk *widget.Clickable, ic *widget.Icon, box, size unit.Dp, fg color.NRGBA, active bool, title string) layout.Dimensions {
	if title != "" && clk.Hovered() && !clk.Pressed() {
		th.tip.hover(gtx, clk, title)
	}
	px := gtx.Dp(box)
	return clk.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		r := image.Rectangle{Max: image.Pt(px, px)}
		switch {
		case !gtx.Enabled():
			fg = alpha(fg, 0x55)
		case clk.Pressed():
			fillRR(gtx.Ops, r, gtx.Dp(6), th.Bg4)
		case clk.Hovered():
			fillRR(gtx.Ops, r, gtx.Dp(6), th.Bg3)
		case active:
			fillRR(gtx.Ops, r, gtx.Dp(6), alpha(th.Accent, 0x14))
		}
		if active {
			fg = th.AccentText
		}
		ip := gtx.Dp(size)
		st := op.Offset(image.Pt((px-ip)/2, (px-ip)/2)).Push(gtx.Ops)
		drawIcon(gtx, ic, size, fg)
		st.Pop()
		return layout.Dimensions{Size: r.Max}
	})
}

// Field is a single-line text input with an optional label.
type Field struct {
	Editor widget.Editor
	Label  string
	Hint   string
	Mono   bool
	// Height of the input box; zero means the standard 32dp.
	Height unit.Dp
	init   bool
	menu   rightClick
}

func (f *Field) setup() {
	if !f.init {
		f.init = true
		f.Editor.SingleLine = true
		f.Editor.Submit = true
	}
}

// Text returns the field contents.
func (f *Field) Text() string { return f.Editor.Text() }

// SetText replaces the field contents and moves the caret to the end.
func (f *Field) SetText(s string) {
	f.setup()
	f.Editor.SetText(s)
	n := f.Editor.Len()
	f.Editor.SetCaret(n, n)
}

// Focus gives the field keyboard focus.
func (f *Field) Focus(gtx layout.Context) {
	gtx.Execute(key.FocusCmd{Tag: &f.Editor})
}

// Submitted reports whether Enter was pressed in the field, and drains
// other editor events. changed reports whether the text was edited.
func (f *Field) Events(gtx layout.Context) (submitted, changed bool) {
	f.setup()
	for {
		e, ok := f.Editor.Update(gtx)
		if !ok {
			break
		}
		switch e.(type) {
		case widget.SubmitEvent:
			submitted = true
		case widget.ChangeEvent:
			changed = true
		}
	}
	return
}

// Layout draws the field at the full available width.
func (f *Field) Layout(gtx layout.Context, th *Theme) layout.Dimensions {
	f.setup()
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if f.Label == "" {
				return layout.Dimensions{}
			}
			return layout.Inset{Bottom: 5}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return th.txt(gtx, f.Label, 12, th.Text2)
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return f.box(gtx, th, nil)
		}),
	)
}

// hitArea makes the whole box r focus the editor when clicked, not just
// the text inside it. It must be called before the editor is laid out so
// that the editor's own area stays on top.
func (f *Field) hitArea(gtx layout.Context, r image.Rectangle) {
	for {
		e, ok := gtx.Event(pointer.Filter{Target: f, Kinds: pointer.Press})
		if !ok {
			break
		}
		if pe, ok := e.(pointer.Event); ok && pe.Kind == pointer.Press {
			gtx.Execute(key.FocusCmd{Tag: &f.Editor})
		}
	}
	a := clip.Rect(r).Push(gtx.Ops)
	event.Op(gtx.Ops, f)
	pointer.CursorText.Add(gtx.Ops)
	a.Pop()
}

// fillEditor lays out a single-line editor across the full available width,
// vertically centered in the available height.
func fillEditor(gtx layout.Context, es editorStyle) layout.Dimensions {
	h := gtx.Constraints.Max.Y
	gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, 0)
	rec := op.Record(gtx.Ops)
	d := es.Layout(gtx)
	call := rec.Stop()
	st := op.Offset(image.Pt(0, max((h-d.Size.Y)/2, 0))).Push(gtx.Ops)
	call.Add(gtx.Ops)
	st.Pop()
	return layout.Dimensions{Size: image.Pt(d.Size.X, max(h, d.Size.Y))}
}

// box draws just the input box. trailing, if set, is drawn at the right
// edge inside the box.
func (f *Field) box(gtx layout.Context, th *Theme, trailing layout.Widget) layout.Dimensions {
	f.setup()
	h := gtx.Dp(32)
	if f.Height > 0 {
		h = gtx.Dp(f.Height)
	}
	w := gtx.Constraints.Max.X
	r := image.Rectangle{Max: image.Pt(w, h)}
	rad := gtx.Dp(6)
	fillRR(gtx.Ops, r, rad, th.Bg0)
	border := th.Border
	if gtx.Focused(&f.Editor) {
		border = th.Accent
	}
	strokeRR(gtx.Ops, r, rad, float32(gtx.Dp(1)), border)
	f.hitArea(gtx, r)
	cg := gtx
	cg.Constraints = layout.Exact(r.Max)
	layout.Inset{Left: 10, Right: 6}.Layout(cg, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return fillEditor(gtx, editorStyle{th: th, e: &f.Editor, hint: f.Hint, size: 13, mono: f.Mono})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if trailing == nil {
					return layout.Dimensions{}
				}
				return trailing(gtx)
			}),
		)
	})
	f.menuArea(gtx, th, r)
	return layout.Dimensions{Size: r.Max}
}

// menuArea opens the cut/copy/paste menu when r is right-clicked. It must be
// added after the editor: the area is pass-through, so the editor below it
// still receives every event.
func (f *Field) menuArea(gtx layout.Context, th *Theme, r image.Rectangle) {
	editMenuArea(gtx, th, &f.menu, &f.Editor, r)
}

// editMenuArea is menuArea for any editor.
func editMenuArea(gtx layout.Context, th *Theme, rc *rightClick, e *widget.Editor, r image.Rectangle) {
	if rc.Clicked(gtx) && th.editMenu != nil {
		gtx.Execute(key.FocusCmd{Tag: e})
		th.editMenu(e)
	}
	a := clip.Rect(r).Push(gtx.Ops)
	rc.Add(gtx.Ops)
	a.Pop()
}

// editorStyle draws a widget.Editor with the theme's fonts and colors.
type editorStyle struct {
	th   *Theme
	e    *widget.Editor
	hint string
	size unit.Sp
	mono bool
}

func (s editorStyle) Layout(gtx layout.Context) layout.Dimensions {
	es := material.Editor(s.th.Mat, s.e, s.hint)
	es.TextSize = s.size
	es.Color = s.th.Text
	es.HintColor = s.th.Text3
	es.SelectionColor = s.th.Select
	es.Font.Typeface = s.th.Face
	if s.mono {
		es.Font.Typeface = s.th.Mono
	}
	return es.Layout(gtx)
}

// checkbox draws a labeled check box bound to b.
func (th *Theme) checkbox(gtx layout.Context, b *widget.Bool, label string) layout.Dimensions {
	return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				s := gtx.Dp(16)
				r := image.Rectangle{Max: image.Pt(s, s)}
				if b.Value {
					fillRR(gtx.Ops, r, gtx.Dp(4), th.Accent)
					drawIcon(gtx, icCheck, 16, th.AccentFg)
				} else {
					fillRR(gtx.Ops, r, gtx.Dp(4), th.Bg0)
					c := th.BorderHi
					if b.Hovered() {
						c = th.Text3
					}
					strokeRR(gtx.Ops, r, gtx.Dp(4), float32(gtx.Dp(1)), c)
				}
				a := clip.Rect(r).Push(gtx.Ops)
				pointer.CursorPointer.Add(gtx.Ops)
				a.Pop()
				return layout.Dimensions{Size: r.Max}
			}),
			hspace(8),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return th.txt(gtx, label, 13, th.Text)
			}),
		)
	})
}

// Segmented is a row of mutually exclusive options.
type Segmented struct {
	Value  string
	clicks []widget.Clickable
}

// Layout draws the options (value, label pairs) and updates Value on click.
func (s *Segmented) Layout(gtx layout.Context, th *Theme, opts [][2]string) layout.Dimensions {
	if len(s.clicks) != len(opts) {
		s.clicks = make([]widget.Clickable, len(opts))
	}
	for i := range opts {
		if s.clicks[i].Clicked(gtx) {
			s.Value = opts[i][0]
		}
	}
	h := gtx.Dp(32)
	gtx.Constraints.Min = image.Point{}
	m := op.Record(gtx.Ops)
	children := make([]layout.FlexChild, len(opts))
	for i := range opts {
		i := i
		children[i] = layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return s.clicks[i].Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				sel := s.Value == opts[i][0]
				gtx.Constraints.Min = image.Point{}
				mm := op.Record(gtx.Ops)
				d := layout.Inset{Left: 14, Right: 14}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					c := th.Text2
					if sel {
						c = th.Text
					}
					return th.txt(gtx, opts[i][1], 13, c)
				})
				call := mm.Stop()
				r := image.Rectangle{Max: image.Pt(d.Size.X, h)}
				if sel {
					fillRR(gtx.Ops, r.Inset(gtx.Dp(2)), gtx.Dp(5), th.Bg4)
				} else if s.clicks[i].Hovered() {
					fillRR(gtx.Ops, r.Inset(gtx.Dp(2)), gtx.Dp(5), th.Bg3)
				}
				st := op.Offset(image.Pt(0, (h-d.Size.Y)/2)).Push(gtx.Ops)
				call.Add(gtx.Ops)
				st.Pop()
				a := clip.Rect(r).Push(gtx.Ops)
				pointer.CursorPointer.Add(gtx.Ops)
				a.Pop()
				return layout.Dimensions{Size: r.Max}
			})
		})
	}
	d := layout.Flex{}.Layout(gtx, children...)
	call := m.Stop()
	r := image.Rectangle{Max: image.Pt(d.Size.X, h)}
	fillRR(gtx.Ops, r, gtx.Dp(7), th.Bg0)
	strokeRR(gtx.Ops, r, gtx.Dp(7), float32(gtx.Dp(1)), th.Border)
	call.Add(gtx.Ops)
	return layout.Dimensions{Size: r.Max}
}

// list lays out a vertical scrolling list with a themed scrollbar.
func (th *Theme) list(gtx layout.Context, l *widget.List, n int, el layout.ListElement) layout.Dimensions {
	l.Axis = layout.Vertical
	ls := material.List(th.Mat, l)
	ls.AnchorStrategy = material.Overlay
	ls.Indicator.Color = alpha(th.Text3, 0x88)
	ls.Indicator.HoverColor = alpha(th.Text2, 0xcc)
	ls.Indicator.MinorWidth = 6
	ls.Track.MinorPadding = 2
	ls.Track.MajorPadding = 2
	return ls.Layout(gtx, n, el)
}

// listFade tracks when an auto-hiding scrollbar should show.
type listFade struct {
	hover      gesture.Hover
	first, off int
	active     time.Time
}

// autoHideList is list with a scrollbar that only shows while the pointer
// is over the list, while it scrolls, and for a moment after.
func (th *Theme) autoHideList(gtx layout.Context, l *widget.List, st *listFade, n int, el layout.ListElement) layout.Dimensions {
	const linger = 900 * time.Millisecond
	hovered := st.hover.Update(gtx.Source)
	if l.Position.First != st.first || l.Position.Offset != st.off {
		st.first, st.off, st.active = l.Position.First, l.Position.Offset, gtx.Now
	}
	recent := gtx.Now.Sub(st.active) < linger
	if recent && !hovered {
		gtx.Execute(op.InvalidateCmd{At: st.active.Add(linger)})
	}
	l.Axis = layout.Vertical
	ls := material.List(th.Mat, l)
	ls.AnchorStrategy = material.Overlay
	ls.Indicator.Color = alpha(th.Text3, 0x88)
	ls.Indicator.HoverColor = alpha(th.Text2, 0xcc)
	ls.Indicator.MinorWidth = 6
	ls.Track.MinorPadding = 2
	ls.Track.MajorPadding = 2
	if !hovered && !recent && !l.Scrollbar.Dragging() {
		ls.Indicator.Color, ls.Indicator.HoverColor = color.NRGBA{}, color.NRGBA{}
	}
	d := ls.Layout(gtx, n, el)
	// Watch for the pointer without taking events from the list.
	area := clip.Rect{Max: d.Size}.Push(gtx.Ops)
	pass := pointer.PassOp{}.Push(gtx.Ops)
	st.hover.Add(gtx.Ops)
	pass.Pop()
	area.Pop()
	return d
}

// Splitter is a draggable divider that adjusts a size in dp.
type Splitter struct {
	dragging bool
	startPos image.Point
	startVal int
	hover    bool
}

// Layout draws the handle with the given thickness along the minor axis and
// reports the new value while dragging. grow is +1 if moving the pointer in
// the positive axis direction should grow the value, -1 otherwise. mouse is
// the pointer position in window coordinates.
func (s *Splitter) Layout(gtx layout.Context, th *Theme, axis layout.Axis, value *int, lo, hi, grow int, mouse image.Point) layout.Dimensions {
	for {
		e, ok := gtx.Event(pointer.Filter{Target: s, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel | pointer.Enter | pointer.Leave})
		if !ok {
			break
		}
		pe, ok := e.(pointer.Event)
		if !ok {
			continue
		}
		switch pe.Kind {
		case pointer.Enter:
			s.hover = true
		case pointer.Leave:
			s.hover = false
		case pointer.Press:
			s.dragging = true
			s.startPos = mouse
			s.startVal = *value
		case pointer.Release, pointer.Cancel:
			s.dragging = false
		}
	}
	if s.dragging {
		d := mouse.Sub(s.startPos)
		delta := d.X
		if axis == layout.Vertical {
			delta = d.Y
		}
		v := s.startVal + int(float32(delta*grow)/gtx.Metric.PxPerDp)
		*value = min(max(v, lo), hi)
	}
	thick := gtx.Dp(5)
	var size image.Point
	if axis == layout.Horizontal {
		size = image.Pt(thick, gtx.Constraints.Max.Y)
	} else {
		size = image.Pt(gtx.Constraints.Max.X, thick)
	}
	r := image.Rectangle{Max: size}
	// A thin line normally, highlighted while hovered or dragged.
	lineR := r
	one := max(gtx.Dp(1), 1)
	if axis == layout.Horizontal {
		lineR.Min.X = thick / 2
		lineR.Max.X = lineR.Min.X + one
	} else {
		lineR.Min.Y = thick / 2
		lineR.Max.Y = lineR.Min.Y + one
	}
	if s.hover || s.dragging {
		fill(gtx.Ops, r, alpha(th.Accent, 0x55))
	} else {
		fill(gtx.Ops, lineR, th.Border)
	}
	a := clip.Rect(r).Push(gtx.Ops)
	event.Op(gtx.Ops, s)
	if axis == layout.Horizontal {
		pointer.CursorColResize.Add(gtx.Ops)
	} else {
		pointer.CursorRowResize.Add(gtx.Ops)
	}
	a.Pop()
	return layout.Dimensions{Size: size}
}

// rightClick detects secondary-button presses on the current clip area.
type rightClick struct{ tag bool }

// Add registers the handler for the current clip area. It must be called
// inside the area's clip, with pass-through so that left clicks still reach
// widgets below.
func (r *rightClick) Add(ops *op.Ops) {
	p := pointer.PassOp{}.Push(ops)
	event.Op(ops, r)
	p.Pop()
}

// Clicked reports whether the area was right-clicked this frame.
func (r *rightClick) Clicked(gtx layout.Context) bool {
	return r.Buttons(gtx).Contain(pointer.ButtonSecondary)
}

// Buttons returns the buttons pressed on the area this frame, for callers
// that also care about the middle button. Use it instead of Clicked, not
// together with it.
func (r *rightClick) Buttons(gtx layout.Context) pointer.Buttons {
	var b pointer.Buttons
	for {
		e, ok := gtx.Event(pointer.Filter{Target: r, Kinds: pointer.Press})
		if !ok {
			break
		}
		if pe, ok := e.(pointer.Event); ok {
			b |= pe.Buttons
		}
	}
	return b
}

// progress draws a horizontal bar filled to frac in [0,1].
func (th *Theme) progress(gtx layout.Context, frac float32, h unit.Dp, c color.NRGBA) layout.Dimensions {
	w, hp := gtx.Constraints.Max.X, gtx.Dp(h)
	r := image.Rectangle{Max: image.Pt(w, hp)}
	fillRR(gtx.Ops, r, hp/2, th.Bg3)
	frac = min(max(frac, 0), 1)
	if fw := int(float32(w) * frac); fw > 0 {
		fillRR(gtx.Ops, image.Rectangle{Max: image.Pt(max(fw, hp), hp)}, hp/2, c)
	}
	return layout.Dimensions{Size: r.Max}
}

// spinner draws an indeterminate progress ring.
func (th *Theme) spinner(gtx layout.Context, size unit.Dp) layout.Dimensions {
	px := gtx.Dp(size)
	gtx.Constraints = layout.Exact(image.Pt(px, px))
	l := material.Loader(th.Mat)
	l.Color = th.Accent
	return l.Layout(gtx)
}
