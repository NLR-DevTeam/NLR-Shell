package ui

import (
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"net"
	"path"
	"strconv"
	"strings"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"

	"nlrshell/internal/sshx"
)

// sessionTip is the hover card of anything that belongs to a session: the
// server address and the login user.
func sessionTip(sess *sshx.Session) []tipRow {
	p := sess.Profile
	port := p.Port
	if port == 0 {
		port = 22
	}
	return []tipRow{
		{icServer, net.JoinHostPort(p.Host, strconv.Itoa(port)), true},
		{icUser, p.User, false},
	}
}

// maxImageSize is the largest image file the viewer will load.
const maxImageSize = 64 << 20

// fileBarH is the height of the status bar under a file tab; it leaves a
// margin above and below the 30dp buttons.
const fileBarH = unit.Dp(44)

var imageExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".bmp": true, ".webp": true}

func isImageName(name string) bool { return imageExts[strings.ToLower(path.Ext(name))] }

// openRemote opens a remote file: images and text files get a tab of their
// own, anything else is handed to the default local application.
func (a *App) openRemote(sess *sshx.Session, p string, size int64) {
	if ft := a.findFile(sess, p); ft != nil {
		a.activate(a.indexOf(ft))
		return
	}
	// The file is downloaded as a task; what it turns out to be decides
	// between a tab and a local application.
	a.ext.fetch(a, sess, p, true)
}

// fileBase is the part shared by tabs that show one remote file.
type fileBase struct {
	tabChrome
	a      *App
	sess   *sshx.Session
	path   string
	extClk widget.Clickable
}

func (f *fileBase) owner() *sshx.Session        { return f.sess }
func (f *fileBase) remotePath() string          { return f.path }
func (f *fileBase) origin() string              { return f.sess.Profile.Addr() + ":" + f.path }
func (f *fileBase) dot(active bool) color.NRGBA { return color.NRGBA{} }
func (f *fileBase) closed()                     {}

// tipCard describes where the file comes from: host, user and path.
func (f *fileBase) tipCard() []tipRow {
	return append(sessionTip(f.sess), tipRow{icFile, f.path, false})
}

func (f *fileBase) openExternal() { f.a.ext.fetch(f.a, f.sess, f.path, false) }

// statusBar draws the strip under a file tab: the origin of the file on the
// left, then info and the tab's actions on the right.
func (f *fileBase) statusBar(gtx layout.Context, info string, actions ...layout.Widget) layout.Dimensions {
	th := f.a.th
	if f.extClk.Clicked(gtx) {
		f.openExternal()
	}
	h := gtx.Dp(fileBarH)
	w := gtx.Constraints.Max.X
	fill(gtx.Ops, image.Rect(0, 0, w, h), th.Bg1)
	fill(gtx.Ops, image.Rect(0, 0, w, max(gtx.Dp(1), 1)), th.Border)
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	children := []layout.FlexChild{
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			l := Label{Text: f.origin(), Size: 12, Color: th.Text3, Mono: true}
			return th.secretIn(gtx, l.Text, []string{f.sess.Profile.Addr()}, func(gtx layout.Context, s string, c color.NRGBA) layout.Dimensions {
				l.Text, l.Color = s, c
				return l.Layout(gtx, th)
			}, l.Color)
		}),
		hspace(12),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.txt(gtx, info, 12, th.Text2) }),
		hspace(10),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return th.iconButton(gtx, &f.extClk, icOpen, 26, 15, th.Text2, false, "用本地程序打开")
		}),
	}
	for _, act := range actions {
		children = append(children, hspace(6), layout.Rigid(act))
	}
	layout.Inset{Left: 12, Right: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, children...)
	})
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// ---- Text editor tab --------------------------------------------------

type editorTab struct {
	fileBase
	editor  widget.Editor
	orig    string
	dirty   bool
	saving  bool
	saveClk widget.Clickable
	editRC  rightClick
}

func newEditorTab(a *App, sess *sshx.Session, p, text string) *editorTab {
	t := &editorTab{fileBase: fileBase{a: a, sess: sess, path: p}, orig: text}
	t.editor.SetText(text)
	t.editor.SetCaret(0, 0)
	return t
}

func (t *editorTab) title() string {
	if t.dirty {
		return baseName(t.path) + " •"
	}
	return baseName(t.path)
}

func (t *editorTab) icon() *widget.Icon       { return icFile }
func (t *editorTab) disposable() bool         { return !t.dirty }
func (t *editorTab) focus(gtx layout.Context) { gtx.Execute(key.FocusCmd{Tag: &t.editor}) }

func (t *editorTab) closing() bool {
	if !t.dirty {
		return true
	}
	a := t.a
	a.Open(&confirmDialog{title: "放弃修改？", message: baseName(t.path) + " 尚未保存", okLabel: "放弃", danger: true,
		onOK: func() { a.removeTab(t) }})
	return false
}

func (t *editorTab) menu() []MenuItem {
	return []MenuItem{
		{Label: "保存", Icon: icSave, Hint: "Ctrl+S", Disabled: !t.dirty, Do: t.save},
		{Label: "用本地程序打开", Icon: icOpen, Do: t.openExternal},
	}
}

func (t *editorTab) save() {
	if t.saving || !t.dirty {
		return
	}
	t.saving = true
	a, sess, p := t.a, t.sess, t.path
	text := t.editor.Text()
	go func() {
		err := sess.WriteText(p, text)
		a.Post(func() {
			t.saving = false
			if err != nil {
				a.Toast(toastError, "保存失败："+err.Error())
				return
			}
			t.orig = text
			t.dirty = t.editor.Text() != text
			a.Toast(toastOK, "已保存 "+baseName(p))
			if sv := a.viewOf(sess); sv != nil {
				sv.files.reload()
			}
		})
	}()
}

func (t *editorTab) Layout(gtx layout.Context) layout.Dimensions {
	a := t.a
	th := a.th
	size := gtx.Constraints.Max
	for {
		e, ok := t.editor.Update(gtx)
		if !ok {
			break
		}
		// Loading the text also raises a change event, so compare with what
		// is on the server instead of trusting the event alone.
		if _, ok := e.(widget.ChangeEvent); ok {
			t.dirty = t.editor.Text() != t.orig
		}
	}
	for {
		e, ok := gtx.Event(
			key.Filter{Focus: &t.editor, Name: "S", Required: key.ModCtrl},
			key.Filter{Focus: &t.editor, Name: "W", Required: key.ModCtrl},
		)
		if !ok {
			break
		}
		if ke, ok := e.(key.Event); ok && ke.State == key.Press {
			if ke.Name == "S" {
				t.save()
			} else {
				a.Post(func() { a.closeTab(a.indexOf(t)) })
			}
		}
	}
	if t.saveClk.Clicked(gtx) {
		t.save()
	}
	if a.menu == nil && len(a.dialogs) == 0 && !gtx.Focused(&t.editor) {
		t.focus(gtx)
	}
	fill(gtx.Ops, image.Rectangle{Max: size}, th.Bg0)
	layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min = gtx.Constraints.Max
			d := layout.Inset{Left: 14, Right: 6, Top: 10, Bottom: 6}.Layout(gtx,
				editorStyle{th: th, e: &t.editor, size: 13, mono: true}.Layout)
			editMenuArea(gtx, th, &t.editRC, &t.editor, image.Rectangle{Max: d.Size})
			return d
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			line, col := t.editor.CaretPos()
			return t.statusBar(gtx, fmt.Sprintf("%d:%d", line+1, col+1), func(gtx layout.Context) layout.Dimensions {
				if !t.dirty || t.saving {
					gtx = gtx.Disabled()
				}
				return th.button(gtx, &t.saveClk, "保存", icSave, btnPrimary)
			})
		}),
	)
	return layout.Dimensions{Size: size}
}

// ---- Image tab --------------------------------------------------------

type imageTab struct {
	fileBase
	img   paint.ImageOp
	dims  image.Point
	bytes int64

	zoom     float32 // 0 means fit to the view
	off      f32.Point
	tag      bool
	dragging bool
	dragFrom image.Point
	offFrom  f32.Point
	lastTap  time.Time
}

func (t *imageTab) title() string            { return baseName(t.path) }
func (t *imageTab) icon() *widget.Icon       { return icImage }
func (t *imageTab) disposable() bool         { return true }
func (t *imageTab) closing() bool            { return true }
func (t *imageTab) focus(gtx layout.Context) { gtx.Execute(key.FocusCmd{}) }

func (t *imageTab) menu() []MenuItem {
	return []MenuItem{{Label: "用本地程序打开", Icon: icOpen, Do: t.openExternal}}
}

func (t *imageTab) Layout(gtx layout.Context) layout.Dimensions {
	size := gtx.Constraints.Max
	fill(gtx.Ops, image.Rectangle{Max: size}, t.a.th.Bg0)
	// The view is laid out first because the status bar reports its zoom.
	view := image.Pt(size.X, size.Y-gtx.Dp(fileBarH))
	vg := gtx
	vg.Constraints = layout.Exact(view)
	info := t.view(vg, view)
	st := op.Offset(image.Pt(0, view.Y)).Push(gtx.Ops)
	t.statusBar(gtx, info)
	st.Pop()
	return layout.Dimensions{Size: size}
}

// view draws the image with zoom and pan and returns the status text.
func (t *imageTab) view(gtx layout.Context, view image.Point) string {
	w, h := float32(t.dims.X), float32(t.dims.Y)
	fit := min(float32(view.X-gtx.Dp(24))/w, float32(view.Y-gtx.Dp(24))/h, 1)
	if fit <= 0 {
		fit = 1
	}
	for {
		e, ok := gtx.Event(pointer.Filter{Target: &t.tag, Kinds: pointer.Press | pointer.Release | pointer.Cancel | pointer.Scroll,
			ScrollY: pointer.ScrollRange{Min: -1 << 20, Max: 1 << 20}})
		if !ok {
			break
		}
		pe, ok := e.(pointer.Event)
		if !ok {
			continue
		}
		switch pe.Kind {
		case pointer.Scroll:
			z := t.zoom
			if z == 0 {
				z = fit
			}
			z *= float32(math.Pow(1.0015, float64(-pe.Scroll.Y)))
			t.zoom = min(max(z, min(fit, 0.05)), 32)
		case pointer.Press:
			if !pe.Buttons.Contain(pointer.ButtonPrimary) {
				break
			}
			if gtx.Now.Sub(t.lastTap) < 400*time.Millisecond {
				// Double click toggles between fit and actual size.
				if t.zoom == 0 && fit < 1 {
					t.zoom = 1
				} else {
					t.zoom = 0
				}
				t.off = f32.Point{}
				t.lastTap = time.Time{}
				break
			}
			t.lastTap = gtx.Now
			t.dragging, t.dragFrom, t.offFrom = true, t.a.mouse, t.off
		case pointer.Release, pointer.Cancel:
			t.dragging = false
		}
	}
	if t.dragging {
		d := t.a.mouse.Sub(t.dragFrom)
		t.off = t.offFrom.Add(f32.Pt(float32(d.X), float32(d.Y)))
	}
	scale := t.zoom
	if scale == 0 {
		scale = fit
		t.off = f32.Point{}
	}
	sw, sh := w*scale, h*scale
	// Panning is limited to what does not fit in the view.
	limX, limY := max((sw-float32(view.X))/2, 0), max((sh-float32(view.Y))/2, 0)
	t.off.X = min(max(t.off.X, -limX), limX)
	t.off.Y = min(max(t.off.Y, -limY), limY)
	pos := f32.Pt((float32(view.X)-sw)/2+t.off.X, (float32(view.Y)-sh)/2+t.off.Y)

	area := clip.Rect{Max: view}.Push(gtx.Ops)
	event.Op(gtx.Ops, &t.tag)
	if limX > 0 || limY > 0 {
		pointer.CursorGrab.Add(gtx.Ops)
	}
	tr := op.Affine(f32.AffineId().Scale(f32.Point{}, f32.Pt(scale, scale)).Offset(pos)).Push(gtx.Ops)
	if scale >= 3 {
		t.img.Filter = paint.FilterNearest
	} else {
		t.img.Filter = paint.FilterLinear
	}
	t.img.Add(gtx.Ops)
	ic := clip.Rect{Max: t.dims}.Push(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	ic.Pop()
	tr.Pop()
	area.Pop()
	return fmt.Sprintf("%d×%d · %s · %d%%", t.dims.X, t.dims.Y, fmtBytes(uint64(t.bytes)), int(scale*100+0.5))
}
