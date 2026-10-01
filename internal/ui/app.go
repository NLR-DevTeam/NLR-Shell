package ui

import (
	"image"
	"image/color"
	"sync"
	"sync/atomic"
	"time"

	"gioui.org/app"
	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"nlrshell/internal/store"
)

// Host is the window the app is displayed in.
type Host interface {
	// Invalidate requests a new frame; safe to call from any goroutine.
	Invalidate()
	// Perform executes a window action such as close or move.
	Perform(system.Action)
	// SetWindowMode changes the window state (windowed, maximized or
	// minimized). The titlebar buttons use it because the Gio backends
	// implement window states as options; their Perform only handles the
	// pointer gestures and closing.
	SetWindowMode(app.WindowMode)
	// HWND returns the native window handle, or 0 if there is none.
	HWND() uintptr
}

// App is the root of the user interface.
type App struct {
	host Host
	th   *Theme
	st   *store.Store
	set  store.Settings

	home   *homeView
	tabs   []tab
	active int // index into tabs, or -1 for the home page
	ext    external
	bg     background
	// activeID mirrors the active session id for use from other goroutines.
	activeID atomic.Value

	postMu sync.Mutex
	posted []func()
	// withGtx holds UI-goroutine actions that need a layout context, such as
	// clipboard commands chosen from a menu.
	withGtx []func(gtx layout.Context)

	mouse     image.Point
	size      image.Point
	pxPerDp   float32
	maximized bool
	decorated bool
	// unattended is set for -connect: the connection must not open dialogs,
	// so new host keys are trusted, changed ones refused, and missing
	// credentials fail the connection.
	unattended bool
	refocus    bool
	setDirty   time.Time

	menu    *menu
	dialogs []Dialog
	toasts  []toast

	logoClk, homeClk, addClk          widget.Clickable
	homeChrome                        tabChrome
	drag                              tabDrag
	winBtns                           [3]widget.Clickable
	sideBtn, filesBtn, tunBtn, setBtn widget.Clickable
	homeRC                            rightClick
}

// New creates the app.
func New(host Host, st *store.Store) *App {
	set := st.Settings()
	a := &App{host: host, st: st, set: set, th: NewTheme(set.FontFamily, set.CJKFont), active: -1}
	a.activeID.Store("")
	a.th.editMenu = a.editMenu
	a.applyTheme()
	a.loadBackground()
	go a.watchSystemTheme()
	a.home = newHomeView(a)
	return a
}

// Theme returns the app theme.
func (a *App) Theme() *Theme { return a.th }

// SetMaximized tells the app whether the window is maximized.
func (a *App) SetMaximized(m bool) { a.maximized = m }

// SetDecorated tells the app whether the platform draws the window
// decorations. Windows requests a borderless window, but X11 and Wayland
// always decorate, so the in-app window buttons are dropped there.
func (a *App) SetDecorated(on bool) { a.decorated = on }

// SetUnattended makes connection prompts go away: new host keys are trusted
// without asking, changed ones are refused, and anything else that would
// need input fails instead of opening a dialog. The -connect option sets
// it.
func (a *App) SetUnattended(on bool) { a.unattended = on }

// RememberWindow records the window size (in dp) and state so the next
// launch restores it. It must be called from the UI goroutine.
func (a *App) RememberWindow(w, h int, maximized bool) {
	if maximized {
		if !a.set.Maximized {
			a.updateSettings(func(s *store.Settings) { s.Maximized = true })
		}
		return
	}
	if w < 760 || h < 480 || (w == a.set.WindowWidth && h == a.set.WindowHeight && !a.set.Maximized) {
		return
	}
	a.updateSettings(func(s *store.Settings) { s.WindowWidth, s.WindowHeight, s.Maximized = w, h, false })
}

// Post schedules f to run on the UI goroutine before the next frame.
func (a *App) Post(f func()) {
	a.postMu.Lock()
	a.posted = append(a.posted, f)
	a.postMu.Unlock()
	a.host.Invalidate()
}

func (a *App) runPosted() {
	a.postMu.Lock()
	fs := a.posted
	a.posted = nil
	a.postMu.Unlock()
	for _, f := range fs {
		f()
	}
}

// updateSettings changes settings and persists them shortly after.
func (a *App) updateSettings(f func(*store.Settings)) {
	f(&a.set)
	if a.setDirty.IsZero() {
		a.setDirty = time.Now()
	}
}

// Flush writes pending settings to disk.
func (a *App) Flush() {
	if !a.setDirty.IsZero() {
		a.setDirty = time.Time{}
		set := a.set
		a.st.UpdateSettings(func(s *store.Settings) { *s = set })
	}
}

// Shutdown closes all sessions and saves state.
func (a *App) Shutdown() {
	a.Flush()
	for _, t := range a.sessionViews() {
		t.sess.Close()
	}
	a.ext.cleanup()
}

// DropFiles handles files dragged onto the window from the file manager by
// uploading them to the directory shown in the active tab's file panel. It
// is safe to call from any goroutine. Only Windows installs a drop handler;
// Gio's X11 and Wayland backends do not deliver drag-and-drop events.
func (a *App) DropFiles(paths []string) {
	a.Post(func() {
		if len(a.dialogs) > 0 {
			return
		}
		sv := a.current()
		if sv == nil {
			a.Toast(toastInfo, "请先打开连接")
			return
		}
		if !a.set.ShowFiles {
			a.updateSettings(func(s *store.Settings) { s.ShowFiles = true })
		}
		sv.files.upload(paths)
	})
}

// ---- Sessions ---------------------------------------------------------

// Connect opens a new tab for p.
func (a *App) Connect(p store.Profile) {
	sv := newSessionView(a, p)
	a.addTab(sv)
	sv.sess.Start()
}

func (a *App) activate(i int) {
	if i >= len(a.tabs) {
		i = len(a.tabs) - 1
	}
	a.active = i
	a.activeID.Store("")
	if sv := a.current(); sv != nil {
		a.activeID.Store(sv.sess.ID)
	}
	if i < 0 {
		a.home.wantFocus = true
	}
	a.refocus = true
	a.closeMenu()
	a.host.Invalidate()
}

func (a *App) setFontSize(size float32) {
	size = min(max(size, 8), 40)
	a.updateSettings(func(s *store.Settings) { s.FontSize = size })
}

// ---- Layout -----------------------------------------------------------

// Layout draws the whole window.
func (a *App) Layout(gtx layout.Context) layout.Dimensions {
	a.size = gtx.Constraints.Max
	a.pxPerDp = gtx.Metric.PxPerDp
	a.runPosted()
	for _, fn := range a.withGtx {
		fn(gtx)
	}
	a.withGtx = nil
	if !a.setDirty.IsZero() {
		if gtx.Now.Sub(a.setDirty) > time.Second {
			a.Flush()
		} else {
			gtx.Execute(op.InvalidateCmd{At: a.setDirty.Add(1100 * time.Millisecond)})
		}
	}

	// Track the pointer in window coordinates for menus and splitters.
	for {
		e, ok := gtx.Event(pointer.Filter{Target: a, Kinds: pointer.Move | pointer.Drag | pointer.Press | pointer.Release})
		if !ok {
			break
		}
		if pe, ok := e.(pointer.Event); ok {
			a.mouse = image.Pt(int(pe.Position.X), int(pe.Position.Y))
		}
	}
	a.shortcuts(gtx)

	root := clip.Rect{Max: a.size}.Push(gtx.Ops)
	event.Op(gtx.Ops, a)
	paint.Fill(gtx.Ops, a.th.Bg1)

	layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.layoutTitlebar),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			if t := a.currentTab(); t != nil {
				return t.Layout(gtx)
			}
			return a.home.Layout(gtx)
		}),
	)
	if a.refocus && a.menu == nil && len(a.dialogs) == 0 {
		a.refocus = false
		if t := a.currentTab(); t != nil {
			t.focus(gtx)
		} else {
			a.home.search.Focus(gtx)
		}
	}

	a.layoutBackground(gtx)
	a.layoutDialogs(gtx)
	a.layoutMenu(gtx)
	a.layoutToasts(gtx)
	a.layoutTooltip(gtx)
	if !a.maximized {
		strokeRR(gtx.Ops, image.Rectangle{Max: a.size}, 0, float32(max(gtx.Dp(1), 1)), a.th.Border)
	}
	root.Pop()
	return layout.Dimensions{Size: a.size}
}

func (a *App) shortcuts(gtx layout.Context) {
	var filters []event.Filter
	modal := len(a.dialogs) > 0
	if a.menu != nil || modal {
		filters = append(filters, key.Filter{Name: key.NameEscape})
	}
	if modal {
		top := a.dialogs[len(a.dialogs)-1]
		if m, ok := top.(multiline); !ok || !m.Multiline() {
			filters = append(filters, key.Filter{Name: key.NameReturn}, key.Filter{Name: key.NameEnter})
		}
	} else {
		cs := key.ModCtrl | key.ModShift
		for _, n := range []key.Name{"T", "W", "D", "B", "E", "N"} {
			filters = append(filters, key.Filter{Name: n, Required: cs})
		}
		// Ctrl+W closes file tabs. In a terminal it belongs to the shell
		// (delete word), so session tabs keep Ctrl+Shift+W only.
		if t := a.currentTab(); t != nil && a.current() == nil {
			filters = append(filters, key.Filter{Name: "W", Required: key.ModCtrl})
		}
		filters = append(filters, key.Filter{Name: key.NameTab, Required: key.ModCtrl, Optional: key.ModShift})
		for _, n := range []key.Name{"1", "2", "3", "4", "5", "6", "7", "8", "9"} {
			filters = append(filters, key.Filter{Name: n, Required: key.ModAlt})
		}
	}
	if len(filters) == 0 {
		return
	}
	for {
		e, ok := gtx.Event(filters...)
		if !ok {
			break
		}
		ke, ok := e.(key.Event)
		if !ok || ke.State != key.Press {
			continue
		}
		switch {
		case ke.Name == key.NameEscape:
			if a.menu != nil {
				a.closeMenu()
			} else if n := len(a.dialogs); n > 0 {
				a.dialogs[n-1].Cancel(a)
			}
		case ke.Name == key.NameReturn || ke.Name == key.NameEnter:
			if n := len(a.dialogs); n > 0 {
				a.dialogs[n-1].Submit(a)
			}
		case ke.Name == key.NameTab:
			n := len(a.tabs) + 1
			cur := a.active + 1 // 0 is home
			if ke.Modifiers.Contain(key.ModShift) {
				cur = (cur + n - 1) % n
			} else {
				cur = (cur + 1) % n
			}
			a.activate(cur - 1)
		case ke.Modifiers.Contain(key.ModAlt):
			i := int(ke.Name[0] - '1')
			if i == 0 {
				a.activate(-1)
			} else if i-1 < len(a.tabs) {
				a.activate(i - 1)
			}
		case ke.Name == "T":
			a.activate(-1)
		case ke.Name == "N":
			a.home.editProfile(store.Profile{Port: 22, Auth: store.AuthPassword}, true)
		case ke.Name == "W":
			if a.active >= 0 {
				a.closeTab(a.active)
			}
		case ke.Name == "D":
			if sv := a.current(); sv != nil {
				a.Connect(sv.sess.Profile)
			}
		case ke.Name == "B":
			a.updateSettings(func(s *store.Settings) { s.ShowSidebar = !s.ShowSidebar })
		case ke.Name == "E":
			a.updateSettings(func(s *store.Settings) { s.ShowFiles = !s.ShowFiles })
		}
	}
}

const titleH = unit.Dp(40)

func (a *App) layoutTitlebar(gtx layout.Context) layout.Dimensions {
	th := a.th
	h := gtx.Dp(titleH)
	w := gtx.Constraints.Max.X
	bar := image.Rectangle{Max: image.Pt(w, h)}

	// The whole bar drags the window; controls drawn later sit on top.
	drag := clip.Rect(bar).Push(gtx.Ops)
	system.ActionInputOp(system.ActionMove).Add(gtx.Ops)
	drag.Pop()
	fill(gtx.Ops, image.Rect(0, h-max(gtx.Dp(1), 1), w, h), th.Border)

	// Clicks.
	if a.logoClk.Clicked(gtx) {
		a.appMenu()
	}
	if a.homeClk.Clicked(gtx) {
		a.activate(-1)
	}
	if a.addClk.Clicked(gtx) {
		// Inside a server's tab "+" opens another session on that server;
		// on the home page it creates a new connection.
		if p, ok := a.tabProfile(); ok {
			a.Connect(p)
		} else {
			a.home.editProfile(store.Profile{Port: 22, Auth: store.AuthPassword}, true)
		}
	}
	if a.winBtns[0].Clicked(gtx) {
		a.host.SetWindowMode(app.Minimized)
	}
	if a.winBtns[1].Clicked(gtx) {
		if a.maximized {
			a.host.SetWindowMode(app.Windowed)
		} else {
			a.host.SetWindowMode(app.Maximized)
		}
	}
	if a.winBtns[2].Clicked(gtx) {
		a.host.Perform(system.ActionClose)
	}
	if a.sideBtn.Clicked(gtx) {
		a.updateSettings(func(s *store.Settings) { s.ShowSidebar = !s.ShowSidebar })
	}
	if a.filesBtn.Clicked(gtx) {
		a.updateSettings(func(s *store.Settings) { s.ShowFiles = !s.ShowFiles })
	}
	if a.tunBtn.Clicked(gtx) {
		if sv := a.current(); sv != nil {
			a.Open(newTunnelDialog(sv))
		}
	}
	if a.setBtn.Clicked(gtx) {
		a.Open(newSettingsDialog(a))
	}
	// Tabs switch on press rather than on release, and a left drag
	// reorders them.
	if b, _ := tabPress(gtx, &a.homeChrome); b.Contain(pointer.ButtonPrimary) {
		a.activate(-1)
	}
	for _, t := range append([]tab(nil), a.tabs...) {
		c := t.chrome()
		if c.tabClose.Clicked(gtx) {
			a.closeTab(a.indexOf(t))
			continue
		}
		pressed, released := tabPress(gtx, c)
		switch {
		case pressed.Contain(pointer.ButtonPrimary) && !c.tabClose.Hovered():
			a.activate(a.indexOf(t))
			a.drag = tabDrag{t: t, startX: a.mouse.X}
		case pressed.Contain(pointer.ButtonTertiary):
			// Middle click closes the tab, right click opens its menu.
			a.closeTab(a.indexOf(t))
			continue
		case pressed.Contain(pointer.ButtonSecondary):
			a.tabMenu(t)
		}
		if released && a.drag.t == t {
			a.drag = tabDrag{}
		}
	}
	if a.drag.t != nil && a.indexOf(a.drag.t) < 0 {
		a.drag = tabDrag{}
	}

	at := func(x int, wd layout.Widget) int {
		st := op.Offset(image.Pt(x, 0)).Push(gtx.Ops)
		d := wd(gtx)
		st.Pop()
		return x + d.Size.X
	}

	// Left: logo.
	x := at(0, func(gtx layout.Context) layout.Dimensions {
		sz := image.Pt(gtx.Dp(46), h)
		return a.logoClk.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			if a.logoClk.Hovered() {
				fillRR(gtx.Ops, image.Rectangle{Max: sz}.Inset(gtx.Dp(5)), gtx.Dp(6), th.Bg3)
			}
			ls := gtx.Dp(24)
			st := op.Offset(image.Pt((sz.X-ls)/2, (sz.Y-ls)/2)).Push(gtx.Ops)
			drawLogo(gtx, ls, th.Text, th.Accent)
			st.Pop()
			return layout.Dimensions{Size: sz}
		})
	})

	// Right: window buttons and tools; measured first so tabs know their
	// room. The buttons are only drawn when the platform does not provide
	// window decorations of its own (Linux/X11 and Wayland always do).
	btnW := gtx.Dp(46)
	right := w
	if !a.decorated {
		right -= 3 * btnW
		for i := range 3 {
			at(right+i*btnW, func(gtx layout.Context) layout.Dimensions {
				sz := image.Pt(btnW, h)
				return a.winBtns[i].Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					fg := th.Text2
					if a.winBtns[i].Hovered() {
						c := th.Bg3
						fg = th.Text
						if i == 2 {
							c, fg = rgb(0xe5484d), color.NRGBA{R: 255, G: 255, B: 255, A: 255}
						}
						fill(gtx.Ops, image.Rectangle{Max: sz}, c)
					}
					kind := []int{0, 1, 3}[i]
					if i == 1 && a.maximized {
						kind = 2
					}
					drawWinGlyph(gtx.Ops, kind, image.Pt(sz.X/2, sz.Y/2), float32(gtx.Dp(10)), fg, float32(gtx.Dp(1)))
					return layout.Dimensions{Size: sz}
				})
			})
		}
	}
	tool := gtx.Dp(32)
	type toolBtn struct {
		clk    *widget.Clickable
		ic     *widget.Icon
		active bool
		title  string
	}
	tools := []toolBtn{{&a.setBtn, icSettings, false, "设置"}}
	if sv := a.current(); sv != nil {
		tools = []toolBtn{
			{&a.sideBtn, icMonitor, a.set.ShowSidebar, "监控"},
			{&a.filesBtn, icFiles, a.set.ShowFiles, "文件"},
			{&a.tunBtn, icTunnel, sv.activeForwards() > 0, "端口转发"},
			{&a.setBtn, icSettings, false, "设置"},
		}
	}
	right -= gtx.Dp(8) + len(tools)*tool
	for i, t := range tools {
		t := t
		st := op.Offset(image.Pt(right+i*tool, (h-gtx.Dp(28))/2)).Push(gtx.Ops)
		th.iconButton(gtx, t.clk, t.ic, 28, 17, th.Text2, t.active, t.title)
		st.Pop()
	}

	// Tabs.
	x = at(x, func(gtx layout.Context) layout.Dimensions {
		d := a.layoutTab(gtx, &a.homeClk, nil, icHome, "主页", color.NRGBA{}, a.active == -1, gtx.Dp(84), h)
		addTabPointer(gtx, &a.homeChrome, image.Rectangle{Max: d.Size})
		return d
	})
	addW := gtx.Dp(34)
	room := right - x - addW - gtx.Dp(70)
	tabW := gtx.Dp(190)
	n := len(a.tabs)
	if n > 0 {
		tabW = min(tabW, max(room/n, gtx.Dp(64)))
	}
	tabsX := x
	// A pressed tab starts moving once the pointer has travelled a little;
	// it then follows the pointer and the others make room for it.
	dragX := -1
	if d := &a.drag; d.t != nil && n > 1 {
		if !d.moved && abs(a.mouse.X-d.startX) > gtx.Dp(6) {
			d.moved = true
			d.grabX = d.startX - (tabsX + a.indexOf(d.t)*tabW)
		}
		if d.moved {
			dragX = min(max(a.mouse.X-d.grabX, tabsX), tabsX+(n-1)*tabW)
			a.moveTab(d.t, min(max((dragX-tabsX+tabW/2)/tabW, 0), n-1))
		}
	}
	drawTab := func(i int, t tab, at int) {
		st := op.Offset(image.Pt(at, 0)).Push(gtx.Ops)
		c := t.chrome()
		if c.tabClick.Hovered() && !c.tabClose.Hovered() && dragX < 0 {
			th.tip.hoverCard(gtx, c, t.tipCard())
		}
		d := a.layoutTab(gtx, &c.tabClick, &c.tabClose, t.icon(), t.title(), t.dot(a.active == i), a.active == i, tabW, h)
		addTabPointer(gtx, c, image.Rectangle{Max: d.Size})
		st.Pop()
	}
	for i, t := range a.tabs {
		if dragX >= 0 && t == a.drag.t {
			continue
		}
		drawTab(i, t, tabsX+i*tabW)
	}
	if dragX >= 0 {
		drawTab(a.indexOf(a.drag.t), a.drag.t, dragX)
		gtx.Execute(op.InvalidateCmd{})
	}
	x = tabsX + n*tabW
	at(x+gtx.Dp(4), func(gtx layout.Context) layout.Dimensions {
		st := op.Offset(image.Pt(0, (h-gtx.Dp(28))/2)).Push(gtx.Ops)
		tip := "新建连接"
		if _, ok := a.tabProfile(); ok {
			tip = "新建会话"
		}
		d := th.iconButton(gtx, &a.addClk, icAdd, 28, 18, th.Text2, false, tip)
		st.Pop()
		return d
	})
	return layout.Dimensions{Size: bar.Max}
}

// layoutTab draws one tab. closeClk may be nil for tabs that cannot close.
func (a *App) layoutTab(gtx layout.Context, clk, closeClk *widget.Clickable, ic *widget.Icon, title string, dot color.NRGBA, active bool, w, h int) layout.Dimensions {
	th := a.th
	size := image.Pt(w, h)
	// Tabs are pills that stop short of the bar's bottom edge: a tab that
	// runs into the content below would have to match the color of
	// whatever is there (sidebar, terminal, editor), which never fits all.
	margin := gtx.Dp(6)
	r := image.Rect(gtx.Dp(2), margin, w-gtx.Dp(2), h-margin)
	activeBg, hoverBg := th.Bg3, mix(th.Bg1, th.Bg3, 0.55)
	if th.Light {
		activeBg, hoverBg = th.Bg0, mix(th.Bg1, th.Bg3, 0.6)
	}
	// The close button is laid out after the tab so that it wins the click.
	d := clk.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		fg := th.Text2
		switch {
		case active:
			fg = th.Text
			fillRR(gtx.Ops, r, gtx.Dp(7), activeBg)
			if th.Light {
				strokeRR(gtx.Ops, r, gtx.Dp(7), float32(gtx.Dp(1)), th.Border)
			}
		case clk.Hovered():
			fillRR(gtx.Ops, r, gtx.Dp(7), hoverBg)
		}
		top := 0
		inner := gtx
		inner.Constraints = layout.Exact(image.Pt(w, h))
		st := op.Offset(image.Pt(0, top)).Push(gtx.Ops)
		closeRoom := unit.Dp(10)
		if closeClk != nil {
			closeRoom = 30
		}
		layout.Inset{Left: 12, Right: closeRoom}.Layout(inner, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if ic != nil {
						d := drawIcon(gtx, ic, 16, fg)
						d.Size.X += gtx.Dp(7)
						return d
					}
					s := gtx.Dp(8)
					paint.FillShape(gtx.Ops, dot, clip.Ellipse{Max: image.Pt(s, s)}.Op(gtx.Ops))
					return layout.Dimensions{Size: image.Pt(s+gtx.Dp(8), s)}
				}),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = 0
					wt := font.Normal
					if active {
						wt = font.Medium
					}
					return th.txtW(gtx, title, 13, fg, wt)
				}),
			)
		})
		st.Pop()
		return layout.Dimensions{Size: size}
	})
	if closeClk != nil {
		cs := gtx.Dp(22)
		st := op.Offset(image.Pt(w-cs-gtx.Dp(8), (h-cs)/2)).Push(gtx.Ops)
		if active || clk.Hovered() || closeClk.Hovered() {
			th.iconButton(gtx, closeClk, icClose, 22, 13, th.Text2, false, "关闭")
		}
		st.Pop()
	}
	return d
}

func (a *App) tabMenu(t tab) {
	items := t.menu()
	if len(items) > 0 {
		items = append(items, MenuItem{Sep: true})
	}
	items = append(items,
		MenuItem{Label: "关闭", Icon: icClose, Hint: "Ctrl+Shift+W", Do: func() { a.closeTab(a.indexOf(t)) }},
		MenuItem{Label: "关闭其他", Disabled: len(a.tabs) < 2, Do: func() {
			for _, o := range append([]tab(nil), a.tabs...) {
				if o != t {
					a.closeTab(a.indexOf(o))
				}
			}
			a.activate(a.indexOf(t))
		}},
	)
	a.Menu(items...)
}

// layoutTooltip draws the title of the control the pointer rests on.
func (a *App) layoutTooltip(gtx layout.Context) {
	th := a.th
	t := th.tip
	if !t.seen || a.menu != nil {
		t.key, t.seen = nil, false
		return
	}
	t.seen = false
	const delay = 450 * time.Millisecond
	if dt := gtx.Now.Sub(t.since); dt < delay {
		gtx.Execute(op.InvalidateCmd{At: t.since.Add(delay)})
		return
	}
	if !t.shown {
		t.shown, t.pos = true, a.mouse
	}
	rec := op.Record(gtx.Ops)
	cg := gtx
	cg.Constraints = layout.Constraints{Max: image.Pt(gtx.Dp(520), gtx.Dp(80))}
	var d layout.Dimensions
	if len(t.rows) > 0 {
		// A card: one icon and value per line.
		cg.Constraints.Max.Y = gtx.Dp(200)
		d = layout.Inset{Left: 10, Right: 12, Top: 8, Bottom: 8}.Layout(cg, func(gtx layout.Context) layout.Dimensions {
			children := make([]layout.FlexChild, 0, len(t.rows)*2)
			for i, r := range t.rows {
				r := r
				if i > 0 {
					children = append(children, vspace(6))
				}
				children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions { return drawIcon(gtx, r.icon, 15, th.Text2) }),
						hspace(8),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.txt(gtx, r.text, 12, th.Text) }),
					)
				}))
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
		})
	} else {
		d = layout.Inset{Left: 8, Right: 8, Top: 4, Bottom: 5}.Layout(cg, func(gtx layout.Context) layout.Dimensions {
			return th.txt(gtx, t.text, 12, th.Text)
		})
	}
	call := rec.Stop()
	pos := t.pos.Add(image.Pt(gtx.Dp(10), gtx.Dp(20)))
	if pos.X+d.Size.X > a.size.X-4 {
		pos.X = max(a.size.X-d.Size.X-4, 4)
	}
	if pos.Y+d.Size.Y > a.size.Y-4 {
		pos.Y = max(t.pos.Y-d.Size.Y-gtx.Dp(8), 4)
	}
	st := op.Offset(pos).Push(gtx.Ops)
	r := image.Rectangle{Max: d.Size}
	fillRR(gtx.Ops, r, gtx.Dp(5), th.Bg3)
	strokeRR(gtx.Ops, r, gtx.Dp(5), float32(gtx.Dp(1)), th.BorderHi)
	call.Add(gtx.Ops)
	st.Pop()
}

func (a *App) appMenu() {
	a.MenuAt(image.Pt(a.dp(4), a.dp(titleH)),
		MenuItem{Label: "新建连接", Icon: icAdd, Hint: "Ctrl+Shift+N", Do: func() {
			a.home.editProfile(store.Profile{Port: 22, Auth: store.AuthPassword}, true)
		}},
		MenuItem{Label: "主页", Icon: icHome, Hint: "Ctrl+Shift+T", Do: func() { a.activate(-1) }},
		MenuItem{Sep: true},
		MenuItem{Label: "设置", Icon: icSettings, Do: func() { a.Open(newSettingsDialog(a)) }},
		MenuItem{Label: "关于", Icon: icInfo, Do: func() { a.Open(&aboutDialog{}) }},
		MenuItem{Sep: true},
		MenuItem{Label: "退出", Icon: icPower, Do: func() { a.host.Perform(system.ActionClose) }},
	)
}

// Version is the application version shown in the about box.
var Version = "1.1.0"

// dp converts dp to pixels using the metric of the last frame.
func (a *App) dp(v unit.Dp) int {
	s := a.pxPerDp
	if s == 0 {
		s = 1
	}
	return int(float32(v)*s + 0.5)
}
