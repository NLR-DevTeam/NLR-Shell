package ui

import (
	"image"
	"image/color"
	"testing"

	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"nlrshell/internal/term"
)

func TestTerminalColorEmoji(t *testing.T) {
	if len(emojiFontCollection()) == 0 {
		t.Skip("Segoe UI Emoji is not installed")
	}
	t.Setenv("LOCALAPPDATA", t.TempDir())
	th := NewTheme("Cascadia Mono", "Microsoft YaHei UI", "")
	v := NewTermView(th, nil)
	v.measure(layout.Context{Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}})
	win, err := headless.NewWindow(80, 80)
	if err != nil {
		t.Skipf("headless rendering unavailable: %v", err)
	}
	defer win.Release()
	render := func(r rune, attr term.Attr) bool {
		var ops op.Ops
		cells := []term.Cell{{R: ' '}, {R: r, Attr: attr | term.AttrWide}, {R: ' ', Attr: term.AttrWideTail}}
		v.drawRowText(layout.Context{Ops: &ops}, cells, 20, color.NRGBA{A: 255}, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
		if err := win.Frame(&ops); err != nil {
			t.Fatal(err)
		}
		img := image.NewRGBA(image.Rect(0, 0, 80, 80))
		if err := win.Screenshot(img); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < len(img.Pix); i += 4 {
			if img.Pix[i] != img.Pix[i+1] || img.Pix[i+1] != img.Pix[i+2] {
				return true
			}
		}
		return false
	}
	for _, r := range "🚀🔧📦😀🔥🧩🦄😍😂🎉⚠🤔💻☁🌈💯👍" {
		for _, attr := range []term.Attr{0, term.AttrBold, term.AttrItalic, term.AttrBold | term.AttrItalic} {
			if !render(r, attr) {
				t.Fatalf("%U with attrs %x did not render in color", r, attr)
			}
		}
	}
	if render('🚀', term.AttrHidden) {
		t.Fatal("hidden emoji remains visible")
	}
	// Font changes must invalidate the current view's cached glyphs and layers.
	th.SetFonts("Cascadia Mono", "Microsoft YaHei UI", "Segoe UI Symbol")
	v.measure(layout.Context{Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}})
	if render('🚀', 0) {
		t.Fatal("monochrome font still renders the old color emoji")
	}
	th.SetFonts("Cascadia Mono", "Microsoft YaHei UI", "missing-emoji-font")
	v.measure(layout.Context{Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}})
	if !render('🚀', 0) {
		t.Fatal("missing font does not fall back to color emoji")
	}
}
