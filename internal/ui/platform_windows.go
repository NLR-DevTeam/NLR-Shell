package ui

import (
	"os/exec"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modOle32   = windows.NewLazySystemDLL("ole32.dll")
	modShell32 = windows.NewLazySystemDLL("shell32.dll")
	modUser32  = windows.NewLazySystemDLL("user32.dll")

	procCoCreateInstance  = modOle32.NewProc("CoCreateInstance")
	procDragAcceptFiles   = modShell32.NewProc("DragAcceptFiles")
	procDragQueryFileW    = modShell32.NewProc("DragQueryFileW")
	procDragFinish        = modShell32.NewProc("DragFinish")
	procSetWindowLongPtrW = modUser32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW   = modUser32.NewProc("CallWindowProcW")
	procDefWindowProcW    = modUser32.NewProc("DefWindowProcW")

	clsidFileOpenDialog = windows.GUID{Data1: 0xdc1c5a9c, Data2: 0xe88a, Data3: 0x4dde, Data4: [8]byte{0xa5, 0xa1, 0x60, 0xf8, 0x2a, 0x20, 0xae, 0xf7}}
	iidFileOpenDialog   = windows.GUID{Data1: 0xd57c7288, Data2: 0xd4ad, Data3: 0x4768, Data4: [8]byte{0xbe, 0x02, 0x9d, 0x96, 0x95, 0x32, 0xd9, 0x60}}
)

const (
	fosPickFolders     = 0x20
	fosForceFileSystem = 0x40
	fosAllowMulti      = 0x200
	fosPathMustExist   = 0x800
	sigdnFileSysPath   = 0x80058000
)

// comCall invokes method idx of the COM object at obj.
func comCall(obj uintptr, idx int, args ...uintptr) uintptr {
	vtbl := *(*uintptr)(unsafe.Pointer(obj))
	fn := *(*uintptr)(unsafe.Pointer(vtbl + uintptr(idx)*unsafe.Sizeof(uintptr(0))))
	all := append([]uintptr{obj}, args...)
	r, _, _ := syscall.SyscallN(fn, all...)
	return r
}

// fileDialog shows the system open dialog and returns the chosen paths. It
// blocks, so call it from a goroutine other than the UI one.
func fileDialog(owner uintptr, title string, flags uint32) ([]string, bool) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); err != nil {
		// S_FALSE (already initialized) is reported as an error too; only
		// a changed threading mode is fatal for us.
		if errno, ok := err.(syscall.Errno); !ok || uint32(errno) != 1 {
			return nil, false
		}
	}
	defer windows.CoUninitialize()

	var dlg uintptr
	hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidFileOpenDialog)), 0, 1,
		uintptr(unsafe.Pointer(&iidFileOpenDialog)), uintptr(unsafe.Pointer(&dlg)))
	if hr != 0 || dlg == 0 {
		return nil, false
	}
	defer comCall(dlg, 2) // Release

	var opts uint32
	comCall(dlg, 10, uintptr(unsafe.Pointer(&opts))) // GetOptions
	comCall(dlg, 9, uintptr(opts|flags|fosForceFileSystem|fosPathMustExist))
	if t, err := windows.UTF16PtrFromString(title); err == nil {
		comCall(dlg, 17, uintptr(unsafe.Pointer(t))) // SetTitle
	}
	if comCall(dlg, 3, owner) != 0 { // Show; non-zero means cancelled
		return nil, false
	}
	itemPath := func(item uintptr) string {
		var p *uint16
		if comCall(item, 5, sigdnFileSysPath, uintptr(unsafe.Pointer(&p))) != 0 || p == nil { // GetDisplayName
			return ""
		}
		defer windows.CoTaskMemFree(unsafe.Pointer(p))
		return windows.UTF16PtrToString(p)
	}
	var out []string
	if flags&fosAllowMulti != 0 {
		var arr uintptr
		if comCall(dlg, 27, uintptr(unsafe.Pointer(&arr))) != 0 || arr == 0 { // GetResults
			return nil, false
		}
		defer comCall(arr, 2)
		var n uint32
		comCall(arr, 7, uintptr(unsafe.Pointer(&n))) // GetCount
		for i := uint32(0); i < n; i++ {
			var item uintptr
			if comCall(arr, 8, uintptr(i), uintptr(unsafe.Pointer(&item))) == 0 && item != 0 { // GetItemAt
				if s := itemPath(item); s != "" {
					out = append(out, s)
				}
				comCall(item, 2)
			}
		}
	} else {
		var item uintptr
		if comCall(dlg, 20, uintptr(unsafe.Pointer(&item))) != 0 || item == 0 { // GetResult
			return nil, false
		}
		if s := itemPath(item); s != "" {
			out = append(out, s)
		}
		comCall(item, 2)
	}
	return out, len(out) > 0
}

// pickFiles lets the user choose one or more files.
func pickFiles(owner uintptr, title string) ([]string, bool) {
	return fileDialog(owner, title, fosAllowMulti)
}

// pickFolder lets the user choose a folder.
func pickFolder(owner uintptr, title string) (string, bool) {
	paths, ok := fileDialog(owner, title, fosPickFolders)
	if !ok {
		return "", false
	}
	return paths[0], true
}

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

// ---- File drops from Explorer -----------------------------------------

const (
	wmDropFiles = 0x0233
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
	r, _, _ := procCallWindowProcW.Call(old, hwnd, msg, wParam, lParam)
	return r
}

// InstallDropHandler makes the window accept files dragged from Explorer
// and reports their paths to fn (called on the window thread).
func InstallDropHandler(hwnd uintptr, fn func([]string)) {
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
	h := &dropHook{fn: fn}
	drop.hooks[hwnd] = h
	cb := drop.cb
	drop.Unlock()
	// The lock must not be held here: these calls send messages to the
	// window synchronously, which re-enter dropWndProc on this thread.
	old, _, _ := procSetWindowLongPtrW.Call(hwnd, gwlpWndProc, cb)
	h.old.Store(old)
	procDragAcceptFiles.Call(hwnd, 1)
}
