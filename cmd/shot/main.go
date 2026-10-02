// Command shot drives the NLR Shell UI off-screen against the in-process
// test SSH server and writes PNG screenshots. It is a development tool for
// checking layout, drawing and input handling without opening a window.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gioui.org/app"
	"gioui.org/f32"
	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/system"
	"gioui.org/io/transfer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"nlrshell/internal/sshx"
	"nlrshell/internal/sshx/sshtest"
	"nlrshell/internal/store"
	"nlrshell/internal/ui"
)

// host records the window mode the titlebar buttons ask for and any close
// request; the shot renderer has no window system to apply them to.
type host struct {
	mode app.WindowMode
	// closeAsked is set when the app asks the window to close.
	closeAsked bool
}

func (h *host) Invalidate() {}

func (h *host) Perform(a system.Action) {
	if a == system.ActionClose {
		h.closeAsked = true
	}
}

func (h *host) HWND() uintptr { return 0 }
func (h *host) SetWindowMode(m app.WindowMode) {
	h.mode = m
}

type driver struct {
	a      *ui.App
	host   *host
	router *input.Router
	ops    op.Ops
	win    *headless.Window
	size   image.Point
	scale  float32
	dir    string
	start  time.Time
	// clipboard answers clipboard reads the way the window systems do.
	clipboard string
}

// fail prints a diagnostic and stops the renderer.
func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "shot: "+format+"\n", args...)
	os.Exit(1)
}

// windowButtonsShot clicks the titlebar window buttons and checks that they
// ask the host for a window mode. Gio only implements window states in its
// option handling, so the buttons must not call Window.Perform.
func (d *driver) windowButtonsShot(want func(string) bool) {
	if !want("window-buttons") {
		return
	}
	const btnW, titleH = 46, 20
	w := float32(d.size.X)
	if d.a.WindowButtonsHidden() {
		fail("window buttons are hidden; the shot renderer draws an undecorated window")
	}
	click := func(i int) {
		d.click(w-d.px(float32((3-i)*btnW))+d.px(btnW/2), d.px(titleH), pointer.ButtonPrimary)
	}
	click(0)
	if d.host.mode != app.Minimized {
		fail("minimize button asked for mode %v", d.host.mode)
	}
	click(1)
	if d.host.mode != app.Maximized {
		fail("maximize button asked for mode %v", d.host.mode)
	}
	// While maximized the same button restores the window.
	d.a.SetMaximized(true)
	click(1)
	if d.host.mode != app.Windowed {
		fail("restore button asked for mode %v", d.host.mode)
	}
	d.a.SetMaximized(false)
	d.run(50 * time.Millisecond)
	fmt.Printf("%-14s %s\n", "window-buttons", time.Since(d.start).Round(time.Millisecond))
}

func (d *driver) frame() {
	d.ops.Reset()
	gtx := layout.Context{
		Ops:         &d.ops,
		Now:         time.Now(),
		Metric:      unit.Metric{PxPerDp: d.scale, PxPerSp: d.scale},
		Constraints: layout.Exact(d.size),
		Source:      d.router.Source(),
	}
	d.a.Layout(gtx)
	d.router.Frame(gtx.Ops)
	// Stand in for the window system's clipboard: when the app asks to
	// read it, hand it the text set for the current scenario.
	if d.router.ClipboardRequested() {
		text := d.clipboard
		d.router.Queue(transfer.DataEvent{Type: "application/text", Open: func() io.ReadCloser {
			return io.NopCloser(strings.NewReader(text))
		}})
	}
}

// run renders frames for the given duration so background work can land.
func (d *driver) run(dur time.Duration) {
	end := time.Now().Add(dur)
	for {
		d.frame()
		if time.Now().After(end) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (d *driver) until(what string, cond func() bool) {
	end := time.Now().Add(10 * time.Second)
	for time.Now().Before(end) {
		d.frame()
		if cond() {
			d.run(60 * time.Millisecond)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	fmt.Fprintln(os.Stderr, "timeout waiting for", what)
}

func (d *driver) shot(name string) {
	d.frame()
	d.frame()
	if err := d.win.Frame(&d.ops); err != nil {
		fmt.Fprintln(os.Stderr, "frame:", err)
		os.Exit(1)
	}
	img := image.NewRGBA(image.Rectangle{Max: d.size})
	if err := d.win.Screenshot(img); err != nil {
		fmt.Fprintln(os.Stderr, "screenshot:", err)
		os.Exit(1)
	}
	f, err := os.Create(filepath.Join(d.dir, name+".png"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	png.Encode(f, img)
	f.Close()
	fmt.Printf("%-14s %s\n", name, time.Since(d.start).Round(time.Millisecond))
}

func (d *driver) px(dp float32) float32 { return dp * d.scale }

func (d *driver) move(x, y float32) {
	d.router.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(x, y)})
	d.frame()
}

func (d *driver) click(x, y float32, btn pointer.Buttons) {
	p := f32.Pt(x, y)
	d.move(x, y)
	d.router.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: btn, Position: p})
	d.frame()
	d.router.Queue(pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p})
	d.frame()
	d.frame()
}

// drag presses the left button at (x0, y0), moves to (x1, y1) in steps
// and releases there.
func (d *driver) drag(x0, y0, x1, y1 float32) {
	d.move(x0, y0)
	d.router.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: f32.Pt(x0, y0)})
	d.frame()
	const steps = 8
	for i := 1; i <= steps; i++ {
		p := f32.Pt(x0+(x1-x0)*float32(i)/steps, y0+(y1-y0)*float32(i)/steps)
		// The platform reports moves; the router turns them into drags
		// while a button is held.
		d.router.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p})
		d.frame()
	}
	d.router.Queue(pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: f32.Pt(x1, y1)})
	d.frame()
	d.frame()
}

func (d *driver) key(name key.Name, mods key.Modifiers) {
	d.router.Queue(key.Event{Name: name, Modifiers: mods, State: key.Press})
	d.frame()
	d.router.Queue(key.Event{Name: name, Modifiers: mods, State: key.Release})
	d.frame()
}

// typeText enters text the way the platform does: an edit at the caret
// followed by a selection update. It assumes the focused input is empty at
// the start of each line.
func (d *driver) typeText(s string) {
	for _, line := range strings.SplitAfter(s, "\n") {
		txt := strings.TrimSuffix(line, "\n")
		if txt != "" {
			n := len([]rune(txt))
			d.router.Queue(key.EditEvent{Text: txt}, key.SelectionEvent{Start: n, End: n})
			d.frame()
		}
		if strings.HasSuffix(line, "\n") {
			d.key(key.NameReturn, 0)
		}
		d.run(150 * time.Millisecond)
	}
}

// clearField empties the focused text input.
func (d *driver) clearField() {
	d.key("A", key.ModCtrl)
	d.key(key.NameDeleteBackward, 0)
}

func main() {
	out := flag.String("o", ".", "output directory")
	w := flag.Int("w", 1280, "width in dp")
	h := flag.Int("h", 800, "height in dp")
	scale := flag.Float64("scale", 1, "pixels per dp")
	only := flag.String("only", "", "comma separated list of shots to take (default all)")
	bgImage := flag.String("bg", "build/icon.png", "image for the background shot")
	flag.Parse()
	want := func(name string) bool {
		return *only == "" || strings.Contains(","+*only+",", ","+name+",")
	}

	srv, err := sshtest.Start("127.0.0.1:0", "demo", "demo")
	if err != nil {
		panic(err)
	}
	defer srv.Close()
	srv.Populate()

	dir, _ := os.MkdirTemp("", "nlrshot")
	defer os.RemoveAll(dir)
	// The built-in file picker (Linux) browses the home directory; give it
	// a known one so the screenshots are deterministic.
	if home, err := os.MkdirTemp("", "nlrshot-home"); err == nil {
		defer os.RemoveAll(home)
		// os.UserHomeDir reads HOME on Unix and USERPROFILE on Windows.
		os.Setenv("HOME", home)
		os.Setenv("USERPROFILE", home)
		os.MkdirAll(filepath.Join(home, "docs"), 0o755)
		os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
		os.WriteFile(filepath.Join(home, "notes.txt"), []byte("hello\n"), 0o644)
		os.WriteFile(filepath.Join(home, "backup.tar.gz"), []byte("data"), 0o644)
	}
	st := store.Open(dir)
	st.UpdateSettings(func(s *store.Settings) { s.DownloadDir = filepath.Join(dir, "dl") })
	now := time.Now().Unix()
	demo := st.SaveProfile(store.Profile{Name: "nlr-demo", Group: "生产环境", Host: "127.0.0.1", Port: srv.Port(), User: "demo", LastUsed: now - 120})
	st.SaveProfile(store.Profile{Name: "web-01", Group: "生产环境", Host: "10.0.0.11", User: "root", Auth: store.AuthKey, KeyPath: `~\.ssh\id_ed25519`, LastUsed: now - 3600*5})
	st.SaveProfile(store.Profile{Name: "db-master", Group: "生产环境", Host: "10.0.0.21", User: "root", LastUsed: now - 86400*2, JumpID: demo.ID})
	st.SaveProfile(store.Profile{Name: "测试机", Group: "开发", Host: "192.168.1.50", Port: 2222, User: "dev", LastUsed: now - 86400*9})
	st.SaveProfile(store.Profile{Group: "开发", Host: "build.example.com", User: "ci", Auth: store.AuthAgent})
	st.SaveProfile(store.Profile{Name: "树莓派", Host: "raspberrypi.local", User: "pi"})

	size := image.Pt(int(float64(*w)**scale), int(float64(*h)**scale))
	win, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		fmt.Fprintln(os.Stderr, "headless:", err)
		os.Exit(1)
	}
	defer win.Release()
	winHost := &host{}
	a := ui.New(winHost, st)
	// The shot renderer shows the layout the release builds use: both
	// Windows and the patched Linux build run without platform decorations
	// (see tools/giopatch/gioui-fixes.patch).
	a.SetDecorated(false)
	d := &driver{a: a, host: winHost, router: new(input.Router), win: win, size: size, scale: float32(*scale), dir: *out, start: time.Now()}
	os.MkdirAll(*out, 0o755)

	d.run(200 * time.Millisecond)
	if want("home") {
		d.shot("home")
	}
	d.windowButtonsShot(want)
	if want("home-search") {
		d.typeText("root@172.16.8.4:2200")
		d.shot("home-search")
		d.clearField()
	}
	if want("profile") {
		d.key("N", key.ModCtrl|key.ModShift)
		d.run(100 * time.Millisecond)
		d.shot("profile")
		d.key(key.NameEscape, 0)
	}
	d.pickerShot(want)

	// Connect to the demo host by typing its name and pressing Enter.
	d.typeText("nlr-demo\n")
	d.until("host key prompt", func() bool { return a.DialogCount() == 1 })
	if want("hostkey") {
		d.shot("hostkey")
	}
	d.key(key.NameReturn, 0)
	d.until("password prompt", func() bool { return a.DialogCount() == 1 })
	if want("password") {
		d.shot("password")
	}
	d.typeText("demo\n")
	sess := func() *sshx.Session { return a.Sessions()[0] }
	d.until("connect", func() bool { return len(a.Sessions()) == 1 && sess().State() == sshx.StateConnected })
	d.until("monitor", func() bool { m := sess().Monitor(); return m.Ready && len(m.CPU) >= 2 })
	d.until("files", func() bool { return a.FilesPath() != "" })
	d.typeText("ls -l\ncolors\nbox\ncd projects/api\nls\n")
	d.run(2500 * time.Millisecond)
	if want("session") {
		d.shot("session")
	}
	if want("term-menu") {
		d.click(d.px(700), d.px(300), pointer.ButtonSecondary)
		d.shot("term-menu")
		d.key(key.NameEscape, 0)
	}
	if want("file-menu") {
		// Right-click the first row of the file list.
		d.click(d.px(500), float32(size.Y)-d.px(150), pointer.ButtonSecondary)
		d.shot("file-menu")
		d.key(key.NameEscape, 0)
	}
	if want("tunnels") {
		a.OpenTunnels()
		d.run(100 * time.Millisecond)
		d.shot("tunnels")
		d.key(key.NameEscape, 0)
	}
	if want("settings") || want("settings-picker") {
		a.OpenSettings()
		d.run(100 * time.Millisecond)
		d.shot("settings")
		d.settingsPickerShot(want)
		d.key(key.NameEscape, 0)
	}
	if want("drag-file") {
		// Drag go.mod (second row) onto src (first row): it must move there.
		rowY := func(i int) float32 { return float32(size.Y) - d.px(800-627) + d.px(26)*float32(i) }
		d.drag(d.px(500), rowY(1), d.px(500), rowY(0))
		fs := srv.FS()
		moved := func() bool { _, err := fs.Stat("/home/demo/projects/api/src/go.mod"); return err == nil }
		d.until("file moved by drag", moved)
		if !moved() {
			fail("dragging go.mod onto src did not move it")
		}
		fs.Close()
		fmt.Printf("%-14s %s\n", "drag-file", time.Since(d.start).Round(time.Millisecond))
	}
	if want("drag-tab") {
		// Open a second tab, then drag the session tab past it.
		a.OpenRemote("/etc/hosts", 60)
		d.until("second tab", func() bool { return a.TabCount() == 2 })
		before := a.TabTitles()
		d.drag(d.px(220), d.px(20), d.px(560), d.px(20))
		after := a.TabTitles()
		if len(after) != 2 || after[0] != before[1] || after[1] != before[0] {
			fail("tab drag did not reorder: %v -> %v", before, after)
		}
		d.shot("drag-tab")
		d.click(d.px(220), d.px(20), pointer.ButtonTertiary)
		d.until("tab closed", func() bool { return a.TabCount() == 1 })
		fmt.Printf("%-14s %v -> %v\n", "drag-tab", before, after)
	}
	if want("quick") {
		st.SaveSnippet(store.Snippet{Name: "磁盘占用", Command: "df -h"})
		st.SaveSnippet(store.Snippet{Name: "重启 nginx", Command: "sudo systemctl restart nginx"})
		st.SaveSnippet(store.Snippet{Name: "uptime", Command: "uptime"})
		a.ShowQuickCommands(true)
		d.run(150 * time.Millisecond)
		d.shot("quick")
		a.ShowQuickCommands(false)
	}
	if want("perm") {
		a.OpenPermissions("src")
		d.run(400 * time.Millisecond)
		d.shot("perm")
		d.key(key.NameEscape, 0)
	}
	if want("input-menu") {
		// Right-click the command bar input.
		d.click(d.px(700), float32(size.Y)-d.px(21+float32(272)), pointer.ButtonSecondary)
		d.shot("input-menu")
		d.key(key.NameEscape, 0)
	}
	if want("background") {
		a.SetBackground(*bgImage)
		d.until("background", a.BackgroundLoaded)
		d.shot("background")
		a.SetLook(store.AppearanceLight, store.AccentGreen)
		d.run(150 * time.Millisecond)
		d.shot("background-light")
		a.SetLook(store.AppearanceDark, store.AccentGreen)
		a.SetBackground("")
	}
	if want("fonts") {
		a.OpenSettings()
		d.run(100 * time.Millisecond)
		a.OpenFontList()
		d.run(1500 * time.Millisecond)
		d.shot("fonts")
		d.key(key.NameEscape, 0)
	}
	if want("light") {
		a.SetLook(store.AppearanceLight, store.AccentBlue)
		d.run(150 * time.Millisecond)
		d.shot("light")
		a.SetLook(store.AppearanceLightTerm, store.AccentOrange)
		d.run(150 * time.Millisecond)
		d.shot("light-term")
		a.SetLook(store.AppearanceDark, store.AccentGreen)
	}
	if want("transfers") {
		sess().Transfers.Download("/var/log/syslog", filepath.Join(dir, "dl"))
		sess().Transfers.Download("/tmp/backup-2026-09-30.tar.gz", filepath.Join(dir, "dl"))
		sess().Transfers.Download("/etc/does-not-exist", filepath.Join(dir, "dl"))
		a.ShowTasks(true)
		d.run(700 * time.Millisecond)
		d.shot("transfers")
		a.ShowTasks(false)
	}
	if want("paste") {
		// Pasting a file list from the file manager uploads those files.
		// The text goes through the driver's clipboard stand-in, so this
		// covers the Ctrl+V binding and the clipboard read as well.
		local := filepath.Join(dir, "pasted-notes.txt")
		os.WriteFile(local, []byte("pasted\n"), 0o644)
		d.clipboard = (&url.URL{Scheme: "file", Path: local}).String() + "\n" + local + "\n"
		d.click(d.px(500), float32(d.size.Y)-d.px(150), pointer.ButtonPrimary)
		d.key("V", key.ModCtrl)
		d.run(100 * time.Millisecond)
		uploaded := func() bool {
			for _, ti := range sess().Transfers.List() {
				if ti.Upload && ti.Name == "pasted-notes.txt" {
					return true
				}
			}
			return false
		}
		d.until("paste upload", uploaded)
		if !uploaded() {
			fail("paste did not upload %s", local)
		}
		d.run(500 * time.Millisecond)
		for _, ti := range sess().Transfers.List() {
			if ti.Upload && ti.Name == "pasted-notes.txt" && ti.State == sshx.TransferFailed {
				fail("paste upload failed: %s", ti.Err)
			}
		}
		fmt.Printf("%-14s %s\n", "paste", time.Since(d.start).Round(time.Millisecond))
	}
	if want("editor") {
		a.OpenRemote("/etc/nginx/nginx.conf", 200)
		d.until("editor tab", func() bool { return a.TabCount() == 2 })
		d.move(d.px(400), d.px(20))
		d.run(700 * time.Millisecond)
		d.shot("editor")
		// Middle click on the tab closes it.
		d.click(d.px(400), d.px(20), pointer.ButtonTertiary)
		d.until("tab closed by middle click", func() bool { return a.TabCount() == 1 })
	}
	if want("closed") {
		d.typeText("exit\n")
		d.until("close", func() bool { return sess().State() == sshx.StateClosed })
		d.shot("closed")
		// CloseOnExit defaults to off, so the window must stay open.
		if d.host.closeAsked {
			fail("the window was closed although CloseOnExit is off by default")
		}
	}
	if want("exit-close") {
		// Switch the setting on in the dialog, then let shells exit with
		// status 0: the session's page must close, and the window must
		// follow only the last page.
		if sess().State() == sshx.StateClosed {
			// The "closed" shot ran first; reconnect so that switching the
			// setting on does not close the already exited page.
			sess().Reconnect()
			d.until("reconnect", func() bool { return sess().State() == sshx.StateConnected })
		}
		if st.Settings().CloseOnExit {
			fail("CloseOnExit should default to off")
		}
		a.OpenSettings()
		d.run(100 * time.Millisecond)
		// The settings dialog is centered and 650dp tall; the check box is
		// its last row, 571dp below the top edge.
		top := (float32(d.size.Y) - d.px(650)) / 2
		d.click(d.px(500), top+d.px(571), pointer.ButtonPrimary)
		d.key(key.NameReturn, 0) // 保存
		d.run(100 * time.Millisecond)
		if !st.Settings().CloseOnExit {
			fail("saving the settings dialog did not store CloseOnExit")
		}
		// Exiting one of two sessions closes its page and nothing else.
		st.SetPassword(demo.ID, "demo")
		prof, _ := st.Profile(demo.ID)
		a.Connect(prof)
		d.until("second tab", func() bool { return a.TabCount() == 2 })
		second := a.Sessions()[1]
		d.until("second session", func() bool { return second.State() == sshx.StateConnected })
		d.typeText("exit\n")
		d.until("page closed", func() bool { return a.TabCount() == 1 })
		if second.State() != sshx.StateClosed {
			fail("the second session did not exit")
		}
		if sess().State() != sshx.StateConnected {
			fail("the other session was closed with the page")
		}
		if d.host.closeAsked {
			fail("the window was closed although another page is still open")
		}
		// The last page closes and leaves the home page showing.
		a.ShowTab(0)
		d.run(100 * time.Millisecond)
		d.typeText("exit\n")
		d.until("last page closed", func() bool { return a.TabCount() == 0 })
		if d.host.closeAsked {
			fail("the window was closed instead of showing the home page")
		}
		d.shot("exit-close")
	}
}
