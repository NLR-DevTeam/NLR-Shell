package ui

import (
	"image"
	"image/color"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"
)

// Installed font families, loaded once in the background.
var (
	fontsOnce   sync.Once
	fontsLoaded atomic.Bool
	fontsList   []string
)

func installedFonts() ([]string, bool) {
	fontsOnce.Do(func() {
		go func() {
			fontsList = systemFontFamilies()
			fontsLoaded.Store(true)
		}()
	})
	if !fontsLoaded.Load() {
		return nil, false
	}
	return fontsList, true
}

// styleWords are trailing words of a font name that name a style rather
// than a family.
var styleWords = map[string]bool{
	"regular": true, "bold": true, "italic": true, "oblique": true, "light": true,
	"semilight": true, "semibold": true, "demibold": true, "black": true, "thin": true,
	"medium": true, "extralight": true, "ultralight": true, "extrabold": true,
	"ultrabold": true, "heavy": true, "book": true, "roman": true,
}

// cleanFamilies strips style words, drops duplicates and sorts.
func cleanFamilies(names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		f := strings.Fields(n)
		for len(f) > 1 && styleWords[strings.ToLower(f[len(f)-1])] {
			f = f[:len(f)-1]
		}
		name := strings.Join(f, " ")
		if name == "" || strings.HasPrefix(name, ".") || seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		out = append(out, name)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}

// fontCombo is an input with a dropdown: type a value, or pick one from the
// list. By default the list holds the installed fonts, each shown in its own
// typeface.
type fontCombo struct {
	Field
	// items, if set, supplies the list instead of the installed fonts; ok is
	// false while it is still loading.
	items     func() (list []string, ok bool)
	open      bool
	filtering bool // the list is filtered by the typed text
	// picked is the value last chosen from the list; the change event its
	// SetText causes must not reopen the list.
	picked string
	toggle widget.Clickable
	list   widget.List
	rows   []widget.Clickable
	scrim  bool
	panel  bool
}

// Layout draws the input with its label and, when open, the list below it.
func (c *fontCombo) Layout(gtx layout.Context, th *Theme) layout.Dimensions {
	c.setup()
	if _, changed := c.Events(gtx); changed && gtx.Focused(&c.Editor) && c.Text() != c.picked {
		c.open, c.filtering = true, true
	}
	if c.toggle.Clicked(gtx) {
		c.open, c.filtering = !c.open, false
	}
	for {
		e, ok := gtx.Event(pointer.Filter{Target: &c.scrim, Kinds: pointer.Press})
		if !ok {
			break
		}
		if _, ok := e.(pointer.Event); ok {
			c.open = false
		}
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if c.Label == "" {
				return layout.Dimensions{}
			}
			return layout.Inset{Bottom: 5}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return th.txt(gtx, c.Label, 12, th.Text2)
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			d := c.box(gtx, th, func(gtx layout.Context) layout.Dimensions {
				return th.iconButton(gtx, &c.toggle, icExpand, 24, 18, th.Text2, c.open, "")
			})
			if c.open {
				// Deferred so the list draws above the rest of the dialog
				// and escapes its clipping.
				rec := op.Record(gtx.Ops)
				st := op.Offset(image.Pt(0, d.Size.Y+gtx.Dp(4))).Push(gtx.Ops)
				c.layoutList(gtx, th, d.Size.X)
				st.Pop()
				op.Defer(gtx.Ops, rec.Stop())
			}
			return d
		}),
	)
}

func (c *fontCombo) layoutList(gtx layout.Context, th *Theme, w int) {
	// A transparent scrim closes the list on any click outside it.
	big := image.Rect(-1<<14, -1<<14, 1<<14, 1<<14)
	sc := clip.Rect(big).Push(gtx.Ops)
	event.Op(gtx.Ops, &c.scrim)
	sc.Pop()

	var fonts []string
	var ok bool
	if c.items != nil {
		fonts, ok = c.items()
	} else {
		fonts, ok = installedFonts()
	}
	if c.filtering {
		q := strings.ToLower(strings.TrimSpace(c.Text()))
		var out []string
		for _, f := range fonts {
			if strings.Contains(strings.ToLower(f), q) {
				out = append(out, f)
			}
		}
		fonts = out
	}
	if len(c.rows) < len(fonts) {
		c.rows = make([]widget.Clickable, len(fonts))
	}
	for i, f := range fonts {
		if c.rows[i].Clicked(gtx) {
			c.SetText(f)
			c.picked = f
			c.open = false
			gtx.Execute(key.FocusCmd{Tag: &c.Editor})
		}
	}
	if !c.open {
		return
	}

	rowH := gtx.Dp(34)
	h := min(max(len(fonts), 1)*rowH+gtx.Dp(8), gtx.Dp(320))
	r := image.Rectangle{Max: image.Pt(w, h)}
	shadow(gtx.Ops, r, gtx.Dp(8))
	fillRR(gtx.Ops, r, gtx.Dp(8), th.Bg2)
	strokeRR(gtx.Ops, r, gtx.Dp(8), float32(gtx.Dp(1)), th.BorderHi)
	pc := clip.UniformRRect(r, gtx.Dp(8)).Push(gtx.Ops)
	defer pc.Pop()
	event.Op(gtx.Ops, &c.panel)

	lg := gtx
	lg.Constraints = layout.Exact(image.Pt(w, h-gtx.Dp(8)))
	st := op.Offset(image.Pt(0, gtx.Dp(4))).Push(gtx.Ops)
	defer st.Pop()
	switch {
	case !ok:
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(100 * time.Millisecond)})
		layout.Center.Layout(lg, func(gtx layout.Context) layout.Dimensions { return th.spinner(gtx, 18) })
	case len(fonts) == 0:
		layout.Center.Layout(lg, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min = image.Point{}
			return th.txt(gtx, "无匹配", 12, th.Text3)
		})
	default:
		current := strings.ToLower(strings.TrimSpace(c.Text()))
		th.list(lg, &c.list, len(fonts), func(gtx layout.Context, i int) layout.Dimensions {
			f := fonts[i]
			gtx.Constraints = layout.Exact(image.Pt(w, rowH))
			return c.rows[i].Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				rr := image.Rect(gtx.Dp(4), 0, w-gtx.Dp(4), rowH)
				switch {
				case strings.ToLower(f) == current:
					fillRR(gtx.Ops, rr, gtx.Dp(5), alpha(th.Accent, 0x22))
				case c.rows[i].Hovered():
					fillRR(gtx.Ops, rr, gtx.Dp(5), th.Bg3)
				}
				layout.Inset{Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.W.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints.Min = image.Point{}
						// The name is drawn in the font itself, falling back to
						// the UI font for characters it lacks.
						if c.items != nil {
							return th.txt(gtx, f, 13, th.Text)
						}
						return fontPreview(gtx, th, f, th.Text)
					})
				})
				return layout.Dimensions{Size: gtx.Constraints.Max}
			})
		})
	}
}

// fontPreview draws a font name in that font.
func fontPreview(gtx layout.Context, th *Theme, family string, c color.NRGBA) layout.Dimensions {
	gtx.Constraints.Min.Y = 0
	wl := widget.Label{MaxLines: 1}
	return wl.Layout(gtx, th.Shaper, font.Font{Typeface: font.Typeface(family + ", " + string(th.Face))}, unit.Sp(15), family, colorMat(gtx, c))
}
