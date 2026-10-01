package ui

import (
	"image"
	"image/color"
	"time"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"
)

// ---- Context menu -----------------------------------------------------

// MenuItem is one entry of a popup menu.
type MenuItem struct {
	Label    string
	Icon     *widget.Icon
	Hint     string // shortcut text shown at the right
	Danger   bool
	Disabled bool
	Checked  bool
	Sep      bool // a separator line; other fields are ignored
	Do       func()
}

type menu struct {
	items  []MenuItem
	clicks []widget.Clickable
	pos    image.Point
	scrim  bool
}

// Menu opens a popup menu at the pointer position.
func (a *App) Menu(items ...MenuItem) {
	a.MenuAt(a.mouse, items...)
}

// MenuAt opens a popup menu at pos (window coordinates).
func (a *App) MenuAt(pos image.Point, items ...MenuItem) {
	a.menu = &menu{items: items, clicks: make([]widget.Clickable, len(items)), pos: pos}
	a.host.Invalidate()
}

func (a *App) closeMenu() {
	if a.menu != nil {
		a.menu = nil
		a.refocus = true
	}
}

func shadow(ops *op.Ops, r image.Rectangle, radius int) {
	for i := 1; i <= 6; i++ {
		g := i * 2
		rr := image.Rect(r.Min.X-g, r.Min.Y-g+i, r.Max.X+g, r.Max.Y+g+i)
		fillRR(ops, rr, radius+g, color.NRGBA{A: uint8(26 - i*3)})
	}
}

func (a *App) layoutMenu(gtx layout.Context) {
	m := a.menu
	if m == nil {
		return
	}
	th := a.th
	for {
		e, ok := gtx.Event(pointer.Filter{Target: &m.scrim, Kinds: pointer.Press})
		if !ok {
			break
		}
		if _, ok := e.(pointer.Event); ok {
			a.closeMenu()
		}
	}
	for i := range m.items {
		it := &m.items[i]
		if it.Sep || it.Disabled {
			continue
		}
		if m.clicks[i].Clicked(gtx) {
			a.closeMenu()
			if it.Do != nil {
				it.Do()
			}
			gtx.Execute(op.InvalidateCmd{})
		}
	}
	if a.menu == nil {
		return
	}
	area := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
	event.Op(gtx.Ops, &m.scrim)
	area.Pop()

	// Measure the widest row.
	width := gtx.Dp(180)
	for i := range m.items {
		it := &m.items[i]
		if it.Sep {
			continue
		}
		rec := op.Record(gtx.Ops)
		mg := gtx
		mg.Constraints = layout.Constraints{Max: image.Pt(gtx.Dp(420), gtx.Dp(40))}
		w := th.txt(mg, it.Label, 13, th.Text).Size.X + gtx.Dp(12+16+10+12)
		if it.Hint != "" {
			w += th.txt(mg, it.Hint, 12, th.Text3).Size.X + gtx.Dp(28)
		}
		rec.Stop()
		width = max(width, w)
	}

	rec := op.Record(gtx.Ops)
	cgtx := gtx
	cgtx.Constraints = layout.Constraints{Min: image.Pt(width, 0), Max: image.Pt(width, gtx.Constraints.Max.Y)}
	rows := make([]layout.FlexChild, 0, len(m.items)+2)
	rows = append(rows, vspace(5))
	for i := range m.items {
		i := i
		it := &m.items[i]
		if it.Sep {
			rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				h := gtx.Dp(9)
				fill(gtx.Ops, image.Rect(0, h/2, width, h/2+max(gtx.Dp(1), 1)), th.Border)
				return layout.Dimensions{Size: image.Pt(width, h)}
			}))
			continue
		}
		rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			h := gtx.Dp(30)
			gtx.Constraints = layout.Exact(image.Pt(width, h))
			if it.Disabled {
				gtx = gtx.Disabled()
			}
			return m.clicks[i].Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				fg := th.Text
				switch {
				case it.Disabled:
					fg = th.Text3
				case it.Danger:
					fg = th.Danger
				}
				if m.clicks[i].Hovered() && !it.Disabled {
					c := th.Bg4
					if it.Danger {
						c = alpha(th.Danger, 0x28)
					}
					fillRR(gtx.Ops, image.Rect(gtx.Dp(5), 0, width-gtx.Dp(5), h), gtx.Dp(5), c)
				}
				layout.Inset{Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							s := gtx.Dp(16)
							ic := it.Icon
							if it.Checked {
								ic = icCheck
							}
							if ic != nil {
								drawIcon(gtx, ic, 16, mix(fg, th.Bg2, 0.25))
							}
							return layout.Dimensions{Size: image.Pt(s+gtx.Dp(10), s)}
						}),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return th.txt(gtx, it.Label, 13, fg)
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if it.Hint == "" {
								return layout.Dimensions{}
							}
							return th.txt(gtx, it.Hint, 12, th.Text3)
						}),
					)
				})
				return layout.Dimensions{Size: image.Pt(width, h)}
			})
		}))
	}
	rows = append(rows, vspace(5))
	d := layout.Flex{Axis: layout.Vertical}.Layout(cgtx, rows...)
	call := rec.Stop()

	pos := m.pos
	win := gtx.Constraints.Max
	if pos.X+d.Size.X > win.X-4 {
		pos.X = max(win.X-d.Size.X-4, 4)
	}
	if pos.Y+d.Size.Y > win.Y-4 {
		pos.Y = max(pos.Y-d.Size.Y, 4)
	}
	st := op.Offset(pos).Push(gtx.Ops)
	r := image.Rectangle{Max: d.Size}
	shadow(gtx.Ops, r, gtx.Dp(8))
	fillRR(gtx.Ops, r, gtx.Dp(8), th.Bg2)
	strokeRR(gtx.Ops, r, gtx.Dp(8), float32(gtx.Dp(1)), th.BorderHi)
	// The menu itself swallows clicks that miss an item.
	ca := clip.Rect(r).Push(gtx.Ops)
	event.Op(gtx.Ops, m)
	call.Add(gtx.Ops)
	ca.Pop()
	st.Pop()
}

// ---- Dialogs ----------------------------------------------------------

// Dialog is a modal window drawn above the main content.
type Dialog interface {
	Layout(gtx layout.Context, a *App) layout.Dimensions
	// Submit is called when Enter is pressed.
	Submit(a *App)
	// Cancel is called when Escape is pressed or the dialog is dismissed.
	Cancel(a *App)
}

// multiline dialogs keep Enter for their text editor.
type multiline interface{ Multiline() bool }

// Open shows a dialog.
func (a *App) Open(d Dialog) {
	a.dialogs = append(a.dialogs, d)
	a.closeMenu()
	a.host.Invalidate()
}

// Close removes a dialog.
func (a *App) Close(d Dialog) {
	for i, x := range a.dialogs {
		if x == d {
			a.dialogs = append(a.dialogs[:i], a.dialogs[i+1:]...)
			break
		}
	}
	a.refocus = true
	a.host.Invalidate()
}

func (a *App) layoutDialogs(gtx layout.Context) {
	for i, d := range a.dialogs {
		top := i == len(a.dialogs)-1
		// Scrim blocks input to everything below.
		area := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
		fill(gtx.Ops, image.Rectangle{Max: gtx.Constraints.Max}, color.NRGBA{A: 0x8c})
		event.Op(gtx.Ops, d)
		pointer.CursorDefault.Add(gtx.Ops)
		for {
			if _, ok := gtx.Event(pointer.Filter{Target: d, Kinds: pointer.Press}); !ok {
				break
			}
		}
		dgtx := gtx
		if !top {
			dgtx = dgtx.Disabled()
		}
		layout.Center.Layout(dgtx, func(gtx layout.Context) layout.Dimensions {
			return d.Layout(gtx, a)
		})
		area.Pop()
	}
}

// dialogFrame draws the standard dialog chrome around body and footer.
func (a *App) dialogFrame(gtx layout.Context, title string, width unit.Dp, closeClk *widget.Clickable, d Dialog, body, footer layout.Widget) layout.Dimensions {
	th := a.th
	if closeClk.Clicked(gtx) {
		d.Cancel(a)
	}
	w := min(gtx.Dp(width), gtx.Constraints.Max.X-gtx.Dp(32))
	maxH := gtx.Constraints.Max.Y - gtx.Dp(48)
	rec := op.Record(gtx.Ops)
	cgtx := gtx
	cgtx.Constraints = layout.Constraints{Min: image.Pt(w, 0), Max: image.Pt(w, maxH)}
	dims := layout.Flex{Axis: layout.Vertical}.Layout(cgtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: 20, Right: 10, Top: 12, Bottom: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return th.txtW(gtx, title, 15, th.Text, font.SemiBold)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return th.iconButton(gtx, closeClk, icClose, 28, 16, th.Text2, false, "关闭")
					}),
				)
			})
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.Y = 0
			return layout.Inset{Left: 20, Right: 20, Bottom: 4}.Layout(gtx, body)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if footer == nil {
				return layout.Spacer{Height: 16}.Layout(gtx)
			}
			return layout.Inset{Left: 20, Right: 20, Top: 14, Bottom: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.E.Layout(gtx, footer)
			})
		}),
	)
	call := rec.Stop()
	r := image.Rectangle{Max: dims.Size}
	shadow(gtx.Ops, r, gtx.Dp(12))
	fillRR(gtx.Ops, r, gtx.Dp(12), th.Bg1)
	strokeRR(gtx.Ops, r, gtx.Dp(12), float32(gtx.Dp(1)), th.BorderHi)
	call.Add(gtx.Ops)
	return dims
}

// buttonRow lays out dialog buttons right-aligned with spacing.
func buttonRow(gtx layout.Context, btns ...layout.Widget) layout.Dimensions {
	children := make([]layout.FlexChild, 0, len(btns)*2)
	for i, b := range btns {
		if i > 0 {
			children = append(children, hspace(8))
		}
		children = append(children, layout.Rigid(b))
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx, children...)
}

// confirmDialog asks a yes/no question.
type confirmDialog struct {
	title   string
	message string
	detail  string // monospaced, e.g. a fingerprint
	okLabel string
	danger  bool
	onOK    func()
	onNo    func()

	closeClk, okClk, noClk widget.Clickable
}

func (d *confirmDialog) Submit(a *App) {
	a.Close(d)
	if d.onOK != nil {
		d.onOK()
	}
}

func (d *confirmDialog) Cancel(a *App) {
	a.Close(d)
	if d.onNo != nil {
		d.onNo()
	}
}

func (d *confirmDialog) Layout(gtx layout.Context, a *App) layout.Dimensions {
	th := a.th
	if d.okClk.Clicked(gtx) {
		d.Submit(a)
	}
	if d.noClk.Clicked(gtx) {
		d.Cancel(a)
	}
	ok := d.okLabel
	if ok == "" {
		ok = "确定"
	}
	kind := btnPrimary
	if d.danger {
		kind = btnDanger
	}
	return a.dialogFrame(gtx, d.title, 440, &d.closeClk, d,
		func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if d.message == "" {
						return layout.Dimensions{}
					}
					return layout.Inset{Bottom: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return Label{Text: d.message, Size: 13, Color: th.Text2, MaxLines: -1}.Layout(gtx, th)
					})
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if d.detail == "" {
						return layout.Dimensions{}
					}
					return layout.Inset{}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						rec := op.Record(gtx.Ops)
						dm := layout.UniformInset(10).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return Label{Text: d.detail, Size: 12, Color: th.Text, Mono: true, MaxLines: -1}.Layout(gtx, th)
						})
						call := rec.Stop()
						r := image.Rectangle{Max: image.Pt(gtx.Constraints.Max.X, dm.Size.Y)}
						fillRR(gtx.Ops, r, gtx.Dp(6), th.Bg0)
						strokeRR(gtx.Ops, r, gtx.Dp(6), float32(gtx.Dp(1)), th.Border)
						call.Add(gtx.Ops)
						return layout.Dimensions{Size: r.Max}
					})
				}),
			)
		},
		func(gtx layout.Context) layout.Dimensions {
			return buttonRow(gtx,
				func(gtx layout.Context) layout.Dimensions { return th.button(gtx, &d.noClk, "取消", nil, btnDefault) },
				func(gtx layout.Context) layout.Dimensions { return th.button(gtx, &d.okClk, ok, nil, kind) },
			)
		})
}

// inputDialog asks for one or more lines of text.
type inputDialog struct {
	title   string
	message string
	fields  []*Field
	check   *widget.Bool // optional checkbox
	checkLb string
	okLabel string
	onOK    func(values []string, checked bool)
	onNo    func()

	focused                bool
	closeClk, okClk, noClk widget.Clickable
}

func newInputDialog(title, label, value string, onOK func(string)) *inputDialog {
	f := &Field{Label: label}
	f.SetText(value)
	return &inputDialog{title: title, fields: []*Field{f}, onOK: func(v []string, _ bool) { onOK(v[0]) }}
}

func (d *inputDialog) Submit(a *App) {
	vals := make([]string, len(d.fields))
	for i, f := range d.fields {
		vals[i] = f.Text()
	}
	a.Close(d)
	if d.onOK != nil {
		d.onOK(vals, d.check != nil && d.check.Value)
	}
}

func (d *inputDialog) Cancel(a *App) {
	a.Close(d)
	if d.onNo != nil {
		d.onNo()
	}
}

func (d *inputDialog) Layout(gtx layout.Context, a *App) layout.Dimensions {
	th := a.th
	if !d.focused && len(d.fields) > 0 {
		d.focused = true
		d.fields[0].Focus(gtx)
		// Select the name part of a file name for quick renaming.
		n := d.fields[0].Editor.Len()
		d.fields[0].Editor.SetCaret(n, 0)
	}
	for _, f := range d.fields {
		f.Events(gtx)
	}
	if d.okClk.Clicked(gtx) {
		d.Submit(a)
	}
	if d.noClk.Clicked(gtx) {
		d.Cancel(a)
	}
	ok := d.okLabel
	if ok == "" {
		ok = "确定"
	}
	return a.dialogFrame(gtx, d.title, 420, &d.closeClk, d,
		func(gtx layout.Context) layout.Dimensions {
			var rows []layout.FlexChild
			if d.message != "" {
				rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Bottom: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return Label{Text: d.message, Size: 13, Color: th.Text2, MaxLines: -1}.Layout(gtx, th)
					})
				}))
			}
			for i, f := range d.fields {
				f := f
				if i > 0 {
					rows = append(rows, vspace(10))
				}
				rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions { return f.Layout(gtx, th) }))
			}
			if d.check != nil {
				rows = append(rows, vspace(12), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return th.checkbox(gtx, d.check, d.checkLb)
				}))
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
		},
		func(gtx layout.Context) layout.Dimensions {
			return buttonRow(gtx,
				func(gtx layout.Context) layout.Dimensions { return th.button(gtx, &d.noClk, "取消", nil, btnDefault) },
				func(gtx layout.Context) layout.Dimensions { return th.button(gtx, &d.okClk, ok, nil, btnPrimary) },
			)
		})
}

// ---- Toasts -----------------------------------------------------------

const (
	toastInfo = iota
	toastOK
	toastError
)

type toast struct {
	kind  int
	msg   string
	until time.Time
}

// Toast shows a transient message in the corner of the window.
func (a *App) Toast(kind int, msg string) {
	d := 3500 * time.Millisecond
	if kind == toastError {
		d = 7 * time.Second
	}
	a.toasts = append(a.toasts, toast{kind: kind, msg: msg, until: time.Now().Add(d)})
	if len(a.toasts) > 4 {
		a.toasts = a.toasts[len(a.toasts)-4:]
	}
	a.host.Invalidate()
}

func (a *App) layoutToasts(gtx layout.Context) {
	th := a.th
	live := a.toasts[:0]
	for _, t := range a.toasts {
		if t.until.After(gtx.Now) {
			live = append(live, t)
		}
	}
	a.toasts = live
	if len(live) == 0 {
		return
	}
	// Toasts stack downwards from the top right corner, where they do not
	// cover the buttons along the bottom edge.
	y := gtx.Dp(titleH) + gtx.Dp(12)
	for i := len(live) - 1; i >= 0; i-- {
		t := live[i]
		gtx.Execute(op.InvalidateCmd{At: t.until})
		ic, c := icInfo, th.Blue
		switch t.kind {
		case toastOK:
			ic, c = icDone, th.Accent
		case toastError:
			ic, c = icError, th.Danger
		}
		rec := op.Record(gtx.Ops)
		cgtx := gtx
		cgtx.Constraints = layout.Constraints{Max: image.Pt(min(gtx.Dp(460), gtx.Constraints.Max.X-gtx.Dp(32)), gtx.Dp(200))}
		d := layout.Inset{Left: 12, Right: 14, Top: 10, Bottom: 10}.Layout(cgtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Start}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return drawIcon(gtx, ic, 18, c) }),
				hspace(10),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = 0
					return Label{Text: t.msg, Size: 13, Color: th.Text, MaxLines: 4}.Layout(gtx, th)
				}),
			)
		})
		call := rec.Stop()
		pos := image.Pt(gtx.Constraints.Max.X-d.Size.X-gtx.Dp(16), y)
		st := op.Offset(pos).Push(gtx.Ops)
		r := image.Rectangle{Max: d.Size}
		shadow(gtx.Ops, r, gtx.Dp(8))
		fillRR(gtx.Ops, r, gtx.Dp(8), th.Bg2)
		strokeRR(gtx.Ops, r, gtx.Dp(8), float32(gtx.Dp(1)), mix(th.BorderHi, c, 0.35))
		call.Add(gtx.Ops)
		st.Pop()
		y += d.Size.Y + gtx.Dp(8)
	}
}
