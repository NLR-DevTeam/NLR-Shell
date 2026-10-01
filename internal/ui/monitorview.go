package ui

import (
	"fmt"
	"image"
	"image/color"
	"io"
	"sort"
	"strconv"
	"strings"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/clipboard"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"

	"nlrshell/internal/monitor"
	"nlrshell/internal/sshx"
)

const maxProcRows = 14

// monitorView is the system resource sidebar.
type monitorView struct {
	sv   *sessionView
	list widget.List

	sortMem        bool
	cpuHdr, memHdr widget.Clickable
	coresClk       widget.Clickable
	coresOpen      bool
	procRC         [maxProcRows]rightClick
	procs          []monitor.Proc
	copyText       string
}

func newMonitorView(sv *sessionView) *monitorView {
	return &monitorView{sv: sv, coresOpen: true}
}

// sparkline draws one or two series as filled area charts in a box.
func sparkline(gtx layout.Context, th *Theme, h unit.Dp, maxV float64, series [][]float64, colors []color.NRGBA) layout.Dimensions {
	w, hp := gtx.Constraints.Max.X, gtx.Dp(h)
	r := image.Rectangle{Max: image.Pt(w, hp)}
	fillRR(gtx.Ops, r, gtx.Dp(6), th.Bg0)
	defer clip.UniformRRect(r, gtx.Dp(6)).Push(gtx.Ops).Pop()
	// Faint horizontal guides.
	for i := 1; i < 3; i++ {
		y := hp * i / 3
		fill(gtx.Ops, image.Rect(0, y, w, y+1), alpha(th.Border, 0x99))
	}
	if maxV <= 0 {
		maxV = 1
	}
	n := sshx.HistoryLen
	step := float32(w) / float32(n-1)
	for si, data := range series {
		if len(data) < 2 {
			continue
		}
		pt := func(i int) f32.Point {
			// Right-align so new samples enter from the right edge.
			x := float32(w) - float32(len(data)-1-i)*step
			v := data[i] / maxV
			if v > 1 {
				v = 1
			}
			return f32.Pt(x, float32(hp)-1-float32(v)*float32(hp-3))
		}
		var area clip.Path
		area.Begin(gtx.Ops)
		first := pt(0)
		area.MoveTo(f32.Pt(first.X, float32(hp)))
		for i := range data {
			area.LineTo(pt(i))
		}
		area.LineTo(f32.Pt(float32(w), float32(hp)))
		area.Close()
		paint.FillShape(gtx.Ops, alpha(colors[si], 0x2a), clip.Outline{Path: area.End()}.Op())

		var ln clip.Path
		ln.Begin(gtx.Ops)
		ln.MoveTo(first)
		for i := 1; i < len(data); i++ {
			ln.LineTo(pt(i))
		}
		paint.FillShape(gtx.Ops, colors[si], clip.Stroke{Path: ln.End(), Width: float32(gtx.Dp(1)) * 1.4}.Op())
	}
	return layout.Dimensions{Size: r.Max}
}

func usageColor(th *Theme, pct float64) color.NRGBA {
	switch {
	case pct >= 90:
		return th.Danger
	case pct >= 75:
		return th.Warn
	}
	return th.Accent
}

// section draws a titled block with a value on the right of the title.
func (m *monitorView) section(gtx layout.Context, title, value string, valueColor color.NRGBA, body layout.Widget) layout.Dimensions {
	th := m.sv.a.th
	return layout.Inset{Left: 14, Right: 14, Top: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return th.txtW(gtx, title, 11, th.Text3, font.SemiBold)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return th.txtW(gtx, value, 13, valueColor, font.Medium)
					}),
				)
			}),
			vspace(7),
			layout.Rigid(body),
		)
	})
}

func (m *monitorView) kv(gtx layout.Context, k, v string) layout.Dimensions {
	th := m.sv.a.th
	return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Dp(44)
			return th.txt(gtx, k, 12, th.Text3)
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return th.txt(gtx, v, 12, th.Text2)
		}),
	)
}

// Layout draws the sidebar.
func (m *monitorView) Layout(gtx layout.Context) layout.Dimensions {
	a := m.sv.a
	th := a.th
	size := gtx.Constraints.Max
	fill(gtx.Ops, image.Rectangle{Max: size}, th.Bg1)
	if m.copyText != "" {
		gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(m.copyText))})
		m.copyText = ""
	}
	if m.cpuHdr.Clicked(gtx) {
		m.sortMem = false
	}
	if m.memHdr.Clicked(gtx) {
		m.sortMem = true
	}
	if m.coresClk.Clicked(gtx) {
		m.coresOpen = !m.coresOpen
	}

	d := m.sv.sess.Monitor()
	p := m.sv.sess.Profile
	state := m.sv.sess.State()

	header := func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Left: 14, Right: 14, Top: 14}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			host := d.Static.Host
			if host == "" {
				host = p.Title()
			}
			rows := []layout.FlexChild{
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							c := th.Accent
							switch state {
							case sshx.StateConnecting:
								c = th.Warn
							case sshx.StateClosed:
								c = th.Text3
							}
							return drawIcon(gtx, icServer, 18, c)
						}),
						hspace(8),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return th.txtW(gtx, host, 15, th.Text, font.SemiBold)
						}),
					)
				}),
				vspace(8),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return m.kv(gtx, "地址", p.Addr()) }),
			}
			if d.Static.OS != "" {
				rows = append(rows, vspace(3), layout.Rigid(func(gtx layout.Context) layout.Dimensions { return m.kv(gtx, "系统", d.Static.OS) }))
			}
			if d.Static.Kernel != "" {
				rows = append(rows, vspace(3), layout.Rigid(func(gtx layout.Context) layout.Dimensions { return m.kv(gtx, "内核", d.Static.Kernel) }))
			}
			if d.Ready && !d.Unsupported {
				rows = append(rows,
					vspace(3), layout.Rigid(func(gtx layout.Context) layout.Dimensions { return m.kv(gtx, "运行", fmtUptime(d.Snap.Uptime)) }),
					vspace(3), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return m.kv(gtx, "负载", fmt.Sprintf("%.2f  %.2f  %.2f", d.Snap.Load[0], d.Snap.Load[1], d.Snap.Load[2]))
					}),
				)
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
		})
	}

	var sections []layout.Widget
	sections = append(sections, header)
	switch {
	case state != sshx.StateConnected:
		sections = append(sections, m.note("未连接"))
	case d.Unsupported:
		sections = append(sections, m.note("不支持监控"))
	case !d.Ready:
		sections = append(sections, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: 14, Top: 18}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.spinner(gtx, 16) }),
					hspace(10),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.txt(gtx, "加载中…", 12, th.Text2) }),
				)
			})
		})
	default:
		sections = append(sections, m.cpu(d), m.memory(d), m.network(d), m.disks(d), m.processes(d))
	}
	sections = append(sections, func(gtx layout.Context) layout.Dimensions { return layout.Spacer{Height: 16}.Layout(gtx) })

	gtx.Constraints = layout.Exact(size)
	th.list(gtx, &m.list, len(sections), func(gtx layout.Context, i int) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return sections[i](gtx)
	})
	return layout.Dimensions{Size: size}
}

func (m *monitorView) note(s string) layout.Widget {
	th := m.sv.a.th
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Left: 14, Right: 14, Top: 18}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return Label{Text: s, Size: 12, Color: th.Text3, MaxLines: -1}.Layout(gtx, th)
		})
	}
}

func (m *monitorView) cpu(d sshx.MonitorData) layout.Widget {
	th := m.sv.a.th
	return func(gtx layout.Context) layout.Dimensions {
		title := "CPU"
		if d.Static.NCPU > 0 {
			title = fmt.Sprintf("CPU · %d 核", d.Static.NCPU)
		}
		return m.section(gtx, title, fmt.Sprintf("%.0f%%", d.Snap.CPU), usageColor(th, d.Snap.CPU), func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return sparkline(gtx, th, 46, 100, [][]float64{d.CPU}, []color.NRGBA{th.Accent})
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					n := len(d.Snap.Cores)
					if n < 2 || n > 64 {
						return layout.Dimensions{}
					}
					// One thin vertical bar per core.
					w := gtx.Constraints.Max.X
					gap := gtx.Dp(3)
					if n > 16 {
						gap = gtx.Dp(1)
					}
					h := gtx.Dp(22)
					top := gtx.Dp(8)
					for i, v := range d.Snap.Cores {
						// Bars share the full width evenly.
						x := i * (w + gap) / n
						bw := max((i+1)*(w+gap)/n-gap-x, 1)
						fillRR(gtx.Ops, image.Rect(x, top, x+bw, top+h), gtx.Dp(2), th.Bg3)
						fh := int(float64(h) * v / 100)
						if fh > 0 {
							fillRR(gtx.Ops, image.Rect(x, top+h-max(fh, gtx.Dp(2)), x+bw, top+h), gtx.Dp(2), usageColor(th, v))
						}
					}
					return layout.Dimensions{Size: image.Pt(w, top+h)}
				}),
			)
		})
	}
}

func (m *monitorView) memory(d sshx.MonitorData) layout.Widget {
	th := m.sv.a.th
	return func(gtx layout.Context) layout.Dimensions {
		s := d.Snap
		pct := 0.0
		if s.MemTotal > 0 {
			pct = float64(s.MemUsed) / float64(s.MemTotal) * 100
		}
		return m.section(gtx, "内存", fmt.Sprintf("%.0f%%", pct), usageColor(th, pct), func(gtx layout.Context) layout.Dimensions {
			bar := func(used, cache, total uint64, c color.NRGBA) layout.Widget {
				return func(gtx layout.Context) layout.Dimensions {
					w, h := gtx.Constraints.Max.X, gtx.Dp(8)
					fillRR(gtx.Ops, image.Rect(0, 0, w, h), h/2, th.Bg3)
					if total > 0 {
						uw := int(float64(w) * float64(used) / float64(total))
						cw := int(float64(w) * float64(min(used+cache, total)) / float64(total))
						if cw > 0 && cache > 0 {
							fillRR(gtx.Ops, image.Rect(0, 0, max(cw, h), h), h/2, alpha(c, 0x44))
						}
						if uw > 0 {
							fillRR(gtx.Ops, image.Rect(0, 0, max(uw, h), h), h/2, c)
						}
					}
					return layout.Dimensions{Size: image.Pt(w, h)}
				}
			}
			line := func(k, v string) layout.Widget {
				return func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return th.txt(gtx, k, 12, th.Text3) }),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.txt(gtx, v, 12, th.Text2) }),
					)
				}
			}
			rows := []layout.FlexChild{
				layout.Rigid(bar(s.MemUsed, s.MemCache, s.MemTotal, usageColor(th, pct))),
				vspace(6),
				layout.Rigid(line("已用 / 总计", fmtBytes(s.MemUsed)+" / "+fmtBytes(s.MemTotal))),
				vspace(2),
				layout.Rigid(line("缓存", fmtBytes(s.MemCache))),
			}
			if s.SwapTotal > 0 {
				sp := float64(s.SwapUsed) / float64(s.SwapTotal) * 100
				rows = append(rows,
					vspace(8),
					layout.Rigid(bar(s.SwapUsed, 0, s.SwapTotal, mix(th.Purple, usageColor(th, sp), 0.0))),
					vspace(6),
					layout.Rigid(line("交换分区", fmtBytes(s.SwapUsed)+" / "+fmtBytes(s.SwapTotal))),
				)
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
		})
	}
}

func (m *monitorView) network(d sshx.MonitorData) layout.Widget {
	th := m.sv.a.th
	return func(gtx layout.Context) layout.Dimensions {
		s := d.Snap
		return m.section(gtx, "网络", "", th.Text, func(gtx layout.Context) layout.Dimensions {
			peak := 1024.0
			for _, v := range d.Rx {
				peak = max(peak, v)
			}
			for _, v := range d.Tx {
				peak = max(peak, v)
			}
			rate := func(ic *widget.Icon, c color.NRGBA, label string, v float64) layout.FlexChild {
				return layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions { return drawIcon(gtx, ic, 14, c) }),
						hspace(4),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.txtW(gtx, fmtRate(v), 12, th.Text, font.Medium) }),
					)
				})
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{}.Layout(gtx, rate(icDown, th.Accent, "下行", s.RxRate), rate(icUp, th.Blue, "上行", s.TxRate))
				}),
				vspace(7),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return sparkline(gtx, th, 46, peak*1.15, [][]float64{d.Rx, d.Tx}, []color.NRGBA{th.Accent, th.Blue})
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if len(s.Ifaces) < 2 {
						return layout.Dimensions{}
					}
					rows := []layout.FlexChild{vspace(6)}
					for _, it := range s.Ifaces {
						it := it
						rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
								layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return th.txt(gtx, it.Name, 12, th.Text3) }),
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									return th.txt(gtx, "↓ "+fmtRate(it.RxRate)+"   ↑ "+fmtRate(it.TxRate), 11, th.Text2)
								}),
							)
						}), vspace(2))
					}
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
				}),
			)
		})
	}
}

func (m *monitorView) disks(d sshx.MonitorData) layout.Widget {
	th := m.sv.a.th
	return func(gtx layout.Context) layout.Dimensions {
		if len(d.Snap.Disks) == 0 {
			return layout.Dimensions{}
		}
		return m.section(gtx, "磁盘", "", th.Text, func(gtx layout.Context) layout.Dimensions {
			var rows []layout.FlexChild
			for i, dk := range d.Snap.Disks {
				dk := dk
				if i > 0 {
					rows = append(rows, vspace(9))
				}
				pct := float64(dk.Used) / float64(dk.Used+dk.Avail) * 100
				rows = append(rows,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
							layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
								return th.mono(gtx, dk.Mount, 12, th.Text)
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return th.txt(gtx, fmtBytes(dk.Used)+" / "+fmtBytes(dk.Total), 11, th.Text2)
							}),
						)
					}),
					vspace(4),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return th.progress(gtx, float32(pct/100), 5, usageColor(th, pct))
					}),
				)
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
		})
	}
}

func (m *monitorView) processes(d sshx.MonitorData) layout.Widget {
	a := m.sv.a
	th := a.th
	return func(gtx layout.Context) layout.Dimensions {
		procs := append(m.procs[:0], d.Snap.Procs...)
		if m.sortMem {
			sort.SliceStable(procs, func(i, j int) bool { return procs[i].Mem > procs[j].Mem })
		} else {
			sort.SliceStable(procs, func(i, j int) bool {
				if procs[i].CPU != procs[j].CPU {
					return procs[i].CPU > procs[j].CPU
				}
				return procs[i].Mem > procs[j].Mem
			})
		}
		if len(procs) > maxProcRows {
			procs = procs[:maxProcRows]
		}
		m.procs = procs
		for i := range procs {
			if m.procRC[i].Clicked(gtx) {
				pr := procs[i]
				a.Menu(
					MenuItem{Label: fmt.Sprintf("%s  (PID %d)", pr.Name, pr.PID), Disabled: true},
					MenuItem{Sep: true},
					MenuItem{Label: "复制 PID", Icon: icCopy, Do: func() { m.copyText = strconv.Itoa(pr.PID) }},
					MenuItem{Label: "结束", Icon: icStop, Do: func() { m.kill(pr, "TERM") }},
					MenuItem{Label: "强制结束", Icon: icClose, Danger: true, Do: func() { m.kill(pr, "KILL") }},
				)
			}
		}
		cpuW, memW := gtx.Dp(46), gtx.Dp(62)
		hdr := func(clk *widget.Clickable, label string, w int, active bool) layout.FlexChild {
			return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints = layout.Exact(image.Pt(w, gtx.Dp(18)))
				return clk.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					c := th.Text3
					if active {
						c = th.Accent
						label += " ↓"
					} else if clk.Hovered() {
						c = th.Text2
					}
					return Label{Text: label, Size: 11, Color: c, Weight: font.SemiBold, Alignment: text.End}.Layout(gtx, th)
				})
			})
		}
		return m.section(gtx, fmt.Sprintf("进程 · %d", d.Snap.ProcCount), "", th.Text, func(gtx layout.Context) layout.Dimensions {
			rows := []layout.FlexChild{
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return th.txtW(gtx, "名称", 11, th.Text3, font.SemiBold)
						}),
						hdr(&m.cpuHdr, "CPU", cpuW, !m.sortMem),
						hdr(&m.memHdr, "内存", memW, m.sortMem),
					)
				}),
				vspace(3),
			}
			for i := range procs {
				i := i
				pr := procs[i]
				rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					h := gtx.Dp(21)
					w := gtx.Constraints.Max.X
					gtx.Constraints = layout.Exact(image.Pt(w, h))
					layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return th.mono(gtx, pr.Name, 12, th.Text)
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							gtx.Constraints = layout.Exact(image.Pt(cpuW, h))
							c := th.Text2
							if pr.CPU >= 50 {
								c = th.Warn
							}
							return layout.E.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								gtx.Constraints.Min = image.Point{}
								return th.txt(gtx, fmt.Sprintf("%.1f", pr.CPU), 12, c)
							})
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							gtx.Constraints = layout.Exact(image.Pt(memW, h))
							return layout.E.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								gtx.Constraints.Min = image.Point{}
								return th.txt(gtx, fmtBytes(pr.Mem), 12, th.Text2)
							})
						}),
					)
					ar := clip.Rect{Max: image.Pt(w, h)}.Push(gtx.Ops)
					m.procRC[i].Add(gtx.Ops)
					ar.Pop()
					return layout.Dimensions{Size: image.Pt(w, h)}
				}))
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
		})
	}
}

func (m *monitorView) kill(pr monitor.Proc, sig string) {
	a := m.sv.a
	sess := m.sv.sess
	go func() {
		out, err := sess.Exec(fmt.Sprintf("kill -%s %d", sig, pr.PID))
		a.Post(func() {
			if err != nil {
				msg := out
				if msg == "" {
					msg = err.Error()
				}
				a.Toast(toastError, "无法结束 "+pr.Name+"："+msg)
				return
			}
			a.Toast(toastOK, "已结束 "+pr.Name)
			sess.RefreshMonitor()
		})
	}()
}
