package ui

import (
	"image"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"

	"nlrshell/internal/store"
)

// homeDrag rearranges the saved connections on the home page: a card
// dragged onto another card goes in front of or behind it, onto a group
// title to the front of that group, and into the free space after a
// group's last card to its end. A drop into another group moves the
// connection there.
type homeDrag struct {
	pressed, active bool
	id              string
	start, pos      f32.Point
	grab            image.Point // pointer position inside the card
	// dropped is set for the frame of a drop, so that releasing the
	// button over the card does not also connect.
	dropped bool

	// Geometry of the last frame, in page coordinates.
	cards  []dragCard
	heads  []dragZone // group titles: the front of the group
	ends   []dragZone // free space after the last card: the end
	cardSz image.Point

	// Layout bookkeeping for the frame being drawn.
	rowH     map[int]int
	rowCards map[int][]dragCard // card x offsets and profiles per row
	rowHead  map[int]string
	rowEnd   map[int]dragZone // x range of the free space in a row
}

type dragCard struct {
	r    image.Rectangle
	p    store.Profile
	next string // the following card of the same group, if any
}

type dragZone struct {
	r     image.Rectangle
	group string
	first string // first card of the group, for a title
}

// dragTarget is where a drop would put the card.
type dragTarget struct {
	ok            bool
	group, before string
	bar           image.Rectangle // insertion mark
}

func (d *homeDrag) beginFrame() {
	d.rowH, d.rowCards, d.rowHead, d.rowEnd = map[int]int{}, map[int][]dragCard{}, map[int]string{}, map[int]dragZone{}
}

// events handles the pointer for dragging; enabled is false while the list
// is filtered, where positions do not tell the arranged order.
func (d *homeDrag) events(gtx layout.Context, h *homeView, enabled bool) {
	d.dropped = false
	for {
		e, ok := gtx.Event(pointer.Filter{Target: d, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel})
		if !ok {
			break
		}
		pe, ok := e.(pointer.Event)
		if !ok {
			continue
		}
		switch pe.Kind {
		case pointer.Press:
			d.pressed, d.active, d.id = false, false, ""
			if !enabled || !pe.Buttons.Contain(pointer.ButtonPrimary) {
				break
			}
			pt := image.Pt(int(pe.Position.X), int(pe.Position.Y))
			for _, c := range d.cards {
				if pt.In(c.r) {
					d.pressed, d.id, d.start, d.pos = true, c.p.ID, pe.Position, pe.Position
					d.grab = pt.Sub(c.r.Min)
				}
			}
		case pointer.Drag:
			if !d.pressed {
				break
			}
			d.pos = pe.Position
			if !d.active && abs(int(pe.Position.X-d.start.X))+abs(int(pe.Position.Y-d.start.Y)) > gtx.Dp(6) {
				d.active = true
			}
		case pointer.Release, pointer.Cancel:
			if d.active && pe.Kind == pointer.Release {
				d.pos = pe.Position
				if t := d.target(); t.ok {
					h.a.st.MoveProfile(d.id, t.group, t.before)
					h.refresh()
				}
				d.dropped = true
			}
			d.pressed, d.active = false, false
		}
	}
}

// target returns where the card would land at the pointer.
func (d *homeDrag) target() dragTarget {
	pt := image.Pt(int(d.pos.X), int(d.pos.Y))
	w := max(d.cardSz.X/40, 3)
	bar := func(x int, r image.Rectangle) image.Rectangle {
		return image.Rect(x-w/2, r.Min.Y, x-w/2+w, r.Max.Y)
	}
	skip := func(id string) string {
		// The card goes in front of id; in front of itself means in
		// front of the card after it.
		if id != d.id {
			return id
		}
		for _, c := range d.cards {
			if c.p.ID == id {
				return c.next
			}
		}
		return ""
	}
	gap := 0
	if len(d.cards) > 1 {
		gap = (d.cards[1].r.Min.X - d.cards[0].r.Max.X) / 2
	}
	for _, c := range d.cards {
		if !pt.In(c.r) {
			continue
		}
		if c.p.ID == d.id {
			return dragTarget{}
		}
		if pt.X < (c.r.Min.X+c.r.Max.X)/2 {
			return dragTarget{ok: true, group: c.p.Group, before: skip(c.p.ID), bar: bar(c.r.Min.X-max(gap, 0), c.r)}
		}
		return dragTarget{ok: true, group: c.p.Group, before: skip(c.next), bar: bar(c.r.Max.X+max(gap, 0), c.r)}
	}
	for _, z := range d.heads {
		if pt.In(z.r) {
			r := image.Rect(z.r.Min.X, z.r.Max.Y-w, z.r.Max.X, z.r.Max.Y)
			return dragTarget{ok: true, group: z.group, before: skip(z.first), bar: r}
		}
	}
	for _, z := range d.ends {
		if pt.In(z.r) {
			return dragTarget{ok: true, group: z.group, bar: bar(z.r.Min.X, z.r)}
		}
	}
	return dragTarget{}
}

// endFrame turns the rows laid out by the list into page geometry. x0 and
// w are the left edge and the width of the content column.
func (d *homeDrag) endFrame(pos layout.Position, x0, w int) {
	d.cards, d.heads, d.ends = d.cards[:0], d.heads[:0], d.ends[:0]
	y := -pos.Offset
	for i := pos.First; i < pos.First+pos.Count; i++ {
		h := d.rowH[i]
		for _, c := range d.rowCards[i] {
			c.r = c.r.Add(image.Pt(x0, y))
			d.cards = append(d.cards, c)
		}
		if g, ok := d.rowHead[i]; ok {
			first := ""
			// The title row is followed by the first card row.
			if cs := d.rowCards[i+1]; len(cs) > 0 {
				first = cs[0].p.ID
			}
			d.heads = append(d.heads, dragZone{r: image.Rect(x0, y, x0+w, y+h), group: g, first: first})
		}
		if z, ok := d.rowEnd[i]; ok {
			z.r = z.r.Add(image.Pt(x0, y))
			z.r.Max.Y = z.r.Min.Y + d.cardSz.Y
			d.ends = append(d.ends, z)
		}
		y += h
	}
}

// layout draws the insertion mark and the card under the pointer, and
// takes the pointer events of the whole page without blocking them.
func (d *homeDrag) layout(gtx layout.Context, th *Theme, h *homeView, size image.Point) {
	area := clip.Rect{Max: size}.Push(gtx.Ops)
	pass := pointer.PassOp{}.Push(gtx.Ops)
	event.Op(gtx.Ops, d)
	if d.active {
		pointer.CursorGrabbing.Add(gtx.Ops)
	}
	pass.Pop()
	area.Pop()
	if !d.active {
		return
	}
	if t := d.target(); t.ok {
		fillRR(gtx.Ops, t.bar, t.bar.Dx()/2, th.Accent)
	}
	var p store.Profile
	for _, c := range d.cards {
		if c.p.ID == d.id {
			p = c.p
		}
	}
	gtx.Execute(op.InvalidateCmd{})
	st := op.Offset(image.Pt(int(d.pos.X), int(d.pos.Y)).Sub(d.grab)).Push(gtx.Ops)
	defer st.Pop()
	r := image.Rectangle{Max: d.cardSz}
	rad := gtx.Dp(10)
	fillRR(gtx.Ops, r, rad, alpha(th.Bg2, 0xee))
	strokeRR(gtx.Ops, r, rad, float32(gtx.Dp(1)), th.Accent)
	gtx.Constraints = layout.Exact(d.cardSz)
	layout.Inset{Left: 14, Right: 14}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.W.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min = image.Point{}
			return column(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return h.title(gtx, p)
				}),
				vspace(3),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return th.secretLabel(gtx, Label{Text: p.Addr(), Size: 12, Color: th.Text2, Mono: true})
				}),
			)
		})
	})
}
