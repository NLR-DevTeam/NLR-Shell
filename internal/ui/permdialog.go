package ui

import (
	"fmt"
	"image"
	"os"
	"strconv"
	"strings"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"

	"nlrshell/internal/sshx"
)

// permDialog edits the permission bits and owner of a remote file. The
// checkboxes and the octal input show the same value and update each other.
type permDialog struct {
	fv   *filesView
	e    sshx.Entry
	path string

	// bits are r, w, x for owner, group and others, highest bit first.
	bits      [9]widget.Bool
	octal     Field
	owner     fontCombo
	recursive widget.Bool
	users     []string
	usersOK   bool
	errMsg    string
	focused   bool

	closeClk, cancelClk, okClk widget.Clickable
}

func newPermDialog(fv *filesView, e sshx.Entry) *permDialog {
	d := &permDialog{fv: fv, e: e, path: fv.full(e.Name)}
	d.setBits(e.Mode.Perm())
	d.octal.Editor.Filter = "01234567"
	d.octal.Editor.MaxLen = 4
	d.octal.Mono = true
	d.octal.SetText(fmt.Sprintf("%03o", e.Mode.Perm()))
	d.owner.SetText(e.Owner)
	d.owner.items = func() ([]string, bool) { return d.users, d.usersOK }
	a, sess := fv.a, fv.sv.sess
	go func() {
		users, err := sess.Owners()
		a.Post(func() {
			d.users, d.usersOK = users, true
			if err != nil {
				d.users = nil
			}
		})
	}()
	return d
}

func (d *permDialog) setBits(m os.FileMode) {
	for i := range d.bits {
		d.bits[i].Value = m&(1<<uint(8-i)) != 0
	}
}

func (d *permDialog) mode() os.FileMode {
	var m os.FileMode
	for i := range d.bits {
		if d.bits[i].Value {
			m |= 1 << uint(8-i)
		}
	}
	return m
}

// parseOctal reads a 3 or 4 digit octal mode; four digits include the
// setuid, setgid and sticky bits.
func parseOctal(s string) (mode os.FileMode, special, ok bool) {
	if len(s) != 3 && len(s) != 4 {
		return 0, false, false
	}
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return 0, false, false
	}
	mode = os.FileMode(n & 0o777)
	if len(s) == 4 {
		special = true
		if n&0o4000 != 0 {
			mode |= os.ModeSetuid
		}
		if n&0o2000 != 0 {
			mode |= os.ModeSetgid
		}
		if n&0o1000 != 0 {
			mode |= os.ModeSticky
		}
	}
	return mode, special, true
}

func (d *permDialog) Cancel(a *App) { a.Close(d) }

func (d *permDialog) Submit(a *App) {
	mode, special, ok := parseOctal(strings.TrimSpace(d.octal.Text()))
	if !ok {
		d.errMsg = "权限应为 3 或 4 位八进制数"
		return
	}
	owner := strings.TrimSpace(d.owner.Text())
	if owner == d.e.Owner {
		owner = "" // unchanged: leave ownership alone
	}
	recursive := d.e.IsDir && d.recursive.Value
	sess, p := d.fv.sv.sess, d.path
	a.Close(d)
	d.fv.run("修改权限", func() error { return sess.SetAttrs(p, mode, special, owner, recursive) })
}

func (d *permDialog) Layout(gtx layout.Context, a *App) layout.Dimensions {
	th := a.th
	if !d.focused {
		d.focused = true
		d.octal.Focus(gtx)
		d.octal.Editor.SetCaret(d.octal.Editor.Len(), 0)
	}
	// Checkboxes drive the octal input, and a complete octal input drives
	// the checkboxes.
	for i := range d.bits {
		if d.bits[i].Update(gtx) {
			d.octal.SetText(fmt.Sprintf("%03o", d.mode()))
			d.errMsg = ""
		}
	}
	if _, changed := d.octal.Events(gtx); changed {
		d.errMsg = ""
		if m, _, ok := parseOctal(strings.TrimSpace(d.octal.Text())); ok {
			d.setBits(m.Perm())
		}
	}
	if d.cancelClk.Clicked(gtx) {
		d.Cancel(a)
	}
	if d.okClk.Clicked(gtx) {
		d.Submit(a)
	}

	group := func(title string, first int) layout.FlexChild {
		return layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return permGroup(gtx, th, title, d.bits[first:first+3])
		})
	}
	return a.dialogFrame(gtx, "权限 · "+d.e.Name, 520, &d.closeClk, d,
		func(gtx layout.Context) layout.Dimensions {
			rows := []layout.FlexChild{
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{}.Layout(gtx,
						group("所有者", 0), hspace(14), group("用户组", 3), hspace(14), group("公共", 6))
				}),
				vspace(16),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							gtx.Constraints = layout.Exact(image.Pt(gtx.Dp(76), gtx.Dp(32)))
							return d.octal.box(gtx, th, nil)
						}),
						hspace(12),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return th.txt(gtx, "所有者", 13, th.Text2)
						}),
						hspace(10),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return d.owner.Layout(gtx, th)
						}),
					)
				}),
			}
			if d.e.IsDir {
				rows = append(rows, vspace(14), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return th.checkbox(gtx, &d.recursive, "应用到子目录")
				}))
			}
			if d.errMsg != "" {
				rows = append(rows, vspace(10), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return th.txt(gtx, d.errMsg, 12, th.Danger)
				}))
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
		},
		func(gtx layout.Context) layout.Dimensions {
			return buttonRow(gtx,
				func(gtx layout.Context) layout.Dimensions {
					return th.button(gtx, &d.cancelClk, "取消", nil, btnDefault)
				},
				func(gtx layout.Context) layout.Dimensions { return th.button(gtx, &d.okClk, "应用", nil, btnPrimary) },
			)
		})
}

// permGroup draws one framed group of read/write/execute checkboxes, with
// its title set into the top border.
func permGroup(gtx layout.Context, th *Theme, title string, bits []widget.Bool) layout.Dimensions {
	w := gtx.Constraints.Max.X
	rec := op.Record(gtx.Ops)
	tg := gtx
	tg.Constraints.Min = image.Point{}
	td := th.txt(tg, title, 13, th.Text2)
	titleCall := rec.Stop()

	top := td.Size.Y / 2
	rec = op.Record(gtx.Ops)
	cg := gtx
	cg.Constraints = layout.Constraints{Max: image.Pt(w, gtx.Constraints.Max.Y)}
	cd := layout.Inset{Left: 16, Right: 12, Top: 14, Bottom: 14}.Layout(cg, func(gtx layout.Context) layout.Dimensions {
		labels := []string{"读取", "写入", "执行"}
		children := make([]layout.FlexChild, 0, 5)
		for i := range bits {
			i := i
			if i > 0 {
				children = append(children, vspace(12))
			}
			children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return th.checkbox(gtx, &bits[i], labels[i])
			}))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	})
	content := rec.Stop()

	h := td.Size.Y + cd.Size.Y
	frame := image.Rect(0, top, w, h)
	strokeRR(gtx.Ops, frame, gtx.Dp(6), float32(gtx.Dp(1)), th.BorderHi)
	// The title interrupts the top border, as in a fieldset legend.
	tx := gtx.Dp(10)
	fill(gtx.Ops, image.Rect(tx-gtx.Dp(4), 0, tx+td.Size.X+gtx.Dp(4), td.Size.Y), th.Bg1)
	st := op.Offset(image.Pt(tx, 0)).Push(gtx.Ops)
	titleCall.Add(gtx.Ops)
	st.Pop()
	st = op.Offset(image.Pt(0, td.Size.Y)).Push(gtx.Ops)
	content.Add(gtx.Ops)
	st.Pop()
	return layout.Dimensions{Size: image.Pt(w, h)}
}
