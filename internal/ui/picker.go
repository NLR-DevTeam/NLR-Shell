package ui

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/widget"

	"nlrshell/internal/store"
)

// pickFiles lets the user choose one or more files. It returns at once; the
// chosen paths are reported through done on the UI goroutine.
func pickFiles(a *App, title string, done func([]string)) {
	a.Open(newFilePicker(a, title, false, done))
}

// pickFolder lets the user choose a folder, again reporting through done.
func pickFolder(a *App, title string, done func(string)) {
	a.Open(newFilePicker(a, title, true, func(paths []string) {
		if len(paths) > 0 {
			done(paths[0])
		}
	}))
}

// pickEntry is one row of the file picker.
type pickEntry struct {
	name  string
	dir   bool
	link  bool
	size  int64
	mtime time.Time
}

// filePicker is a modal file browser for platforms whose windowing layer
// has no native file dialog (see platform_unix.go). It follows the remote
// file panel: click selects, double click opens or confirms, Ctrl and
// Shift extend the selection.
type filePicker struct {
	a       *App
	title   string
	folders bool // choose a directory instead of files

	dir     string
	entries []pickEntry
	sel     map[string]bool
	cursor  int
	anchor  int
	hover   int
	errMsg  string
	hidden  bool
	loaded  bool
	focused bool

	path    Field
	list    widget.List
	listTag struct{}

	upClk, homeClk, hiddenClk widget.Clickable
	closeClk, okClk, noClk    widget.Clickable

	lastHit  int
	lastTime time.Time

	done func([]string)
}

// OpenPicker opens the built-in file picker, reporting the chosen paths to
// done. It exists for cmd/shot; the application opens the picker through
// pickFiles and pickFolder, which pick the title and mode for each caller.
func (a *App) OpenPicker(title string, folders bool, done func([]string)) {
	a.Open(newFilePicker(a, title, folders, done))
}

// PickerEntries returns the file names the built-in file picker lists, or
// nil when it is not open. It exists for cmd/shot.
func (a *App) PickerEntries() []string {
	if n := len(a.dialogs); n > 0 {
		if p, ok := a.dialogs[n-1].(*filePicker); ok {
			names := make([]string, len(p.entries))
			for i, e := range p.entries {
				names[i] = e.name
			}
			return names
		}
	}
	return nil
}

// PickerPath returns the directory the built-in file picker is showing, or
// "" when it is not open. It exists for cmd/shot.
func (a *App) PickerPath() string {
	if n := len(a.dialogs); n > 0 {
		if p, ok := a.dialogs[n-1].(*filePicker); ok {
			return p.dir
		}
	}
	return ""
}

// newFilePicker returns a picker dialog that reports the chosen paths
// through done (called on the UI goroutine).
func newFilePicker(a *App, title string, folders bool, done func([]string)) *filePicker {
	d := &filePicker{a: a, title: title, folders: folders, done: done}
	// Hidden files are shown by default here: the interesting ones are
	// ~/.ssh, ~/.config and friends, and the eye button hides them again.
	d.hidden = true
	d.sel = map[string]bool{}
	d.cursor, d.anchor, d.hover, d.lastHit = -1, -1, -1, -1
	return d
}

// Multiline keeps the dialog from consuming Enter: the path field and the
// list handle it themselves.
func (d *filePicker) Multiline() bool { return true }

func (d *filePicker) Cancel(a *App) { a.Close(d) }

func (d *filePicker) Submit(a *App) {
	var paths []string
	if d.folders {
		switch es := d.chosen(); {
		case len(es) == 0:
			paths = []string{d.dir}
		case len(es) == 1 && es[0].dir:
			paths = []string{filepath.Join(d.dir, es[0].name)}
		default:
			d.errMsg = "请选择一个文件夹"
			return
		}
	} else {
		es := d.chosen()
		if len(es) == 0 {
			d.errMsg = "请选择文件"
			return
		}
		for _, e := range es {
			paths = append(paths, filepath.Join(d.dir, e.name))
		}
	}
	// Remember where the user was for the next time the picker opens.
	if dir := d.dir; dir != "" && dir != a.set.PickerDir {
		a.updateSettings(func(s *store.Settings) { s.PickerDir = dir })
	}
	a.Close(d)
	if d.done != nil {
		d.done(paths)
	}
}

// chosen returns the selected entries in display order.
func (d *filePicker) chosen() []pickEntry {
	var out []pickEntry
	for _, e := range d.entries {
		if d.sel[e.name] {
			out = append(out, e)
		}
	}
	return out
}

// load lists dir and resets the selection.
func (d *filePicker) load(dir string) {
	d.dir = dir
	d.path.SetText(dir)
	d.errMsg = ""
	d.entries = nil
	d.sel = map[string]bool{}
	d.cursor, d.anchor, d.hover, d.lastHit = -1, -1, -1, -1
	d.list.ScrollTo(0)
	des, err := os.ReadDir(dir)
	if err != nil {
		d.errMsg = err.Error()
		return
	}
	for _, de := range des {
		name := de.Name()
		if !d.hidden && strings.HasPrefix(name, ".") {
			continue
		}
		e := pickEntry{name: name, dir: de.IsDir(), link: de.Type()&os.ModeSymlink != 0}
		if e.link {
			// Follow links so a link to a directory can be entered.
			if fi, err := os.Stat(filepath.Join(dir, name)); err == nil {
				e.dir = fi.IsDir()
			}
		}
		if fi, err := de.Info(); err == nil {
			e.size, e.mtime = fi.Size(), fi.ModTime()
		}
		d.entries = append(d.entries, e)
	}
	sort.SliceStable(d.entries, func(i, j int) bool {
		a, b := d.entries[i], d.entries[j]
		if a.dir != b.dir {
			return a.dir
		}
		an, bn := strings.ToLower(a.name), strings.ToLower(b.name)
		if an != bn {
			return an < bn
		}
		return a.name < b.name
	})
}

// start is where the picker opens: the directory it was last used in, if
// it still exists, otherwise the home directory.
func (d *filePicker) start() string {
	if p := d.a.set.PickerDir; p != "" {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			return p
		}
	}
	return d.home()
}

func (d *filePicker) home() string {
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return "/"
}

func (d *filePicker) up() {
	if p := filepath.Dir(d.dir); p != d.dir {
		d.load(p)
	}
}

// selectRow applies a click to row i: plain clicks select one entry, Ctrl
// toggles, Shift extends from the anchor.
func (d *filePicker) selectRow(i int, mods key.Modifiers) {
	if i < 0 || i >= len(d.entries) {
		return
	}
	e := d.entries[i]
	switch {
	case mods.Contain(key.ModCtrl):
		d.sel[e.name] = !d.sel[e.name]
	case mods.Contain(key.ModShift) && d.anchor >= 0:
		d.sel = map[string]bool{}
		lo, hi := d.anchor, i
		if lo > hi {
			lo, hi = hi, lo
		}
		for j := max(lo, 0); j <= hi && j < len(d.entries); j++ {
			d.sel[d.entries[j].name] = true
		}
	default:
		d.sel = map[string]bool{e.name: true}
		d.anchor = i
	}
	d.cursor = i
}

// activate opens a directory or confirms the file under the cursor.
func (d *filePicker) activate(i int) {
	if i < 0 || i >= len(d.entries) {
		return
	}
	e := d.entries[i]
	switch {
	case e.dir:
		d.load(filepath.Join(d.dir, e.name))
	case !d.folders:
		d.sel = map[string]bool{e.name: true}
		d.Submit(d.a)
	}
}

func (d *filePicker) events(gtx layout.Context) {
	a := d.a
	if !d.loaded {
		d.loaded = true
		d.load(d.start())
		// The list takes the keyboard first; clicking the path field moves
		// the focus to its editor from there.
		gtx.Execute(key.FocusCmd{Tag: d})
	}
	if d.upClk.Clicked(gtx) {
		d.up()
	}
	if d.homeClk.Clicked(gtx) {
		d.load(d.home())
	}
	if d.hiddenClk.Clicked(gtx) {
		d.hidden = !d.hidden
		d.load(d.dir)
	}
	if submitted, _ := d.path.Events(gtx); submitted {
		p := strings.TrimSpace(d.path.Text())
		if p == "~" || strings.HasPrefix(p, "~/") {
			p = filepath.Join(d.home(), strings.TrimPrefix(p, "~"))
		}
		if p != "" {
			d.load(p)
			gtx.Execute(key.FocusCmd{Tag: d})
		}
	}
	// Keyboard.
	move := func(to int) {
		if len(d.entries) == 0 {
			return
		}
		to = min(max(to, 0), len(d.entries)-1)
		d.selectRow(to, 0)
		first, count := d.list.Position.First, d.list.Position.Count
		switch {
		case to <= first:
			d.list.ScrollTo(to)
		case count > 0 && to >= first+count-1:
			d.list.ScrollTo(max(to-count+2, 0))
		}
	}
	page := max(d.list.Position.Count-1, 1)
	for {
		e, ok := gtx.Event(
			key.FocusFilter{Target: d},
			key.Filter{Focus: d, Name: key.NameUpArrow, Optional: key.ModShift},
			key.Filter{Focus: d, Name: key.NameDownArrow, Optional: key.ModShift},
			key.Filter{Focus: d, Name: key.NameHome},
			key.Filter{Focus: d, Name: key.NameEnd},
			key.Filter{Focus: d, Name: key.NamePageUp},
			key.Filter{Focus: d, Name: key.NamePageDown},
			key.Filter{Focus: d, Name: key.NameReturn},
			key.Filter{Focus: d, Name: key.NameEnter},
			key.Filter{Focus: d, Name: key.NameDeleteBackward},
			key.Filter{Focus: d, Name: "A", Required: key.ModCtrl},
			key.Filter{Focus: &d.path.Editor, Name: key.NameEscape},
		)
		if !ok {
			break
		}
		switch e := e.(type) {
		case key.FocusEvent:
			d.focused = e.Focus
		case key.Event:
			if e.State != key.Press {
				continue
			}
			switch e.Name {
			case key.NameUpArrow:
				move(d.cursor - 1)
			case key.NameDownArrow:
				move(d.cursor + 1)
			case key.NameHome:
				move(0)
			case key.NameEnd:
				move(len(d.entries) - 1)
			case key.NamePageUp:
				move(d.cursor - page)
			case key.NamePageDown:
				move(d.cursor + page)
			case key.NameReturn, key.NameEnter:
				if d.cursor >= 0 {
					d.activate(d.cursor)
				} else {
					d.Submit(a)
				}
			case key.NameDeleteBackward:
				d.up()
			case "A":
				d.sel = map[string]bool{}
				for _, en := range d.entries {
					d.sel[en.name] = true
				}
			}
		}
	}
}

func (d *filePicker) Layout(gtx layout.Context, a *App) layout.Dimensions {
	th := a.th
	d.events(gtx)
	if d.okClk.Clicked(gtx) {
		d.Submit(a)
	}
	if d.noClk.Clicked(gtx) {
		d.Cancel(a)
	}
	ok := "选择"
	if d.folders {
		ok = "选择此文件夹"
	}
	return a.dialogFrame(gtx, d.title, 660, &d.closeClk, d,
		func(gtx layout.Context) layout.Dimensions {
			// dialogFrame allows the body to grow to the window height; a
			// file list does not need that much room.
			gtx.Constraints.Min.Y = 0
			gtx.Constraints.Max.Y = min(gtx.Constraints.Max.Y, gtx.Dp(420))
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(d.toolbar),
				vspace(8),
				layout.Flexed(1, d.listArea),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if d.errMsg == "" {
						return layout.Dimensions{}
					}
					return layout.Inset{Top: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return th.txt(gtx, d.errMsg, 12, th.Danger)
					})
				}),
			)
		},
		func(gtx layout.Context) layout.Dimensions {
			return buttonRow(gtx,
				func(gtx layout.Context) layout.Dimensions {
					return th.button(gtx, &d.noClk, "取消", nil, btnDefault)
				},
				func(gtx layout.Context) layout.Dimensions {
					return th.button(gtx, &d.okClk, ok, nil, btnPrimary)
				},
			)
		})
}

func (d *filePicker) toolbar(gtx layout.Context) layout.Dimensions {
	th := d.a.th
	btn := func(clk *widget.Clickable, ic *widget.Icon, on, enabled bool, title string) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if !enabled {
				gtx = gtx.Disabled()
			}
			return th.iconButton(gtx, clk, ic, 28, 17, th.Text2, on, title)
		})
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		btn(&d.upClk, icUp, false, filepath.Dir(d.dir) != d.dir, "上级目录"),
		hspace(4),
		btn(&d.homeClk, icHome, false, true, "主目录"),
		hspace(6),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			d.path.Hint = "路径"
			return d.path.box(gtx, th, nil)
		}),
		hspace(6),
		btn(&d.hiddenClk, icEye, d.hidden, true, "隐藏文件"),
	)
}

func (d *filePicker) listArea(gtx layout.Context) layout.Dimensions {
	th := d.a.th
	size := gtx.Constraints.Max
	rowH := gtx.Dp(30)

	// Pointer handling over the rows.
	for {
		e, ok := gtx.Event(pointer.Filter{Target: &d.listTag, Kinds: pointer.Press | pointer.Move | pointer.Leave})
		if !ok {
			break
		}
		pe, ok := e.(pointer.Event)
		if !ok {
			continue
		}
		idx := d.list.Position.First + (int(pe.Position.Y)+d.list.Position.Offset)/rowH
		if idx < 0 || idx >= len(d.entries) || pe.Position.Y < 0 {
			idx = -1
		}
		switch pe.Kind {
		case pointer.Leave:
			d.hover = -1
		case pointer.Move:
			d.hover = idx
		case pointer.Press:
			gtx.Execute(key.FocusCmd{Tag: d})
			if !pe.Buttons.Contain(pointer.ButtonPrimary) {
				break
			}
			if idx < 0 {
				d.sel = map[string]bool{}
				d.cursor, d.anchor = -1, -1
				break
			}
			double := idx == d.lastHit && gtx.Now.Sub(d.lastTime) < 450*time.Millisecond
			d.lastHit, d.lastTime = idx, gtx.Now
			if double {
				d.lastHit = -1
				d.activate(idx)
				break
			}
			d.selectRow(idx, pe.Modifiers)
		}
	}

	gtx.Constraints = layout.Exact(size)
	if d.errMsg != "" && len(d.entries) == 0 {
		return d.message(gtx, size.Y, icError, th.Danger, "无法打开文件夹")
	}
	if len(d.entries) == 0 {
		return d.message(gtx, size.Y, icFolder, th.Text3, "空文件夹")
	}
	area := clip.Rect{Max: size}.Push(gtx.Ops)
	event.Op(gtx.Ops, &d.listTag)
	event.Op(gtx.Ops, d)
	th.list(gtx, &d.list, len(d.entries), func(gtx layout.Context, i int) layout.Dimensions {
		return d.row(gtx, i, rowH)
	})
	area.Pop()
	return layout.Dimensions{Size: size}
}

func (d *filePicker) message(gtx layout.Context, h int, ic *widget.Icon, c color.NRGBA, msg string) layout.Dimensions {
	th := d.a.th
	gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, h))
	layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min = image.Point{}
		return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return drawIcon(gtx, ic, 28, alpha(c, 0xaa)) }),
			vspace(8),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.txt(gtx, msg, 12, th.Text3) }),
		)
	})
	return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, h)}
}

func (d *filePicker) row(gtx layout.Context, i int, rowH int) layout.Dimensions {
	th := d.a.th
	e := d.entries[i]
	w := gtx.Constraints.Max.X
	size := image.Pt(w, rowH)
	sel := d.sel[e.name]
	r := image.Rect(gtx.Dp(4), 0, w-gtx.Dp(4), rowH)
	switch {
	case sel && d.focused:
		fillRR(gtx.Ops, r, gtx.Dp(5), alpha(th.Accent, 0x16))
	case sel:
		fillRR(gtx.Ops, r, gtx.Dp(5), th.Bg4)
	case d.hover == i:
		fillRR(gtx.Ops, r, gtx.Dp(5), th.Bg2)
	}
	cell := func(x, cw int, right bool, wd layout.Widget) {
		if cw <= 0 {
			return
		}
		st := op.Offset(image.Pt(x, 0)).Push(gtx.Ops)
		cg := gtx
		cg.Constraints = layout.Exact(image.Pt(cw, rowH))
		layout.Inset{Left: 12, Right: 12}.Layout(cg, func(gtx layout.Context) layout.Dimensions {
			dir := layout.W
			if right {
				dir = layout.E
			}
			return dir.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min = image.Point{}
				return wd(gtx)
			})
		})
		st.Pop()
	}
	ic, icc, nc := icFile, th.Text3, th.Text
	switch {
	case e.dir:
		ic, icc = icFolder, th.Blue
	case e.link:
		ic, icc = icLink, th.Purple
	}
	sizeW, timeW := gtx.Dp(90), gtx.Dp(130)
	cell(0, max(w-sizeW-timeW-gtx.Dp(14), gtx.Dp(120)), false, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return drawIcon(gtx, ic, 16, icc) }),
			hspace(10),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return th.txt(gtx, e.name, 13, nc) }),
		)
	})
	cell(max(w-sizeW-timeW-gtx.Dp(14), gtx.Dp(120)), sizeW, true, func(gtx layout.Context) layout.Dimensions {
		if e.dir {
			return th.txt(gtx, "—", 12, th.Text3)
		}
		return th.txt(gtx, fmtBytes(uint64(max(e.size, 0))), 12, th.Text2)
	})
	cell(w-timeW, timeW, false, func(gtx layout.Context) layout.Dimensions {
		return th.txt(gtx, fmtModTime(e.mtime), 12, th.Text2)
	})
	return layout.Dimensions{Size: size}
}
