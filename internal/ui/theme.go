// Package ui is the Gio user interface of NLR Shell.
package ui

import (
	"image"
	"image/color"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// Theme holds fonts and colors shared by all widgets.
type Theme struct {
	Shaper *text.Shaper
	Mat    *material.Theme
	Face   font.Typeface
	Mono   font.Typeface
	tip    tooltip

	Bg0      color.NRGBA // window and terminal background
	Bg1      color.NRGBA // panels
	Bg2      color.NRGBA // raised surfaces, inputs
	Bg3      color.NRGBA // hover
	Bg4      color.NRGBA // pressed, selected
	Border   color.NRGBA
	BorderHi color.NRGBA
	Text     color.NRGBA
	Text2    color.NRGBA
	Text3    color.NRGBA
	Accent   color.NRGBA
	AccentFg color.NRGBA
	Blue     color.NRGBA
	Purple   color.NRGBA
	Warn     color.NRGBA
	Danger   color.NRGBA
	Select   color.NRGBA // text selection

	TermFg     color.NRGBA
	TermCursor color.NRGBA
	TermSel    color.NRGBA
	Palette    [16]color.NRGBA
}

func rgb(c uint32) color.NRGBA {
	return color.NRGBA{R: uint8(c >> 16), G: uint8(c >> 8), B: uint8(c), A: 0xff}
}

func alpha(c color.NRGBA, a uint8) color.NRGBA {
	c.A = a
	return c
}

// mix blends a towards b by t in [0,1].
func mix(a, b color.NRGBA, t float32) color.NRGBA {
	f := func(x, y uint8) uint8 { return uint8(float32(x) + (float32(y)-float32(x))*t) }
	return color.NRGBA{R: f(a.R, b.R), G: f(a.G, b.G), B: f(a.B, b.B), A: f(a.A, b.A)}
}

// NewTheme builds the default dark theme.
func NewTheme(monoFamily string) *Theme {
	th := &Theme{
		Shaper: text.NewShaper(text.WithCollection(gofont.Collection())),
		Face:   "Microsoft YaHei UI, Segoe UI, PingFang SC, sans-serif",

		Bg0:      rgb(0x0b0d10),
		Bg1:      rgb(0x101317),
		Bg2:      rgb(0x171b21),
		Bg3:      rgb(0x1e242c),
		Bg4:      rgb(0x27303a),
		Border:   rgb(0x232a33),
		BorderHi: rgb(0x34404d),
		Text:     rgb(0xd9dee6),
		Text2:    rgb(0x8d97a6),
		Text3:    rgb(0x5a6472),
		Accent:   rgb(0x3ddc97),
		AccentFg: rgb(0x04130c),
		Blue:     rgb(0x5aa9ff),
		Purple:   rgb(0xb18cff),
		Warn:     rgb(0xf2c45a),
		Danger:   rgb(0xff6b6b),
		Select:   color.NRGBA{R: 0x3d, G: 0xdc, B: 0x97, A: 0x50},

		TermFg:     rgb(0xd9dee6),
		TermCursor: rgb(0x3ddc97),
		TermSel:    color.NRGBA{R: 0x5a, G: 0xa9, B: 0xff, A: 0x66},
		Palette: [16]color.NRGBA{
			rgb(0x1c2128), rgb(0xff6b6b), rgb(0x3ddc97), rgb(0xf2c45a),
			rgb(0x5aa9ff), rgb(0xc58cff), rgb(0x4fd6d6), rgb(0xc9d1d9),
			rgb(0x5a6472), rgb(0xff8f8f), rgb(0x7ff0ba), rgb(0xffd98a),
			rgb(0x8cc4ff), rgb(0xd9b3ff), rgb(0x8be9e9), rgb(0xffffff),
		},
	}
	th.SetMono(monoFamily)
	m := material.NewTheme()
	m.Shaper = th.Shaper
	m.Face = th.Face
	m.Palette = material.Palette{Bg: th.Bg1, Fg: th.Text, ContrastBg: th.Accent, ContrastFg: th.AccentFg}
	m.TextSize = 13
	th.Mat = m
	return th
}

// SetMono changes the terminal font family, keeping sensible fallbacks.
func (th *Theme) SetMono(family string) {
	if family == "" {
		family = "Cascadia Mono"
	}
	th.Mono = font.Typeface(family + ", Cascadia Mono, Consolas, Microsoft YaHei UI, Go Mono, monospace")
}

// TermColor resolves a palette index to a color.
func (th *Theme) indexed(i uint8) color.NRGBA {
	switch {
	case i < 16:
		return th.Palette[i]
	case i < 232:
		i -= 16
		lv := [6]uint8{0, 95, 135, 175, 215, 255}
		return color.NRGBA{R: lv[i/36], G: lv[(i/6)%6], B: lv[i%6], A: 0xff}
	default:
		v := 8 + (i-232)*10
		return color.NRGBA{R: v, G: v, B: v, A: 0xff}
	}
}

// ---- Drawing helpers --------------------------------------------------

func fill(ops *op.Ops, r image.Rectangle, c color.NRGBA) {
	if r.Empty() {
		return
	}
	paint.FillShape(ops, c, clip.Rect(r).Op())
}

func fillRR(ops *op.Ops, r image.Rectangle, radius int, c color.NRGBA) {
	if r.Empty() {
		return
	}
	paint.FillShape(ops, c, clip.UniformRRect(r, radius).Op(ops))
}

// strokeRR draws a border of the given width just inside r. The outer edge
// follows exactly the outline that fillRR(r, radius) fills.
//
// The border is filled as a ring (an outer outline and an inner one wound
// the other way) rather than stroked: stroking an inset path gives the
// border a larger corner radius than the fill underneath, which then pokes
// out at the corners, and the stroker leaves faint streaks around arcs at
// some display scales.
func strokeRR(ops *op.Ops, r image.Rectangle, radius int, width float32, c color.NRGBA) {
	if r.Empty() || width <= 0 {
		return
	}
	w := int(width + 0.5)
	if w < 1 {
		w = 1
	}
	if 2*w >= r.Dx() || 2*w >= r.Dy() {
		fillRR(ops, r, radius, c)
		return
	}
	if radius <= 0 {
		// Square corners need no curves: four pixel-exact bars.
		fill(ops, image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+w), c)
		fill(ops, image.Rect(r.Min.X, r.Max.Y-w, r.Max.X, r.Max.Y), c)
		fill(ops, image.Rect(r.Min.X, r.Min.Y+w, r.Min.X+w, r.Max.Y-w), c)
		fill(ops, image.Rect(r.Max.X-w, r.Min.Y+w, r.Max.X, r.Max.Y-w), c)
		return
	}
	rad := float32(min(radius, r.Dx()/2, r.Dy()/2))
	fw := float32(w)
	x0, y0, x1, y1 := float32(r.Min.X), float32(r.Min.Y), float32(r.Max.X), float32(r.Max.Y)
	var p clip.Path
	p.Begin(ops)
	rrOutline(&p, x0, y0, x1, y1, rad, true)
	rrOutline(&p, x0+fw, y0+fw, x1-fw, y1-fw, max(rad-fw, 0), false)
	paint.FillShape(ops, c, clip.Outline{Path: p.End()}.Op())
}

// rrOutline appends a closed rounded rectangle to p, clockwise or
// counter-clockwise, with corners drawn as cubic Bézier quarter circles.
func rrOutline(p *clip.Path, x0, y0, x1, y1, r float32, clockwise bool) {
	const k = 0.5522847498 // control point distance for a quarter circle
	c := r * (1 - k)
	pt := f32.Pt
	if clockwise {
		p.MoveTo(pt(x0+r, y0))
		p.LineTo(pt(x1-r, y0))
		p.CubeTo(pt(x1-c, y0), pt(x1, y0+c), pt(x1, y0+r))
		p.LineTo(pt(x1, y1-r))
		p.CubeTo(pt(x1, y1-c), pt(x1-c, y1), pt(x1-r, y1))
		p.LineTo(pt(x0+r, y1))
		p.CubeTo(pt(x0+c, y1), pt(x0, y1-c), pt(x0, y1-r))
		p.LineTo(pt(x0, y0+r))
		p.CubeTo(pt(x0, y0+c), pt(x0+c, y0), pt(x0+r, y0))
	} else {
		p.MoveTo(pt(x0+r, y0))
		p.CubeTo(pt(x0+c, y0), pt(x0, y0+c), pt(x0, y0+r))
		p.LineTo(pt(x0, y1-r))
		p.CubeTo(pt(x0, y1-c), pt(x0+c, y1), pt(x0+r, y1))
		p.LineTo(pt(x1-r, y1))
		p.CubeTo(pt(x1-c, y1), pt(x1, y1-c), pt(x1, y1-r))
		p.LineTo(pt(x1, y0+r))
		p.CubeTo(pt(x1, y0+c), pt(x1-c, y0), pt(x1-r, y0))
		p.LineTo(pt(x0+r, y0))
	}
	p.Close()
}

func line(ops *op.Ops, a, b f32.Point, width float32, c color.NRGBA) {
	var p clip.Path
	p.Begin(ops)
	p.MoveTo(a)
	p.LineTo(b)
	paint.FillShape(ops, c, clip.Stroke{Path: p.End(), Width: width}.Op())
}

func colorMat(gtx layout.Context, c color.NRGBA) op.CallOp {
	m := op.Record(gtx.Ops)
	paint.ColorOp{Color: c}.Add(gtx.Ops)
	return m.Stop()
}

// Label describes one run of text.
type Label struct {
	Text      string
	Size      unit.Sp
	Color     color.NRGBA
	Weight    font.Weight
	Mono      bool
	Italic    bool
	MaxLines  int // 0 means 1; -1 means unlimited
	Alignment text.Alignment
}

// Layout draws the label.
func (l Label) Layout(gtx layout.Context, th *Theme) layout.Dimensions {
	f := font.Font{Typeface: th.Face, Weight: l.Weight}
	if l.Mono {
		f.Typeface = th.Mono
	}
	if l.Italic {
		f.Style = font.Italic
	}
	if l.Size == 0 {
		l.Size = 13
	}
	lines := l.MaxLines
	switch lines {
	case 0:
		lines = 1
	case -1:
		lines = 0
	}
	// A label never stretches vertically; containers center it instead.
	gtx.Constraints.Min.Y = 0
	wl := widget.Label{MaxLines: lines, Alignment: l.Alignment}
	return wl.Layout(gtx, th.Shaper, f, l.Size, l.Text, colorMat(gtx, l.Color))
}

// txt is shorthand for a single-line label.
func (th *Theme) txt(gtx layout.Context, s string, size unit.Sp, c color.NRGBA) layout.Dimensions {
	return Label{Text: s, Size: size, Color: c}.Layout(gtx, th)
}

func (th *Theme) txtW(gtx layout.Context, s string, size unit.Sp, c color.NRGBA, w font.Weight) layout.Dimensions {
	return Label{Text: s, Size: size, Color: c, Weight: w}.Layout(gtx, th)
}

func (th *Theme) mono(gtx layout.Context, s string, size unit.Sp, c color.NRGBA) layout.Dimensions {
	return Label{Text: s, Size: size, Color: c, Mono: true}.Layout(gtx, th)
}

// icon draws ic at the given size.
func drawIcon(gtx layout.Context, ic *widget.Icon, size unit.Dp, c color.NRGBA) layout.Dimensions {
	px := gtx.Dp(size)
	gtx.Constraints = layout.Exact(image.Pt(px, px))
	return ic.Layout(gtx, c)
}

// exact lays out w with exact dimensions.
func exact(gtx layout.Context, wpx, hpx int, w layout.Widget) layout.Dimensions {
	gtx.Constraints = layout.Exact(image.Pt(wpx, hpx))
	w(gtx)
	return layout.Dimensions{Size: image.Pt(wpx, hpx)}
}

// column is a vertical Flex that takes its natural height even inside a
// fixed-height parent, so the parent can center it.
func column(gtx layout.Context, children ...layout.FlexChild) layout.Dimensions {
	gtx.Constraints.Min.Y = 0
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

func hspace(dp unit.Dp) layout.FlexChild {
	return layout.Rigid(layout.Spacer{Width: dp}.Layout)
}

func vspace(dp unit.Dp) layout.FlexChild {
	return layout.Rigid(layout.Spacer{Height: dp}.Layout)
}
