package sshx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
)

// TransferState is the lifecycle state of a transfer.
type TransferState int32

const (
	TransferQueued TransferState = iota
	TransferRunning
	TransferDone
	TransferFailed
	TransferCanceled
)

// TransferInfo is a snapshot of a transfer for display.
type TransferInfo struct {
	ID     int
	Upload bool
	Name   string
	Local  string // local file or directory
	Remote string
	Total  int64
	Done   int64
	Rate   float64 // bytes per second
	State  TransferState
	Err    string
	// Temp marks a download into the temporary directory made to view or
	// open a file; it leaves the list by itself once it has finished.
	Temp bool
}

type transfer struct {
	id     int
	upload bool
	temp   bool
	name   string
	local  string
	remote string
	total  atomic.Int64
	done   atomic.Int64
	state  atomic.Int32
	err    string
	cancel context.CancelFunc

	// Rate estimation, touched only by Transfers.List.
	lastDone int64
	lastTime time.Time
	rate     float64
}

// Transfers runs uploads and downloads for one session, a few at a time.
type Transfers struct {
	s   *Session
	sem chan struct{}

	mu     sync.Mutex
	list   []*transfer
	nextID int
	active int
	// OnDone, if set, is called after each transfer finishes.
	OnDone func(TransferInfo)
}

func newTransfers(s *Session) *Transfers {
	return &Transfers{s: s, sem: make(chan struct{}, 3)}
}

// List returns all transfers, newest first.
func (t *Transfers) List() []TransferInfo {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	out := make([]TransferInfo, 0, len(t.list))
	for i := len(t.list) - 1; i >= 0; i-- {
		tr := t.list[i]
		done := tr.done.Load()
		state := TransferState(tr.state.Load())
		if state == TransferRunning {
			if dt := now.Sub(tr.lastTime).Seconds(); dt >= 0.5 {
				inst := float64(done-tr.lastDone) / dt
				if tr.lastTime.IsZero() {
					inst = 0
				}
				if tr.rate == 0 {
					tr.rate = inst
				} else {
					tr.rate = tr.rate*0.6 + inst*0.4
				}
				tr.lastDone, tr.lastTime = done, now
			}
		} else {
			tr.rate = 0
		}
		out = append(out, tr.info(done, state))
	}
	return out
}

func (tr *transfer) info(done int64, state TransferState) TransferInfo {
	return TransferInfo{ID: tr.id, Upload: tr.upload, Name: tr.name, Local: tr.local, Remote: tr.remote,
		Total: tr.total.Load(), Done: done, Rate: tr.rate, State: state, Err: tr.err, Temp: tr.temp}
}

// Seq returns a counter that increases with every transfer started.
func (t *Transfers) Seq() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.nextID
}

// Active returns the number of queued or running transfers.
func (t *Transfers) Active() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.active
}

// Cancel stops a transfer.
func (t *Transfers) Cancel(id int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, tr := range t.list {
		if tr.id == id {
			tr.cancel()
		}
	}
}

// Clear removes finished transfers from the list.
func (t *Transfers) Clear() {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.list[:0]
	for _, tr := range t.list {
		if st := TransferState(tr.state.Load()); st == TransferQueued || st == TransferRunning {
			out = append(out, tr)
		}
	}
	t.list = out
}

func (t *Transfers) abortAll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, tr := range t.list {
		tr.cancel()
	}
}

// Upload copies a local file or directory into remoteDir.
func (t *Transfers) Upload(local, remoteDir string) {
	name := filepath.Base(local)
	t.start(&transfer{upload: true, name: name, local: local, remote: path.Join(remoteDir, name)})
}

// UploadAs copies a local file to an exact remote path, overwriting it.
func (t *Transfers) UploadAs(local, remote string) {
	t.start(&transfer{upload: true, name: path.Base(remote), local: local, remote: remote})
}

// SafeName returns name with characters that the local file system does not
// allow in file names replaced.
func SafeName(name string) string { return safeName(name) }

// Download copies a remote file or directory into localDir. If a file of
// the same name exists a numbered name is used. It returns the local path.
func (t *Transfers) Download(remote, localDir string) string {
	name := path.Base(remote)
	// Names claimed by transfers that have not created their file yet count
	// as taken too.
	t.mu.Lock()
	local := uniqueLocal(filepath.Join(localDir, safeName(name)), func(p string) bool {
		for _, tr := range t.list {
			if st := TransferState(tr.state.Load()); !tr.upload && tr.local == p && (st == TransferQueued || st == TransferRunning) {
				return true
			}
		}
		return false
	})
	t.mu.Unlock()
	t.start(&transfer{name: name, local: local, remote: remote})
	return local
}

// DownloadTo copies a remote file to an exact local path, overwriting it.
func (t *Transfers) DownloadTo(remote, local string) {
	t.start(&transfer{name: path.Base(remote), local: local, remote: remote})
}

// DownloadTemp is DownloadTo for a temporary copy: the transfer is removed
// from the list as soon as it completes or is canceled.
func (t *Transfers) DownloadTemp(remote, local string) {
	t.start(&transfer{name: path.Base(remote), local: local, remote: remote, temp: true})
}

func (t *Transfers) start(tr *transfer) {
	ctx, cancel := context.WithCancel(context.Background())
	tr.cancel = cancel
	t.mu.Lock()
	t.nextID++
	tr.id = t.nextID
	t.list = append(t.list, tr)
	t.active++
	first := t.active == 1
	t.mu.Unlock()
	if first {
		go t.ticker()
	}
	go func() {
		var err error
		select {
		case t.sem <- struct{}{}:
			tr.state.Store(int32(TransferRunning))
			if tr.upload {
				err = t.upload(ctx, tr)
			} else {
				err = t.download(ctx, tr)
			}
			<-t.sem
		case <-ctx.Done():
			err = ctx.Err()
		}
		state := TransferDone
		switch {
		case errors.Is(err, context.Canceled) || ctx.Err() != nil && err != nil:
			state = TransferCanceled
		case err != nil:
			state = TransferFailed
		}
		cancel()
		t.mu.Lock()
		if state == TransferFailed {
			tr.err = fsErr(err).Error()
		}
		tr.state.Store(int32(state))
		t.active--
		if tr.temp && state != TransferFailed {
			for i, x := range t.list {
				if x == tr {
					t.list = append(t.list[:i], t.list[i+1:]...)
					break
				}
			}
		}
		info := tr.info(tr.done.Load(), state)
		cb := t.OnDone
		t.mu.Unlock()
		if cb != nil {
			cb(info)
		}
		t.s.notify()
	}()
	t.s.notify()
}

// ticker refreshes the UI while transfers are in flight.
func (t *Transfers) ticker() {
	tk := time.NewTicker(250 * time.Millisecond)
	defer tk.Stop()
	for range tk.C {
		if t.Active() == 0 {
			return
		}
		t.s.notify()
	}
}

type progressReader struct {
	r    io.Reader
	tr   *transfer
	ctx  context.Context
	size int64
}

func (p *progressReader) Read(b []byte) (int, error) {
	if err := p.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := p.r.Read(b)
	p.tr.done.Add(int64(n))
	return n, err
}

// Size lets pkg/sftp use concurrent writes.
func (p *progressReader) Size() int64 { return p.size }

type progressWriter struct {
	w   io.Writer
	tr  *transfer
	ctx context.Context
}

func (p *progressWriter) Write(b []byte) (int, error) {
	if err := p.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := p.w.Write(b)
	p.tr.done.Add(int64(n))
	return n, err
}

func (t *Transfers) upload(ctx context.Context, tr *transfer) error {
	c, err := t.s.SFTP()
	if err != nil {
		return err
	}
	fi, err := os.Stat(tr.local)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		tr.total.Store(fi.Size())
		return uploadFile(ctx, c, tr, tr.local, tr.remote, fi.Size())
	}
	type item struct {
		rel  string
		dir  bool
		size int64
	}
	var items []item
	err = filepath.WalkDir(tr.local, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel, _ := filepath.Rel(tr.local, p)
		if d.IsDir() {
			items = append(items, item{rel: rel, dir: true})
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		items = append(items, item{rel: rel, size: info.Size()})
		tr.total.Add(info.Size())
		return nil
	})
	if err != nil {
		return err
	}
	for _, it := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rp := path.Join(tr.remote, filepath.ToSlash(it.rel))
		if it.dir {
			if err := c.MkdirAll(rp); err != nil {
				return fmt.Errorf("%s: %w", it.rel, fsErr(err))
			}
			continue
		}
		if err := uploadFile(ctx, c, tr, filepath.Join(tr.local, it.rel), rp, it.size); err != nil {
			return fmt.Errorf("%s: %w", it.rel, fsErr(err))
		}
	}
	return nil
}

func uploadFile(ctx context.Context, c *sftp.Client, tr *transfer, local, remote string, size int64) error {
	src, err := os.Open(local)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := c.OpenFile(remote, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return err
	}
	_, err = dst.ReadFrom(&progressReader{r: src, tr: tr, ctx: ctx, size: size})
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil && ctx.Err() != nil {
		c.Remove(remote)
		return ctx.Err()
	}
	return err
}

func (t *Transfers) download(ctx context.Context, tr *transfer) error {
	c, err := t.s.SFTP()
	if err != nil {
		return err
	}
	fi, err := c.Stat(tr.remote)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		tr.total.Store(fi.Size())
		return downloadFile(ctx, c, tr, tr.remote, tr.local, fi)
	}
	type item struct {
		rel string
		fi  os.FileInfo
	}
	var items []item
	w := c.Walk(tr.remote)
	for w.Step() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if w.Err() != nil {
			continue
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(w.Path(), tr.remote), "/")
		st := w.Stat()
		if st.IsDir() || st.Mode().IsRegular() {
			items = append(items, item{rel: rel, fi: st})
			if !st.IsDir() {
				tr.total.Add(st.Size())
			}
		}
	}
	for _, it := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		lp := tr.local
		if it.rel != "" {
			parts := strings.Split(it.rel, "/")
			for i := range parts {
				parts[i] = safeName(parts[i])
			}
			lp = filepath.Join(tr.local, filepath.Join(parts...))
		}
		if it.fi.IsDir() {
			if err := os.MkdirAll(lp, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := downloadFile(ctx, c, tr, path.Join(tr.remote, it.rel), lp, it.fi); err != nil {
			return fmt.Errorf("%s: %w", it.rel, fsErr(err))
		}
	}
	return nil
}

func downloadFile(ctx context.Context, c *sftp.Client, tr *transfer, remote, local string, fi os.FileInfo) error {
	src, err := c.Open(remote)
	if err != nil {
		return err
	}
	defer src.Close()
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		return err
	}
	dst, err := os.Create(local)
	if err != nil {
		return err
	}
	_, err = src.WriteTo(&progressWriter{w: dst, tr: tr, ctx: ctx})
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(local)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	os.Chtimes(local, time.Now(), fi.ModTime())
	return nil
}

// uniqueLocal returns p, or p with a numeric suffix if p already exists or
// is reported as taken.
func uniqueLocal(p string, taken func(string) bool) string {
	free := func(c string) bool {
		_, err := os.Lstat(c)
		return err != nil && !taken(c)
	}
	if free(p) {
		return p
	}
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	for i := 1; ; i++ {
		if c := fmt.Sprintf("%s (%d)%s", base, i, ext); free(c) {
			return c
		}
	}
}
