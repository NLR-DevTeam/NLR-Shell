package ui

import (
	"image"
	"image/color"
	"image/draw"
	"math"

	"github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/font/opentype/tables"
	"golang.org/x/image/vector"
)

// COLRv1 gradients are rasterized once per glyph and pixel size, then
// cached as a Gio image. Font coordinates use an upward Y axis.
type colorGlyphRaster struct {
	face, outlines *font.Face
	bounds         image.Rectangle
	scale          float64
	foreground     color.NRGBA
}

type emojiAffine struct{ a, b, c, d, x, y float64 }

var emojiIdentity = emojiAffine{a: 1, d: 1}

func (t emojiAffine) point(x, y float64) (float64, float64) {
	return t.a*x + t.c*y + t.x, t.b*x + t.d*y + t.y
}
func (t emojiAffine) mul(u emojiAffine) emojiAffine {
	x, y := t.point(u.x, u.y)
	return emojiAffine{t.a*u.a + t.c*u.b, t.b*u.a + t.d*u.b, t.a*u.c + t.c*u.d, t.b*u.c + t.d*u.d, x, y}
}
func (t emojiAffine) inverse() (emojiAffine, bool) {
	det := t.a*t.d - t.b*t.c
	if math.Abs(det) < 1e-12 {
		return emojiAffine{}, false
	}
	u := emojiAffine{a: t.d / det, b: -t.b / det, c: -t.c / det, d: t.a / det}
	u.x, u.y = -u.a*t.x-u.c*t.y, -u.b*t.x-u.d*t.y
	return u, true
}

func rasterColorGlyph(face *font.Face, gid font.GID, root tables.PaintTable, px int, fg color.NRGBA) (*image.RGBA, image.Rectangle, bool) {
	ext, ok := face.GlyphExtents(gid)
	if !ok {
		return nil, image.Rectangle{}, false
	}
	s := float64(px) / float64(face.Upem())
	bounds := image.Rect(int(math.Floor(float64(ext.XBearing)*s))-1, int(math.Floor(-float64(ext.YBearing)*s))-1,
		int(math.Ceil(float64(ext.XBearing+ext.Width)*s))+1, int(math.Ceil(-float64(ext.YBearing+ext.Height)*s))+1)
	if bounds.Empty() || bounds.Dx() > 1024 || bounds.Dy() > 1024 {
		return nil, bounds, false
	}
	base := *face.Font
	base.COLR = nil
	r := colorGlyphRaster{face: face, outlines: font.NewFace(&base), bounds: bounds, scale: s, foreground: fg}
	img, ok := r.render(root, emojiIdentity, 0)
	return img, bounds, ok
}

func (r *colorGlyphRaster) empty() *image.RGBA {
	return image.NewRGBA(image.Rect(0, 0, r.bounds.Dx(), r.bounds.Dy()))
}

func (r *colorGlyphRaster) palette(index uint16, alpha tables.Fixed214) (color.NRGBA, bool) {
	if index == 0xffff {
		c := r.foreground
		c.A = uint8(float64(c.A) * float64(alpha) / 16384)
		return c, true
	}
	if len(r.face.CPAL) == 0 || int(index) >= len(r.face.CPAL[0]) {
		return color.NRGBA{}, false
	}
	c := r.face.CPAL[0][index]
	return color.NRGBA{R: c.Red, G: c.Green, B: c.Blue, A: uint8(float64(c.Alpha) * float64(alpha) / 16384)}, true
}

func (r *colorGlyphRaster) render(p tables.PaintTable, t emojiAffine, depth int) (*image.RGBA, bool) {
	if depth > 64 {
		return nil, false
	}
	recurse := func(child tables.PaintTable, transform emojiAffine) (*image.RGBA, bool) {
		return r.render(child, transform, depth+1)
	}
	switch p := p.(type) {
	case tables.PaintColrLayers:
		children, err := r.face.COLR.LayerList.Resolve(p)
		if err != nil {
			return nil, false
		}
		out := r.empty()
		for _, child := range children {
			img, ok := recurse(child, t)
			if !ok {
				return nil, false
			}
			draw.Draw(out, out.Bounds(), img, image.Point{}, draw.Over)
		}
		return out, true
	case tables.PaintColrLayersResolved:
		out := r.empty()
		for _, layer := range p {
			c, ok := r.palette(layer.PaletteIndex, 16384)
			if !ok {
				return nil, false
			}
			mask, ok := r.mask(font.GID(layer.GlyphID), t)
			if !ok {
				return nil, false
			}
			draw.DrawMask(out, out.Bounds(), image.NewUniform(c), image.Point{}, mask, image.Point{}, draw.Over)
		}
		return out, true
	case tables.PaintGlyph:
		img, ok := recurse(p.Paint, t)
		if !ok {
			return nil, false
		}
		mask, ok := r.mask(font.GID(p.GlyphID), t)
		if !ok {
			return nil, false
		}
		out := r.empty()
		draw.DrawMask(out, out.Bounds(), img, image.Point{}, mask, image.Point{}, draw.Src)
		return out, true
	case tables.PaintColrGlyph:
		child, ok := r.face.COLR.Search(tables.GlyphID(p.GlyphID))
		if !ok {
			return nil, false
		}
		return recurse(child, t)
	case tables.PaintSolid:
		c, ok := r.palette(p.PaletteIndex, p.Alpha)
		if !ok {
			return nil, false
		}
		out := r.empty()
		draw.Draw(out, out.Bounds(), image.NewUniform(c), image.Point{}, draw.Src)
		return out, true
	case tables.PaintLinearGradient:
		x0, y0, x1, y1, x2, y2 := float64(p.X0), float64(p.Y0), float64(p.X1), float64(p.Y1), float64(p.X2), float64(p.Y2)
		det := (x1-x0)*(y2-y0) - (y1-y0)*(x2-x0)
		return r.gradient(p.ColorLine, t, func(x, y float64) float64 {
			if det == 0 {
				return 0
			}
			return ((x-x0)*(y2-y0) - (y-y0)*(x2-x0)) / det
		})
	case tables.PaintRadialGradient:
		x0, y0, dx, dy := float64(p.X0), float64(p.Y0), float64(p.X1)-float64(p.X0), float64(p.Y1)-float64(p.Y0)
		r0, dr := float64(p.Radius0), float64(p.Radius1)-float64(p.Radius0)
		return r.gradient(p.ColorLine, t, func(x, y float64) float64 {
			x, y = x-x0, y-y0
			a, b, c := dx*dx+dy*dy-dr*dr, -2*(x*dx+y*dy+r0*dr), x*x+y*y-r0*r0
			if math.Abs(a) < 1e-12 {
				if b == 0 {
					return 0
				}
				return -c / b
			}
			disc := b*b - 4*a*c
			if disc < 0 {
				return math.NaN()
			}
			u, v := (-b+math.Sqrt(disc))/(2*a), (-b-math.Sqrt(disc))/(2*a)
			if u < v {
				u, v = v, u
			}
			if r0+u*dr >= 0 {
				return u
			}
			if r0+v*dr >= 0 {
				return v
			}
			return math.NaN()
		})
	case tables.PaintSweepGradient:
		start, end := (float64(p.StartAngle)/16384+1)*math.Pi, (float64(p.EndAngle)/16384+1)*math.Pi
		return r.gradient(p.ColorLine, t, func(x, y float64) float64 {
			angle := math.Atan2(y-float64(p.CenterY), x-float64(p.CenterX))
			if angle < 0 {
				angle += 2 * math.Pi
			}
			if end == start {
				return 0
			}
			return (angle - start) / (end - start)
		})
	case tables.PaintTransform:
		m := p.Transform
		return recurse(p.Paint, t.mul(emojiAffine{float64(m.Xx), float64(m.Yx), float64(m.Xy), float64(m.Yy), float64(m.Dx), float64(m.Dy)}))
	case tables.PaintTranslate:
		return recurse(p.Paint, t.mul(emojiAffine{a: 1, d: 1, x: float64(p.Dx), y: float64(p.Dy)}))
	case tables.PaintScale:
		return recurse(p.Paint, t.mul(emojiAffine{a: float64(p.ScaleX) / 16384, d: float64(p.ScaleY) / 16384}))
	case tables.PaintScaleUniform:
		return recurse(p.Paint, t.mul(emojiAffine{a: float64(p.Scale) / 16384, d: float64(p.Scale) / 16384}))
	case tables.PaintScaleAroundCenter:
		m := emojiAffine{a: float64(p.ScaleX) / 16384, d: float64(p.ScaleY) / 16384}
		return recurse(p.Paint, t.mul(emojiAround(m, p.CenterX, p.CenterY)))
	case tables.PaintScaleUniformAroundCenter:
		m := emojiAffine{a: float64(p.Scale) / 16384, d: float64(p.Scale) / 16384}
		return recurse(p.Paint, t.mul(emojiAround(m, p.CenterX, p.CenterY)))
	case tables.PaintRotate:
		return recurse(p.Paint, t.mul(emojiRotation(p.Angle)))
	case tables.PaintRotateAroundCenter:
		return recurse(p.Paint, t.mul(emojiAround(emojiRotation(p.Angle), p.CenterX, p.CenterY)))
	case tables.PaintSkew:
		m := emojiAffine{a: 1, b: math.Tan(float64(p.YSkewAngle) / 16384 * math.Pi), c: -math.Tan(float64(p.XSkewAngle) / 16384 * math.Pi), d: 1}
		return recurse(p.Paint, t.mul(m))
	case tables.PaintSkewAroundCenter:
		m := emojiAffine{a: 1, b: math.Tan(float64(p.YSkewAngle) / 16384 * math.Pi), c: -math.Tan(float64(p.XSkewAngle) / 16384 * math.Pi), d: 1}
		return recurse(p.Paint, t.mul(emojiAround(m, p.CenterX, p.CenterY)))
	case tables.PaintComposite:
		src, ok := recurse(p.SourcePaint, t)
		if !ok {
			return nil, false
		}
		dst, ok := recurse(p.BackdropPaint, t)
		if !ok {
			return nil, false
		}
		return emojiComposite(src, dst, p.CompositeMode)
	}
	return nil, false
}

func emojiAround(m emojiAffine, x, y int16) emojiAffine {
	m.x, m.y = float64(x)-m.a*float64(x)-m.c*float64(y), float64(y)-m.b*float64(x)-m.d*float64(y)
	return m
}
func emojiRotation(angle tables.Fixed214) emojiAffine {
	s, c := math.Sincos(float64(angle) / 16384 * math.Pi)
	return emojiAffine{a: c, b: s, c: -s, d: c}
}

func (r *colorGlyphRaster) mask(gid font.GID, t emojiAffine) (*image.Alpha, bool) {
	outline, ok := r.outlines.GlyphData(gid).(font.GlyphOutline)
	if !ok {
		return nil, false
	}
	z := vector.NewRasterizer(r.bounds.Dx(), r.bounds.Dy())
	for _, seg := range outline.Segments {
		pt := func(i int) (float32, float32) {
			x, y := t.point(float64(seg.Args[i].X), float64(seg.Args[i].Y))
			return float32(x*r.scale - float64(r.bounds.Min.X)), float32(-y*r.scale - float64(r.bounds.Min.Y))
		}
		x, y := pt(0)
		switch seg.Op {
		case opentype.SegmentOpMoveTo:
			z.ClosePath()
			z.MoveTo(x, y)
		case opentype.SegmentOpLineTo:
			z.LineTo(x, y)
		case opentype.SegmentOpQuadTo:
			x1, y1 := pt(1)
			z.QuadTo(x, y, x1, y1)
		case opentype.SegmentOpCubeTo:
			x1, y1 := pt(1)
			x2, y2 := pt(2)
			z.CubeTo(x, y, x1, y1, x2, y2)
		}
	}
	z.ClosePath()
	mask := image.NewAlpha(image.Rect(0, 0, r.bounds.Dx(), r.bounds.Dy()))
	z.Draw(mask, mask.Bounds(), image.Opaque, image.Point{})
	return mask, true
}

func (r *colorGlyphRaster) gradient(line tables.ColorLine, t emojiAffine, position func(float64, float64) float64) (*image.RGBA, bool) {
	inv, ok := t.inverse()
	if !ok || len(line.ColorStops) == 0 {
		return nil, false
	}
	colors := make([]color.NRGBA, len(line.ColorStops))
	for i, stop := range line.ColorStops {
		c, ok := r.palette(stop.PaletteIndex, stop.Alpha)
		if !ok {
			return nil, false
		}
		colors[i] = c
	}
	out := r.empty()
	first, last := float64(line.ColorStops[0].StopOffset)/16384, float64(line.ColorStops[len(line.ColorStops)-1].StopOffset)/16384
	for y := 0; y < out.Bounds().Dy(); y++ {
		for x := 0; x < out.Bounds().Dx(); x++ {
			fx, fy := inv.point((float64(x+r.bounds.Min.X)+0.5)/r.scale, -(float64(y+r.bounds.Min.Y)+0.5)/r.scale)
			v := position(fx, fy)
			if math.IsNaN(v) {
				continue
			}
			if last > first && line.Extend != 0 {
				u := (v - first) / (last - first)
				if line.Extend == 1 {
					u = math.Mod(u, 2)
					if u < 0 {
						u += 2
					}
					if u > 1 {
						u = 2 - u
					}
				} else {
					u -= math.Floor(u)
				}
				v = first + u*(last-first)
			}
			c := colors[0]
			for i := 1; i < len(colors); i++ {
				hi, lo := float64(line.ColorStops[i].StopOffset)/16384, float64(line.ColorStops[i-1].StopOffset)/16384
				if v >= hi {
					c = colors[i]
					continue
				}
				if v > lo && hi > lo {
					c = emojiColorMix(colors[i-1], colors[i], (v-lo)/(hi-lo))
				}
				break
			}
			out.Set(x, y, c)
		}
	}
	return out, true
}

func emojiColorMix(a, b color.NRGBA, t float64) color.NRGBA {
	// Interpolate premultiplied colors, including transparent gradient stops.
	alpha := float64(a.A)*(1-t) + float64(b.A)*t
	if alpha <= 0 {
		return color.NRGBA{}
	}
	channel := func(x, y uint8) uint8 {
		return uint8(math.Round((float64(x)*float64(a.A)*(1-t) + float64(y)*float64(b.A)*t) / alpha))
	}
	return color.NRGBA{R: channel(a.R, b.R), G: channel(a.G, b.G), B: channel(a.B, b.B), A: uint8(math.Round(alpha))}
}

func emojiComposite(src, dst *image.RGBA, mode tables.CompositeMode) (*image.RGBA, bool) {
	if mode > tables.CompositePlus {
		return nil, false
	}
	for i := 0; i < len(dst.Pix); i += 4 {
		sa, da := float64(src.Pix[i+3])/255, float64(dst.Pix[i+3])/255
		fs, fd := 0.0, 0.0
		switch mode {
		case tables.CompositeSrc:
			fs = 1
		case tables.CompositeDest:
			fd = 1
		case tables.CompositeSrcOver:
			fs, fd = 1, 1-sa
		case tables.CompositeDestOver:
			fs, fd = 1-da, 1
		case tables.CompositeSrcIn:
			fs = da
		case tables.CompositeDestIn:
			fd = sa
		case tables.CompositeSrcOut:
			fs = 1 - da
		case tables.CompositeDestOut:
			fd = 1 - sa
		case tables.CompositeSrcAtop:
			fs, fd = da, 1-sa
		case tables.CompositeDestAtop:
			fs, fd = 1-da, sa
		case tables.CompositeXor:
			fs, fd = 1-da, 1-sa
		case tables.CompositePlus:
			fs, fd = 1, 1
		}
		for c := 0; c < 4; c++ {
			dst.Pix[i+c] = uint8(math.Min(255, math.Round(float64(src.Pix[i+c])*fs+float64(dst.Pix[i+c])*fd)))
		}
	}
	return dst, true
}
