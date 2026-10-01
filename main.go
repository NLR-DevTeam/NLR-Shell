// NLR Shell is a native SSH client: terminal, system monitor, SFTP file
// manager and port forwarding in one window. It builds for Windows and for
// Linux/X11/Wayland; platform-specific window handling lives in event_*.go
// and in the ui and store packages.
package main

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"

	"gioui.org/app"
	"gioui.org/io/system"
	"gioui.org/op"
	"gioui.org/unit"

	"nlrshell/internal/store"
	"nlrshell/internal/ui"
)

// Command line options. -connect opens a session right away and never opens
// a prompt: unknown host keys are trusted, changed ones refuse the
// connection, and credentials come from -identity or -password.
var (
	connectFlag    = flag.String("connect", "", "connect to [user@]host[:port] on startup")
	identityFlag   = flag.String("identity", "", "private key file for -connect")
	passwordFlag   = flag.String("password", "", "password for -connect")
	passphraseFlag = flag.String("passphrase", "", "private key passphrase for -connect")
)

// connectProfile builds a profile for -connect. The profile is not saved;
// it exists for the lifetime of the session.
func connectProfile(addr, identity, password, passphrase string) (store.Profile, error) {
	p := store.Profile{Host: addr, Port: 22, Auth: store.AuthPassword}
	if i := strings.LastIndex(addr, "@"); i >= 0 {
		p.User, p.Host = addr[:i], addr[i+1:]
	} else {
		p.User = os.Getenv("USER")
		if p.User == "" {
			p.User = os.Getenv("USERNAME")
		}
	}
	if h, port, err := net.SplitHostPort(p.Host); err == nil {
		p.Host = h
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return p, fmt.Errorf("无效端口 %q", port)
		}
		p.Port = n
	}
	if p.Host == "" || p.User == "" {
		return p, errors.New("-connect 需要 [user@]host[:port]")
	}
	if identity != "" {
		p.Auth = store.AuthKey
		p.KeyPath = identity
	}
	p.Password = store.Encrypt(password)
	p.Passphrase = store.Encrypt(passphrase)
	return p, nil
}

type host struct {
	w    *app.Window
	hwnd atomic.Uintptr
}

func (h *host) Invalidate()             { h.w.Invalidate() }
func (h *host) Perform(a system.Action) { h.w.Perform(a) }
func (h *host) HWND() uintptr           { return h.hwnd.Load() }

// SetWindowMode drives the window state through Gio options: the backends
// implement the states in their option handling, while Perform only knows
// about pointer gestures and closing.
func (h *host) SetWindowMode(m app.WindowMode) { h.w.Option(m.Option()) }

func main() {
	flag.Parse()
	st := store.Open(store.Dir())
	set := st.Settings()

	w := new(app.Window)
	opts := []app.Option{
		app.Title("NLR Shell"),
		app.Size(unit.Dp(set.WindowWidth), unit.Dp(set.WindowHeight)),
		app.MinSize(760, 480),
		app.Decorated(false),
	}
	if set.Maximized {
		opts = append(opts, app.Maximized.Option())
	}
	w.Option(opts...)

	h := &host{w: w}
	a := ui.New(h, st)
	if *connectFlag != "" {
		p, err := connectProfile(*connectFlag, *identityFlag, *passwordFlag, *passphraseFlag)
		if err != nil {
			fmt.Fprintln(os.Stderr, "nlrshell:", err)
			os.Exit(2)
		}
		a.SetUnattended(true)
		a.Post(func() { a.Connect(p) })
	}
	go func() {
		run(w, h, a)
		os.Exit(0)
	}()
	app.Main()
}

func run(w *app.Window, h *host, a *ui.App) {
	var ops op.Ops
	var metric unit.Metric
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			a.Shutdown()
			return
		case app.ConfigEvent:
			a.SetDecorated(e.Config.Decorated)
			a.SetMaximized(e.Config.Mode == app.Maximized)
			switch {
			case e.Config.Mode == app.Maximized:
				a.RememberWindow(0, 0, true)
			case e.Config.Mode == app.Windowed && metric.PxPerDp > 0:
				a.RememberWindow(int(metric.PxToDp(e.Config.Size.X)), int(metric.PxToDp(e.Config.Size.Y)), false)
			}
		case app.FrameEvent:
			metric = e.Metric
			gtx := app.NewContext(&ops, e)
			a.Layout(gtx)
			e.Frame(gtx.Ops)
		default:
			platformEvent(h, a, e)
		}
	}
}
