package ui

import (
	"fmt"
	"image"
	"strconv"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"

	"nlrshell/internal/sshx"
	"nlrshell/internal/store"
)

// ---- Connection profile editor ------------------------------------------

type profileDialog struct {
	a            *App
	p            store.Profile
	connectAfter bool

	name, group, host, port, user Field
	password, keyPath, passphrase Field
	auth                          Segmented
	jumpClk, browseClk            widget.Clickable
	errMsg                        string
	focused                       bool

	closeClk, cancelClk, saveClk, connectClk widget.Clickable
}

func newProfileDialog(a *App, p store.Profile, connectAfter bool) *profileDialog {
	d := &profileDialog{a: a, p: p, connectAfter: connectAfter}
	d.name.Label, d.name.Hint = "名称", "可选"
	d.group.Label, d.group.Hint = "分组", "可选"
	d.host.Label = "主机"
	d.port.Label = "端口"
	d.user.Label = "用户名"
	d.password.Label = "密码"
	d.keyPath.Label = "私钥文件"
	d.passphrase.Label, d.passphrase.Hint = "私钥口令", "可选"
	d.password.Editor.Mask, d.passphrase.Editor.Mask = '•', '•'
	d.port.Editor.Filter = "0123456789"
	d.name.SetText(p.Name)
	d.group.SetText(p.Group)
	d.host.SetText(p.Host)
	if p.Port == 0 {
		p.Port = 22
	}
	d.port.SetText(strconv.Itoa(p.Port))
	d.user.SetText(p.User)
	d.keyPath.SetText(p.KeyPath)
	d.auth.Value = p.Auth
	if d.auth.Value == "" {
		d.auth.Value = store.AuthPassword
	}
	if p.Password != "" {
		d.password.Hint = "已保存"
	} else {
		d.password.Hint = "可选"
	}
	if p.Passphrase != "" {
		d.passphrase.Hint = "已保存"
	}
	return d
}

func (d *profileDialog) build() (store.Profile, bool) {
	p := d.p
	p.Name = strings.TrimSpace(d.name.Text())
	p.Group = strings.TrimSpace(d.group.Text())
	p.Host = strings.TrimSpace(d.host.Text())
	p.User = strings.TrimSpace(d.user.Text())
	p.Auth = d.auth.Value
	p.KeyPath = strings.TrimSpace(d.keyPath.Text())
	port, err := strconv.Atoi(strings.TrimSpace(d.port.Text()))
	switch {
	case p.Host == "":
		d.errMsg = "请填写主机"
		return p, false
	case err != nil || port <= 0 || port > 65535:
		d.errMsg = "端口无效"
		return p, false
	case p.User == "":
		d.errMsg = "请填写用户名"
		return p, false
	case p.Auth == store.AuthKey && p.KeyPath == "":
		d.errMsg = "请选择私钥"
		return p, false
	}
	p.Port = port
	if pw := d.password.Text(); pw != "" {
		p.Password = store.Encrypt(pw)
	}
	if pp := d.passphrase.Text(); pp != "" {
		p.Passphrase = store.Encrypt(pp)
	}
	return p, true
}

func (d *profileDialog) save(connect bool) {
	p, ok := d.build()
	if !ok {
		return
	}
	p = d.a.st.SaveProfile(p)
	d.a.home.refresh()
	d.a.Close(d)
	if connect {
		d.a.Connect(p)
	}
}

func (d *profileDialog) Submit(a *App) { d.save(d.connectAfter) }
func (d *profileDialog) Cancel(a *App) { a.Close(d) }

func (d *profileDialog) Layout(gtx layout.Context, a *App) layout.Dimensions {
	th := a.th
	if !d.focused {
		d.focused = true
		if d.host.Text() == "" {
			d.host.Focus(gtx)
		} else {
			d.name.Focus(gtx)
		}
	}
	for _, f := range []*Field{&d.name, &d.group, &d.host, &d.port, &d.user, &d.password, &d.keyPath, &d.passphrase} {
		if _, changed := f.Events(gtx); changed {
			d.errMsg = ""
		}
	}
	if d.cancelClk.Clicked(gtx) {
		d.Cancel(a)
	}
	if d.saveClk.Clicked(gtx) {
		d.save(false)
	}
	if d.connectClk.Clicked(gtx) {
		d.save(true)
	}
	if d.browseClk.Clicked(gtx) {
		go func() {
			if paths, ok := pickFiles(a.host.HWND(), "选择私钥文件"); ok && len(paths) > 0 {
				a.Post(func() { d.keyPath.SetText(paths[0]) })
			}
		}()
	}
	if d.jumpClk.Clicked(gtx) {
		items := []MenuItem{{Label: "无", Checked: d.p.JumpID == "", Do: func() { d.p.JumpID = "" }}}
		for _, jp := range a.st.Profiles() {
			jp := jp
			if jp.ID == d.p.ID {
				continue
			}
			items = append(items, MenuItem{Label: jp.Title() + "  (" + jp.Addr() + ")", Checked: d.p.JumpID == jp.ID, Do: func() { d.p.JumpID = jp.ID }})
		}
		a.Menu(items...)
	}
	title := "新建连接"
	if d.p.ID != "" {
		title = "编辑连接"
	}
	row := func(gap unit.Dp, ws ...layout.FlexChild) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Bottom: gap}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.End}.Layout(gtx, ws...)
			})
		})
	}
	fld := func(weight float32, f *Field) layout.FlexChild {
		return layout.Flexed(weight, func(gtx layout.Context) layout.Dimensions { return f.Layout(gtx, th) })
	}
	return a.dialogFrame(gtx, title, 520, &d.closeClk, d,
		func(gtx layout.Context) layout.Dimensions {
			rows := []layout.FlexChild{
				row(12, fld(3, &d.host), hspace(10), fld(1, &d.port)),
				row(12, fld(1, &d.user), hspace(10), fld(1, &d.name)),
				row(14, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.txt(gtx, "认证方式", 12, th.Text2) }),
						vspace(5),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return d.auth.Layout(gtx, th, [][2]string{{store.AuthPassword, "密码"}, {store.AuthKey, "私钥"}, {store.AuthAgent, "SSH Agent"}})
						}),
					)
				})),
			}
			switch d.auth.Value {
			case store.AuthPassword:
				rows = append(rows, row(12, fld(1, &d.password)))
			case store.AuthKey:
				rows = append(rows,
					row(12, fld(1, &d.keyPath), hspace(8), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Bottom: 1}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return th.button(gtx, &d.browseClk, "浏览…", nil, btnDefault)
						})
					})),
					row(12, fld(1, &d.passphrase)),
				)
			}
			rows = append(rows,
				row(4, fld(1, &d.group), hspace(10), layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					jump := "无"
					if jp, ok := a.st.Profile(d.p.JumpID); ok && d.p.JumpID != "" {
						jump = jp.Title()
					}
					return selectBox(gtx, th, &d.jumpClk, "跳板机", jump)
				})),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if d.errMsg == "" {
						return layout.Dimensions{}
					}
					return layout.Inset{Top: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return th.txt(gtx, d.errMsg, 12, th.Danger)
					})
				}),
			)
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
		},
		func(gtx layout.Context) layout.Dimensions {
			return buttonRow(gtx,
				func(gtx layout.Context) layout.Dimensions {
					return th.button(gtx, &d.cancelClk, "取消", nil, btnDefault)
				},
				func(gtx layout.Context) layout.Dimensions {
					return th.button(gtx, &d.saveClk, "保存", nil, btnDefault)
				},
				func(gtx layout.Context) layout.Dimensions {
					return th.button(gtx, &d.connectClk, "保存并连接", icPlay, btnPrimary)
				},
			)
		})
}

// selectBox draws a labeled drop-down style button showing value.
func selectBox(gtx layout.Context, th *Theme, clk *widget.Clickable, label, value string) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if label == "" {
				return layout.Dimensions{}
			}
			return layout.Inset{Bottom: 5}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return th.txt(gtx, label, 12, th.Text2)
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			h := gtx.Dp(32)
			w := gtx.Constraints.Max.X
			gtx.Constraints = layout.Exact(image.Pt(w, h))
			return clk.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				r := image.Rectangle{Max: image.Pt(w, h)}
				bg := th.Bg0
				if clk.Hovered() {
					bg = th.Bg2
				}
				fillRR(gtx.Ops, r, gtx.Dp(6), bg)
				strokeRR(gtx.Ops, r, gtx.Dp(6), float32(gtx.Dp(1)), th.Border)
				layout.Inset{Left: 10, Right: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return th.txt(gtx, value, 13, th.Text) }),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions { return drawIcon(gtx, icExpand, 18, th.Text3) }),
					)
				})
				return layout.Dimensions{Size: r.Max}
			})
		}),
	)
}

// ---- Prompter -----------------------------------------------------------

// prompter implements sshx.Prompter with modal dialogs. Its methods block
// the calling (connection) goroutine until the user answers.
type prompter struct {
	a     *App
	title string
}

func (p *prompter) HostKey(host, keyType, fingerprint string, changed bool) bool {
	ch := make(chan bool, 1)
	p.a.Post(func() {
		d := &confirmDialog{
			title:   "主机密钥",
			message: "首次连接 " + host,
			detail:  keyType + "\n" + fingerprint,
			okLabel: "信任",
			onOK:    func() { ch <- true },
			onNo:    func() { ch <- false },
		}
		if changed {
			d.title = "主机密钥已变更"
			d.message = host + " 的密钥与记录不一致，可能遭到中间人攻击。"
			d.okLabel = "仍然连接"
			d.danger = true
		}
		p.a.Open(d)
	})
	return <-ch
}

func (p *prompter) Password(title string) (string, bool, bool) {
	type res struct {
		pw   string
		save bool
		ok   bool
	}
	ch := make(chan res, 1)
	p.a.Post(func() {
		f := &Field{Label: "密码"}
		f.Editor.Mask = '•'
		chk := &widget.Bool{Value: true}
		p.a.Open(&inputDialog{title: "密码", message: title, fields: []*Field{f}, check: chk, checkLb: "记住密码", okLabel: "连接",
			onOK: func(v []string, save bool) { ch <- res{v[0], save, true} },
			onNo: func() { ch <- res{} }})
	})
	r := <-ch
	return r.pw, r.save, r.ok
}

func (p *prompter) Passphrase(keyPath string) (string, bool) {
	type res struct {
		pp string
		ok bool
	}
	ch := make(chan res, 1)
	p.a.Post(func() {
		f := &Field{Label: "口令"}
		f.Editor.Mask = '•'
		p.a.Open(&inputDialog{title: "私钥口令", message: keyPath, fields: []*Field{f}, okLabel: "解锁",
			onOK: func(v []string, _ bool) { ch <- res{v[0], true} },
			onNo: func() { ch <- res{} }})
	})
	r := <-ch
	return r.pp, r.ok
}

func (p *prompter) Interactive(title, instruction string, questions []string, echo []bool) ([]string, bool) {
	type res struct {
		ans []string
		ok  bool
	}
	ch := make(chan res, 1)
	p.a.Post(func() {
		fields := make([]*Field, len(questions))
		for i, q := range questions {
			fields[i] = &Field{Label: strings.TrimSpace(q)}
			if i < len(echo) && !echo[i] {
				fields[i].Editor.Mask = '•'
			}
		}
		p.a.Open(&inputDialog{title: title, message: instruction, fields: fields, okLabel: "继续",
			onOK: func(v []string, _ bool) { ch <- res{v, true} },
			onNo: func() { ch <- res{} }})
	})
	r := <-ch
	return r.ans, r.ok
}

// ---- Port forwarding ------------------------------------------------------

type tunnelDialog struct {
	sv   *sessionView
	list widget.List

	kind                Segmented
	lport, thost, tport Field
	auto                widget.Bool
	addClk              widget.Clickable
	toggles, dels       map[string]*widget.Clickable
	errMsg              string
	closeClk, doneClk   widget.Clickable
}

func newTunnelDialog(sv *sessionView) *tunnelDialog {
	d := &tunnelDialog{sv: sv, toggles: map[string]*widget.Clickable{}, dels: map[string]*widget.Clickable{}}
	d.kind.Value = store.ForwardLocal
	d.lport.Hint, d.thost.Hint, d.tport.Hint = "端口", "目标主机", "目标端口"
	d.lport.Editor.Filter, d.tport.Editor.Filter = "0123456789", "0123456789"
	d.auto.Value = true
	return d
}

func (d *tunnelDialog) Submit(a *App) { d.add() }
func (d *tunnelDialog) Cancel(a *App) { a.Close(d) }

func (d *tunnelDialog) persist() {
	sess := d.sv.sess
	if sess.Profile.ID != "" {
		if p, ok := d.sv.a.st.Profile(sess.Profile.ID); ok {
			p.Forwards = sess.Profile.Forwards
			d.sv.a.st.SaveProfile(p)
			d.sv.a.home.refresh()
		}
	}
}

func (d *tunnelDialog) add() {
	lp, err := strconv.Atoi(d.lport.Text())
	if err != nil || lp <= 0 || lp > 65535 {
		d.errMsg = "端口无效"
		return
	}
	f := store.Forward{ID: store.NewID(), Kind: d.kind.Value, ListenHost: "127.0.0.1", ListenPort: lp, AutoStart: d.auto.Value}
	if f.Kind != store.ForwardDynamic {
		tp, err := strconv.Atoi(d.tport.Text())
		host := strings.TrimSpace(d.thost.Text())
		if host == "" {
			host = "127.0.0.1"
		}
		if err != nil || tp <= 0 || tp > 65535 {
			d.errMsg = "目标端口无效"
			return
		}
		f.TargetHost, f.TargetPort = host, tp
	}
	d.errMsg = ""
	sess := d.sv.sess
	sess.Profile.Forwards = append(sess.Profile.Forwards, f)
	d.persist()
	d.lport.SetText("")
	d.tport.SetText("")
	d.start(f)
}

func (d *tunnelDialog) start(f store.Forward) {
	a := d.sv.a
	sess := d.sv.sess
	go func() {
		err := sess.Forwards.Start(f)
		a.Post(func() {
			if err != nil {
				a.Toast(toastError, "无法启动转发："+err.Error())
			}
		})
	}()
}

func forwardText(f store.Forward) (kind, desc string) {
	listen := fmt.Sprintf("%s:%d", f.ListenHost, f.ListenPort)
	if f.ListenHost == "" {
		listen = fmt.Sprintf("127.0.0.1:%d", f.ListenPort)
	}
	switch f.Kind {
	case store.ForwardLocal, store.ForwardRemote:
		kind := "本地"
		if f.Kind == store.ForwardRemote {
			kind = "远程"
		}
		return kind, fmt.Sprintf("%s → %s:%d", listen, f.TargetHost, f.TargetPort)
	}
	return "动态", listen + " SOCKS5"
}

func (d *tunnelDialog) Layout(gtx layout.Context, a *App) layout.Dimensions {
	th := a.th
	sess := d.sv.sess
	for _, f := range []*Field{&d.lport, &d.thost, &d.tport} {
		f.Events(gtx)
	}
	if d.addClk.Clicked(gtx) {
		d.add()
	}
	if d.doneClk.Clicked(gtx) {
		d.Cancel(a)
	}
	fwds := sess.Forwards.List()
	for _, f := range fwds {
		f := f
		if d.toggles[f.ID] == nil {
			d.toggles[f.ID] = new(widget.Clickable)
			d.dels[f.ID] = new(widget.Clickable)
		}
		if d.toggles[f.ID].Clicked(gtx) {
			if f.Active {
				sess.Forwards.Stop(f.ID)
			} else {
				d.start(f.Forward)
			}
		}
		if d.dels[f.ID].Clicked(gtx) {
			sess.Forwards.Stop(f.ID)
			out := sess.Profile.Forwards[:0]
			for _, x := range sess.Profile.Forwards {
				if x.ID != f.ID {
					out = append(out, x)
				}
			}
			sess.Profile.Forwards = out
			d.persist()
		}
	}
	connected := sess.State() == sshx.StateConnected
	return a.dialogFrame(gtx, "端口转发", 620, &d.closeClk, d,
		func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if len(fwds) == 0 {
						return layout.Inset{Top: 6, Bottom: 14}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return th.txt(gtx, "暂无规则", 12, th.Text3)
						})
					}
					gtx.Constraints.Max.Y = min(gtx.Constraints.Max.Y, gtx.Dp(260))
					return th.list(gtx, &d.list, len(fwds), func(gtx layout.Context, i int) layout.Dimensions {
						f := fwds[i]
						kind, desc := forwardText(f.Forward)
						return layout.Inset{Bottom: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							rec := op.Record(gtx.Ops)
							dm := layout.Inset{Left: 12, Right: 8, Top: 8, Bottom: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
									layout.Rigid(func(gtx layout.Context) layout.Dimensions {
										c := th.Text3
										if f.Active {
											c = th.Accent
										} else if f.Err != "" {
											c = th.Danger
										}
										s := gtx.Dp(8)
										fillRR(gtx.Ops, image.Rectangle{Max: image.Pt(s, s)}, s/2, c)
										return layout.Dimensions{Size: image.Pt(s, s)}
									}),
									hspace(10),
									layout.Rigid(func(gtx layout.Context) layout.Dimensions {
										gtx.Constraints.Min.X = gtx.Dp(34)
										return th.txtW(gtx, kind, 12, th.Text2, font.Medium)
									}),
									layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
										return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
											layout.Rigid(func(gtx layout.Context) layout.Dimensions {
												return Label{Text: desc, Size: 12, Color: th.Text, Mono: true}.Layout(gtx, th)
											}),
											layout.Rigid(func(gtx layout.Context) layout.Dimensions {
												s, c := "已停止", th.Text3
												switch {
												case f.Err != "":
													s, c = f.Err, th.Danger
												case f.Active:
													s = fmt.Sprintf("运行中 · %d", f.Conns)
												}
												return th.txt(gtx, s, 11, c)
											}),
										)
									}),
									layout.Rigid(func(gtx layout.Context) layout.Dimensions {
										if !connected {
											gtx = gtx.Disabled()
										}
										ic := icPlay
										if f.Active {
											ic = icStop
										}
										tip := "启动"
										if f.Active {
											tip = "停止"
										}
										return th.iconButton(gtx, d.toggles[f.ID], ic, 28, 16, th.Text2, false, tip)
									}),
									layout.Rigid(func(gtx layout.Context) layout.Dimensions {
										return th.iconButton(gtx, d.dels[f.ID], icDelete, 28, 16, th.Text2, false, "删除")
									}),
								)
							})
							call := rec.Stop()
							r := image.Rectangle{Max: image.Pt(gtx.Constraints.Max.X, dm.Size.Y)}
							fillRR(gtx.Ops, r, gtx.Dp(8), th.Bg2)
							call.Add(gtx.Ops)
							return layout.Dimensions{Size: r.Max}
						})
					})
				}),
				vspace(8),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return d.kind.Layout(gtx, th, [][2]string{{store.ForwardLocal, "本地"}, {store.ForwardRemote, "远程"}, {store.ForwardDynamic, "动态"}})
				}),
				vspace(10),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					children := []layout.FlexChild{
						layout.Flexed(2, func(gtx layout.Context) layout.Dimensions { return d.lport.box(gtx, th, nil) }),
					}
					if d.kind.Value != store.ForwardDynamic {
						children = append(children,
							hspace(6),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.txt(gtx, "→", 14, th.Text3) }),
							hspace(6),
							layout.Flexed(3, func(gtx layout.Context) layout.Dimensions { return d.thost.box(gtx, th, nil) }),
							hspace(6),
							layout.Flexed(2, func(gtx layout.Context) layout.Dimensions { return d.tport.box(gtx, th, nil) }),
						)
					}
					children = append(children, hspace(8), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return th.button(gtx, &d.addClk, "添加", icAdd, btnPrimary)
					}))
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx, children...)
				}),
				vspace(10),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return th.checkbox(gtx, &d.auto, "连接时自动启动")
				}),
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
			return th.button(gtx, &d.doneClk, "完成", nil, btnDefault)
		})
}

// ---- Settings -------------------------------------------------------------

type settingsDialog struct {
	a *App

	fontFamily, fontSize, scrollback, interval, downloadDir Field
	copySel, rightPaste, follow, hidden                     widget.Bool
	browseClk                                               widget.Clickable
	closeClk, cancelClk, saveClk                            widget.Clickable
	errMsg                                                  string
}

func newSettingsDialog(a *App) *settingsDialog {
	s := a.set
	d := &settingsDialog{a: a}
	d.fontFamily.Label = "字体"
	d.fontSize.Label = "字号"
	d.scrollback.Label = "回滚行数"
	d.interval.Label = "监控间隔（秒）"
	d.downloadDir.Label = "下载位置"
	for _, f := range []*Field{&d.fontSize, &d.scrollback, &d.interval} {
		f.Editor.Filter = "0123456789"
	}
	d.fontFamily.SetText(s.FontFamily)
	d.fontSize.SetText(strconv.Itoa(int(s.FontSize)))
	d.scrollback.SetText(strconv.Itoa(s.Scrollback))
	d.interval.SetText(strconv.Itoa(s.MonitorInterval))
	d.downloadDir.SetText(s.DownloadDir)
	d.copySel.Value, d.rightPaste.Value, d.follow.Value, d.hidden.Value = s.CopyOnSelect, s.RightClickPaste, s.FollowCwd, s.ShowHidden
	return d
}

func (d *settingsDialog) Cancel(a *App) { a.Close(d) }

func (d *settingsDialog) Submit(a *App) {
	size, _ := strconv.Atoi(d.fontSize.Text())
	sb, _ := strconv.Atoi(d.scrollback.Text())
	iv, _ := strconv.Atoi(d.interval.Text())
	switch {
	case size < 8 || size > 40:
		d.errMsg = "字号：8–40"
		return
	case sb < 100 || sb > 1000000:
		d.errMsg = "回滚行数：100–1000000"
		return
	case iv < 1 || iv > 60:
		d.errMsg = "监控间隔：1–60"
		return
	}
	family := strings.TrimSpace(d.fontFamily.Text())
	if family == "" {
		family = store.DefaultSettings().FontFamily
	}
	a.updateSettings(func(s *store.Settings) {
		s.FontFamily, s.FontSize, s.Scrollback, s.MonitorInterval = family, float32(size), sb, iv
		if dir := strings.TrimSpace(d.downloadDir.Text()); dir != "" {
			s.DownloadDir = dir
		}
		s.CopyOnSelect, s.RightClickPaste, s.FollowCwd, s.ShowHidden = d.copySel.Value, d.rightPaste.Value, d.follow.Value, d.hidden.Value
	})
	a.th.SetMono(family)
	for _, t := range a.sessionViews() {
		t.sess.Term.SetScrollback(sb)
		t.sess.SetMonitorInterval(time.Duration(iv) * time.Second)
		t.files.rebuild()
	}
	a.Flush()
	a.Close(d)
}

func (d *settingsDialog) Layout(gtx layout.Context, a *App) layout.Dimensions {
	th := a.th
	for _, f := range []*Field{&d.fontFamily, &d.fontSize, &d.scrollback, &d.interval, &d.downloadDir} {
		if _, changed := f.Events(gtx); changed {
			d.errMsg = ""
		}
	}
	if d.cancelClk.Clicked(gtx) {
		d.Cancel(a)
	}
	if d.saveClk.Clicked(gtx) {
		d.Submit(a)
	}
	if d.browseClk.Clicked(gtx) {
		go func() {
			if dir, ok := pickFolder(a.host.HWND(), "选择默认下载位置"); ok {
				a.Post(func() { d.downloadDir.SetText(dir) })
			}
		}()
	}
	fld := func(weight float32, f *Field) layout.FlexChild {
		return layout.Flexed(weight, func(gtx layout.Context) layout.Dimensions { return f.Layout(gtx, th) })
	}
	chk := func(b *widget.Bool, label string) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Bottom: 9}.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return th.checkbox(gtx, b, label) })
		})
	}
	return a.dialogFrame(gtx, "设置", 520, &d.closeClk, d,
		func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.End}.Layout(gtx, fld(3, &d.fontFamily), hspace(10), fld(1, &d.fontSize))
				}),
				vspace(12),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.End}.Layout(gtx, fld(1, &d.scrollback), hspace(10), fld(1, &d.interval))
				}),
				vspace(12),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.End}.Layout(gtx, fld(1, &d.downloadDir), hspace(8),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layout.Inset{Bottom: 1}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return th.button(gtx, &d.browseClk, "浏览…", nil, btnDefault)
							})
						}))
				}),
				vspace(16),
				chk(&d.copySel, "选中即复制"),
				chk(&d.rightPaste, "右键粘贴"),
				chk(&d.follow, "文件跟随终端目录"),
				chk(&d.hidden, "显示隐藏文件"),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if d.errMsg == "" {
						return layout.Dimensions{}
					}
					return th.txt(gtx, d.errMsg, 12, th.Danger)
				}),
			)
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
