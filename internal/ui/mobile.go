package ui

import (
	"fmt"
	"image"
	"time"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"

	"nlrshell/internal/droid"
	"nlrshell/internal/sshx"
	"nlrshell/internal/store"
)

// Phones and tablets. Mobile mode (Android) is touch first: no window
// buttons, a row of terminal keys the soft keyboard lacks, the back key and
// a foreground service that keeps sessions connected in the background.
// Independently of that, a narrow window (a phone in portrait) gets the
// compact layout, which shows the terminal, the monitor and the files one at
// a time and folds the tabs into a menu.

// compactWidth is the window width in dp below which the layout is compact.
const compactWidth = 600

// Panes of a session in the compact layout.
const (
	paneTerminal = iota
	paneMonitor
	paneFiles
)

// mobileState is the App state that only mobile mode and the compact layout
// use.
type mobileState struct {
	// kbdInset is the height in dp the system takes at the bottom of the
	// window: the navigation bar, plus the soft keyboard while it shows.
	kbdInset float32
	// keepOn and keepText are what the keep-alive service was last told.
	keepOn   bool
	keepText string
	// notified is set once the notification permission was asked for.
	notified bool
	// storageWait is the action waiting for the storage permission, which
	// the user grants on a system page; it runs once they come back with
	// it. storageAsked is when they were sent there, storageCheck when the
	// permission was last checked.
	storageWait                func()
	storageAsked, storageCheck time.Time

	tabPill, moreBtn widget.Clickable
}

// SetMobile switches mobile mode on (Android) or off.
func (a *App) SetMobile(on bool) {
	a.mobile = on
	if on {
		// The window has no chrome of its own to replace.
		a.decorated, a.maximized = true, true
	}
}

// SetBottomInset tells the app how much of the window bottom (in dp) the
// system covers: the navigation bar, and the soft keyboard while it shows.
func (a *App) SetBottomInset(dp float32) { a.mob.kbdInset = dp }

// ViewAttached is called when Android (re)creates the app's view, after the
// activity was recreated or the app came back from the background.
func (a *App) ViewAttached() {
	a.Post(func() {
		a.applyTheme()
		// A service start refused while the app was in the background is
		// retried now that it is in front.
		a.mob.keepOn, a.mob.keepText = false, ""
	})
}

// compact reports whether the window is narrow enough for the compact
// layout.
func (a *App) compact() bool {
	return a.pxPerDp > 0 && float32(a.size.X)/a.pxPerDp < compactWidth
}

// keyboardShown guesses from the bottom inset whether the soft keyboard is
// up: navigation bars are well under 100dp, keyboards well over.
func (a *App) keyboardShown() bool { return a.mob.kbdInset > 120 }

// backKey handles the Android back key: it closes the menu or the top
// dialog, then returns from the monitor or files pane to the terminal, then
// from a tab to the home page, and finally sends the app to the background
// so the sessions stay open.
func (a *App) backKey(gtx layout.Context) {
	if !a.mobile {
		return
	}
	for {
		e, ok := gtx.Event(key.Filter{Name: key.NameBack})
		if !ok {
			break
		}
		if ke, ok := e.(key.Event); !ok || ke.State != key.Press {
			continue
		}
		switch sv := a.current(); {
		case a.menu != nil:
			a.closeMenu()
		case len(a.dialogs) > 0:
			a.dialogs[len(a.dialogs)-1].Cancel(a)
		case sv != nil && sv.pane != paneTerminal:
			sv.pane = paneTerminal
			a.refocus = true
		case a.active >= 0:
			a.activate(-1)
		default:
			droid.MoveToBack()
		}
		a.host.Invalidate()
	}
}

// syncKeepAlive runs the foreground service while sessions are connected,
// with the count in its notification.
func (a *App) syncKeepAlive() {
	if !a.mobile {
		return
	}
	n := 0
	for _, sv := range a.sessionViews() {
		if sv.sess.State() != sshx.StateClosed {
			n++
		}
	}
	on := n > 0 && a.set.KeepAlive
	text := fmt.Sprintf("%d 个 SSH 会话保持连接中", n)
	if on == a.mob.keepOn && (!on || text == a.mob.keepText) {
		return
	}
	a.mob.keepOn, a.mob.keepText = on, text
	if on && !a.mob.notified {
		a.mob.notified = true
		go droid.RequestNotifications()
	}
	go droid.KeepAlive(on, text)
}

// quitMobile closes the sessions and ends the process: Android apps have
// no window to close.
func (a *App) quitMobile() {
	a.Shutdown()
	droid.KeepAlive(false, "")
	droid.Quit()
}

// needStorage runs f when the app may use shared storage, and otherwise
// explains why it is needed and opens the system page that grants it.
func (a *App) needStorage(f func()) {
	if !a.mobile || droid.HasStorage() {
		f()
		return
	}
	a.Open(&confirmDialog{
		title:   "需要文件访问权限",
		message: "选择本地文件、上传和下载都需要访问手机存储。请在接下来打开的系统页面中允许 NLR Shell「访问所有文件」，然后返回。",
		okLabel: "去授权",
		onOK: func() {
			a.mob.storageWait, a.mob.storageAsked = f, time.Now()
			go droid.RequestStorage()
		},
	})
}

// storageTick runs the action waiting for the storage permission once the
// user has granted it, checking every second for a few minutes.
func (a *App) storageTick(gtx layout.Context) {
	m := &a.mob
	if m.storageWait == nil {
		return
	}
	if gtx.Now.Sub(m.storageAsked) > 3*time.Minute {
		m.storageWait = nil
		return
	}
	if gtx.Now.Sub(m.storageCheck) >= time.Second {
		m.storageCheck = gtx.Now
		if droid.HasStorage() {
			f := m.storageWait
			m.storageWait = nil
			f()
			return
		}
	}
	gtx.Execute(op.InvalidateCmd{At: m.storageCheck.Add(time.Second)})
}

// ---- Long press ---------------------------------------------------------

// longPressDelay is how long a touch must rest before it counts as a long
// press, Android's default.
const longPressDelay = 450 * time.Millisecond

// longPress turns a touch that rests in place into a secondary-button
// press, the way touch screens open context menus.
type longPress struct {
	id         pointer.ID
	start      f32.Point
	at         time.Time
	down, done bool
}

// Event feeds a pointer event of the handler that owns l.
func (l *longPress) Event(gtx layout.Context, e pointer.Event) {
	switch e.Kind {
	case pointer.Press:
		if e.Source != pointer.Touch {
			l.down = false
			return
		}
		*l = longPress{id: e.PointerID, start: e.Position, at: gtx.Now, down: true}
		gtx.Execute(op.InvalidateCmd{At: l.at.Add(longPressDelay)})
	case pointer.Drag:
		if d := e.Position.Sub(l.start); l.down && e.PointerID == l.id && d.X*d.X+d.Y*d.Y > float32(gtx.Dp(8)*gtx.Dp(8)) {
			l.down = false
		}
	case pointer.Release:
		if e.PointerID == l.id {
			l.down = false
		}
	case pointer.Cancel:
		// Another handler took the touch, as a list does when the finger
		// starts to scroll it. Cancel events name no pointer.
		l.down = false
	}
}

// Fired reports, once per touch, that it has rested long enough. It then
// grabs the pointer for tag, so the widgets below cancel their click.
func (l *longPress) Fired(gtx layout.Context, tag event.Tag) bool {
	if !l.down || l.done {
		return false
	}
	if gtx.Now.Sub(l.at) < longPressDelay {
		gtx.Execute(op.InvalidateCmd{At: l.at.Add(longPressDelay)})
		return false
	}
	l.done = true
	gtx.Execute(pointer.GrabCmd{Tag: tag, ID: l.id})
	return true
}

// ---- Compact title bar --------------------------------------------------

// layoutCompactTitlebar draws the title bar of the compact layout: the logo,
// the current page as a pill that opens the list of pages, and the tools.
func (a *App) layoutCompactTitlebar(gtx layout.Context) layout.Dimensions {
	th := a.th
	h := gtx.Dp(titleH)
	w := gtx.Constraints.Max.X
	fill(gtx.Ops, image.Rect(0, h-max(gtx.Dp(1), 1), w, h), th.Border)

	sv := a.current()
	if a.logoClk.Clicked(gtx) {
		a.appMenu()
	}
	if a.mob.tabPill.Clicked(gtx) {
		a.pageMenu()
	}
	if a.addClk.Clicked(gtx) {
		if p, ok := a.tabProfile(); ok {
			a.Connect(p)
		} else {
			a.home.editProfile(store.Profile{Port: 22, Auth: store.AuthPassword}, true)
		}
	}
	if a.sideBtn.Clicked(gtx) && sv != nil {
		sv.togglePane(paneMonitor)
	}
	if a.filesBtn.Clicked(gtx) && sv != nil {
		sv.togglePane(paneFiles)
	}
	if a.setBtn.Clicked(gtx) {
		a.Open(newSettingsDialog(a))
	}
	if a.mob.moreBtn.Clicked(gtx) && sv != nil {
		a.MenuAt(image.Pt(w, h),
			MenuItem{Label: "端口转发", Icon: icTunnel, Do: func() { a.Open(newTunnelDialog(sv)) }},
			MenuItem{Label: "隐私模式", Icon: icEyeOff, Checked: a.set.Privacy, Do: func() {
				a.updateSettings(func(s *store.Settings) { s.Privacy = !s.Privacy })
			}},
			MenuItem{Label: "命令栏", Icon: icTerminal, Checked: a.set.CommandBar, Do: func() {
				a.updateSettings(func(s *store.Settings) { s.CommandBar = !s.CommandBar })
			}},
			MenuItem{Sep: true},
			MenuItem{Label: "设置", Icon: icSettings, Do: func() { a.Open(newSettingsDialog(a)) }},
		)
	}

	at := func(x int, wd layout.Widget) int {
		st := op.Offset(image.Pt(x, 0)).Push(gtx.Ops)
		d := wd(gtx)
		st.Pop()
		return x + d.Size.X
	}
	x := at(0, func(gtx layout.Context) layout.Dimensions {
		sz := image.Pt(gtx.Dp(46), h)
		return a.logoClk.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			if a.logoClk.Pressed() {
				fillRR(gtx.Ops, image.Rectangle{Max: sz}.Inset(gtx.Dp(5)), gtx.Dp(6), th.Bg3)
			}
			ls := gtx.Dp(24)
			st := op.Offset(image.Pt((sz.X-ls)/2, (sz.Y-ls)/2)).Push(gtx.Ops)
			drawLogo(gtx, ls, th.Text, th.Accent)
			st.Pop()
			return layout.Dimensions{Size: sz}
		})
	})

	// Tools, from the right edge.
	tool := gtx.Dp(36)
	type toolBtn struct {
		clk    *widget.Clickable
		ic     *widget.Icon
		active bool
	}
	tools := []toolBtn{{&a.privBtn, icEyeOff, a.set.Privacy}, {&a.setBtn, icSettings, false}}
	if sv != nil {
		tools = []toolBtn{
			{&a.sideBtn, icMonitor, sv.pane == paneMonitor},
			{&a.filesBtn, icFiles, sv.pane == paneFiles},
			{&a.mob.moreBtn, icMoreVert, sv.activeForwards() > 0},
		}
	}
	right := w - gtx.Dp(4) - len(tools)*tool
	for i, t := range tools {
		st := op.Offset(image.Pt(right+i*tool, (h-tool)/2)).Push(gtx.Ops)
		th.iconButton(gtx, t.clk, t.ic, 36, 20, th.Text2, t.active, "")
		st.Pop()
	}
	addW := gtx.Dp(36)
	right -= addW
	at(right, func(gtx layout.Context) layout.Dimensions {
		st := op.Offset(image.Pt(0, (h-addW)/2)).Push(gtx.Ops)
		d := th.iconButton(gtx, &a.addClk, icAdd, 36, 21, th.Text2, false, "")
		st.Pop()
		return d
	})

	// The current page.
	at(x, func(gtx layout.Context) layout.Dimensions {
		pw := right - x - gtx.Dp(4)
		ic, title, secret := icHome, "主页", false
		dot := th.Accent
		if t := a.currentTab(); t != nil {
			ic, title = t.icon(), t.title()
			dot = t.dot(true)
			if s, ok := t.(*sessionView); ok {
				secret = s.sess.Profile.Name == ""
			}
		}
		size := image.Pt(pw, h)
		return a.mob.tabPill.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			r := image.Rect(gtx.Dp(2), gtx.Dp(6), pw-gtx.Dp(2), h-gtx.Dp(6))
			bg := th.Bg3
			if th.Light {
				bg = th.Bg0
			}
			if a.mob.tabPill.Pressed() {
				bg = th.Bg4
			}
			fillRR(gtx.Ops, r, gtx.Dp(7), bg)
			if th.Light {
				strokeRR(gtx.Ops, r, gtx.Dp(7), float32(gtx.Dp(1)), th.Border)
			}
			inner := gtx
			inner.Constraints = layout.Exact(size)
			layout.Inset{Left: 12, Right: 8}.Layout(inner, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if ic != nil {
							d := drawIcon(gtx, ic, 16, th.Text)
							d.Size.X += gtx.Dp(7)
							return d
						}
						s := gtx.Dp(8)
						fillRR(gtx.Ops, image.Rectangle{Max: image.Pt(s, s)}, s/2, dot)
						return layout.Dimensions{Size: image.Pt(s+gtx.Dp(8), s)}
					}),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints.Min.X = 0
						if secret {
							return th.secretTxtW(gtx, title, 14, th.Text, font.Medium)
						}
						return th.txtW(gtx, title, 14, th.Text, font.Medium)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return drawIcon(gtx, icExpand, 18, th.Text2)
					}),
				)
			})
			return layout.Dimensions{Size: size}
		})
	})
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// pageMenu lists the open pages for the compact title bar.
func (a *App) pageMenu() {
	items := []MenuItem{{Label: "主页", Icon: icHome, Checked: a.active == -1, Do: func() { a.activate(-1) }}}
	for i, t := range a.tabs {
		title := t.title()
		if s, ok := t.(*sessionView); ok && s.sess.Profile.Name == "" && a.set.Privacy {
			title = "••••••"
		}
		items = append(items, MenuItem{Label: title, Icon: t.icon(), Checked: a.active == i, Do: func() { a.activate(a.indexOf(t)) }})
		if items[len(items)-1].Icon == nil {
			items[len(items)-1].Icon = icTerminal
		}
	}
	if t := a.currentTab(); t != nil {
		items = append(items, MenuItem{Sep: true})
		items = append(items, t.menu()...)
		items = append(items, MenuItem{Label: "关闭当前页", Icon: icClose, Danger: true, Do: func() { a.closeTab(a.indexOf(t)) }})
	}
	a.MenuAt(image.Pt(a.dp(48), a.dp(titleH)), items...)
}

// togglePane shows p in the compact layout, or the terminal if p already
// shows.
func (sv *sessionView) togglePane(p int) {
	if sv.pane == p {
		p = paneTerminal
	}
	sv.pane = p
	sv.a.refocus = true
	sv.a.host.Invalidate()
}

// ---- Extra keys ---------------------------------------------------------

// extraKey is a key of the bar above the soft keyboard.
type extraKey struct {
	label string
	name  key.Name // a named key, or the character to type
	// sticky keys (Ctrl, Alt) modify the next key instead.
	sticky key.Modifiers
	// repeat keys fire again while held, like arrow keys on a keyboard.
	repeat bool
}

var extraKeyDefs = []extraKey{
	{label: "Esc", name: key.NameEscape},
	{label: "Tab", name: key.NameTab},
	{label: "Ctrl", sticky: key.ModCtrl},
	{label: "Alt", sticky: key.ModAlt},
	{label: "←", name: key.NameLeftArrow, repeat: true},
	{label: "↑", name: key.NameUpArrow, repeat: true},
	{label: "↓", name: key.NameDownArrow, repeat: true},
	{label: "→", name: key.NameRightArrow, repeat: true},
	{label: "Home", name: key.NameHome},
	{label: "End", name: key.NameEnd},
	{label: "PgUp", name: key.NamePageUp, repeat: true},
	{label: "PgDn", name: key.NamePageDown, repeat: true},
	{label: "|", name: "|"},
	{label: "/", name: "/"},
	{label: "-", name: "-"},
	{label: "~", name: "~"},
	{label: "F1", name: key.NameF1},
	{label: "F2", name: key.NameF2},
	{label: "F3", name: key.NameF3},
	{label: "F4", name: key.NameF4},
	{label: "F5", name: key.NameF5},
	{label: "F6", name: key.NameF6},
	{label: "F7", name: key.NameF7},
	{label: "F8", name: key.NameF8},
	{label: "F9", name: key.NameF9},
	{label: "F10", name: key.NameF10},
	{label: "F11", name: key.NameF11},
	{label: "F12", name: key.NameF12},
}

// extraKeys is the row of keys below the terminal in mobile mode.
type extraKeys struct {
	list   layout.List
	btns   []widget.Clickable
	kbd    widget.Clickable
	paste  widget.Clickable
	held   int // index of the repeating key being held, or -1
	heldAt time.Time
}

const extraKeysH = unit.Dp(40)

// Layout draws the bar for sv's terminal.
func (k *extraKeys) Layout(gtx layout.Context, sv *sessionView) layout.Dimensions {
	a := sv.a
	th := a.th
	v := sv.term
	h := gtx.Dp(extraKeysH)
	w := gtx.Constraints.Max.X
	if k.btns == nil {
		k.btns = make([]widget.Clickable, len(extraKeyDefs))
	}
	fill(gtx.Ops, image.Rectangle{Max: image.Pt(w, h)}, th.Bg1)
	fill(gtx.Ops, image.Rect(0, 0, w, max(gtx.Dp(1), 1)), th.Border)

	if k.kbd.Clicked(gtx) {
		v.Focus(gtx)
		gtx.Execute(key.SoftKeyboardCmd{Show: !a.keyboardShown()})
	}
	if k.paste.Clicked(gtx) {
		v.Focus(gtx)
		v.Paste(gtx)
	}
	for i := range extraKeyDefs {
		def := &extraKeyDefs[i]
		for k.btns[i].Clicked(gtx) {
			if def.repeat && k.held == i {
				// Already sent on press and while held.
				continue
			}
			k.press(gtx, v, def)
		}
		if def.repeat {
			// Arrow keys fire on press and then repeat while held.
			switch pressed := k.btns[i].Pressed(); {
			case pressed && k.held != i:
				k.held, k.heldAt = i, gtx.Now
				k.press(gtx, v, def)
				gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(400 * time.Millisecond)})
			case pressed && gtx.Now.Sub(k.heldAt) >= 400*time.Millisecond:
				k.press(gtx, v, def)
				gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(60 * time.Millisecond)})
			case pressed:
				gtx.Execute(op.InvalidateCmd{At: k.heldAt.Add(400 * time.Millisecond)})
			case k.held == i:
				k.held = -1
			}
		}
	}

	// The keyboard and paste buttons stay at the right; the keys scroll.
	side := gtx.Dp(44)
	btn := func(gtx layout.Context, clk *widget.Clickable, ic *widget.Icon, active bool) layout.Dimensions {
		return clk.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			sz := image.Pt(side, h)
			r := image.Rectangle{Max: sz}.Inset(gtx.Dp(4))
			switch {
			case clk.Pressed():
				fillRR(gtx.Ops, r, gtx.Dp(6), th.Bg4)
			case active:
				fillRR(gtx.Ops, r, gtx.Dp(6), alpha(th.Accent, 0x20))
			}
			fg := th.Text2
			if active {
				fg = th.AccentText
			}
			ip := gtx.Dp(20)
			st := op.Offset(image.Pt((sz.X-ip)/2, (sz.Y-ip)/2)).Push(gtx.Ops)
			drawIcon(gtx, ic, 20, fg)
			st.Pop()
			return layout.Dimensions{Size: sz}
		})
	}
	st := op.Offset(image.Pt(w-2*side, 0)).Push(gtx.Ops)
	fill(gtx.Ops, image.Rect(0, gtx.Dp(8), max(gtx.Dp(1), 1), h-gtx.Dp(8)), th.Border)
	btn(gtx, &k.paste, icPaste, false)
	st.Pop()
	st = op.Offset(image.Pt(w-side, 0)).Push(gtx.Ops)
	btn(gtx, &k.kbd, icKeyboard, a.keyboardShown())
	st.Pop()

	lg := gtx
	lg.Constraints = layout.Exact(image.Pt(w-2*side, h))
	cl := clip.Rect{Max: lg.Constraints.Max}.Push(gtx.Ops)
	k.list.Axis = layout.Horizontal
	k.list.Layout(lg, len(extraKeyDefs), func(gtx layout.Context, i int) layout.Dimensions {
		def := &extraKeyDefs[i]
		active := def.sticky != 0 && v.Sticky.Contain(def.sticky)
		return k.btns[i].Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			rec := op.Record(gtx.Ops)
			lgtx := gtx
			lgtx.Constraints.Min = image.Point{}
			ld := th.txtW(lgtx, def.label, 13, th.Text, font.Medium)
			call := rec.Stop()
			sz := image.Pt(max(ld.Size.X+gtx.Dp(20), gtx.Dp(44)), h)
			r := image.Rectangle{Max: sz}.Inset(gtx.Dp(4))
			switch {
			case k.btns[i].Pressed():
				fillRR(gtx.Ops, r, gtx.Dp(6), th.Bg4)
			case active:
				fillRR(gtx.Ops, r, gtx.Dp(6), alpha(th.Accent, 0x30))
				strokeRR(gtx.Ops, r, gtx.Dp(6), float32(gtx.Dp(1)), th.Accent)
			default:
				fillRR(gtx.Ops, r, gtx.Dp(6), th.Bg2)
			}
			st := op.Offset(image.Pt((sz.X-ld.Size.X)/2, (sz.Y-ld.Size.Y)/2)).Push(gtx.Ops)
			call.Add(gtx.Ops)
			st.Pop()
			return layout.Dimensions{Size: sz}
		})
	})
	cl.Pop()
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// press sends one key of the bar to the terminal.
func (k *extraKeys) press(gtx layout.Context, v *TermView, def *extraKey) {
	if def.sticky != 0 {
		v.Sticky ^= def.sticky
		return
	}
	v.Focus(gtx)
	v.TypeKey(gtx, def.name)
}

// ---- Touch in lists -----------------------------------------------------

// touchTap reads touches on a list whose rows react to taps and long
// presses. Drags are left to the list, which scrolls.
type touchTap struct {
	lp    longPress
	idx   int
	start f32.Point
	down  bool
}

// Event feeds a touch event over row idx (-1 between rows) and reports a
// tap: a touch that lifted where it went down, before it became a long
// press.
func (t *touchTap) Event(gtx layout.Context, e pointer.Event, idx int) (tap bool) {
	t.lp.Event(gtx, e)
	switch e.Kind {
	case pointer.Press:
		t.idx, t.start, t.down = idx, e.Position, true
	case pointer.Drag:
		if d := e.Position.Sub(t.start); d.X*d.X+d.Y*d.Y > float32(gtx.Dp(8)*gtx.Dp(8)) {
			t.down = false
		}
	case pointer.Release:
		tap = t.down && !t.lp.done
		t.down = false
	case pointer.Cancel:
		t.down = false
	}
	return tap
}

// Long reports the row of a long press, once per touch.
func (t *touchTap) Long(gtx layout.Context, tag event.Tag) (idx int, ok bool) {
	if t.lp.Fired(gtx, tag) {
		t.down = false
		return t.idx, true
	}
	return 0, false
}
