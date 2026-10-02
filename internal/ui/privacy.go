package ui

import (
	"image"
	"image/color"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
)

// Privacy mode hides server addresses and system information for
// screenshots and screen sharing. A hidden text is not drawn at all: a
// stand-in of the same shape is drawn blurred in its place, so its width
// stays and nothing about the real text can be recovered from the pixels.
// The terminal is left alone; what the server prints is not ours to edit.

// privacyFade is how long switching privacy mode takes.
const privacyFade = 180 * time.Millisecond

// stepPrivacy moves the theme's privacy level towards the setting.
func (a *App) stepPrivacy(gtx layout.Context) {
	target := float32(0)
	if a.set.Privacy {
		target = 1
	}
	last := a.privLast
	a.privLast = gtx.Now
	if a.th.privacy == target {
		return
	}
	step := float32(1)
	if !last.IsZero() {
		step = float32(gtx.Now.Sub(last)) / float32(privacyFade)
	}
	if a.th.privacy < target {
		a.th.privacy = min(a.th.privacy+step, target)
	} else {
		a.th.privacy = max(a.th.privacy-step, target)
	}
	gtx.Execute(op.InvalidateCmd{})
}

// standIns keeps the stand-in of each hidden text, so that it does not
// change from frame to frame.
var standIns = map[string]string{}

// standIn returns a text of the same shape as s that tells nothing about
// it: digits and letters become random ones, picked once per text while
// the program runs; separators stay.
func standIn(s string) string {
	if v, ok := standIns[s]; ok {
		return v
	}
	const lower = "abcdefghijklmnopqrstuvwxyz"
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteByte(byte('0' + rand.IntN(10)))
		case r >= 'a' && r <= 'z':
			b.WriteByte(lower[rand.IntN(26)])
		case r >= 'A' && r <= 'Z':
			b.WriteByte(lower[rand.IntN(26)] - 'a' + 'A')
		case strings.ContainsRune(" .:@-_/()[]", r):
			b.WriteRune(r)
		case r < 0x80:
			b.WriteByte('x')
		default:
			b.WriteRune('国')
		}
	}
	if len(standIns) > 1000 {
		clear(standIns)
	}
	standIns[s] = b.String()
	return standIns[s]
}

// blurTaps are the offsets, in units of the blur radius, at which the
// stand-in is drawn: rings around the middle, denser towards it, which
// adds up to a soft blur.
var blurTaps = func() [][2]float32 {
	taps := [][2]float32{{0, 0}}
	for _, ring := range []struct {
		r float64
		n int
	}{{1, 12}, {0.62, 8}, {0.28, 4}} {
		for i := 0; i < ring.n; i++ {
			a := (float64(i) + ring.r) * 2 * math.Pi / float64(ring.n)
			taps = append(taps, [2]float32{float32(ring.r * math.Cos(a)), float32(ring.r * math.Sin(a))})
		}
	}
	return taps
}()

// secret draws text that privacy mode hides. draw lays out one line of
// text in a color; secret calls it for the real text and, while privacy
// mode is on or fading in, for the blurred stand-in.
func (th *Theme) secret(gtx layout.Context, text string, c color.NRGBA, draw func(gtx layout.Context, s string, c color.NRGBA) layout.Dimensions) layout.Dimensions {
	t := th.privacy
	if t <= 0 || text == "" {
		return draw(gtx, text, c)
	}
	scale := func(k float32) uint8 { return uint8(min(float32(c.A)*k, 255)) }
	rec := op.Record(gtx.Ops)
	d := draw(gtx, text, alpha(c, scale(1-t)))
	real := rec.Stop()
	if t < 1 {
		real.Add(gtx.Ops)
	}

	// The stand-in is measured at its own width, not the room given to the
	// text, so that the capsule hugs it.
	fake := standIn(text)
	fg := gtx
	fg.Constraints.Min.X = 0
	rec = op.Record(gtx.Ops)
	fd := draw(fg, fake, c)
	rec.Stop()
	h := fd.Size.Y
	r := max(float32(h)*0.3, float32(gtx.Dp(3)))
	// A frosted capsule under the blur.
	pad := int(r)
	capsule := image.Rect(-pad/2, h*16/100, min(fd.Size.X, gtx.Constraints.Max.X)+pad/2, h*88/100)
	fillRR(gtx.Ops, capsule, capsule.Dy()/2, alpha(c, scale(0.08*t)))
	tap := alpha(c, scale(0.075*t))
	for _, o := range blurTaps {
		st := op.Offset(image.Pt(int(o[0]*r), int(o[1]*r))).Push(gtx.Ops)
		draw(fg, fake, tap)
		st.Pop()
	}
	return d
}

// secretTxt is th.txt for text that privacy mode hides.
func (th *Theme) secretTxt(gtx layout.Context, s string, size unit.Sp, c color.NRGBA) layout.Dimensions {
	return th.secret(gtx, s, c, func(gtx layout.Context, s string, c color.NRGBA) layout.Dimensions {
		return th.txt(gtx, s, size, c)
	})
}

// secretTxtW is th.txtW for text that privacy mode hides.
func (th *Theme) secretTxtW(gtx layout.Context, s string, size unit.Sp, c color.NRGBA, w font.Weight) layout.Dimensions {
	return th.secret(gtx, s, c, func(gtx layout.Context, s string, c color.NRGBA) layout.Dimensions {
		return th.txtW(gtx, s, size, c, w)
	})
}

// secretLabel lays out l with its text hidden in privacy mode.
func (th *Theme) secretLabel(gtx layout.Context, l Label) layout.Dimensions {
	return th.secret(gtx, l.Text, l.Color, func(gtx layout.Context, s string, c color.NRGBA) layout.Dimensions {
		l.Text, l.Color = s, c
		return l.Layout(gtx, th)
	})
}

// secretIn draws one line of text in which the given parts are hidden in
// privacy mode, such as a status message naming the server.
func (th *Theme) secretIn(gtx layout.Context, text string, parts []string, draw func(gtx layout.Context, s string, c color.NRGBA) layout.Dimensions, c color.NRGBA) layout.Dimensions {
	if th.privacy <= 0 {
		return draw(gtx, text, c)
	}
	type seg struct {
		s      string
		secret bool
	}
	segs := []seg{{s: text}}
	for _, p := range parts {
		if p == "" {
			continue
		}
		var out []seg
		for _, sg := range segs {
			if sg.secret {
				out = append(out, sg)
				continue
			}
			pieces := strings.Split(sg.s, p)
			for i, piece := range pieces {
				if i > 0 {
					out = append(out, seg{p, true})
				}
				if piece != "" {
					out = append(out, seg{s: piece})
				}
			}
		}
		segs = out
	}
	children := make([]layout.FlexChild, 0, len(segs))
	for _, sg := range segs {
		sg := sg
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if sg.secret {
				return th.secret(gtx, sg.s, c, draw)
			}
			return draw(gtx, sg.s, c)
		}))
	}
	return layout.Flex{Alignment: layout.Baseline}.Layout(gtx, children...)
}
