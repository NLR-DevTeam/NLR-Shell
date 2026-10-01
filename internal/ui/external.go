package ui

import (
	"bytes"
	"errors"
	"image"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gioui.org/op/paint"
	xdraw "golang.org/x/image/draw"

	"nlrshell/internal/sshx"
	"nlrshell/internal/store"
)

// fileStamp identifies a version of a local file.
type fileStamp struct {
	mod  time.Time
	size int64
}

func stampOf(p string) (fileStamp, bool) {
	fi, err := os.Stat(p)
	if err != nil {
		return fileStamp{}, false
	}
	return fileStamp{mod: fi.ModTime(), size: fi.Size()}, true
}

// extFile is a remote file copied to a temporary directory, either on its
// way into a built-in tab or opened with a local application.
type extFile struct {
	sess   *sshx.Session
	remote string
	local  string
	// builtin asks for a tab if the file turns out to be text or an image.
	builtin bool
	// base is the version known to match the server (or that the user chose
	// not to upload); last is the most recent version seen by the watcher.
	base      fileStamp
	last      fileStamp
	ready     bool // opened with a local application and being watched
	prompting bool
}

// external fetches remote files into the temporary directory, opens them in
// a tab or with a local application, and offers to upload local edits.
type external struct {
	mu      sync.Mutex
	files   []*extFile
	running bool
}

func (x *external) find(sess *sshx.Session, remote string) *extFile {
	for _, f := range x.files {
		if f.sess == sess && f.remote == remote {
			return f
		}
	}
	return nil
}

// fetch downloads remote as a task. With builtin set, text and images open
// in a tab when the download completes; everything else, and every file
// when builtin is false, opens with the default local application.
func (x *external) fetch(a *App, sess *sshx.Session, remote string, builtin bool) {
	x.mu.Lock()
	f := x.find(sess, remote)
	x.mu.Unlock()
	if f != nil {
		if !f.ready {
			// Still downloading; a second request only changes the target.
			x.mu.Lock()
			f.builtin = f.builtin && builtin
			x.mu.Unlock()
			return
		}
		if !builtin {
			// Already fetched: reopen the local copy, which may hold edits.
			if _, ok := stampOf(f.local); ok {
				x.launch(a, f.local)
				return
			}
			x.forget(f, false)
		}
	}
	if sess.State() != sshx.StateConnected {
		a.Toast(toastError, "未连接")
		return
	}
	dir := filepath.Join(os.TempDir(), "NLRShell", store.NewID())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		a.Toast(toastError, err.Error())
		return
	}
	nf := &extFile{sess: sess, remote: remote, builtin: builtin, local: filepath.Join(dir, sshx.SafeName(baseName(remote)))}
	x.mu.Lock()
	x.files = append(x.files, nf)
	x.mu.Unlock()
	sess.Transfers.DownloadTemp(remote, nf.local)
}

// downloaded is called on the UI goroutine when a transfer of sess
// finishes. It reports whether the transfer was one of ours.
func (x *external) downloaded(a *App, sess *sshx.Session, ti sshx.TransferInfo) bool {
	if ti.Upload || !ti.Temp {
		return false
	}
	x.mu.Lock()
	var f *extFile
	for _, c := range x.files {
		if c.sess == sess && c.local == ti.Local && !c.ready {
			f = c
		}
	}
	x.mu.Unlock()
	if f == nil {
		return false
	}
	if ti.State != sshx.TransferDone {
		x.forget(f, true)
		if ti.State == sshx.TransferFailed {
			a.Toast(toastError, ti.Name+"："+ti.Err)
		}
		return true
	}
	if !f.builtin {
		x.ready(a, f)
		return true
	}
	// Decide between a tab and a local application off the UI goroutine.
	go func() {
		open := x.inspect(f)
		a.Post(func() {
			if open == nil {
				x.ready(a, f)
				return
			}
			x.forget(f, true)
			if ft := a.findFile(f.sess, f.remote); ft != nil {
				a.activate(a.indexOf(ft))
				return
			}
			a.addTab(open(a))
		})
	}()
	return true
}

// inspect looks at a downloaded file and returns a constructor for the tab
// that can show it, or nil if it should go to a local application.
func (x *external) inspect(f *extFile) func(a *App) tab {
	fi, err := os.Stat(f.local)
	if err != nil {
		return nil
	}
	if isImageName(f.remote) && fi.Size() <= maxImageSize {
		img, err := decodeImage(f.local)
		if err != nil {
			return nil
		}
		dims := img.Bounds().Size()
		op := paint.NewImageOp(img)
		return func(a *App) tab {
			return &imageTab{fileBase: fileBase{a: a, sess: f.sess, path: f.remote}, img: op, dims: dims, bytes: fi.Size()}
		}
	}
	if fi.Size() > sshx.MaxEditSize {
		return nil
	}
	data, err := os.ReadFile(f.local)
	if err != nil || !sshx.IsText(data) {
		return nil
	}
	return func(a *App) tab { return newEditorTab(a, f.sess, f.remote, string(data)) }
}

func decodeImage(p string) (image.Image, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("无法解码图片")
	}
	// Keep textures within what every GPU accepts.
	if b := img.Bounds(); b.Dx() > 8192 || b.Dy() > 8192 {
		s := 8192 / float64(max(b.Dx(), b.Dy()))
		dst := image.NewRGBA(image.Rect(0, 0, int(float64(b.Dx())*s), int(float64(b.Dy())*s)))
		xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, xdraw.Src, nil)
		img = dst
	}
	return img, nil
}

// ready starts watching a downloaded file and opens it locally.
func (x *external) ready(a *App, f *extFile) {
	st, _ := stampOf(f.local)
	x.mu.Lock()
	f.base, f.last, f.ready = st, st, true
	start := !x.running
	x.running = true
	x.mu.Unlock()
	if start {
		go x.watch(a)
	}
	x.launch(a, f.local)
}

func (x *external) launch(a *App, local string) {
	go func() {
		if err := shellOpen(local); err != nil {
			a.Post(func() { a.Toast(toastError, "无法打开 "+baseName(local)) })
		}
	}()
}

// forget stops tracking f, optionally deleting its temporary copy.
func (x *external) forget(f *extFile, remove bool) {
	x.mu.Lock()
	for i, c := range x.files {
		if c == f {
			x.files = append(x.files[:i], x.files[i+1:]...)
			break
		}
	}
	x.mu.Unlock()
	if remove {
		os.Remove(f.local)
		os.Remove(filepath.Dir(f.local))
	}
}

// watch polls the local copies and asks about uploading the ones that
// changed. A change must be seen unchanged on two polls in a row so that a
// file is not picked up while the application is still writing it.
func (x *external) watch(a *App) {
	for range time.Tick(time.Second) {
		x.mu.Lock()
		var changed []*extFile
		for _, f := range x.files {
			if !f.ready || f.prompting {
				continue
			}
			st, ok := stampOf(f.local)
			if !ok || st == f.base {
				f.last = st
				continue
			}
			if st == f.last {
				f.prompting = true
				changed = append(changed, f)
			}
			f.last = st
		}
		x.mu.Unlock()
		for _, f := range changed {
			f := f
			a.Post(func() { x.prompt(a, f) })
		}
	}
}

func (x *external) prompt(a *App, f *extFile) {
	// Whatever the answer, the version on disk at that moment becomes the
	// baseline, so only later edits ask again.
	settle := func() {
		st, _ := stampOf(f.local)
		x.mu.Lock()
		f.base, f.last, f.prompting = st, st, false
		x.mu.Unlock()
	}
	a.Open(&confirmDialog{
		title:   "保存到服务器？",
		message: baseName(f.remote) + " 已修改",
		detail:  f.sess.Profile.Addr() + ":" + f.remote,
		okLabel: "保存",
		onOK: func() {
			settle()
			if f.sess.State() != sshx.StateConnected {
				a.Toast(toastError, "未连接，无法保存 "+baseName(f.remote))
				return
			}
			f.sess.Transfers.UploadAs(f.local, f.remote)
		},
		onNo: settle,
	})
}

// cleanup removes the temporary copies that hold no unsaved changes.
func (x *external) cleanup() {
	x.mu.Lock()
	defer x.mu.Unlock()
	for _, f := range x.files {
		if st, ok := stampOf(f.local); ok && f.ready && st == f.base {
			os.Remove(f.local)
			os.Remove(filepath.Dir(f.local))
		}
	}
}
