package ui

import (
	"fmt"
	"image"
	"image/color"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"

	"nlrshell/internal/sshx"
	"nlrshell/internal/store"
)

// sessionView is one tab: a terminal with its monitor sidebar and file
// panel.
type sessionView struct {
	a     *App
	sess  *sshx.Session
	term  *TermView
	mon   *monitorView
	files *filesView

	tabChrome
	seenGen uint64

	sideSplit, filesSplit           Splitter
	reconnectClk, closeClk, stopClk widget.Clickable
	overlayTag                      bool
	clipSeq                         int
	lastState                       sshx.State
	// stateAt is when the connection state last changed, for the fade of
	// the connecting card and the disconnected banner.
	stateAt time.Time
	// pending holds terminal actions chosen from a menu; they run on the
	// next frame because they need a layout context.
	pending []string
	cmd     *commandBar
}

func newSessionView(a *App, p store.Profile) *sessionView {
	sv := &sessionView{a: a}
	sv.sess = sshx.NewSession(p, a.st, &prompter{a: a, title: p.Title()}, a.host.Invalidate)
	sv.term = NewTermView(a.th, sv.sess.Term)
	sv.term.Send = func(b []byte) {
		if sv.sess.State() == sshx.StateClosed {
			// Enter on a dead session reconnects, like most terminals.
			if len(b) == 1 && b[0] == '\r' {
				sv.sess.Reconnect()
			}
			return
		}
		sv.sess.Write(b)
	}
	sv.term.OnSize = func(cols, rows int) { sv.sess.Resize(cols, rows) }
	sv.term.OnZoom = func(delta int) {
		if delta == 0 {
			a.setFontSize(store.DefaultSettings().FontSize)
		} else {
			a.setFontSize(a.set.FontSize + float32(delta))
		}
	}
	sv.term.OnMenu = sv.termMenu
	sv.mon = newMonitorView(sv)
	sv.files = newFilesView(sv)
	sv.cmd = newCommandBar(sv)
	sv.term.OnConfirmPaste = sv.confirmPaste
	sv.sess.Transfers.OnDone = func(ti sshx.TransferInfo) {
		a.Post(func() {
			// Downloads made for "open with local app" are not announced.
			if !a.ext.downloaded(a, sv.sess, ti) {
				sv.files.transferDone(ti)
			}
		})
	}
	return sv
}

func (sv *sessionView) title() string {
	return sv.sess.Profile.Title()
}

// secrets are the parts of status messages that privacy mode hides: the
// server's address in its usual spellings.
func (sv *sessionView) secrets() []string {
	p := sv.sess.Profile
	port := p.Port
	if port == 0 {
		port = 22
	}
	return []string{p.Addr(), net.JoinHostPort(p.Host, strconv.Itoa(port)), p.Host, p.ProxyAddr}
}

func (sv *sessionView) tipCard() []tipRow  { return sessionTip(sv.sess) }
func (sv *sessionView) icon() *widget.Icon { return nil }
func (sv *sessionView) closing() bool      { return true }
func (sv *sessionView) closed()            { sv.sess.Close() }

func (sv *sessionView) focus(gtx layout.Context) { sv.term.Focus(gtx) }

func (sv *sessionView) dot(active bool) color.NRGBA {
	th := sv.a.th
	switch sv.sess.State() {
	case sshx.StateConnecting:
		return th.Warn
	case sshx.StateClosed:
		if _, _, err := sv.sess.Info(); err != nil {
			return th.Danger
		}
		return th.Text3
	}
	if !active && sv.hasUnseen() {
		return th.Blue
	}
	return th.Accent
}

func (sv *sessionView) menu() []MenuItem {
	a := sv.a
	return []MenuItem{
		{Label: "重新连接", Icon: icReplay, Disabled: sv.sess.State() != sshx.StateClosed, Do: func() { sv.sess.Reconnect() }},
		{Label: "复制会话", Icon: icCopy, Hint: "Ctrl+Shift+D", Do: func() { a.Connect(sv.sess.Profile) }},
	}
}

// hasUnseen reports whether the terminal changed since it was last shown.
func (sv *sessionView) hasUnseen() bool {
	sv.sess.Term.Lock()
	g := sv.sess.Term.Gen()
	sv.sess.Term.Unlock()
	return g != sv.seenGen
}

func (sv *sessionView) activeForwards() int {
	n := 0
	for _, f := range sv.sess.Forwards.List() {
		if f.Active {
			n++
		}
	}
	return n
}

func (sv *sessionView) termMenu() {
	a := sv.a
	a.Menu(
		MenuItem{Label: "复制", Icon: icCopy, Hint: "Ctrl+Shift+C", Disabled: !sv.term.HasSelection(), Do: func() { sv.pending = append(sv.pending, "copy") }},
		MenuItem{Label: "粘贴", Icon: icPaste, Hint: "Ctrl+V", Do: func() { sv.pending = append(sv.pending, "paste") }},
		MenuItem{Label: "全选", Icon: icSelectAll, Hint: "Ctrl+Shift+A", Do: func() { sv.term.SelectAll() }},
		MenuItem{Sep: true},
		MenuItem{Label: "清屏", Icon: icClear, Do: func() { sv.sess.Term.ClearScrollback(); sv.term.ScrollToBottom() }},
		MenuItem{Label: "复制会话", Icon: icTerminal, Hint: "Ctrl+Shift+D", Do: func() { a.Connect(sv.sess.Profile) }},
		MenuItem{Label: "重新连接", Icon: icReplay, Disabled: sv.sess.State() != sshx.StateClosed, Do: func() { sv.sess.Reconnect() }},
	)
}

// Layout draws the session tab.
func (sv *sessionView) Layout(gtx layout.Context) layout.Dimensions {
	a := sv.a
	th := a.th
	set := &a.set

	sv.sess.Term.Lock()
	sv.seenGen = sv.sess.Term.Gen()
	sv.sess.Term.Unlock()

	sv.term.FontSize = unit.Sp(set.FontSize)
	sv.term.CopyOnSelect = set.CopyOnSelect
	sv.term.RightClick = set.RightClick

	// Deferred terminal actions requested from menus need a frame context.
	for _, act := range sv.pending {
		switch act {
		case "copy":
			sv.term.Copy(gtx)
			sv.term.ClearSelection()
		case "paste":
			sv.term.Paste(gtx)
		}
	}
	sv.pending = sv.pending[:0]

	// OSC 52: the remote application put something on the clipboard.
	if text, seq := sv.sess.Clipboard(); seq != sv.clipSeq {
		sv.clipSeq = seq
		gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(text))})
	}

	state, status, err := sv.sess.Info()
	if state != sv.lastState {
		sv.lastState, sv.stateAt = state, gtx.Now
		if state == sshx.StateConnected {
			sv.files.onConnected()
		}
	}

	// Keep keyboard focus on the terminal unless another input owns it.
	if a.menu == nil && len(a.dialogs) == 0 && !sv.term.Focused() && !sv.files.wantsFocus(gtx) && !gtx.Focused(&sv.cmd.field.Editor) {
		sv.term.Focus(gtx)
	}

	// section draws a panel that fades in and out as it is switched on and
	// off; it keeps its place until it has faded out.
	section := func(gtx layout.Context, f *fader, on bool, w layout.Widget) layout.Dimensions {
		p := f.step(gtx, a, on)
		if p <= 0 {
			return layout.Dimensions{}
		}
		var d layout.Dimensions
		faded(gtx, p, image.Point{}, func() { d = w(gtx) })
		return d
	}

	return layout.Flex{}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return section(gtx, &a.sideFade, set.ShowSidebar, func(gtx layout.Context) layout.Dimensions {
				w := set.SidebarWidth
				hi := max(int(float32(gtx.Constraints.Max.X)/gtx.Metric.PxPerDp)-360, 200)
				return layout.Flex{}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						px := gtx.Dp(unit.Dp(min(w, hi)))
						gtx.Constraints = layout.Exact(image.Pt(px, gtx.Constraints.Max.Y))
						return sv.mon.Layout(gtx)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						d := sv.sideSplit.Layout(gtx, th, layout.Horizontal, &w, 200, hi, 1, a.mouse)
						if w != set.SidebarWidth {
							a.updateSettings(func(s *store.Settings) { s.SidebarWidth = w })
						}
						return d
					}),
				)
			})
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			total := gtx.Constraints.Max.Y
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							d := sv.layoutTerminal(gtx, state, status, err)
							sv.cmd.layoutQuick(gtx)
							return d
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return section(gtx, &a.cmdFade, set.CommandBar, sv.cmd.Layout)
						}),
					)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return section(gtx, &a.filesFade, set.ShowFiles, func(gtx layout.Context) layout.Dimensions {
						h := set.FilesHeight
						hi := max(int(float32(total)/gtx.Metric.PxPerDp)-140, 120)
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								d := sv.filesSplit.Layout(gtx, th, layout.Vertical, &h, 120, hi, -1, a.mouse)
								if h != set.FilesHeight {
									a.updateSettings(func(s *store.Settings) { s.FilesHeight = h })
								}
								return d
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								px := gtx.Dp(unit.Dp(min(h, hi)))
								gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, px))
								return sv.files.Layout(gtx)
							}),
						)
					})
				}),
			)
		}),
	)
}

func (sv *sessionView) layoutTerminal(gtx layout.Context, state sshx.State, status string, err error) layout.Dimensions {
	a := sv.a
	th := a.th
	size := gtx.Constraints.Max
	if sv.reconnectClk.Clicked(gtx) {
		sv.sess.Reconnect()
	}
	if sv.closeClk.Clicked(gtx) || sv.stopClk.Clicked(gtx) {
		if state == sshx.StateConnecting {
			sv.sess.Close()
		} else {
			a.Post(func() { a.closeTab(a.indexOf(sv)) })
		}
	}
	sv.term.Layout(gtx)

	switch state {
	case sshx.StateConnecting:
		// A card in the middle of the terminal.
		rec := op.Record(gtx.Ops)
		cgtx := gtx
		cgtx.Constraints = layout.Constraints{Max: image.Pt(min(gtx.Dp(420), size.X-gtx.Dp(32)), size.Y)}
		d := layout.UniformInset(18).Layout(cgtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.spinner(gtx, 22) }),
				hspace(14),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return th.secretTxtW(gtx, sv.sess.Profile.Addr(), 14, th.Text, font.Medium)
						}),
						vspace(3),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return th.secretIn(gtx, status, sv.secrets(), func(gtx layout.Context, s string, c color.NRGBA) layout.Dimensions {
								return th.txt(gtx, s, 12, c)
							}, th.Text2)
						}),
					)
				}),
				hspace(22),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return th.button(gtx, &sv.stopClk, "取消", nil, btnDefault)
				}),
			)
		})
		call := rec.Stop()
		pos := image.Pt((size.X-d.Size.X)/2, (size.Y-d.Size.Y)/2)
		st := op.Offset(pos).Push(gtx.Ops)
		faded(gtx, a.animIn(gtx, sv.stateAt, animPanel), image.Point{}, func() {
			r := image.Rectangle{Max: d.Size}
			shadow(gtx.Ops, r, gtx.Dp(10))
			fillRR(gtx.Ops, r, gtx.Dp(10), th.Bg2)
			strokeRR(gtx.Ops, r, gtx.Dp(10), float32(gtx.Dp(1)), th.BorderHi)
			ar := clip.Rect(r).Push(gtx.Ops)
			event.Op(gtx.Ops, &sv.overlayTag)
			call.Add(gtx.Ops)
			ar.Pop()
		})
		st.Pop()
	case sshx.StateClosed:
		// A banner along the bottom edge.
		msg, c, ic := "已断开", th.Text2, icInfo
		if code, ok := sv.sess.ExitCode(); ok {
			msg = fmt.Sprintf("已断开（退出代码 %d）", code)
		}
		if err != nil {
			msg, c, ic = err.Error(), th.Danger, icError
		}
		rec := op.Record(gtx.Ops)
		cgtx := gtx
		cgtx.Constraints = layout.Constraints{Max: image.Pt(min(gtx.Dp(720), size.X-gtx.Dp(32)), size.Y)}
		d := layout.Inset{Left: 14, Right: 8, Top: 8, Bottom: 8}.Layout(cgtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return drawIcon(gtx, ic, 18, c) }),
				hspace(10),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = 0
					l := Label{Text: msg, Size: 13, Color: th.Text, MaxLines: 2}
					return th.secretIn(gtx, msg, sv.secrets(), func(gtx layout.Context, s string, c color.NRGBA) layout.Dimensions {
						l.Text, l.Color = s, c
						return l.Layout(gtx, th)
					}, l.Color)
				}),
				hspace(14),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return th.button(gtx, &sv.reconnectClk, "重连", icReplay, btnPrimary)
				}),
				hspace(6),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return th.button(gtx, &sv.closeClk, "关闭", nil, btnGhost)
				}),
			)
		})
		call := rec.Stop()
		pos := image.Pt((size.X-d.Size.X)/2, size.Y-d.Size.Y-gtx.Dp(14))
		st := op.Offset(pos).Push(gtx.Ops)
		faded(gtx, a.animIn(gtx, sv.stateAt, animPanel), image.Pt(0, gtx.Dp(8)), func() {
			r := image.Rectangle{Max: d.Size}
			shadow(gtx.Ops, r, gtx.Dp(10))
			fillRR(gtx.Ops, r, gtx.Dp(10), th.Bg2)
			strokeRR(gtx.Ops, r, gtx.Dp(10), float32(gtx.Dp(1)), mix(th.BorderHi, c, 0.4))
			ar := clip.Rect(r).Push(gtx.Ops)
			event.Op(gtx.Ops, &sv.overlayTag)
			call.Add(gtx.Ops)
			ar.Pop()
		})
		st.Pop()
	}
	return layout.Dimensions{Size: size}
}
