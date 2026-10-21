package ui

import (
	"image"
	"io"
	"slices"
	"strconv"
	"strings"

	"gioui.org/font"
	"gioui.org/io/clipboard"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"

	"nlrshell/internal/mcp"
	"nlrshell/internal/store"
)

// mcpDialog shows the local MCP service and edits what AI assistants may
// do through it. The server reads the settings on every request, so a
// change applies to assistants that are already connected once saved.
type mcpDialog struct {
	enabled, readOnly, trustNew widget.Bool
	port                        Field

	scope    string // a value of mcpScopeChoices
	scopeClk widget.Clickable
	profiles []store.Profile
	picked   []widget.Bool

	copyURLClk, restartClk, copyCLIClk widget.Clickable
	closeClk, cancelClk, saveClk       widget.Clickable
	list                               widget.List
	errMsg                             string
}

const (
	mcpScopeAll    = "all"
	mcpScopePicked = "picked"
)

var mcpScopeChoices = [][2]string{
	{mcpScopeAll, "全部服务器"},
	{mcpScopePicked, "仅选中的服务器"},
}

func newMCPDialog(a *App) *mcpDialog {
	m := a.set.MCP
	d := &mcpDialog{scope: mcpScopeAll, profiles: a.st.Profiles()}
	if !m.AllServers {
		d.scope = mcpScopePicked
	}
	d.enabled.Value, d.readOnly.Value, d.trustNew.Value = m.Enabled, m.ReadOnly, m.TrustNewHosts
	d.port.Label = "端口"
	d.port.Editor.Filter = "0123456789"
	d.port.SetText(strconv.Itoa(m.Port))
	d.picked = make([]widget.Bool, len(d.profiles))
	for i, p := range d.profiles {
		d.picked[i].Value = slices.Contains(m.Servers, p.ID)
	}
	return d
}

// url is the address to connect to: the running service's, or the one it
// will have once the port in the form is saved.
func (d *mcpDialog) url(a *App) string {
	if running, url, _ := a.mcp.State(); running {
		return url
	}
	port, err := strconv.Atoi(d.port.Text())
	if err != nil {
		port = a.set.MCP.Port
	}
	return mcp.URL(port)
}

func (d *mcpDialog) Cancel(a *App) { a.Close(d) }

func (d *mcpDialog) Submit(a *App) {
	port, _ := strconv.Atoi(d.port.Text())
	if port < 1024 || port > 65535 {
		d.errMsg = "端口：1024–65535"
		return
	}
	var ids []string
	for i, p := range d.profiles {
		if d.picked[i].Value {
			ids = append(ids, p.ID)
		}
	}
	a.updateSettings(func(s *store.Settings) {
		s.MCP = store.MCPSettings{
			Enabled:       d.enabled.Value,
			Port:          port,
			ReadOnly:      d.readOnly.Value,
			TrustNewHosts: d.trustNew.Value,
			AllServers:    d.scope == mcpScopeAll,
			Servers:       ids,
		}
	})
	a.Flush()
	running, url, _ := a.mcp.State()
	switch {
	case !d.enabled.Value:
		a.mcp.Stop()
	case !running || url != mcp.URL(port):
		if err := a.mcp.Start(port); err != nil {
			d.errMsg = "MCP 服务启动失败：" + err.Error()
			return
		}
	}
	a.Close(d)
}

func (d *mcpDialog) Layout(gtx layout.Context, a *App) layout.Dimensions {
	th := a.th
	if _, changed := d.port.Events(gtx); changed {
		d.errMsg = ""
	}
	if d.cancelClk.Clicked(gtx) {
		d.Cancel(a)
	}
	if d.saveClk.Clicked(gtx) {
		d.Submit(a)
	}
	if d.scopeClk.Clicked(gtx) {
		a.choiceMenu(mcpScopeChoices, &d.scope)
	}
	if d.restartClk.Clicked(gtx) {
		d.errMsg = ""
		if err := a.mcp.Start(a.set.MCP.Port); err != nil {
			d.errMsg = "MCP 服务启动失败：" + err.Error()
		} else {
			a.Toast(toastOK, "MCP 服务已重启")
		}
	}
	url := d.url(a)
	for _, c := range []struct {
		clk  *widget.Clickable
		text string
	}{{&d.copyURLClk, url}, {&d.copyCLIClk, claudeCommand(url)}} {
		if c.clk.Clicked(gtx) {
			gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(c.text))})
			a.Toast(toastOK, "已复制到剪贴板")
		}
	}
	running, _, startErr := a.mcp.State()

	note := func(s string) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Bottom: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return Label{Text: s, Size: 12, Color: th.Text2, MaxLines: -1}.Layout(gtx, th)
			})
		}
	}
	chk := func(b *widget.Bool, label string) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Bottom: 9}.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return th.checkbox(gtx, b, label) })
		}
	}
	heading := func(s string) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 10, Bottom: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return th.txtW(gtx, s, 13, th.Text, font.Medium)
			})
		}
	}
	// status shows whether the service runs, its URL and its controls.
	status := func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Bottom: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return card(gtx, th, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						s := gtx.Dp(8)
						c := th.Danger
						if running {
							c = th.Accent
						}
						fillRR(gtx.Ops, image.Rectangle{Max: image.Pt(s, s)}, s/2, c)
						return layout.Dimensions{Size: image.Pt(s, s)}
					}),
					hspace(8),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if running {
							return th.txt(gtx, "MCP 服务运行中", 13, th.Text)
						}
						return th.txt(gtx, "MCP 服务未运行", 13, th.Text2)
					}),
					hspace(12),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return codeBox(gtx, th, url) }),
					hspace(6),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return th.iconButton(gtx, &d.copyURLClk, icCopy, 32, 18, th.Text2, false, "复制地址")
					}),
					hspace(6),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if !a.set.MCP.Enabled {
							gtx = gtx.Disabled()
						}
						return th.button(gtx, &d.restartClk, "重启服务", nil, btnDefault)
					}),
				)
			})
		})
	}
	enable := func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Bottom: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return card(gtx, th, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return column(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.txt(gtx, "启用 MCP 服务", 14, th.Text) }),
							vspace(4),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return Label{Text: "在本机运行 MCP 服务，让 AI 助手通过上面的地址使用这里保存的服务器", Size: 12, Color: th.Text2, MaxLines: -1}.Layout(gtx, th)
							}),
						)
					}),
					hspace(12),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.toggle(gtx, &d.enabled) }),
				)
			})
		})
	}

	rows := []layout.Widget{status}
	if msg := d.errMsg; msg != "" || startErr != nil {
		if msg == "" {
			msg = "MCP 服务启动失败：" + startErr.Error()
		}
		rows = append(rows, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Bottom: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return Label{Text: msg, Size: 12, Color: th.Danger, MaxLines: -1}.Layout(gtx, th)
			})
		})
	}
	rows = append(rows,
		enable,
		func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Bottom: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(160))
				return d.port.Layout(gtx, th)
			})
		},
		chk(&d.readOnly, "只读模式（不允许执行命令、写入和上传文件）"),
		chk(&d.trustNew, "自动信任首次连接的主机密钥"),
		func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 4, Bottom: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return selectBox(gtx, th, &d.scopeClk, "可访问的服务器", choiceLabel(mcpScopeChoices, d.scope))
			})
		},
	)
	if d.scope == mcpScopePicked {
		if len(d.profiles) == 0 {
			rows = append(rows, note("还没有保存的连接。"))
		}
		for i := range d.profiles {
			p, b := d.profiles[i], &d.picked[i]
			rows = append(rows, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: 4, Bottom: 9}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return th.checkboxW(gtx, b, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.secretTxt(gtx, p.Title(), 13, th.Text) }),
							hspace(8),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.secretTxt(gtx, p.Addr(), 12, th.Text3) }),
						)
					})
				})
			})
		}
	}
	if d.enabled.Value && !d.readOnly.Value {
		rows = append(rows, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 4, Bottom: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return Label{Text: "AI 助手可以在允许的服务器上执行任意命令，请只接入你信任的 AI 工具。", Size: 12, Color: th.Warn, MaxLines: -1}.Layout(gtx, th)
			})
		})
	}
	rows = append(rows,
		heading("接入 AI 助手"),
		note("在 AI 工具中添加 Streamable HTTP（或「HTTP」）类型的 MCP 服务器，填入上面的地址即可。Claude Code 可以直接运行："),
		func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Bottom: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Start}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return codeBox(gtx, th, claudeCommand(url)) }),
					hspace(8),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return th.button(gtx, &d.copyCLIClk, "复制", icCopy, btnDefault)
					}),
				)
			})
		},
		note("地址只在本机可用，并且需要 NLR Shell 保持运行。claude.ai 网页版等云端连接器无法访问本机地址。设置保存后立即生效。"),
	)

	return a.dialogFrame(gtx, "MCP 服务", 600, &d.closeClk, d,
		func(gtx layout.Context) layout.Dimensions {
			return th.list(gtx, &d.list, len(rows), func(gtx layout.Context, i int) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return rows[i](gtx)
			})
		},
		func(gtx layout.Context) layout.Dimensions {
			return buttonRow(gtx,
				func(gtx layout.Context) layout.Dimensions {
					return th.button(gtx, &d.cancelClk, "取消", nil, btnDefault)
				},
				func(gtx layout.Context) layout.Dimensions {
					return th.button(gtx, &d.saveClk, "保存", nil, btnPrimary)
				},
			)
		})
}

// claudeCommand adds the service at url to Claude Code.
func claudeCommand(url string) string {
	return "claude mcp add --transport http nlrshell " + url
}

// card draws w on a raised, rounded panel.
func card(gtx layout.Context, th *Theme, w layout.Widget) layout.Dimensions {
	m := op.Record(gtx.Ops)
	padX, padY := gtx.Dp(14), gtx.Dp(12)
	inner := gtx
	inner.Constraints.Min = image.Point{}
	inner.Constraints.Max.X = max(gtx.Constraints.Max.X-2*padX, 0)
	inner.Constraints.Min.X = inner.Constraints.Max.X
	st := op.Offset(image.Pt(padX, padY)).Push(gtx.Ops)
	dims := w(inner)
	st.Pop()
	call := m.Stop()
	size := image.Pt(gtx.Constraints.Max.X, dims.Size.Y+2*padY)
	r := image.Rectangle{Max: size}
	fillRR(gtx.Ops, r, gtx.Dp(8), th.Bg2)
	strokeRR(gtx.Ops, r, gtx.Dp(8), float32(gtx.Dp(1)), th.Border)
	call.Add(gtx.Ops)
	return layout.Dimensions{Size: size}
}

// codeBox draws text in the monospace font on an input-like background.
func codeBox(gtx layout.Context, th *Theme, text string) layout.Dimensions {
	m := op.Record(gtx.Ops)
	pad := gtx.Dp(8)
	inner := gtx
	inner.Constraints.Min = image.Point{}
	inner.Constraints.Max.X = max(gtx.Constraints.Max.X-2*pad, 0)
	st := op.Offset(image.Pt(pad, pad)).Push(gtx.Ops)
	dims := Label{Text: text, Size: 12, Color: th.Text, Mono: true, MaxLines: -1}.Layout(inner, th)
	st.Pop()
	call := m.Stop()
	size := image.Pt(gtx.Constraints.Max.X, dims.Size.Y+2*pad)
	r := image.Rectangle{Max: size}
	fillRR(gtx.Ops, r, gtx.Dp(5), th.Bg0)
	strokeRR(gtx.Ops, r, gtx.Dp(5), float32(gtx.Dp(1)), th.Border)
	call.Add(gtx.Ops)
	return layout.Dimensions{Size: size}
}
