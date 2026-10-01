package ui

import (
	"fmt"
	"image"
	"image/color"
	"io"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/transfer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"

	"nlrshell/internal/sshx"
	"nlrshell/internal/store"
)

const (
	colName = iota
	colSize
	colTime
	colMode
	colOwner
)

// filesView is the SFTP file manager panel.
type filesView struct {
	sv *sessionView
	a  *App

	path    string
	entries []sshx.Entry
	view    []int // indices into entries after filtering and sorting
	loading bool
	errMsg  string
	loadSeq int
	back    []string

	selected map[string]bool
	cursor   int
	anchor   int
	hover    int

	list     widget.List
	listTag  bool
	focused  bool
	lastHit  int
	lastTime time.Time
	sortCol  int
	sortDesc bool
	hdr      [5]widget.Clickable

	// showTasks opens the task (transfer) list beside the file list.
	showTasks                                                                      bool
	taskSeq                                                                        int
	tasksBtn, tasksClose                                                           widget.Clickable
	backBtn, upBtn, homeBtn, refreshBtn, uploadBtn, mkdirBtn, hiddenBtn, followBtn widget.Clickable
	pathField                                                                      Field
	cwdSeq                                                                         int
	copyText                                                                       string
	focusList                                                                      bool

	tlist    widget.List
	tBtns    map[int]*widget.Clickable
	clearBtn widget.Clickable
}

func newFilesView(sv *sessionView) *filesView {
	return &filesView{sv: sv, a: sv.a, selected: map[string]bool{}, cursor: -1, hover: -1, tBtns: map[int]*widget.Clickable{}}
}

// wantsFocus reports whether one of the panel's inputs holds the keyboard
// focus, in which case the terminal must not take it back.
func (fv *filesView) wantsFocus(gtx layout.Context) bool {
	return fv.focused || fv.focusList || gtx.Focused(&fv.pathField.Editor)
}

func (fv *filesView) onConnected() {
	if fv.path != "" {
		fv.load(fv.path)
		return
	}
	sess := fv.sv.sess
	fv.loading = true
	go func() {
		home, err := sess.Home()
		fv.a.Post(func() {
			if err != nil {
				fv.loading = false
				fv.errMsg = err.Error()
				return
			}
			fv.load(home)
		})
	}()
}

// navigate opens dir, remembering the current one for the back button.
func (fv *filesView) navigate(dir string) {
	if dir == "" {
		return
	}
	if fv.path != "" && dir != fv.path {
		fv.back = append(fv.back, fv.path)
		if len(fv.back) > 50 {
			fv.back = fv.back[1:]
		}
	}
	fv.load(dir)
}

func (fv *filesView) load(dir string) {
	fv.loadSeq++
	seq := fv.loadSeq
	fv.loading = true
	sess := fv.sv.sess
	go func() {
		real, entries, err := sess.List(dir)
		fv.a.Post(func() {
			if seq != fv.loadSeq {
				return
			}
			fv.loading = false
			if err != nil {
				fv.errMsg = err.Error()
				if fv.path == "" {
					fv.path = dir
					fv.pathField.SetText(dir)
				} else {
					// Stay where we were; just report the failure.
					fv.a.Toast(toastError, "无法打开 "+dir+"："+err.Error())
					fv.errMsg = ""
					fv.pathField.SetText(fv.path)
				}
				return
			}
			fv.errMsg = ""
			changed := real != fv.path
			fv.path = real
			fv.entries = entries
			fv.pathField.SetText(real)
			if changed {
				fv.selected = map[string]bool{}
				fv.cursor, fv.anchor = -1, -1
				fv.list.Position = layout.Position{}
			}
			fv.rebuild()
		})
	}()
}

func (fv *filesView) reload() {
	if fv.path != "" {
		fv.load(fv.path)
	}
}

// rebuild recomputes the visible, sorted rows.
func (fv *filesView) rebuild() {
	showHidden := fv.a.set.ShowHidden
	fv.view = fv.view[:0]
	for i, e := range fv.entries {
		if !showHidden && strings.HasPrefix(e.Name, ".") {
			continue
		}
		fv.view = append(fv.view, i)
	}
	es := fv.entries
	less := func(a, b sshx.Entry) bool {
		switch fv.sortCol {
		case colSize:
			if a.Size != b.Size {
				return a.Size < b.Size
			}
		case colTime:
			if !a.ModTime.Equal(b.ModTime) {
				return a.ModTime.Before(b.ModTime)
			}
		case colMode:
			if a.Mode != b.Mode {
				return a.Mode.Perm() < b.Mode.Perm()
			}
		case colOwner:
			if a.Owner != b.Owner {
				return a.Owner < b.Owner
			}
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	}
	sort.SliceStable(fv.view, func(i, j int) bool {
		a, b := es[fv.view[i]], es[fv.view[j]]
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		if fv.sortDesc {
			return less(b, a)
		}
		return less(a, b)
	})
	// Drop selections that no longer exist.
	names := map[string]bool{}
	for _, i := range fv.view {
		names[es[i].Name] = true
	}
	for n := range fv.selected {
		if !names[n] {
			delete(fv.selected, n)
		}
	}
	if fv.cursor >= len(fv.view) {
		fv.cursor = len(fv.view) - 1
	}
}

func (fv *filesView) entry(viewIdx int) *sshx.Entry {
	if viewIdx < 0 || viewIdx >= len(fv.view) {
		return nil
	}
	return &fv.entries[fv.view[viewIdx]]
}

func (fv *filesView) selection() []sshx.Entry {
	var out []sshx.Entry
	for _, i := range fv.view {
		if fv.selected[fv.entries[i].Name] {
			out = append(out, fv.entries[i])
		}
	}
	return out
}

func (fv *filesView) full(name string) string { return path.Join(fv.path, name) }

func (fv *filesView) selectOnly(i int) {
	fv.selected = map[string]bool{}
	if e := fv.entry(i); e != nil {
		fv.selected[e.Name] = true
	}
	fv.cursor, fv.anchor = i, i
}

func (fv *filesView) selectRange(from, to int) {
	if from > to {
		from, to = to, from
	}
	fv.selected = map[string]bool{}
	for i := max(from, 0); i <= to && i < len(fv.view); i++ {
		fv.selected[fv.entries[fv.view[i]].Name] = true
	}
}

// ---- Actions ----------------------------------------------------------

func (fv *filesView) open(e sshx.Entry) {
	if e.IsDir {
		fv.navigate(fv.full(e.Name))
		return
	}
	// Text and images open in a tab; other files in their local application.
	fv.a.openRemote(fv.sv.sess, fv.full(e.Name), e.Size)
}

func (fv *filesView) openExternal(e sshx.Entry) {
	fv.a.ext.fetch(fv.a, fv.sv.sess, fv.full(e.Name), false)
}

func (fv *filesView) download(es []sshx.Entry, dir string) {
	if len(es) == 0 {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fv.a.Toast(toastError, "无法创建下载目录："+err.Error())
		return
	}
	for _, e := range es {
		fv.sv.sess.Transfers.Download(fv.full(e.Name), dir)
	}
	fv.showTasks = true
}

func (fv *filesView) downloadTo(es []sshx.Entry) {
	// The entries are copied: the panel may have moved on by the time the
	// directory is picked.
	es = append([]sshx.Entry(nil), es...)
	pickFolder(fv.a, "选择下载位置", func(dir string) { fv.download(es, dir) })
}

func (fv *filesView) pickUpload(folder bool) {
	if folder {
		pickFolder(fv.a, "选择要上传的文件夹", func(dir string) { fv.upload([]string{dir}) })
		return
	}
	pickFiles(fv.a, "选择要上传的文件", func(paths []string) {
		if len(paths) > 0 {
			fv.upload(paths)
		}
	})
}

// upload sends local paths to the current directory, asking before
// overwriting existing names.
func (fv *filesView) upload(paths []string) {
	if fv.path == "" || fv.sv.sess.State() != sshx.StateConnected {
		fv.a.Toast(toastError, "未连接")
		return
	}
	dir := fv.path
	existing := map[string]bool{}
	for _, e := range fv.entries {
		existing[e.Name] = true
	}
	var clash []string
	for _, p := range paths {
		if n := baseName(p); existing[n] {
			clash = append(clash, n)
		}
	}
	start := func() {
		for _, p := range paths {
			fv.sv.sess.Transfers.Upload(p, dir)
		}
		fv.showTasks = true
	}
	if len(clash) == 0 {
		start()
		return
	}
	detail := strings.Join(clash, "\n")
	if len(clash) > 8 {
		detail = strings.Join(clash[:8], "\n") + fmt.Sprintf("\n… 等 %d 项", len(clash))
	}
	fv.a.Open(&confirmDialog{title: "覆盖同名文件？", detail: detail, okLabel: "覆盖", danger: true, onOK: start})
}

func baseName(p string) string {
	p = strings.TrimRight(p, `/\`)
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

func (fv *filesView) transferDone(ti sshx.TransferInfo) {
	a := fv.a
	if ti.State == sshx.TransferDone && ti.Upload && path.Dir(ti.Remote) == fv.path {
		fv.reload()
	}
	// The task list already shows the outcome when it is on screen.
	if fv.showTasks && a.set.ShowFiles && a.current() == fv.sv {
		return
	}
	verb := "下载"
	if ti.Upload {
		verb = "上传"
	}
	switch ti.State {
	case sshx.TransferDone:
		a.Toast(toastOK, verb+"完成："+ti.Name)
	case sshx.TransferFailed:
		a.Toast(toastError, verb+"失败 "+ti.Name+"："+ti.Err)
	}
}

// run executes a remote file operation in the background and reloads.
func (fv *filesView) run(what string, f func() error) {
	a := fv.a
	go func() {
		err := f()
		a.Post(func() {
			if err != nil {
				a.Toast(toastError, what+"失败："+err.Error())
			}
			fv.reload()
		})
	}()
}

func (fv *filesView) mkdir() {
	sess, dir := fv.sv.sess, fv.path
	fv.a.Open(newInputDialog("新建文件夹", "名称", "", func(name string) {
		if name = strings.TrimSpace(name); name != "" {
			fv.run("新建文件夹", func() error { return sess.Mkdir(path.Join(dir, name)) })
		}
	}))
}

func (fv *filesView) newFile() {
	sess, dir := fv.sv.sess, fv.path
	fv.a.Open(newInputDialog("新建文件", "名称", "", func(name string) {
		if name = strings.TrimSpace(name); name != "" {
			fv.run("新建文件", func() error { return sess.Touch(path.Join(dir, name)) })
		}
	}))
}

func (fv *filesView) rename(e sshx.Entry) {
	sess, dir := fv.sv.sess, fv.path
	fv.a.Open(newInputDialog("重命名", "新名称", e.Name, func(name string) {
		if name = strings.TrimSpace(name); name != "" && name != e.Name {
			fv.run("重命名", func() error { return sess.Rename(path.Join(dir, e.Name), path.Join(dir, name)) })
		}
	}))
}

func (fv *filesView) chmod(e sshx.Entry) {
	sess, p := fv.sv.sess, fv.full(e.Name)
	d := newInputDialog("权限", e.Name, fmt.Sprintf("%03o", e.Mode.Perm()), func(v string) {
		n, err := strconv.ParseUint(strings.TrimSpace(v), 8, 32)
		if err != nil || n > 0o7777 {
			fv.a.Toast(toastError, "无效的权限值："+v)
			return
		}
		fv.run("修改权限", func() error { return sess.Chmod(p, os.FileMode(n)) })
	})
	fv.a.Open(d)
}

func (fv *filesView) remove(es []sshx.Entry) {
	if len(es) == 0 {
		return
	}
	sess := fv.sv.sess
	paths := make([]string, len(es))
	names := make([]string, len(es))
	for i, e := range es {
		paths[i] = fv.full(e.Name)
		names[i] = e.Name
		if e.IsDir {
			names[i] += "/"
		}
	}
	detail := strings.Join(names, "\n")
	if len(names) > 8 {
		detail = strings.Join(names[:8], "\n") + fmt.Sprintf("\n… 共 %d 项", len(names))
	}
	fv.a.Open(&confirmDialog{title: "永久删除？", detail: detail, okLabel: "删除", danger: true, onOK: func() {
		fv.run("删除", func() error { return sess.Remove(paths) })
	}})
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (fv *filesView) cdTerminal(dir string) {
	fv.sv.sess.Write([]byte(" cd " + shellQuote(dir) + "\r"))
	fv.focused, fv.focusList = false, false
	fv.a.refocus = true
}

func (fv *filesView) fileMenu() {
	a := fv.a
	sel := fv.selection()
	if len(sel) == 0 {
		fv.bgMenu()
		return
	}
	single := len(sel) == 1
	first := sel[0]
	items := []MenuItem{}
	if single {
		if first.IsDir {
			items = append(items, MenuItem{Label: "打开", Icon: icFolder, Hint: "Enter", Do: func() { fv.open(first) }},
				MenuItem{Label: "在终端中进入", Icon: icTerminal, Do: func() { fv.cdTerminal(fv.full(first.Name)) }})
		} else {
			items = append(items, MenuItem{Label: "打开", Icon: icFile, Hint: "Enter", Do: func() { fv.open(first) }},
				MenuItem{Label: "用本地程序打开", Icon: icOpen, Do: func() { fv.openExternal(first) }})
		}
		items = append(items, MenuItem{Sep: true})
	}
	items = append(items,
		MenuItem{Label: "下载", Icon: icDownload, Do: func() { fv.download(sel, a.set.DownloadDir) }},
		MenuItem{Label: "下载到…", Do: func() { fv.downloadTo(sel) }},
		MenuItem{Sep: true},
		MenuItem{Label: "重命名", Icon: icEdit, Hint: "F2", Disabled: !single, Do: func() { fv.rename(first) }},
		MenuItem{Label: "修改权限…", Icon: icLock, Disabled: !single, Do: func() { fv.chmod(first) }},
		MenuItem{Label: "复制路径", Icon: icCopy, Do: func() {
			ps := make([]string, len(sel))
			for i, e := range sel {
				ps[i] = fv.full(e.Name)
			}
			fv.copyText = strings.Join(ps, "\n")
		}},
		MenuItem{Sep: true},
		MenuItem{Label: "删除", Icon: icDelete, Hint: "Del", Danger: true, Do: func() { fv.remove(sel) }},
	)
	a.Menu(items...)
}

func (fv *filesView) bgMenu() {
	a := fv.a
	ok := fv.path != "" && fv.sv.sess.State() == sshx.StateConnected
	a.Menu(
		MenuItem{Label: "上传文件…", Icon: icUpload, Disabled: !ok, Do: func() { fv.pickUpload(false) }},
		MenuItem{Label: "上传文件夹…", Disabled: !ok, Do: func() { fv.pickUpload(true) }},
		MenuItem{Sep: true},
		MenuItem{Label: "新建文件夹", Icon: icNewFolder, Disabled: !ok, Do: fv.mkdir},
		MenuItem{Label: "新建文件", Icon: icNewFile, Disabled: !ok, Do: fv.newFile},
		MenuItem{Sep: true},
		MenuItem{Label: "在终端中进入", Icon: icTerminal, Disabled: !ok, Do: func() { fv.cdTerminal(fv.path) }},
		MenuItem{Label: "复制路径", Icon: icCopy, Disabled: !ok, Do: func() { fv.copyText = fv.path }},
		MenuItem{Label: "隐藏文件", Checked: a.set.ShowHidden, Do: fv.toggleHidden},
		MenuItem{Label: "刷新", Icon: icRefresh, Hint: "F5", Disabled: !ok, Do: fv.reload},
	)
}

func (fv *filesView) toggleHidden() {
	fv.a.updateSettings(func(s *store.Settings) { s.ShowHidden = !s.ShowHidden })
	fv.rebuild()
}

func permString(e sshx.Entry) string {
	m := e.Mode
	b := []byte("----------")
	switch {
	case m&os.ModeSymlink != 0 || e.IsLink:
		b[0] = 'l'
	case m.IsDir():
		b[0] = 'd'
	case m&os.ModeDevice != 0:
		b[0] = 'b'
	case m&os.ModeNamedPipe != 0:
		b[0] = 'p'
	case m&os.ModeSocket != 0:
		b[0] = 's'
	}
	const rwx = "rwxrwxrwx"
	for i := 0; i < 9; i++ {
		if m&(1<<uint(8-i)) != 0 {
			b[i+1] = rwx[i]
		}
	}
	if m&os.ModeSetuid != 0 {
		b[3] = 's'
	}
	if m&os.ModeSetgid != 0 {
		b[6] = 's'
	}
	if m&os.ModeSticky != 0 {
		b[9] = 't'
	}
	return string(b)
}

// ---- Layout -----------------------------------------------------------

const fileRowH = unit.Dp(26)

func (fv *filesView) events(gtx layout.Context) {
	a := fv.a
	if fv.copyText != "" {
		gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(fv.copyText))})
		fv.copyText = ""
	}
	if fv.focusList {
		fv.focusList = false
		gtx.Execute(key.FocusCmd{Tag: fv})
	}
	// Follow the terminal's working directory.
	if cwd, seq := fv.sv.sess.Cwd(); seq != fv.cwdSeq {
		fv.cwdSeq = seq
		if a.set.FollowCwd && cwd != "" && cwd != fv.path && fv.path != "" {
			fv.navigate(cwd)
		}
	}
	if fv.tasksBtn.Clicked(gtx) {
		fv.showTasks = !fv.showTasks
	}
	if fv.tasksClose.Clicked(gtx) {
		fv.showTasks = false
	}
	// The task list opens by itself whenever a new task starts.
	if seq := fv.sv.sess.Transfers.Seq(); seq != fv.taskSeq {
		fv.taskSeq = seq
		fv.showTasks = true
	}
	if fv.backBtn.Clicked(gtx) && len(fv.back) > 0 {
		p := fv.back[len(fv.back)-1]
		fv.back = fv.back[:len(fv.back)-1]
		fv.load(p)
	}
	if fv.upBtn.Clicked(gtx) && fv.path != "" && fv.path != "/" {
		fv.navigate(path.Dir(fv.path))
	}
	if fv.homeBtn.Clicked(gtx) {
		sess := fv.sv.sess
		go func() {
			if home, err := sess.Home(); err == nil {
				a.Post(func() { fv.navigate(home) })
			}
		}()
	}
	if fv.refreshBtn.Clicked(gtx) {
		fv.reload()
	}
	if fv.uploadBtn.Clicked(gtx) {
		fv.pickUpload(false)
	}
	if fv.mkdirBtn.Clicked(gtx) {
		fv.mkdir()
	}
	if fv.hiddenBtn.Clicked(gtx) {
		fv.toggleHidden()
	}
	if fv.followBtn.Clicked(gtx) {
		a.updateSettings(func(s *store.Settings) { s.FollowCwd = !s.FollowCwd })
		if a.set.FollowCwd {
			if cwd, _ := fv.sv.sess.Cwd(); cwd != "" && cwd != fv.path {
				fv.navigate(cwd)
			}
		}
	}
	if fv.clearBtn.Clicked(gtx) {
		fv.sv.sess.Transfers.Clear()
	}
	for i := range fv.hdr {
		if fv.hdr[i].Clicked(gtx) {
			if fv.sortCol == i {
				fv.sortDesc = !fv.sortDesc
			} else {
				fv.sortCol, fv.sortDesc = i, false
			}
			fv.rebuild()
		}
	}
	if submitted, _ := fv.pathField.Events(gtx); submitted {
		if p := strings.TrimSpace(fv.pathField.Text()); p != "" {
			fv.navigate(p)
			fv.focusList = true
		}
	}

	// Keyboard.
	for {
		e, ok := gtx.Event(
			key.FocusFilter{Target: fv},
			key.Filter{Focus: fv, Name: key.NameUpArrow, Optional: key.ModShift},
			key.Filter{Focus: fv, Name: key.NameDownArrow, Optional: key.ModShift},
			key.Filter{Focus: fv, Name: key.NameHome},
			key.Filter{Focus: fv, Name: key.NameEnd},
			key.Filter{Focus: fv, Name: key.NamePageUp},
			key.Filter{Focus: fv, Name: key.NamePageDown},
			key.Filter{Focus: fv, Name: key.NameReturn},
			key.Filter{Focus: fv, Name: key.NameEnter},
			key.Filter{Focus: fv, Name: key.NameDeleteBackward},
			key.Filter{Focus: fv, Name: key.NameDeleteForward},
			key.Filter{Focus: fv, Name: key.NameF2},
			key.Filter{Focus: fv, Name: key.NameF5},
			key.Filter{Focus: fv, Name: key.NameEscape},
			key.Filter{Focus: fv, Name: "A", Required: key.ModCtrl},
			key.Filter{Focus: fv, Name: "V", Required: key.ModCtrl},
			key.Filter{Focus: &fv.pathField.Editor, Name: key.NameEscape},
			transfer.TargetFilter{Target: fv, Type: "application/text"},
		)
		if !ok {
			break
		}
		switch e := e.(type) {
		case key.FocusEvent:
			fv.focused = e.Focus
		case key.Event:
			if e.State == key.Press {
				fv.key(gtx, e)
			}
		case transfer.DataEvent:
			// A pasted file list from the file manager, the Linux
			// stand-in for dragging files onto the window.
			if rc := e.Open(); rc != nil {
				b, _ := io.ReadAll(io.LimitReader(rc, 1<<20))
				rc.Close()
				fv.pastePaths(string(b))
			}
		}
	}
}

// pastePaths uploads the local files named in copyText, which is either a
// text/uri-list or a list of paths as file managers put it on the clipboard.
func (fv *filesView) pastePaths(copyText string) {
	var paths []string
	for _, p := range parseLocalPaths(copyText) {
		if _, err := os.Lstat(p); err == nil {
			paths = append(paths, p)
		}
	}
	switch {
	case len(paths) == 0:
		fv.a.Toast(toastInfo, "剪贴板中没有本地文件路径")
		return
	case fv.sv.sess.State() != sshx.StateConnected:
		fv.a.Toast(toastError, "未连接")
		return
	}
	fv.upload(paths)
}

func (fv *filesView) key(gtx layout.Context, e key.Event) {
	n := len(fv.view)
	move := func(to int) {
		if n == 0 {
			return
		}
		to = min(max(to, 0), n-1)
		if e.Modifiers.Contain(key.ModShift) && fv.anchor >= 0 {
			fv.selectRange(fv.anchor, to)
			fv.cursor = to
		} else {
			fv.selectOnly(to)
		}
		// Scroll only when the cursor row leaves the viewport.
		first, count := fv.list.Position.First, fv.list.Position.Count
		if to <= first {
			fv.list.ScrollTo(to)
		} else if count > 0 && to >= first+count-1 {
			fv.list.ScrollTo(max(to-count+2, 0))
		}
	}
	page := max(fv.list.Position.Count-1, 1)
	switch e.Name {
	case key.NameUpArrow:
		move(fv.cursor - 1)
	case key.NameDownArrow:
		move(fv.cursor + 1)
	case key.NameHome:
		move(0)
	case key.NameEnd:
		move(n - 1)
	case key.NamePageUp:
		move(fv.cursor - page)
	case key.NamePageDown:
		move(fv.cursor + page)
	case key.NameReturn, key.NameEnter:
		if en := fv.entry(fv.cursor); en != nil {
			fv.open(*en)
		}
	case key.NameDeleteBackward:
		if fv.path != "/" && fv.path != "" {
			fv.navigate(path.Dir(fv.path))
		}
	case key.NameDeleteForward:
		fv.remove(fv.selection())
	case key.NameF2:
		if sel := fv.selection(); len(sel) == 1 {
			fv.rename(sel[0])
		}
	case key.NameF5:
		fv.reload()
	case key.NameEscape:
		// Give the keyboard back to the terminal.
		fv.pathField.SetText(fv.path)
		fv.focused = false
		fv.a.refocus = true
	case "A":
		fv.selectRange(0, n-1)
	case "V":
		gtx.Execute(clipboard.ReadCmd{Tag: fv})
	}
}

// Layout draws the panel.
func (fv *filesView) Layout(gtx layout.Context) layout.Dimensions {
	th := fv.a.th
	size := gtx.Constraints.Max
	fv.events(gtx)
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	fill(gtx.Ops, image.Rectangle{Max: size}, th.Bg1)
	layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(fv.toolbar),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			if !fv.showTasks {
				return fv.fileList(gtx)
			}
			// The task list sits beside the files while it is open.
			side := min(gtx.Dp(340), gtx.Constraints.Max.X/2)
			return layout.Flex{}.Layout(gtx,
				layout.Flexed(1, fv.fileList),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints = layout.Exact(image.Pt(side, gtx.Constraints.Max.Y))
					return fv.tasks(gtx)
				}),
			)
		}),
	)
	return layout.Dimensions{Size: size}
}

func (fv *filesView) toolbar(gtx layout.Context) layout.Dimensions {
	a := fv.a
	th := a.th
	h := gtx.Dp(38)
	connected := fv.sv.sess.State() == sshx.StateConnected
	active := fv.sv.sess.Transfers.Active()
	gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, h))
	btn := func(clk *widget.Clickable, ic *widget.Icon, on, enabled bool, title string) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if !enabled {
				gtx = gtx.Disabled()
			}
			return th.iconButton(gtx, clk, ic, 28, 17, th.Text2, on, title)
		})
	}
	gap := hspace(4)
	layout.Inset{Left: 8, Right: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			btn(&fv.backBtn, icBack, false, len(fv.back) > 0, "后退"), gap,
			btn(&fv.upBtn, icUp, false, fv.path != "" && fv.path != "/", "上级目录"), gap,
			btn(&fv.homeBtn, icHome, false, connected, "主目录"), gap,
			btn(&fv.refreshBtn, icRefresh, false, connected, "刷新"),
			hspace(6),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				fv.pathField.Hint = "路径"
				return fv.pathField.box(gtx, th, func(gtx layout.Context) layout.Dimensions {
					if !fv.loading {
						return layout.Dimensions{}
					}
					return th.spinner(gtx, 16)
				})
			}),
			hspace(6),
			btn(&fv.followBtn, icFollow, a.set.FollowCwd, true, "跟随终端目录"), gap,
			btn(&fv.hiddenBtn, icEye, a.set.ShowHidden, true, "隐藏文件"), gap,
			btn(&fv.mkdirBtn, icNewFolder, false, connected, "新建文件夹"), gap,
			btn(&fv.uploadBtn, icUpload, false, connected, "上传"), gap,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				d := th.iconButton(gtx, &fv.tasksBtn, icTasks, 28, 17, th.Text2, fv.showTasks, "任务")
				// A dot marks running tasks while the list is closed.
				if active > 0 {
					s := gtx.Dp(7)
					fillRR(gtx.Ops, image.Rect(d.Size.X-s-gtx.Dp(2), gtx.Dp(2), d.Size.X-gtx.Dp(2), gtx.Dp(2)+s), s/2, th.Blue)
				}
				return d
			}),
		)
	})
	fill(gtx.Ops, image.Rect(0, h-max(gtx.Dp(1), 1), gtx.Constraints.Max.X, h), th.Border)
	return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, h)}
}

type fileCols struct {
	name, size, time, mode, owner int // widths in px; 0 hides the column
}

func (fv *filesView) columns(gtx layout.Context, w int) fileCols {
	c := fileCols{size: gtx.Dp(90), time: gtx.Dp(140)}
	if w > gtx.Dp(620) {
		c.mode = gtx.Dp(100)
	}
	if w > gtx.Dp(800) {
		c.owner = gtx.Dp(130)
	}
	c.name = max(w-c.size-c.time-c.mode-c.owner-gtx.Dp(14), gtx.Dp(120))
	return c
}

func (fv *filesView) fileList(gtx layout.Context) layout.Dimensions {
	a := fv.a
	th := a.th
	size := gtx.Constraints.Max
	cols := fv.columns(gtx, size.X)
	rowH := gtx.Dp(fileRowH)
	hdrH := gtx.Dp(26)

	// Pointer handling for the rows area.
	listH := size.Y - hdrH
	for {
		e, ok := gtx.Event(pointer.Filter{Target: &fv.listTag, Kinds: pointer.Press | pointer.Move | pointer.Leave})
		if !ok {
			break
		}
		pe, ok := e.(pointer.Event)
		if !ok {
			continue
		}
		idx := fv.list.Position.First + (int(pe.Position.Y)+fv.list.Position.Offset)/rowH
		if idx < 0 || idx >= len(fv.view) || pe.Position.Y < 0 {
			idx = -1
		}
		switch pe.Kind {
		case pointer.Leave:
			fv.hover = -1
		case pointer.Move:
			fv.hover = idx
		case pointer.Press:
			gtx.Execute(key.FocusCmd{Tag: fv})
			fv.focused = true
			switch {
			case pe.Buttons.Contain(pointer.ButtonSecondary):
				if idx >= 0 {
					if en := fv.entry(idx); en != nil && !fv.selected[en.Name] {
						fv.selectOnly(idx)
					}
					fv.fileMenu()
				} else {
					fv.selected = map[string]bool{}
					fv.bgMenu()
				}
			case pe.Buttons.Contain(pointer.ButtonPrimary):
				if idx < 0 {
					fv.selected = map[string]bool{}
					fv.cursor = -1
					break
				}
				double := idx == fv.lastHit && gtx.Now.Sub(fv.lastTime) < 450*time.Millisecond
				fv.lastHit, fv.lastTime = idx, gtx.Now
				en := fv.entry(idx)
				switch {
				case pe.Modifiers.Contain(key.ModCtrl):
					fv.selected[en.Name] = !fv.selected[en.Name]
					if !fv.selected[en.Name] {
						delete(fv.selected, en.Name)
					}
					fv.cursor, fv.anchor = idx, idx
				case pe.Modifiers.Contain(key.ModShift) && fv.anchor >= 0:
					fv.selectRange(fv.anchor, idx)
					fv.cursor = idx
				case double:
					fv.lastHit = -1
					fv.open(*en)
				default:
					fv.selectOnly(idx)
				}
			}
		}
	}

	// Header.
	hx := 0
	head := func(i int, label string, w int, right bool) {
		if w == 0 {
			return
		}
		st := op.Offset(image.Pt(hx, 0)).Push(gtx.Ops)
		cg := gtx
		cg.Constraints = layout.Exact(image.Pt(w, hdrH))
		fv.hdr[i].Layout(cg, func(gtx layout.Context) layout.Dimensions {
			c := th.Text3
			if fv.hdr[i].Hovered() {
				c = th.Text2
			}
			l := label
			if fv.sortCol == i {
				c = th.Text2
				if fv.sortDesc {
					l += " ↓"
				} else {
					l += " ↑"
				}
			}
			in := layout.Inset{Left: 12, Right: 12}
			if i == colName {
				in.Left = 38
			}
			return in.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				dir := layout.W
				if right {
					dir = layout.E
				}
				return dir.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min = image.Point{}
					return th.txtW(gtx, l, 11, c, font.SemiBold)
				})
			})
		})
		st.Pop()
		hx += w
	}
	head(colName, "名称", cols.name, false)
	head(colSize, "大小", cols.size, true)
	head(colTime, "修改时间", cols.time, false)
	head(colMode, "权限", cols.mode, false)
	head(colOwner, "所有者", cols.owner, false)
	fill(gtx.Ops, image.Rect(0, hdrH-max(gtx.Dp(1), 1), size.X, hdrH), th.Border)

	body := op.Offset(image.Pt(0, hdrH)).Push(gtx.Ops)
	area := clip.Rect{Max: image.Pt(size.X, listH)}.Push(gtx.Ops)
	event.Op(gtx.Ops, &fv.listTag)
	event.Op(gtx.Ops, fv)

	switch {
	case fv.errMsg != "" && len(fv.entries) == 0:
		fv.message(gtx, listH, icError, th.Danger, fv.errMsg)
	case fv.path == "":
		msg := "未连接"
		if fv.loading {
			msg = "加载中…"
		}
		fv.message(gtx, listH, icFolder, th.Text3, msg)
	case len(fv.view) == 0 && !fv.loading:
		fv.message(gtx, listH, icFolder, th.Text3, "空文件夹")
	default:
		lg := gtx
		lg.Constraints = layout.Exact(image.Pt(size.X, listH))
		th.list(lg, &fv.list, len(fv.view), func(gtx layout.Context, i int) layout.Dimensions {
			return fv.row(gtx, i, cols, rowH)
		})
	}
	area.Pop()
	body.Pop()
	return layout.Dimensions{Size: size}
}

func (fv *filesView) message(gtx layout.Context, h int, ic *widget.Icon, c color.NRGBA, msg string) {
	th := fv.a.th
	gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, h))
	layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min = image.Point{}
		return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return drawIcon(gtx, ic, 28, alpha(c, 0xaa)) }),
			vspace(8),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.txt(gtx, msg, 12, th.Text3) }),
		)
	})
}

func (fv *filesView) row(gtx layout.Context, i int, cols fileCols, rowH int) layout.Dimensions {
	th := fv.a.th
	e := fv.entry(i)
	w := gtx.Constraints.Max.X
	size := image.Pt(w, rowH)
	if e == nil {
		return layout.Dimensions{Size: size}
	}
	sel := fv.selected[e.Name]
	r := image.Rect(gtx.Dp(4), 0, w-gtx.Dp(4), rowH)
	switch {
	case sel && fv.focused:
		fillRR(gtx.Ops, r, gtx.Dp(5), alpha(th.Accent, 0x16))
	case sel:
		fillRR(gtx.Ops, r, gtx.Dp(5), th.Bg4)
	case fv.hover == i:
		fillRR(gtx.Ops, r, gtx.Dp(5), th.Bg2)
	}
	cell := func(x, cw int, right bool, wd layout.Widget) {
		if cw <= 0 {
			return
		}
		st := op.Offset(image.Pt(x, 0)).Push(gtx.Ops)
		cg := gtx
		cg.Constraints = layout.Exact(image.Pt(cw, rowH))
		layout.Inset{Left: 12, Right: 12}.Layout(cg, func(gtx layout.Context) layout.Dimensions {
			dir := layout.W
			if right {
				dir = layout.E
			}
			return dir.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min = image.Point{}
				return wd(gtx)
			})
		})
		st.Pop()
	}
	ic, icc, nc := icFile, th.Text3, th.Text
	switch {
	case e.IsDir:
		ic, icc = icFolder, th.Blue
	case e.Mode&0o111 != 0:
		icc, nc = th.Accent, mix(th.Text, th.Accent, 0.35)
	}
	if e.IsLink {
		ic, icc = icLink, th.Purple
		if e.IsDir {
			nc = mix(th.Text, th.Blue, 0.3)
		}
	}
	if strings.HasPrefix(e.Name, ".") {
		nc = mix(nc, th.Bg1, 0.35)
	}
	cell(0, cols.name, false, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return drawIcon(gtx, ic, 16, icc) }),
			hspace(10),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return th.txt(gtx, e.Name, 13, nc) }),
		)
	})
	x := cols.name
	cell(x, cols.size, true, func(gtx layout.Context) layout.Dimensions {
		if e.IsDir {
			return th.txt(gtx, "—", 12, th.Text3)
		}
		return th.txt(gtx, fmtBytes(uint64(max(e.Size, 0))), 12, th.Text2)
	})
	x += cols.size
	cell(x, cols.time, false, func(gtx layout.Context) layout.Dimensions {
		return th.txt(gtx, fmtModTime(e.ModTime), 12, th.Text2)
	})
	x += cols.time
	cell(x, cols.mode, false, func(gtx layout.Context) layout.Dimensions {
		return th.mono(gtx, permString(*e), 12, th.Text3)
	})
	x += cols.mode
	cell(x, cols.owner, false, func(gtx layout.Context) layout.Dimensions {
		o := e.Owner
		if e.Group != "" && e.Group != e.Owner {
			o += ":" + e.Group
		}
		return th.txt(gtx, o, 12, th.Text3)
	})
	return layout.Dimensions{Size: size}
}
