package ui

import (
	"image"
	"strings"

	"gioui.org/font"
	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"

	"nlrshell/internal/sshx"
	"nlrshell/internal/store"
)

// commandBar is the input line under the terminal: a command typed there
// is sent to the shell on Enter. It also hosts the quick command panel.
type commandBar struct {
	sv    *sessionView
	field Field

	pasteBtn, clearBtn, quickBtn widget.Clickable
	// hist walks the command history with the arrow keys; -1 means the
	// line being typed, whose text is kept in draft.
	hist  int
	draft string

	quickOpen        bool
	quickTag         bool
	quickAdd, quickX widget.Clickable
	quickList        widget.List
	quickClicks      map[string]*widget.Clickable
	quickRC          map[string]*rightClick
	snippets         []store.Snippet
}

func newCommandBar(sv *sessionView) *commandBar {
	c := &commandBar{sv: sv, hist: -1, quickClicks: map[string]*widget.Clickable{}, quickRC: map[string]*rightClick{}}
	c.field.Hint = "命令"
	c.field.Mono = true
	c.field.Height = 30
	return c
}

// run sends a command to the shell. Several lines are sent one by one.
func (c *commandBar) run(cmd string) {
	sess := c.sv.sess
	if sess.State() != sshx.StateConnected {
		c.sv.a.Toast(toastError, "未连接")
		return
	}
	cmd = strings.ReplaceAll(strings.TrimRight(cmd, "\r\n"), "\r\n", "\n")
	sess.Write([]byte(strings.ReplaceAll(cmd, "\n", "\r") + "\r"))
	c.sv.term.ScrollToBottom()
}

func (c *commandBar) events(gtx layout.Context) {
	a := c.sv.a
	if submitted, changed := c.field.Events(gtx); submitted {
		cmd := c.field.Text()
		c.run(cmd)
		a.st.AddHistory(cmd)
		c.field.SetText("")
		c.hist, c.draft = -1, ""
	} else if changed {
		c.hist = -1
	}
	for {
		e, ok := gtx.Event(
			key.Filter{Focus: &c.field.Editor, Name: key.NameUpArrow},
			key.Filter{Focus: &c.field.Editor, Name: key.NameDownArrow},
			key.Filter{Focus: &c.field.Editor, Name: key.NameEscape},
		)
		if !ok {
			break
		}
		ke, ok := e.(key.Event)
		if !ok || ke.State != key.Press {
			continue
		}
		switch ke.Name {
		case key.NameEscape:
			a.refocus = true
		case key.NameUpArrow, key.NameDownArrow:
			h := a.st.History()
			if len(h) == 0 {
				continue
			}
			if c.hist == -1 {
				c.draft = c.field.Text()
			}
			if ke.Name == key.NameUpArrow {
				if c.hist == -1 {
					c.hist = len(h) - 1
				} else if c.hist > 0 {
					c.hist--
				}
			} else if c.hist != -1 {
				c.hist++
				if c.hist >= len(h) {
					c.hist = -1
				}
			}
			if c.hist == -1 {
				c.field.SetText(c.draft)
			} else {
				c.field.SetText(h[c.hist])
			}
		}
	}
	if c.pasteBtn.Clicked(gtx) {
		gtx.Execute(key.FocusCmd{Tag: &c.field.Editor})
		gtx.Execute(clipboard.ReadCmd{Tag: &c.field.Editor})
	}
	if c.clearBtn.Clicked(gtx) {
		c.field.SetText("")
		c.hist = -1
		gtx.Execute(key.FocusCmd{Tag: &c.field.Editor})
	}
	if c.quickBtn.Clicked(gtx) {
		c.quickOpen = !c.quickOpen
	}
}

// Layout draws the bar.
func (c *commandBar) Layout(gtx layout.Context) layout.Dimensions {
	// The bar belongs to the terminal and follows its palette.
	th := c.sv.a.th.termUI()
	c.events(gtx)
	h := gtx.Dp(42)
	w := gtx.Constraints.Max.X
	fill(gtx.Ops, image.Rect(0, 0, w, h), th.Bg1)
	fill(gtx.Ops, image.Rect(0, 0, w, max(gtx.Dp(1), 1)), th.Border)
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	btn := func(clk *widget.Clickable, ic *widget.Icon, active bool, title string) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return th.iconButton(gtx, clk, ic, 30, 17, th.Text2, active, title)
		})
	}
	layout.Inset{Left: 8, Right: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return c.field.box(gtx, th, nil)
			}),
			hspace(6),
			btn(&c.pasteBtn, icPaste, false, "粘贴"),
			hspace(4),
			btn(&c.clearBtn, icBackspace, false, "清空"),
			hspace(4),
			btn(&c.quickBtn, icBolt, c.quickOpen, "快捷命令"),
		)
	})
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// ---- Quick commands ---------------------------------------------------

func (c *commandBar) editSnippet(sn store.Snippet) {
	a := c.sv.a
	name, cmd := &Field{Label: "名称"}, &Field{Label: "命令", Mono: true}
	name.SetText(sn.Name)
	cmd.SetText(sn.Command)
	title := "添加命令"
	if sn.ID != "" {
		title = "编辑命令"
	}
	a.Open(&inputDialog{title: title, fields: []*Field{name, cmd}, okLabel: "保存",
		onOK: func(v []string, _ bool) {
			sn.Name, sn.Command = strings.TrimSpace(v[0]), strings.TrimSpace(v[1])
			if sn.Command == "" {
				return
			}
			if sn.Name == "" {
				sn.Name = sn.Command
			}
			a.st.SaveSnippet(sn)
		}})
}

// layoutQuick draws the quick command panel in the bottom right corner of
// the terminal area when it is open.
func (c *commandBar) layoutQuick(gtx layout.Context) {
	if !c.quickOpen || !c.sv.a.set.CommandBar {
		return
	}
	a := c.sv.a
	th := a.th.termUI()
	if c.quickX.Clicked(gtx) {
		c.quickOpen = false
		return
	}
	if c.quickAdd.Clicked(gtx) {
		c.editSnippet(store.Snippet{})
	}
	c.snippets = a.st.Snippets()
	for _, sn := range c.snippets {
		sn := sn
		clk := c.quickClicks[sn.ID]
		if clk == nil {
			clk = new(widget.Clickable)
			c.quickClicks[sn.ID] = clk
			c.quickRC[sn.ID] = new(rightClick)
		}
		if clk.Clicked(gtx) {
			c.run(sn.Command)
		}
		if c.quickRC[sn.ID].Clicked(gtx) {
			a.Menu(
				MenuItem{Label: "编辑", Icon: icEdit, Do: func() { c.editSnippet(sn) }},
				MenuItem{Label: "删除", Icon: icDelete, Danger: true, Do: func() { a.st.DeleteSnippet(sn.ID) }},
			)
		}
	}

	area := gtx.Constraints.Max
	margin := gtx.Dp(12)
	w := min(gtx.Dp(300), area.X-2*margin)
	rowH := gtx.Dp(44)
	hdrH := gtx.Dp(40)
	bodyH := max(len(c.snippets), 1) * rowH
	// One margin around the list on every side; row text lines up with the
	// title (14dp from the edge).
	pad := gtx.Dp(6)
	h := min(hdrH+bodyH+2*pad, area.Y-2*margin, gtx.Dp(360))
	if w <= 0 || h <= hdrH {
		return
	}
	pos := image.Pt(area.X-w-margin-gtx.Dp(10), area.Y-h-margin)
	st := op.Offset(pos).Push(gtx.Ops)
	defer st.Pop()
	r := image.Rectangle{Max: image.Pt(w, h)}
	shadow(gtx.Ops, r, gtx.Dp(10))
	fillRR(gtx.Ops, r, gtx.Dp(10), th.Bg2)
	strokeRR(gtx.Ops, r, gtx.Dp(10), float32(gtx.Dp(1)), th.BorderHi)
	// The panel takes the pointer so clicks do not fall through to the
	// terminal below.
	cl := clip.UniformRRect(r, gtx.Dp(10)).Push(gtx.Ops)
	event.Op(gtx.Ops, &c.quickTag)
	pointer.CursorDefault.Add(gtx.Ops)
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: &c.quickTag, Kinds: pointer.Press}); !ok {
			break
		}
	}

	hg := gtx
	hg.Constraints = layout.Exact(image.Pt(w, hdrH))
	layout.Inset{Left: 14, Right: 6}.Layout(hg, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return th.txtW(gtx, "快捷命令", 13, th.Text, font.SemiBold)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return th.iconButton(gtx, &c.quickAdd, icAdd, 28, 17, th.Text2, false, "添加")
			}),
			hspace(2),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return th.iconButton(gtx, &c.quickX, icClose, 28, 16, th.Text2, false, "关闭")
			}),
		)
	})
	fill(gtx.Ops, image.Rect(0, hdrH-max(gtx.Dp(1), 1), w, hdrH), th.Border)

	bo := op.Offset(image.Pt(0, hdrH+pad)).Push(gtx.Ops)
	bg := gtx
	bg.Constraints = layout.Exact(image.Pt(w, h-hdrH-2*pad))
	if len(c.snippets) == 0 {
		layout.Center.Layout(bg, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min = image.Point{}
			return th.txt(gtx, "暂无命令", 12, th.Text3)
		})
	} else {
		th.list(bg, &c.quickList, len(c.snippets), func(gtx layout.Context, i int) layout.Dimensions {
			sn := c.snippets[i]
			return c.quickRow(gtx, sn, rowH)
		})
	}
	bo.Pop()
	cl.Pop()
}

func (c *commandBar) quickRow(gtx layout.Context, sn store.Snippet, rowH int) layout.Dimensions {
	th := c.sv.a.th.termUI()
	w := gtx.Constraints.Max.X
	gtx.Constraints = layout.Exact(image.Pt(w, rowH))
	clk := c.quickClicks[sn.ID]
	return clk.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		r := image.Rect(gtx.Dp(6), gtx.Dp(1), w-gtx.Dp(6), rowH-gtx.Dp(1))
		if clk.Hovered() {
			fillRR(gtx.Ops, r, gtx.Dp(6), th.Bg3)
		}
		layout.Inset{Left: 14, Right: 14}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.W.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min = image.Point{}
				children := []layout.FlexChild{
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.txt(gtx, sn.Name, 13, th.Text) }),
				}
				if sn.Name != sn.Command {
					children = append(children,
						vspace(2),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return Label{Text: sn.Command, Size: unit.Sp(11), Color: th.Text3, Mono: true}.Layout(gtx, th)
						}))
				}
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
			})
		})
		ar := clip.Rect{Max: image.Pt(w, rowH)}.Push(gtx.Ops)
		pointer.CursorPointer.Add(gtx.Ops)
		c.quickRC[sn.ID].Add(gtx.Ops)
		ar.Pop()
		return layout.Dimensions{Size: image.Pt(w, rowH)}
	})
}

// confirmPaste asks before a right click pastes several lines.
func (sv *sessionView) confirmPaste(text string, send func()) {
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n"), "\n")
	preview := lines
	if len(preview) > 8 {
		preview = append(append([]string(nil), preview[:8]...), "…")
	}
	sv.a.Open(&confirmDialog{
		title:   "粘贴多行？",
		message: "粘贴的文本包含多行，可能导致意外执行，确实要继续吗？",
		detail:  strings.Join(preview, "\n"),
		okLabel: "粘贴",
		onOK:    send,
	})
}
