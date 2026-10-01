package ui

import (
	"image/color"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"gioui.org/io/clipboard"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/widget"

	"nlrshell/internal/store"
)

// accentColor maps a store.Accent* name to its color.
func accentColor(name string) color.NRGBA {
	switch name {
	case store.AccentBlue:
		return rgb(0x0192ff) // 联创蓝
	case store.AccentOrange:
		return rgb(0xffa500) // CSS orange
	}
	return rgb(0x3ddc97) // NLR 绿
}

// systemIsDark caches the last answer of systemDark; 0 unknown, 1 dark,
// 2 light.
var systemIsDark atomic.Int32

// applyTheme sets the palette from the appearance and accent settings.
func (a *App) applyTheme() {
	light, lightTerm := false, false
	switch a.set.Appearance {
	case store.AppearanceLight:
		light = true
	case store.AppearanceLightTerm:
		light, lightTerm = true, true
	case store.AppearanceSystem:
		// A light system gets the light UI with a dark terminal.
		if systemIsDark.Load() == 0 {
			if dark, ok := systemDark(); ok {
				systemIsDark.Store(map[bool]int32{true: 1, false: 2}[dark])
			}
		}
		light = systemIsDark.Load() == 2
	}
	a.th.Apply(light, lightTerm, accentColor(a.set.Accent))
	a.host.Invalidate()
}

// watchSystemTheme follows the OS light/dark preference for the "follow
// system" appearance.
func (a *App) watchSystemTheme() {
	for range time.Tick(3 * time.Second) {
		dark, ok := systemDark()
		if !ok {
			continue
		}
		v := map[bool]int32{true: 1, false: 2}[dark]
		if systemIsDark.Swap(v) != v {
			a.Post(func() {
				if a.set.Appearance == store.AppearanceSystem {
					a.applyTheme()
				}
			})
		}
	}
}

// later runs fn on the UI goroutine with a layout context, before the next
// frame is laid out.
func (a *App) later(fn func(gtx layout.Context)) {
	a.withGtx = append(a.withGtx, fn)
	a.host.Invalidate()
}

// editMenu opens the cut/copy/paste menu for an input.
func (a *App) editMenu(e *widget.Editor) {
	start, end := e.Selection()
	hasSel := start != end
	// Masked inputs hold passwords: their contents never leave the field.
	secret := e.Mask != 0
	copySel := func(gtx layout.Context) {
		if s := e.SelectedText(); s != "" {
			gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(s))})
		}
	}
	items := []MenuItem{
		{Label: "剪切", Icon: icCut, Hint: "Ctrl+X", Disabled: !hasSel || secret || e.ReadOnly, Do: func() {
			a.later(func(gtx layout.Context) {
				copySel(gtx)
				e.Delete(1)
				gtx.Execute(key.FocusCmd{Tag: e})
			})
		}},
		{Label: "复制", Icon: icCopy, Hint: "Ctrl+C", Disabled: !hasSel || secret, Do: func() {
			a.later(func(gtx layout.Context) {
				copySel(gtx)
				gtx.Execute(key.FocusCmd{Tag: e})
			})
		}},
		{Label: "粘贴", Icon: icPaste, Hint: "Ctrl+V", Disabled: e.ReadOnly, Do: func() {
			a.later(func(gtx layout.Context) {
				gtx.Execute(key.FocusCmd{Tag: e})
				gtx.Execute(clipboard.ReadCmd{Tag: e})
			})
		}},
		{Sep: true},
		{Label: "全选", Icon: icSelectAll, Hint: "Ctrl+A", Disabled: e.Len() == 0, Do: func() {
			e.SetCaret(e.Len(), 0)
			a.later(func(gtx layout.Context) { gtx.Execute(key.FocusCmd{Tag: e}) })
		}},
	}
	a.Menu(items...)
	if a.menu != nil {
		a.menu.keepFocus = true
	}
}
