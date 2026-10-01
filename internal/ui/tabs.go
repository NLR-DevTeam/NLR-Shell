package ui

import (
	"image"
	"image/color"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/widget"

	"nlrshell/internal/sshx"
	"nlrshell/internal/store"
)

// tabChrome holds the title-bar widgets every tab needs.
type tabChrome struct {
	tabClick, tabClose widget.Clickable
	// tabPtr tags the pass-through pointer handler of the tab: a press
	// activates (left), closes (middle) or opens the menu (right), and a
	// left drag reorders.
	tabPtr bool
}

func (c *tabChrome) chrome() *tabChrome { return c }

// tab is one closable tab in the title bar: a session, a remote text file
// or a remote image.
type tab interface {
	Layout(gtx layout.Context) layout.Dimensions
	chrome() *tabChrome
	title() string
	// tipCard is shown when the pointer rests on the tab.
	tipCard() []tipRow
	// icon is drawn before the title; nil draws a status dot instead.
	icon() *widget.Icon
	dot(active bool) color.NRGBA
	// focus gives the tab's main input the keyboard focus.
	focus(gtx layout.Context)
	// closing is asked before the tab is removed. Returning false keeps it
	// open; the tab may then ask the user and call App.removeTab itself.
	closing() bool
	// closed releases the tab's resources after removal.
	closed()
	// menu returns extra context-menu items for the tab.
	menu() []MenuItem
}

// fileTab is implemented by tabs that show a file of a session.
type fileTab interface {
	tab
	owner() *sshx.Session
	remotePath() string
	// disposable reports whether the tab can be dropped without losing work.
	disposable() bool
}

func (a *App) currentTab() tab {
	if a.active >= 0 && a.active < len(a.tabs) {
		return a.tabs[a.active]
	}
	return nil
}

// current returns the active session tab, or nil if another kind of tab or
// the home page is showing.
func (a *App) current() *sessionView {
	sv, _ := a.currentTab().(*sessionView)
	return sv
}

// tabProfile returns the connection the active tab belongs to: the session
// itself, or the session a file tab was opened from. The saved copy is
// preferred because it may hold a password stored after connecting.
func (a *App) tabProfile() (store.Profile, bool) {
	var sess *sshx.Session
	switch t := a.currentTab().(type) {
	case *sessionView:
		sess = t.sess
	case fileTab:
		sess = t.owner()
	default:
		return store.Profile{}, false
	}
	if p, ok := a.st.Profile(sess.Profile.ID); ok {
		return p, true
	}
	return sess.Profile, true
}

func (a *App) sessionViews() []*sessionView {
	var out []*sessionView
	for _, t := range a.tabs {
		if sv, ok := t.(*sessionView); ok {
			out = append(out, sv)
		}
	}
	return out
}

func (a *App) viewOf(sess *sshx.Session) *sessionView {
	for _, sv := range a.sessionViews() {
		if sv.sess == sess {
			return sv
		}
	}
	return nil
}

func (a *App) indexOf(t tab) int {
	for i, x := range a.tabs {
		if x == t {
			return i
		}
	}
	return -1
}

func (a *App) addTab(t tab) {
	a.tabs = append(a.tabs, t)
	a.activate(len(a.tabs) - 1)
}

// closeTab closes tab i if the tab agrees.
func (a *App) closeTab(i int) {
	if i < 0 || i >= len(a.tabs) {
		return
	}
	if t := a.tabs[i]; t.closing() {
		a.removeTab(t)
	}
}

// removeTab removes t unconditionally.
func (a *App) removeTab(t tab) {
	i := a.indexOf(t)
	if i < 0 {
		return
	}
	t.closed()
	a.tabs = append(a.tabs[:i], a.tabs[i+1:]...)
	switch {
	case a.active > i:
		a.activate(a.active - 1)
	case a.active == i:
		a.activate(min(i, len(a.tabs)-1))
	}
	// Files of a closed session cannot be saved any more; drop the tabs
	// that have nothing to lose.
	if sv, ok := t.(*sessionView); ok {
		for j := len(a.tabs) - 1; j >= 0; j-- {
			if ft, ok := a.tabs[j].(fileTab); ok && ft.owner() == sv.sess && ft.disposable() {
				a.removeTab(ft)
			}
		}
	}
}

// findFile returns the open tab showing the given remote file, if any.
func (a *App) findFile(sess *sshx.Session, p string) fileTab {
	for _, t := range a.tabs {
		if ft, ok := t.(fileTab); ok && ft.owner() == sess && ft.remotePath() == p {
			return ft
		}
	}
	return nil
}

// tabDrag follows a tab being pressed and possibly dragged.
type tabDrag struct {
	t      tab
	startX int // pointer x at the press
	grabX  int // pointer x minus the tab's left edge at the press
	moved  bool
}

// tabPress reads the pointer events of a tab. It returns the buttons
// pressed this frame and whether the left button was released.
func tabPress(gtx layout.Context, c *tabChrome) (pressed pointer.Buttons, released bool) {
	for {
		e, ok := gtx.Event(pointer.Filter{Target: &c.tabPtr, Kinds: pointer.Press | pointer.Release | pointer.Cancel})
		if !ok {
			break
		}
		pe, ok := e.(pointer.Event)
		if !ok {
			continue
		}
		switch pe.Kind {
		case pointer.Press:
			pressed |= pe.Buttons
		case pointer.Release, pointer.Cancel:
			released = true
		}
	}
	return pressed, released
}

// addTabPointer registers the tab's handler over r. It is pass-through so
// the close button and hover tracking below it keep working.
func addTabPointer(gtx layout.Context, c *tabChrome, r image.Rectangle) {
	area := clip.Rect(r).Push(gtx.Ops)
	pass := pointer.PassOp{}.Push(gtx.Ops)
	event.Op(gtx.Ops, &c.tabPtr)
	pass.Pop()
	area.Pop()
}

// moveTab moves t to index to, keeping the active tab active.
func (a *App) moveTab(t tab, to int) {
	from := a.indexOf(t)
	if from < 0 || to < 0 || to >= len(a.tabs) || from == to {
		return
	}
	activeTab := a.currentTab()
	a.tabs = append(a.tabs[:from], a.tabs[from+1:]...)
	a.tabs = append(a.tabs[:to], append([]tab{t}, a.tabs[to:]...)...)
	if activeTab != nil {
		a.active = a.indexOf(activeTab)
	}
}
