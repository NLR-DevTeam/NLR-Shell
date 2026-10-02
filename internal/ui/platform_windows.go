package ui

import (
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	modShell32 = windows.NewLazySystemDLL("shell32.dll")
	modUser32  = windows.NewLazySystemDLL("user32.dll")

	procDragAcceptFiles   = modShell32.NewProc("DragAcceptFiles")
	procDragQueryFileW    = modShell32.NewProc("DragQueryFileW")
	procDragFinish        = modShell32.NewProc("DragFinish")
	procSetWindowLongPtrW = modUser32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW   = modUser32.NewProc("CallWindowProcW")
	procDefWindowProcW    = modUser32.NewProc("DefWindowProcW")
	procGetKeyState       = modUser32.NewProc("GetKeyState")
)

// Fonts: families are tried in order, so CJK text falls back to a font
// that has the glyphs when the preferred one does not.
const (
	uiFont      = "Microsoft YaHei UI, Segoe UI, PingFang SC, sans-serif"
	defaultMono = "Cascadia Mono"
	// monoFallbacks are monospace Latin fonts tried after the chosen Western
	// font; cjkFallbacks follow the chosen Chinese font. Go Mono ships with
	// the program, so Latin text always has a monospace font before any CJK
	// (proportional) font is reached.
	monoFallbacks = "Cascadia Mono, Consolas, Go Mono"
	cjkFallbacks  = "Microsoft YaHei UI, SimSun, monospace"
)

// shellOpen opens a file with its default application, falling back to the
// system "Open with" chooser when the type has no association.
func shellOpen(path string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	if err := windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL); err != nil {
		return exec.Command("rundll32.exe", "shell32.dll,OpenAs_RunDLL", path).Start()
	}
	return nil
}

// revealInExplorer opens Explorer with path selected.
func revealInExplorer(path string) {
	cmd := exec.Command("explorer.exe", "/select,"+path)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + path + `"`}
	cmd.Start()
}

// ---- File drops from Explorer and window closing ------------------------

const (
	wmDropFiles = 0x0233
	wmClose     = 0x0010
	vkShift     = 0x10
	gwlpWndProc = ^uintptr(3) // -4
)

var drop struct {
	sync.Mutex
	hooks map[uintptr]*dropHook
	cb    uintptr
}

type dropHook struct {
	old atomic.Uintptr
	fn  func([]string)
	// closing is asked when the window is about to close, with whether
	// Shift is held; returning true keeps the window open.
	closing func(shift bool) bool
}

func dropWndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	drop.Lock()
	h := drop.hooks[hwnd]
	drop.Unlock()
	var old uintptr
	if h != nil {
		old = h.old.Load()
	}
	if old == 0 {
		r, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
		return r
	}
	if msg == wmDropFiles {
		n, _, _ := procDragQueryFileW.Call(wParam, 0xffffffff, 0, 0)
		paths := make([]string, 0, n)
		for i := uintptr(0); i < n; i++ {
			l, _, _ := procDragQueryFileW.Call(wParam, i, 0, 0)
			buf := make([]uint16, l+1)
			procDragQueryFileW.Call(wParam, i, uintptr(unsafe.Pointer(&buf[0])), l+1)
			paths = append(paths, windows.UTF16ToString(buf))
		}
		procDragFinish.Call(wParam)
		if len(paths) > 0 {
			h.fn(paths)
		}
		return 0
	}
	if msg == wmClose && h.closing != nil {
		// Covers every way of closing: the title bar, Alt+F4, the taskbar.
		ks, _, _ := procGetKeyState.Call(vkShift)
		if h.closing(ks&0x8000 != 0) {
			return 0
		}
	}
	r, _, _ := procCallWindowProcW.Call(old, hwnd, msg, wParam, lParam)
	return r
}

// InstallDropHandler makes the window accept files dragged from Explorer
// and reports their paths to fn, and lets closing veto closing the window.
// Both are called on the window thread.
func InstallDropHandler(hwnd uintptr, fn func([]string), closing func(shift bool) bool) {
	if hwnd == 0 {
		return
	}
	drop.Lock()
	if drop.hooks == nil {
		drop.hooks = map[uintptr]*dropHook{}
		drop.cb = syscall.NewCallback(dropWndProc)
	}
	if _, ok := drop.hooks[hwnd]; ok {
		drop.Unlock()
		return
	}
	h := &dropHook{fn: fn, closing: closing}
	drop.hooks[hwnd] = h
	cb := drop.cb
	drop.Unlock()
	// The lock must not be held here: these calls send messages to the
	// window synchronously, which re-enter dropWndProc on this thread.
	old, _, _ := procSetWindowLongPtrW.Call(hwnd, gwlpWndProc, cb)
	h.old.Store(old)
	procDragAcceptFiles.Call(hwnd, 1)
}

// systemDark reports whether Windows is set to the dark app mode.
func systemDark() (dark, ok bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false, false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	if err != nil {
		return false, false
	}
	return v == 0, true
}

// systemFontFamilies lists the installed font families, from the font
// registrations of the machine and of the current user.
func systemFontFamilies() []string {
	var names []string
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		k, err := registry.OpenKey(root, `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Fonts`, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		vs, _ := k.ReadValueNames(-1)
		k.Close()
		// Values look like "Microsoft YaHei & Microsoft YaHei UI (TrueType)"
		// or "Arial Bold Italic (TrueType)".
		for _, v := range vs {
			if i := strings.LastIndex(v, " ("); i > 0 {
				v = v[:i]
			}
			for _, n := range strings.Split(v, " & ") {
				names = append(names, n)
			}
		}
	}
	return cleanFamilies(names)
}
