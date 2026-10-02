package ui

import (
	"image"
	"math"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"
)

// Animation durations.
const (
	animMenu   = 110 * time.Millisecond
	animDialog = 160 * time.Millisecond
	animPanel  = 150 * time.Millisecond
	animToast  = 180 * time.Millisecond
)

// animIn returns the eased progress in [0,1] of an animation that began at
// start and lasts d, and asks for another frame while it runs. It is 1 when
// animations are switched off.
func (a *App) animIn(gtx layout.Context, start time.Time, d time.Duration) float32 {
	if !a.set.Animations {
		return 1
	}
	t := float32(gtx.Now.Sub(start)) / float32(d)
	if t >= 1 {
		return 1
	}
	gtx.Execute(op.InvalidateCmd{})
	t = 1 - max(t, 0)
	return 1 - t*t*t
}

// faded draws w at opacity p, shifted by off scaled with the distance left
// to travel, so that it slides into place while it fades in.
func faded(gtx layout.Context, p float32, off image.Point, w func()) {
	if p >= 1 {
		w()
		return
	}
	o := paint.PushOpacity(gtx.Ops, max(p, 0))
	st := op.Offset(image.Pt(int(float32(off.X)*(1-p)), int(float32(off.Y)*(1-p)))).Push(gtx.Ops)
	w()
	st.Pop()
	o.Pop()
}

// fader fades a section in when it appears and out before it goes.
type fader struct {
	init, on bool
	t        time.Time
}

// step returns the opacity of the section given whether it should show;
// zero means it is gone.
func (f *fader) step(gtx layout.Context, a *App, want bool) float32 {
	if !f.init {
		f.init, f.on = true, want
	}
	if want != f.on {
		// Reverse from where an unfinished transition stands.
		done := a.animIn(gtx, f.t, animPanel)
		f.on = want
		f.t = gtx.Now.Add(-time.Duration((1 - done) * float32(animPanel)))
	}
	p := a.animIn(gtx, f.t, animPanel)
	if !want {
		p = 1 - p
	}
	return p
}

// smoothScroll is the distance a list still has to travel after the wheel
// was turned.
type smoothScroll struct {
	pending float32
	last    time.Time
	// first and off are the list position before the last step; a list
	// that did not move has reached its end.
	first, off int
	moved      bool
}

// scrollStep takes the wheel events of l and moves the list a part of the
// way each frame. It must run before the list is laid out.
func (th *Theme) scrollStep(gtx layout.Context, l *widget.List) {
	if !th.animate {
		return
	}
	s := th.scrolls[l]
	for {
		e, ok := gtx.Event(pointer.Filter{Target: l, Kinds: pointer.Scroll,
			ScrollY: pointer.ScrollRange{Min: -1 << 20, Max: 1 << 20}})
		if !ok {
			break
		}
		pe, ok := e.(pointer.Event)
		if !ok || pe.Kind != pointer.Scroll || pe.Scroll.Y == 0 {
			continue
		}
		if s == nil {
			if th.scrolls == nil {
				th.scrolls = map[*widget.List]*smoothScroll{}
			}
			s = &smoothScroll{last: gtx.Now.Add(-16 * time.Millisecond)}
			th.scrolls[l] = s
		}
		s.pending += pe.Scroll.Y
		s.moved = false
	}
	if s == nil {
		return
	}
	if s.moved && s.first == l.Position.First && s.off == l.Position.Offset {
		delete(th.scrolls, l)
		return
	}
	dt := float32(gtx.Now.Sub(s.last).Seconds())
	s.last = gtx.Now
	step := s.pending * (1 - float32(math.Exp(float64(-dt/0.06))))
	if step > -1 && step < 1 {
		step = max(min(s.pending, 1), -1)
	}
	d := int(math.Round(float64(step)))
	s.pending -= float32(d)
	s.first, s.off, s.moved = l.Position.First, l.Position.Offset, d != 0
	l.Position.Offset += d
	l.Position.BeforeEnd = true
	if s.pending > -0.5 && s.pending < 0.5 {
		delete(th.scrolls, l)
		return
	}
	gtx.Execute(op.InvalidateCmd{})
}

// scrollArea makes l's smooth scrolling receive the wheel over size instead
// of the list. It must be added after the list, and passes every other
// event through.
func (th *Theme) scrollArea(gtx layout.Context, l *widget.List, size image.Point) {
	if !th.animate {
		return
	}
	area := clip.Rect{Max: size}.Push(gtx.Ops)
	pass := pointer.PassOp{}.Push(gtx.Ops)
	event.Op(gtx.Ops, l)
	pass.Pop()
	area.Pop()
}
