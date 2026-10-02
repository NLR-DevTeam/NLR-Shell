package ui

import (
	"image"
	"strconv"
	"strings"

	"gioui.org/font"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"nlrshell/internal/store"
)

type profCard struct {
	click widget.Clickable
	edit  widget.Clickable
	rc    rightClick
}

// homeView is the start page: search, quick connect and saved connections.
type homeView struct {
	a         *App
	search    Field
	wantFocus bool
	list      widget.List
	cards     map[string]*profCard
	quick     profCard
	newBtn    widget.Clickable
	profiles  []store.Profile
	sel       int
	drag      homeDrag
}

func newHomeView(a *App) *homeView {
	h := &homeView{a: a, cards: map[string]*profCard{}, wantFocus: true}
	h.search.Hint = "搜索，或输入 user@host"
	h.refresh()
	return h
}

func (h *homeView) refresh() {
	h.profiles = h.a.st.Profiles()
}

// title draws a profile's name; one without a name shows its host, which
// privacy mode hides.
func (h *homeView) title(gtx layout.Context, p store.Profile) layout.Dimensions {
	th := h.a.th
	if p.Name == "" {
		return th.secretTxtW(gtx, p.Title(), 14, th.Text, font.Medium)
	}
	return th.txtW(gtx, p.Title(), 14, th.Text, font.Medium)
}

// parseQuick interprets "[user@]host[:port]".
func parseQuick(s string) (store.Profile, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "ssh ")
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, " \t/\\") {
		return store.Profile{}, false
	}
	p := store.Profile{Port: 22, User: "root", Auth: store.AuthPassword}
	if i := strings.LastIndex(s, "@"); i >= 0 {
		p.User, s = s[:i], s[i+1:]
	}
	if i := strings.LastIndex(s, ":"); i >= 0 && strings.Count(s, ":") == 1 {
		n, err := strconv.Atoi(s[i+1:])
		if err != nil || n <= 0 || n > 65535 {
			return store.Profile{}, false
		}
		p.Port, s = n, s[:i]
	}
	if s == "" || p.User == "" {
		return store.Profile{}, false
	}
	// A host has a dot, is "localhost", or was given with an explicit user.
	p.Host = s
	return p, true
}

func (h *homeView) filtered() []store.Profile {
	q := strings.Fields(strings.ToLower(h.search.Text()))
	if len(q) == 0 {
		return h.profiles
	}
	var out []store.Profile
	for _, p := range h.profiles {
		hay := strings.ToLower(p.Name + " " + p.Host + " " + p.User + " " + p.Group + " " + p.Addr())
		ok := true
		for _, t := range q {
			if !strings.Contains(hay, t) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, p)
		}
	}
	return out
}

func (h *homeView) connectQuick(p store.Profile) {
	// Reuse a saved profile for the same target if there is one.
	for _, sp := range h.profiles {
		if sp.Host == p.Host && sp.User == p.User && sp.Port == p.Port {
			h.a.Connect(sp)
			h.search.SetText("")
			return
		}
	}
	p = h.a.st.SaveProfile(p)
	h.refresh()
	h.search.SetText("")
	h.a.Connect(p)
}

func (h *homeView) editProfile(p store.Profile, connectAfter bool) {
	h.a.Open(newProfileDialog(h.a, p, connectAfter))
}

func (h *homeView) profileMenu(p store.Profile) {
	a := h.a
	a.Menu(
		MenuItem{Label: "连接", Icon: icPlay, Do: func() { a.Connect(p) }},
		MenuItem{Label: "编辑", Icon: icEdit, Do: func() { h.editProfile(p, false) }},
		MenuItem{Label: "复制为新连接", Icon: icCopy, Do: func() {
			c := p
			c.ID, c.LastUsed = "", 0
			c.Name = p.Title() + " 副本"
			h.editProfile(c, false)
		}},
		MenuItem{Sep: true},
		MenuItem{Label: "删除", Icon: icDelete, Danger: true, Do: func() {
			a.Open(&confirmDialog{title: "删除连接", message: "删除 “" + p.Title() + "”？", okLabel: "删除", danger: true,
				onOK: func() { a.st.DeleteProfile(p.ID); h.refresh() }})
		}},
	)
}

// Layout draws the home page.
func (h *homeView) Layout(gtx layout.Context) layout.Dimensions {
	a := h.a
	th := a.th
	size := gtx.Constraints.Max
	fill(gtx.Ops, image.Rectangle{Max: size}, th.Bg0)

	if h.wantFocus && len(a.dialogs) == 0 {
		h.wantFocus = false
		h.search.Focus(gtx)
	}
	if len(a.dialogs) == 0 && a.menu == nil && !gtx.Focused(&h.search.Editor) {
		h.search.Focus(gtx)
	}
	submitted, changed := h.search.Events(gtx)
	if changed {
		h.sel = 0
	}
	list := h.filtered()
	query := h.search.Text()
	h.drag.events(gtx, h, strings.TrimSpace(query) == "" && len(list) > 1)
	if h.drag.dropped {
		list = h.filtered()
	}
	h.drag.beginFrame()
	quick, hasQuick := parseQuick(query)
	if hasQuick {
		// Offer a direct connection when the text looks like an address, or
		// when nothing saved matches it.
		hasQuick = strings.ContainsAny(query, "@.:") || len(list) == 0
		for _, p := range list {
			if p.Host == quick.Host && p.User == quick.User && p.Port == quick.Port {
				hasQuick = false
			}
		}
	}
	// An explicit user@host goes first; otherwise saved matches win.
	quickFirst := hasQuick && (strings.Contains(query, "@") || len(list) == 0)
	total := len(list)
	if hasQuick {
		total++
	}
	quickIdx := -1
	if hasQuick {
		quickIdx = len(list)
		if quickFirst {
			quickIdx = 0
		}
	}
	for {
		e, ok := gtx.Event(
			key.Filter{Focus: &h.search.Editor, Name: key.NameDownArrow},
			key.Filter{Focus: &h.search.Editor, Name: key.NameUpArrow},
		)
		if !ok {
			break
		}
		if ke, ok := e.(key.Event); ok && ke.State == key.Press && total > 0 {
			if ke.Name == key.NameDownArrow {
				h.sel = (h.sel + 1) % total
			} else {
				h.sel = (h.sel + total - 1) % total
			}
		}
	}
	if h.sel >= total {
		h.sel = max(total-1, 0)
	}
	// Index of each profile in the flat, selectable order.
	base := 0
	if quickFirst {
		base = 1
	}
	if submitted && total > 0 {
		if h.sel == quickIdx {
			h.connectQuick(quick)
		} else {
			a.Connect(list[h.sel-base])
			h.search.SetText("")
		}
	}
	if h.newBtn.Clicked(gtx) {
		h.editProfile(store.Profile{Port: 22, Auth: store.AuthPassword}, true)
	}
	if hasQuick && h.quick.click.Clicked(gtx) {
		h.connectQuick(quick)
	}
	for _, p := range list {
		c := h.cards[p.ID]
		if c == nil {
			c = &profCard{}
			h.cards[p.ID] = c
		}
		if h.drag.dropped || h.drag.active {
			// The press started a drag, not a click.
			for c.click.Clicked(gtx) {
			}
		} else if c.edit.Clicked(gtx) {
			h.editProfile(p, false)
		} else if c.click.Clicked(gtx) {
			a.Connect(p)
			h.search.SetText("")
		}
		if c.rc.Clicked(gtx) {
			h.profileMenu(p)
		}
	}

	// Content column.
	colW := min(size.X-gtx.Dp(48), gtx.Dp(980))
	x0 := (size.X - colW) / 2
	st := op.Offset(image.Pt(x0, 0)).Push(gtx.Ops)
	cgtx := gtx
	cgtx.Constraints = layout.Exact(image.Pt(colW, size.Y))

	perRow := max(colW/gtx.Dp(300), 1)
	gap := gtx.Dp(12)
	cardW := (colW - (perRow-1)*gap) / perRow

	// Build rows: hero, search, then groups of cards.
	var rows []layout.Widget
	rows = append(rows, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 36, Bottom: 22}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return drawLogo(gtx, gtx.Dp(44), th.Text, th.Accent) }),
				hspace(14),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return th.txtW(gtx, "NLR Shell", 22, th.Text, font.SemiBold)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return th.button(gtx, &h.newBtn, "新建连接", icAdd, btnPrimary)
				}),
			)
		})
	})
	rows = append(rows, func(gtx layout.Context) layout.Dimensions {
		return h.searchBox(gtx)
	})
	quickWidget := func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 14}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return h.quickRow(gtx, quick, h.sel == quickIdx)
		})
	}
	if quickFirst {
		rows = append(rows, quickWidget)
	}
	if len(h.profiles) == 0 && !hasQuick {
		rows = append(rows, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 70}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = 0
					return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions { return drawIcon(gtx, icServer, 40, th.Text3) }),
						vspace(12),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return th.txt(gtx, "暂无连接", 14, th.Text3)
						}),
					)
				})
			})
		})
	} else if len(list) == 0 && !hasQuick {
		rows = append(rows, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 40}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = 0
					return th.txt(gtx, "无匹配", 13, th.Text3)
				})
			})
		})
	}

	// Group profiles, keeping the most-recently-used order inside groups.
	type group struct {
		name string
		idx  []int
	}
	var groups []*group
	byName := map[string]*group{}
	for i, p := range list {
		g := byName[p.Group]
		if g == nil {
			g = &group{name: p.Group}
			byName[p.Group] = g
			groups = append(groups, g)
		}
		g.idx = append(g.idx, i)
	}
	for _, g := range groups {
		g := g
		name := g.name
		if name == "" {
			name = "连接"
			if len(groups) > 1 {
				name = "未分组"
			}
		}
		h.drag.rowHead[len(rows)] = g.name
		rows = append(rows, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 22, Bottom: 10, Left: 2}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return th.txtW(gtx, name, 12, th.Text2, font.SemiBold)
					}),
					hspace(8),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return th.txt(gtx, strconv.Itoa(len(g.idx)), 12, th.Text3)
					}),
				)
			})
		})
		cardH := gtx.Dp(74)
		h.drag.cardSz = image.Pt(cardW, cardH)
		for start := 0; start < len(g.idx); start += perRow {
			start := start
			ri := len(rows)
			n := min(perRow, len(g.idx)-start)
			for c := 0; c < n; c++ {
				k := start + c
				dc := dragCard{r: image.Rect(c*(cardW+gap), 0, c*(cardW+gap)+cardW, cardH), p: list[g.idx[k]]}
				if k+1 < len(g.idx) {
					dc.next = list[g.idx[k+1]].ID
				}
				h.drag.rowCards[ri] = append(h.drag.rowCards[ri], dc)
			}
			if start+n == len(g.idx) {
				// The rest of the group's last row takes drops for its end.
				x := n*(cardW+gap) - gap
				h.drag.rowEnd[ri] = dragZone{r: image.Rect(x, 0, max(colW, x+gap), cardH), group: g.name}
			}
			rows = append(rows, func(gtx layout.Context) layout.Dimensions {
				hgt := 0
				for c := 0; c < n; c++ {
					i := g.idx[start+c]
					st := op.Offset(image.Pt(c*(cardW+gap), 0)).Push(gtx.Ops)
					cg := gtx
					cg.Constraints = layout.Exact(image.Pt(cardW, cardH))
					// The dragged card stays faintly in its old place.
					var fade paint.OpacityStack
					dragged := h.drag.active && list[i].ID == h.drag.id
					if dragged {
						fade = paint.PushOpacity(gtx.Ops, 0.35)
					}
					d := h.card(cg, list[i], h.sel == i+base && !h.drag.active)
					if dragged {
						fade.Pop()
					}
					st.Pop()
					hgt = d.Size.Y
				}
				return layout.Dimensions{Size: image.Pt(colW, hgt+gap)}
			})
		}
	}
	if hasQuick && !quickFirst {
		rows = append(rows, quickWidget)
	}
	rows = append(rows, func(gtx layout.Context) layout.Dimensions {
		return layout.Spacer{Height: 24}.Layout(gtx)
	})

	th.list(cgtx, &h.list, len(rows), func(gtx layout.Context, i int) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		d := rows[i](gtx)
		h.drag.rowH[i] = d.Size.Y
		return d
	})
	st.Pop()
	h.drag.endFrame(h.list.Position, x0, colW)
	h.drag.layout(gtx, th, h, size)
	return layout.Dimensions{Size: size}
}

func (h *homeView) searchBox(gtx layout.Context) layout.Dimensions {
	th := h.a.th
	hgt := gtx.Dp(44)
	w := gtx.Constraints.Max.X
	r := image.Rectangle{Max: image.Pt(w, hgt)}
	fillRR(gtx.Ops, r, gtx.Dp(10), th.Bg2)
	c := th.Border
	if gtx.Focused(&h.search.Editor) {
		c = alpha(th.Accent, 0xaa)
	}
	strokeRR(gtx.Ops, r, gtx.Dp(10), float32(gtx.Dp(1)), c)
	h.search.hitArea(gtx, r)
	gtx.Constraints = layout.Exact(r.Max)
	layout.Inset{Left: 14, Right: 14}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return drawIcon(gtx, icSearch, 18, th.Text3) }),
			hspace(10),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return fillEditor(gtx, materialEditor(th, &h.search.Editor, h.search.Hint, 14))
			}),
		)
	})
	h.search.menuArea(gtx, th, r)
	return layout.Dimensions{Size: r.Max}
}

func (h *homeView) quickRow(gtx layout.Context, p store.Profile, selected bool) layout.Dimensions {
	th := h.a.th
	hgt := gtx.Dp(48)
	w := gtx.Constraints.Max.X
	gtx.Constraints = layout.Exact(image.Pt(w, hgt))
	return h.quick.click.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		r := image.Rectangle{Max: image.Pt(w, hgt)}
		bg, border := th.Bg1, alpha(th.Accent, 0x66)
		if selected || h.quick.click.Hovered() {
			bg, border = th.Bg2, th.Accent
		}
		fillRR(gtx.Ops, r, gtx.Dp(10), bg)
		strokeRR(gtx.Ops, r, gtx.Dp(10), float32(gtx.Dp(1)), border)
		layout.Inset{Left: 14, Right: 14}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return drawIcon(gtx, icBolt, 18, th.Accent) }),
				hspace(10),
				// The two fonts have different metrics, so the texts share a
				// baseline rather than a center line.
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.Y = 0
					return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.txt(gtx, "连接", 13, th.Text2) }),
						hspace(8),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return Label{Text: p.Addr(), Size: 13, Color: th.Text, Mono: true}.Layout(gtx, th)
						}),
					)
				}),
			)
		})
		ar := clip.Rect(r).Push(gtx.Ops)
		pointer.CursorPointer.Add(gtx.Ops)
		ar.Pop()
		return layout.Dimensions{Size: r.Max}
	})
}

func (h *homeView) card(gtx layout.Context, p store.Profile, selected bool) layout.Dimensions {
	th := h.a.th
	c := h.cards[p.ID]
	size := gtx.Constraints.Max
	r := image.Rectangle{Max: size}
	hovered := c.click.Hovered() || c.edit.Hovered()
	d := c.click.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		bg, border := th.Bg1, th.Border
		switch {
		case selected:
			bg, border = th.Bg2, th.Accent
		case hovered:
			bg, border = th.Bg2, th.BorderHi
		}
		fillRR(gtx.Ops, r, gtx.Dp(10), bg)
		strokeRR(gtx.Ops, r, gtx.Dp(10), float32(gtx.Dp(1)), border)
		layout.Inset{Left: 14, Right: 14}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					s := gtx.Dp(38)
					fillRR(gtx.Ops, image.Rectangle{Max: image.Pt(s, s)}, gtx.Dp(9), th.Bg3)
					ic := icServer
					col := th.Accent
					if p.JumpID != "" {
						col = th.Purple
					}
					st := op.Offset(image.Pt((s-gtx.Dp(20))/2, (s-gtx.Dp(20))/2)).Push(gtx.Ops)
					drawIcon(gtx, ic, 20, col)
					st.Pop()
					return layout.Dimensions{Size: image.Pt(s, s)}
				}),
				hspace(12),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return column(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return h.title(gtx, p)
						}),
						vspace(3),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return th.secretLabel(gtx, Label{Text: p.Addr(), Size: 12, Color: th.Text2, Mono: true})
						}),
						vspace(3),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							auth := "密码"
							switch p.Auth {
							case store.AuthKey:
								auth = "密钥"
							case store.AuthAgent:
								auth = "Agent"
							}
							return th.txt(gtx, auth+" · "+fmtAgo(p.LastUsed), 11, th.Text3)
						}),
					)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Dimensions{Size: image.Pt(gtx.Dp(28), 0)}
				}),
			)
		})
		ar := clip.Rect(r).Push(gtx.Ops)
		pointer.CursorPointer.Add(gtx.Ops)
		c.rc.Add(gtx.Ops)
		ar.Pop()
		return layout.Dimensions{Size: size}
	})
	if hovered || selected {
		st := op.Offset(image.Pt(size.X-gtx.Dp(38), (size.Y-gtx.Dp(28))/2)).Push(gtx.Ops)
		th.iconButton(gtx, &c.edit, icEdit, 28, 15, th.Text2, false, "编辑")
		st.Pop()
	}
	return d
}

// materialEditor returns an editor style using the theme's fonts.
func materialEditor(th *Theme, e *widget.Editor, hint string, size unit.Sp) editorStyle {
	return editorStyle{th: th, e: e, hint: hint, size: size}
}
