package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/io/pointer"

	"nlrshell/internal/sshx"
)

// tap touches (x, y) briefly, as a finger does.
func (d *driver) tap(x, y float32) {
	p := f32.Pt(x, y)
	d.router.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Touch, Buttons: pointer.ButtonPrimary, Position: p})
	d.frame()
	d.router.Queue(pointer.Event{Kind: pointer.Release, Source: pointer.Touch, Position: p})
	d.frame()
	d.frame()
}

// longPress holds a finger on (x, y) past the long press delay, then
// lifts it.
func (d *driver) longPress(x, y float32) {
	p := f32.Pt(x, y)
	d.router.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Touch, Buttons: pointer.ButtonPrimary, Position: p})
	d.run(600 * time.Millisecond)
	d.router.Queue(pointer.Event{Kind: pointer.Release, Source: pointer.Touch, Position: p})
	d.frame()
	d.frame()
}

// swipe drags a finger from (x, y0) to (x, y1), rests there past the long
// press delay and lifts it, as when scrolling a list slowly.
func (d *driver) swipe(x, y0, y1 float32) {
	d.router.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Touch, Buttons: pointer.ButtonPrimary, Position: f32.Pt(x, y0)})
	d.frame()
	// A finger starts slowly: the list takes the touch over for scrolling
	// after a few pixels, before the long press would give up on its own.
	dir := float32(1)
	if y1 < y0 {
		dir = -1
	}
	var ys []float32
	for i := 1; i <= 6; i++ {
		ys = append(ys, y0+dir*d.px(float32(i)))
	}
	for i := 1; i <= 8; i++ {
		ys = append(ys, y0+dir*d.px(6)+(y1-y0-dir*d.px(6))*float32(i)/8)
	}
	for _, y := range ys {
		d.router.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Touch, Buttons: pointer.ButtonPrimary, Position: f32.Pt(x, y)})
		d.frame()
	}
	d.run(600 * time.Millisecond)
	d.router.Queue(pointer.Event{Kind: pointer.Release, Source: pointer.Touch, Position: f32.Pt(x, y1)})
	d.frame()
	d.frame()
}

// mobileShots runs the phone scenario: the app in mobile mode in a
// portrait window (run with -mobile -w 400 -h 860).
func (d *driver) mobileShots(want func(string) bool) {
	a := d.a
	w := float32(d.size.X)
	d.run(200 * time.Millisecond)
	if want("m-home") {
		d.shot("m-home")
	}
	if want("m-appmenu") {
		d.tap(d.px(23), d.px(20))
		d.shot("m-appmenu")
		d.key(key.NameBack, 0)
	}

	if want("m-profile") {
		// The new connection form, from "+" in the title bar. Run with a
		// short window (-h 460) for a phone with the keyboard up: the form
		// scrolls instead of running under its buttons.
		d.tap(w-d.px(94), d.px(20))
		d.run(200 * time.Millisecond)
		d.shot("m-profile")
		// Focus the last field: the form scrolls it into view.
		a.FocusProfileField("group")
		d.run(200 * time.Millisecond)
		d.shot("m-profile-scrolled")
		d.key(key.NameBack, 0)
	}
	// Connect by tapping the first card.
	d.tap(d.px(200), d.px(270))
	d.until("host key prompt", func() bool { return a.DialogCount() == 1 })
	if want("m-hostkey") {
		d.shot("m-hostkey")
	}
	d.key(key.NameReturn, 0)
	d.until("password prompt", func() bool { return a.DialogCount() == 1 })
	if want("m-password") {
		d.shot("m-password")
	}
	d.typeText("demo\n")
	sess := func() *sshx.Session { return a.Sessions()[0] }
	d.until("connect", func() bool { return len(a.Sessions()) == 1 && sess().State() == sshx.StateConnected })
	d.until("monitor", func() bool { m := sess().Monitor(); return m.Ready && len(m.CPU) >= 2 })
	d.until("files", func() bool { return a.FilesPath() != "" })
	d.typeText("ls -l\ncolors\ncd projects/api\nls\n")
	d.run(2 * time.Second)
	if want("m-session") {
		d.shot("m-session")
	}
	if want("m-pages") {
		d.tap(d.px(150), d.px(20))
		d.shot("m-pages")
		d.key(key.NameBack, 0)
	}
	if want("m-more") {
		d.tap(w-d.px(22), d.px(20))
		d.shot("m-more")
		d.key(key.NameBack, 0)
	}
	if want("m-term-select") {
		// A long press selects a word and opens the menu.
		d.longPress(d.px(60), d.px(70))
		d.shot("m-term-select")
		d.key(key.NameBack, 0)
	}
	if want("m-monitor") {
		d.tap(w-d.px(94), d.px(20))
		if a.Pane() != 1 {
			fail("monitor button: pane %d", a.Pane())
		}
		d.run(300 * time.Millisecond)
		d.shot("m-monitor")
	}
	if want("m-files") {
		d.tap(w-d.px(58), d.px(20))
		if a.Pane() != 2 {
			fail("files button: pane %d", a.Pane())
		}
		d.run(300 * time.Millisecond)
		d.shot("m-files")
		d.swipe(d.px(150), d.px(180), d.px(400))
		if a.MenuOpen() {
			d.shot("m-files-swipe")
			fail("a swipe in the files opened a menu")
		}
		if want("m-file-menu") {
			d.longPress(d.px(150), d.px(160))
			d.shot("m-file-menu")
			d.key(key.NameBack, 0)
		}
		d.key(key.NameBack, 0)
		if a.Pane() != 0 {
			fail("back from files: pane %d", a.Pane())
		}
	}
	if want("m-settings") {
		a.OpenSettings()
		d.run(200 * time.Millisecond)
		d.shot("m-settings")
		// The scroll bar shows while the list moves and hides two
		// seconds later.
		d.swipe(d.px(200), d.px(600), d.px(300))
		d.shot("m-settings-scrolled")
		d.run(2200 * time.Millisecond)
		d.shot("m-settings-idle")
		d.key(key.NameBack, 0)
	}
	if want("m-tunnels") {
		a.OpenTunnels()
		d.run(200 * time.Millisecond)
		d.shot("m-tunnels")
		d.key(key.NameBack, 0)
	}
	if want("m-picker") {
		// Enough files that the list scrolls.
		if home, err := os.UserHomeDir(); err == nil {
			for i := range 40 {
				os.WriteFile(filepath.Join(home, fmt.Sprintf("file-%02d.txt", i)), nil, 0o644)
			}
		}
		a.OpenPicker("选择要上传的文件", false, func([]string) {})
		d.run(300 * time.Millisecond)
		d.shot("m-picker")
		// A swipe over the rows scrolls; it is no long press, which on a
		// folder (the rows at the top) would open its menu.
		d.swipe(d.px(200), d.px(150), d.px(300))
		if a.MenuOpen() {
			fail("a swipe in the picker opened a menu")
		}
		d.key(key.NameBack, 0)
	}
	if want("m-reconnect") {
		// The connection drops and comes back: the old output stays above
		// the new session's.
		sess().Close()
		d.until("closed", func() bool { return sess().State() == sshx.StateClosed })
		d.shot("m-disconnected")
		sess().Reconnect()
		d.until("reconnect", func() bool { return sess().State() == sshx.StateConnected })
		d.run(time.Second)
		d.typeText("ls\n")
		d.run(time.Second)
		d.shot("m-reconnect")
	}
	if want("m-light") {
		a.SetLook("light", "green")
		d.run(200 * time.Millisecond)
		d.shot("m-light")
		a.SetLook("dark", "green")
	}
	// Back leaves the session for the home page.
	d.key(key.NameBack, 0)
	if a.TabCount() != 1 || a.Pane() != -1 {
		fail("back from session: tabs %d, pane %d", a.TabCount(), a.Pane())
	}
	if want("m-home-session") {
		d.shot("m-home-session")
	}
}
