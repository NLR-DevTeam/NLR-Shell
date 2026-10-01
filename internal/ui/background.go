package ui

import (
	"image"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
)

// Strength of the background image. Gio only blends with source-over, so
// the image is laid over the UI at low opacity: over white this equals a
// multiply (正片叠底) blend, over black a screen (滤色) blend, which are the
// background colors of the light and dark palettes.
const (
	bgOpacityLight = 0.07
	bgOpacityDark  = 0.09
)

// background is the decoded background image.
type background struct {
	path string // the path that was requested last
	img  paint.ImageOp
	size image.Point
}

// loadBackground decodes the configured background image in the
// background, or drops it when the setting is empty.
func (a *App) loadBackground() {
	p := a.set.Background
	if p == a.bg.path {
		return
	}
	a.bg.path = p
	if p == "" {
		a.bg.size = image.Point{}
		a.host.Invalidate()
		return
	}
	go func() {
		img, err := decodeImage(p)
		a.Post(func() {
			if a.bg.path != p {
				return
			}
			if err != nil {
				a.bg.size = image.Point{}
				a.Toast(toastError, "无法加载背景图片："+err.Error())
				return
			}
			a.bg.img = paint.NewImageOp(img)
			a.bg.size = img.Bounds().Size()
		})
	}()
}

// layoutBackground covers the window with the background image, cropped to
// keep its aspect ratio.
func (a *App) layoutBackground(gtx layout.Context) {
	if a.bg.size.X == 0 || a.bg.size.Y == 0 {
		return
	}
	win := gtx.Constraints.Max
	iw, ih := float32(a.bg.size.X), float32(a.bg.size.Y)
	scale := max(float32(win.X)/iw, float32(win.Y)/ih)
	off := f32.Pt((float32(win.X)-iw*scale)/2, (float32(win.Y)-ih*scale)/2)
	opacity := float32(bgOpacityDark)
	if a.th.Light {
		opacity = bgOpacityLight
	}
	defer clip.Rect{Max: win}.Push(gtx.Ops).Pop()
	defer paint.PushOpacity(gtx.Ops, opacity).Pop()
	defer op.Affine(f32.AffineId().Scale(f32.Point{}, f32.Pt(scale, scale)).Offset(off)).Push(gtx.Ops).Pop()
	a.bg.img.Filter = paint.FilterLinear
	a.bg.img.Add(gtx.Ops)
	defer clip.Rect{Max: a.bg.size}.Push(gtx.Ops).Pop()
	paint.PaintOp{}.Add(gtx.Ops)
}
