package ui

import (
	"image"
	"image/color"
	"os"
	"strings"

	"gioui.org/f32"
	giofont "gioui.org/font/opentype"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/font/opentype/tables"
	"github.com/go-text/typesetting/fontscan"
)

type emojiPaintKey struct {
	r  rune
	px int
	fg color.NRGBA
}

type emojiLayer struct {
	path       clip.PathSpec
	color      color.NRGBA
	foreground bool
}

type emojiPaint struct {
	ops       op.Ops // owns the cached paths
	layers    []emojiLayer
	advance   float32
	bitmap    paint.ImageOp
	bounds    image.Rectangle
	hasBitmap bool
}

// Gio paints outline and bitmap glyphs, but discards COLR glyphs. The
// terminal paints COLR/CPAL layers itself, preserving the selected font's
// own artwork and palette rather than substituting images.
type emojiRenderer struct {
	fonts *fontscan.FontMap
	cache map[emojiPaintKey]*emojiPaint
}

func newEmojiRenderer(families string) *emojiRenderer {
	fm := fontscan.NewFontMap(nil)
	if dir, err := os.UserCacheDir(); err == nil {
		fm.UseSystemFonts(dir)
	}
	for _, ff := range emojiFontCollection() {
		fm.AddFace(ff.Face.Face(), fontscan.Location{File: string(ff.Font.Typeface)}, giofont.FontToDescription(ff.Font))
	}
	var aspect font.Aspect
	aspect.SetDefaults()
	list := strings.Split(families, ",")
	for i := range list {
		list[i] = strings.TrimSpace(list[i])
	}
	fm.SetQuery(fontscan.Query{Families: list, Aspect: aspect})
	return &emojiRenderer{fonts: fm, cache: make(map[emojiPaintKey]*emojiPaint)}
}

func isEmojiRune(r rune) bool {
	return r >= 0x1f000 && r <= 0x1faff || r >= 0x2600 && r <= 0x27bf ||
		r >= 0x23e9 && r <= 0x23f3 || r >= 0x23f8 && r <= 0x23fa ||
		r >= 0x25fb && r <= 0x25fe || r == 0x231a || r == 0x231b ||
		r == 0x2b50 || r == 0x2b55
}

func (e *emojiRenderer) glyph(r rune, px int, fg color.NRGBA) *emojiPaint {
	k := emojiPaintKey{r, px, fg}
	if p, ok := e.cache[k]; ok {
		return p
	}
	e.cache[k] = nil
	face := e.fonts.ResolveFace(r)
	if face == nil {
		return nil
	}
	gid, ok := face.NominalGlyph(r)
	if !ok {
		return nil
	}
	colored, ok := face.GlyphData(gid).(font.GlyphColor)
	if !ok {
		return nil
	} // Gio handles outline and bitmap faces.
	result := &emojiPaint{}
	scale := float32(px) / float32(face.Upem())
	result.advance = face.HorizontalAdvance(gid) * scale
	layers, ok := colored.Paint.(tables.PaintColrLayersResolved)
	if ok && len(face.CPAL) > 0 {
		for _, layer := range layers {
			outline, ok := face.GlyphData(font.GID(layer.GlyphID)).(font.GlyphOutline)
			if !ok {
				return nil
			}
			c := color.NRGBA{}
			foreground := layer.PaletteIndex == 0xffff
			if !foreground {
				if int(layer.PaletteIndex) >= len(face.CPAL[0]) {
					return nil
				}
				p := face.CPAL[0][layer.PaletteIndex]
				c = color.NRGBA{R: p.Red, G: p.Green, B: p.Blue, A: p.Alpha}
			}
			result.layers = append(result.layers, emojiLayer{path: emojiOutline(&result.ops, outline, scale), color: c, foreground: foreground})
		}
	} else {
		if img, bounds, ok := rasterColorGlyph(face, gid, colored.Paint, px, fg); ok {
			result.bitmap, result.bounds, result.hasBitmap = paint.NewImageOp(img), bounds, true
			e.cache[k] = result
			return result
		}
		// Preserve a visible monochrome outline for unsupported COLRv1 paint
		// graphs. Clone the font so the system font cache remains immutable.
		base := *face.Font
		base.COLR = nil
		outline, ok := font.NewFace(&base).GlyphData(gid).(font.GlyphOutline)
		if !ok || len(outline.Segments) == 0 {
			return nil
		}
		result.layers = []emojiLayer{{path: emojiOutline(&result.ops, outline, scale), foreground: true}}
	}
	if len(result.layers) == 0 {
		return nil
	}
	e.cache[k] = result
	return result
}

func emojiOutline(ops *op.Ops, outline font.GlyphOutline, scale float32) clip.PathSpec {
	var path clip.Path
	path.Begin(ops)
	for _, seg := range outline.Segments {
		pt := func(i int) f32.Point { return f32.Pt(seg.Args[i].X*scale, -seg.Args[i].Y*scale) }
		switch seg.Op {
		case opentype.SegmentOpMoveTo:
			path.MoveTo(pt(0))
		case opentype.SegmentOpLineTo:
			path.LineTo(pt(0))
		case opentype.SegmentOpQuadTo:
			path.QuadTo(pt(0), pt(1))
		case opentype.SegmentOpCubeTo:
			path.CubeTo(pt(0), pt(1), pt(2))
		}
	}
	return path.End()
}

func (e *emojiRenderer) draw(ops *op.Ops, r rune, px int, x, baseline, width int, fg color.NRGBA) bool {
	if !isEmojiRune(r) {
		return false
	}
	g := e.glyph(r, px, fg)
	if g == nil {
		return false
	}
	off := op.Affine(f32.AffineId().Offset(f32.Pt(float32(x)+(float32(width)-g.advance)/2, float32(baseline)))).Push(ops)
	if g.hasBitmap {
		pos := op.Offset(g.bounds.Min).Push(ops)
		g.bitmap.Add(ops)
		paint.PaintOp{}.Add(ops)
		pos.Pop()
		off.Pop()
		return true
	}
	for _, layer := range g.layers {
		c := layer.color
		if layer.foreground {
			c = fg
		}
		cl := clip.Outline{Path: layer.path}.Op().Push(ops)
		paint.ColorOp{Color: c}.Add(ops)
		paint.PaintOp{}.Add(ops)
		cl.Pop()
	}
	off.Pop()
	return true
}
