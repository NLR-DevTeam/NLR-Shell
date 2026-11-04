package ui

import (
	"math"
	"strings"
	"time"

	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"

	"nlrshell/internal/term"
)

// Touch input of the terminal. A tap brings up the soft keyboard, a drag
// scrolls, a long press selects the word under the finger (dragging on
// extends the selection) and opens the menu when the finger lifts, and two
// fingers pinch the font size. Programs that ask for mouse reports get the
// tap as a click and the drag as wheel events.

// termTouch is the state of the fingers on the terminal.
type termTouch struct {
	fingers map[pointer.ID]f32.Point
	// id is the first finger down, which taps, scrolls and selects.
	id        pointer.ID
	start     f32.Point
	lastY     float32
	at        time.Time
	down      bool
	moved     bool
	selecting bool
	// reported is set when the press went to the program as a click.
	reported bool
	// pinch is the distance between two fingers at the last zoom step, or
	// zero.
	pinch float32
}

func dist(a, b f32.Point) float32 {
	d := a.Sub(b)
	return float32(math.Hypot(float64(d.X), float64(d.Y)))
}

// fingerSpread returns the distance between the first two fingers found.
func (t *termTouch) fingerSpread() float32 {
	var pts []f32.Point
	for _, p := range t.fingers {
		pts = append(pts, p)
		if len(pts) == 2 {
			return dist(pts[0], pts[1])
		}
	}
	return 0
}

func (v *TermView) touchEvent(gtx layout.Context, e pointer.Event, top int, report bool, cx, cy int) {
	t := &v.touch
	if t.fingers == nil {
		t.fingers = map[pointer.ID]f32.Point{}
	}
	switch e.Kind {
	case pointer.Press:
		t.fingers[e.PointerID] = e.Position
		if len(t.fingers) > 1 {
			// A second finger starts a pinch and ends whatever the first
			// one was doing.
			t.pinch, t.moved = t.fingerSpread(), true
			return
		}
		v.Focus(gtx)
		v.blinkStart = gtx.Now
		*t = termTouch{fingers: t.fingers, id: e.PointerID, start: e.Position, lastY: e.Position.Y, at: gtx.Now, down: true}
		if report && v.follow {
			t.reported = true
			v.send(v.term.EncodeMouse(term.MouseLeft, cx, cy, 0, false, false))
			return
		}
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(longPressDelay)})
	case pointer.Drag:
		t.fingers[e.PointerID] = e.Position
		if t.pinch > 0 && len(t.fingers) > 1 {
			d := t.fingerSpread()
			switch r := d / t.pinch; {
			case r > 1.15 && v.OnZoom != nil:
				v.OnZoom(1)
				t.pinch = d
			case r < 0.87 && v.OnZoom != nil:
				v.OnZoom(-1)
				t.pinch = d
			}
			return
		}
		if e.PointerID != t.id || !t.down {
			return
		}
		if t.selecting {
			v.dragSelect(gtx, e.Position, top)
			return
		}
		if !t.moved && dist(e.Position, t.start) > float32(gtx.Dp(8)) {
			t.moved = true
		}
		if t.moved {
			// The content follows the finger: dragging down shows what is
			// above.
			v.wheel(t.lastY-e.Position.Y, report, cx, cy, 0)
			t.lastY = e.Position.Y
		}
	case pointer.Release, pointer.Cancel:
		if e.Kind == pointer.Cancel {
			// Cancel ends every touch and names no pointer.
			clear(t.fingers)
		} else {
			delete(t.fingers, e.PointerID)
		}
		if len(t.fingers) < 2 {
			t.pinch = 0
		}
		if (e.Kind == pointer.Release && e.PointerID != t.id) || !t.down {
			return
		}
		t.down = false
		switch {
		case t.reported:
			v.send(v.term.EncodeMouse(term.MouseLeft, cx, cy, 0, false, true))
			if !t.moved {
				gtx.Execute(key.SoftKeyboardCmd{Show: true})
			}
		case t.selecting:
			t.selecting = false
			v.dragging = false
			if e.Kind == pointer.Release && v.OnMenu != nil {
				v.OnMenu()
			}
		case !t.moved && e.Kind == pointer.Release:
			v.sel = false
			gtx.Execute(key.SoftKeyboardCmd{Show: true})
		}
	}
}

// touchTick turns a finger resting in place into a word selection.
func (v *TermView) touchTick(gtx layout.Context, top int) {
	t := &v.touch
	if !t.down || t.moved || t.selecting || t.reported || len(t.fingers) != 1 {
		return
	}
	if gtx.Now.Sub(t.at) < longPressDelay {
		gtx.Execute(op.InvalidateCmd{At: t.at.Add(longPressDelay)})
		return
	}
	t.selecting = true
	p := v.posAt(t.start, top)
	v.selMode = selWord
	v.anchor, v.anchorEnd = v.wordAt(cellPos{p.line, min(p.col, v.cols-1)})
	v.head = v.anchorEnd
	v.sel = true
	v.dragging = true
	gtx.Execute(op.InvalidateCmd{})
}

// TypeKey sends a key as if typed on a keyboard, with the sticky modifiers,
// which it then releases. name is a named key or a single character.
func (v *TermView) TypeKey(gtx layout.Context, name key.Name) {
	m := v.Sticky
	v.Sticky = 0
	v.blinkStart = gtx.Now
	if _, named := namedKeys[name]; !named && m&(key.ModCtrl|key.ModAlt) == 0 {
		v.sel = false
		v.send([]byte(strings.ToLower(string(name))))
		return
	}
	v.handleKey(gtx, key.Event{Name: name, Modifiers: m, State: key.Press})
}
