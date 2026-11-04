package ui

import (
	"image"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/widget"
	"golang.org/x/exp/shiny/materialdesign/icons"

	"nlrshell/internal/store"
)

var (
	icDesktop  = mustIcon(icons.HardwareDesktopMac)
	icDocument = mustIcon(icons.ActionDescription)
	icPin      = mustIcon(icons.ActionBookmark)
	icDrive    = mustIcon(icons.DeviceStorage)
	icUSB      = mustIcon(icons.DeviceUSB)
	icDisc     = mustIcon(icons.AVAlbum)
	icNetwork  = mustIcon(icons.FileCloud)
)

// place is an entry of the file picker's side bar: a pinned folder or a
// drive.
type place struct {
	path string
	name string
	icon *widget.Icon
}

// sideItem is one row of the side bar: the pinned folders come first, in
// the user's order, then a separator and the drives.
type sideItem struct {
	place
	pin int // index among the pinned folders, or -1
	sep bool
}

// sideDrag tracks a press in the side bar, which opens the place when it
// is released in place and reorders the pinned folders when dragged.
type sideDrag struct {
	pressed, active bool
	item, pin       int
	startY          float32
	target          int // insertion index among the pinned folders
}

// initPlaces looks up the default places and lists the drives, which may
// be slow, in the background.
func (d *filePicker) initPlaces() {
	d.known = knownPlaces()
	for _, k := range d.known {
		if fi, err := os.Stat(k.path); err == nil && fi.IsDir() {
			d.defaults = append(d.defaults, k.path)
		}
	}
	a := d.a
	go func() {
		drives := listDrives()
		a.Post(func() { d.drives = drives })
	}()
}

// pins returns the pinned folders.
func (d *filePicker) pins() []string {
	if p := d.a.set.PickerPlaces; p != nil {
		return p
	}
	return d.defaults
}

func (d *filePicker) setPins(ps []string) {
	ps = append([]string{}, ps...) // never nil: nil restores the defaults
	d.a.updateSettings(func(s *store.Settings) { s.PickerPlaces = ps })
}

func (d *filePicker) pinned(path string) int {
	return slices.IndexFunc(d.pins(), func(p string) bool { return samePath(p, path) })
}

func (d *filePicker) pin(path string) {
	if d.pinned(path) < 0 {
		d.setPins(append(slices.Clone(d.pins()), path))
	}
}

func (d *filePicker) unpin(path string) {
	if i := d.pinned(path); i >= 0 {
		d.setPins(slices.Delete(slices.Clone(d.pins()), i, i+1))
	}
}

// movePin moves pinned folder from to insertion index to.
func (d *filePicker) movePin(from, to int) {
	ps := slices.Clone(d.pins())
	if from < 0 || from >= len(ps) || to == from || to == from+1 {
		return
	}
	p := ps[from]
	ps = slices.Delete(ps, from, from+1)
	if to > from {
		to--
	}
	d.setPins(slices.Insert(ps, min(max(to, 0), len(ps)), p))
}

// describe names a pinned folder: special folders by their role, others by
// their base name.
func (d *filePicker) describe(path string) place {
	for _, k := range d.known {
		if samePath(k.path, path) {
			return place{path: path, name: k.name, icon: k.icon}
		}
	}
	for _, k := range d.drives {
		if samePath(k.path, path) {
			return k
		}
	}
	name := filepath.Base(path)
	if name == "" || name == "." || strings.ContainsAny(name, `/\`) {
		name = path
	}
	return place{path: path, name: name, icon: icFolder}
}

func (d *filePicker) sideItems() []sideItem {
	var out []sideItem
	for i, p := range d.pins() {
		out = append(out, sideItem{place: d.describe(p), pin: i})
	}
	if len(d.drives) > 0 {
		if len(out) > 0 {
			out = append(out, sideItem{pin: -1, sep: true})
		}
		for _, p := range d.drives {
			out = append(out, sideItem{place: p, pin: -1})
		}
	}
	return out
}

// pinMenuItem pins or unpins path.
func (d *filePicker) pinMenuItem(path string) MenuItem {
	if d.pinned(path) >= 0 {
		return MenuItem{Label: "取消固定", Icon: icPin, Do: func() { d.unpin(path) }}
	}
	return MenuItem{Label: "固定到侧栏", Icon: icPin, Do: func() { d.pin(path) }}
}

func (d *filePicker) sideMenu(items []sideItem, idx int) {
	a := d.a
	reset := MenuItem{Label: "恢复默认", Icon: icReplay, Disabled: a.set.PickerPlaces == nil, Do: func() {
		a.updateSettings(func(s *store.Settings) { s.PickerPlaces = nil })
	}}
	if idx < 0 || items[idx].sep {
		a.Menu(d.pinMenuItem(d.dir), MenuItem{Sep: true}, reset)
		return
	}
	it := items[idx]
	if it.pin < 0 {
		a.Menu(MenuItem{Label: "打开", Icon: icFolder, Do: func() { d.load(it.path) }}, d.pinMenuItem(it.path))
		return
	}
	n := len(d.pins())
	a.Menu(
		MenuItem{Label: "打开", Icon: icFolder, Do: func() { d.load(it.path) }},
		MenuItem{Sep: true},
		MenuItem{Label: "上移", Icon: icUp, Disabled: it.pin == 0, Do: func() { d.movePin(it.pin, it.pin-1) }},
		MenuItem{Label: "下移", Icon: icDown, Disabled: it.pin == n-1, Do: func() { d.movePin(it.pin, it.pin+2) }},
		MenuItem{Sep: true},
		d.pinMenuItem(it.path),
		reset,
	)
}

// sideHeights returns the row and separator heights of the side bar.
func sideHeights(gtx layout.Context) (rowH, sepH int) {
	return gtx.Dp(30), gtx.Dp(13)
}

// sideHit returns the item at y in the side bar's coordinates, or -1, and
// y in the coordinates of the whole (scrolled) list.
func (d *filePicker) sideHit(gtx layout.Context, items []sideItem, y float32) (int, int) {
	rowH, sepH := sideHeights(gtx)
	h := func(i int) int {
		if items[i].sep {
			return sepH
		}
		return rowH
	}
	top := 0
	for i := 0; i < d.side.Position.First && i < len(items); i++ {
		top += h(i)
	}
	cy := top + d.side.Position.Offset + int(y)
	t := 0
	for i := range items {
		if cy >= t && cy < t+h(i) {
			return i, cy
		}
		t += h(i)
	}
	return -1, cy
}

func (d *filePicker) sideEvents(gtx layout.Context, items []sideItem) {
	rowH, _ := sideHeights(gtx)
	for {
		e, ok := gtx.Event(pointer.Filter{Target: &d.sideTag,
			Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel | pointer.Move | pointer.Leave})
		if !ok {
			break
		}
		pe, ok := e.(pointer.Event)
		if !ok {
			continue
		}
		idx, cy := d.sideHit(gtx, items, pe.Position.Y)
		if pe.Position.Y < 0 {
			idx = -1
		}
		// A cancel names no source: it ends a touch as well as a click.
		if pe.Kind == pointer.Cancel {
			d.sideTouch.Event(gtx, pe, idx)
		}
		if pe.Source == pointer.Touch {
			// A tap opens the place; pins are not reordered by touch.
			if d.sideTouch.Event(gtx, pe, idx) && idx >= 0 && !items[idx].sep {
				d.load(items[idx].path)
			}
			continue
		}
		dr := &d.sideDrag
		switch pe.Kind {
		case pointer.Leave:
			d.sideHover = -1
		case pointer.Move:
			d.sideHover = idx
		case pointer.Press:
			gtx.Execute(key.FocusCmd{Tag: d})
			switch {
			case pe.Buttons.Contain(pointer.ButtonSecondary):
				d.sideMenu(items, idx)
			case pe.Buttons.Contain(pointer.ButtonPrimary) && idx >= 0 && !items[idx].sep:
				*dr = sideDrag{pressed: true, item: idx, pin: items[idx].pin, startY: pe.Position.Y}
			}
		case pointer.Drag:
			if !dr.pressed || dr.pin < 0 {
				break
			}
			if !dr.active && abs32(pe.Position.Y-dr.startY) > float32(gtx.Dp(6)) {
				dr.active = true
			}
			if dr.active {
				dr.target = min(max((cy+rowH/2)/rowH, 0), len(d.pins()))
			}
		case pointer.Release, pointer.Cancel:
			done := *dr
			*dr = sideDrag{}
			if pe.Kind == pointer.Cancel {
				break
			}
			switch {
			case done.active:
				d.movePin(done.pin, done.target)
			case done.pressed && idx == done.item:
				d.load(items[idx].path)
			}
		}
	}
	if idx, ok := d.sideTouch.Long(gtx, &d.sideTag); ok {
		d.sideMenu(items, idx)
	}
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// sidebar draws the pinned folders and the drives.
func (d *filePicker) sidebar(gtx layout.Context) layout.Dimensions {
	th := d.a.th
	size := gtx.Constraints.Max
	items := d.sideItems()
	d.sideEvents(gtx, items)
	rowH, sepH := sideHeights(gtx)
	gtx.Constraints = layout.Exact(size)
	area := clip.Rect{Max: size}.Push(gtx.Ops)
	event.Op(gtx.Ops, &d.sideTag)
	th.list(gtx, &d.side, len(items), func(gtx layout.Context, i int) layout.Dimensions {
		it := items[i]
		w := gtx.Constraints.Max.X
		if it.sep {
			y := sepH / 2
			fill(gtx.Ops, image.Rect(gtx.Dp(10), y, w-gtx.Dp(10), y+max(gtx.Dp(1), 1)), th.Border)
			return layout.Dimensions{Size: image.Pt(w, sepH)}
		}
		r := image.Rect(0, 0, w-gtx.Dp(4), rowH)
		switch {
		case samePath(it.path, d.dir):
			fillRR(gtx.Ops, r, gtx.Dp(5), th.Bg4)
		case d.sideHover == i && !d.sideDrag.active:
			fillRR(gtx.Ops, r, gtx.Dp(5), th.Bg2)
		}
		if dr := d.sideDrag; dr.active && it.pin >= 0 {
			bar := gtx.Dp(2)
			switch {
			case dr.target == it.pin:
				fill(gtx.Ops, image.Rect(gtx.Dp(6), 0, r.Max.X-gtx.Dp(6), bar), th.Accent)
			case dr.target == it.pin+1 && it.pin == len(d.pins())-1:
				fill(gtx.Ops, image.Rect(gtx.Dp(6), rowH-bar, r.Max.X-gtx.Dp(6), rowH), th.Accent)
			}
		}
		icc := th.Blue
		if it.pin < 0 {
			icc = th.Text2
		}
		cg := gtx
		cg.Constraints = layout.Exact(image.Pt(r.Max.X, rowH))
		layout.Inset{Left: 10, Right: 8}.Layout(cg, func(gtx layout.Context) layout.Dimensions {
			return layout.W.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min = image.Point{}
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return drawIcon(gtx, it.icon, 16, icc) }),
					hspace(10),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return th.txt(gtx, it.name, 13, th.Text) }),
				)
			})
		})
		return layout.Dimensions{Size: image.Pt(w, rowH)}
	})
	area.Pop()
	// Divider between the side bar and the file list.
	fill(gtx.Ops, image.Rect(size.X-max(gtx.Dp(1), 1), 0, size.X, size.Y), th.Border)
	return layout.Dimensions{Size: size}
}
