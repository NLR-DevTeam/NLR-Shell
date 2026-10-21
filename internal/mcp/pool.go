package mcp

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"

	"nlrshell/internal/sshx"
	"nlrshell/internal/store"
)

// idleTimeout closes connections that no tool used for a while.
const idleTimeout = 10 * time.Minute

// pool keeps one connection per saved profile so that consecutive tool
// calls do not log in again each time.
type pool struct {
	mu    sync.Mutex
	conns map[string]*pooled
}

type pooled struct {
	profile  store.Profile
	trustNew bool
	ready    chan struct{} // closed when dialing has finished
	conn     *sshx.Conn
	err      error
	dead     chan struct{} // closed when the connection drops

	// busy and used are guarded by pool.mu.
	busy int
	used time.Time

	mu sync.Mutex
	sc *sftp.Client
}

func newPool() *pool {
	pl := &pool{conns: map[string]*pooled{}}
	go pl.reap()
	return pl
}

// get returns a connection to p, dialing it if needed. The caller must
// release it when done.
func (pl *pool) get(ctx context.Context, p store.Profile, st *store.Store, trustNew bool) (*pooled, error) {
	pl.mu.Lock()
	e := pl.conns[p.ID]
	if e != nil && (e.failed() || !sameProfile(e.profile, p) || e.trustNew != trustNew) {
		delete(pl.conns, p.ID)
		if e.busy == 0 {
			e.close()
		}
		e = nil
	}
	if e == nil {
		e = dial(p, st, trustNew)
		pl.conns[p.ID] = e
	}
	e.busy++
	pl.mu.Unlock()

	select {
	case <-e.ready:
	case <-ctx.Done():
		pl.release(e)
		return nil, ctx.Err()
	}
	if e.err != nil {
		pl.release(e)
		return nil, e.err
	}
	return e, nil
}

func (pl *pool) release(e *pooled) {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	e.busy--
	e.used = time.Now()
	// Replaced or failed while in use: nobody else will close it.
	if e.busy == 0 && pl.conns[e.profile.ID] != e {
		e.close()
	}
}

func (pl *pool) reap() {
	for range time.Tick(time.Minute) {
		pl.mu.Lock()
		for id, e := range pl.conns {
			if e.busy == 0 && (e.failed() || e.isReady() && time.Since(e.used) > idleTimeout) {
				delete(pl.conns, id)
				e.close()
			}
		}
		pl.mu.Unlock()
	}
}

// closeAll closes every connection; the pool stays usable.
func (pl *pool) closeAll() {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	for id, e := range pl.conns {
		delete(pl.conns, id)
		e.close()
	}
}

// sameProfile reports whether a connection made for a still matches the
// profile as now saved, so that edits in the app take effect.
func sameProfile(a, b store.Profile) bool {
	a.LastUsed, b.LastUsed = 0, 0
	return reflect.DeepEqual(a, b)
}

func dial(p store.Profile, st *store.Store, trustNew bool) *pooled {
	e := &pooled{profile: p, trustNew: trustNew, ready: make(chan struct{}), dead: make(chan struct{}), used: time.Now()}
	go func() {
		defer close(e.ready)
		pr := &prompter{trustNew: trustNew}
		c, err := sshx.Dial(p, st, pr)
		if err != nil {
			if why := pr.reason(); why != "" {
				err = errors.New(why)
			}
			e.err = fmt.Errorf("cannot connect to %s (%s): %w", p.Title(), p.Addr(), err)
			return
		}
		e.conn = c
		go func() {
			c.Wait()
			close(e.dead)
		}()
	}()
	return e
}

func (e *pooled) isReady() bool {
	select {
	case <-e.ready:
		return true
	default:
		return false
	}
}

// failed reports whether dialing failed or the connection has dropped.
func (e *pooled) failed() bool {
	if !e.isReady() {
		return false
	}
	if e.err != nil {
		return true
	}
	select {
	case <-e.dead:
		return true
	default:
		return false
	}
}

// close closes the connection once dialing has finished.
func (e *pooled) close() {
	go func() {
		<-e.ready
		if e.conn == nil {
			return
		}
		e.mu.Lock()
		if e.sc != nil {
			e.sc.Close()
			e.sc = nil
		}
		e.mu.Unlock()
		e.conn.Close()
	}()
}

// sftp returns the SFTP client of the connection, starting it on first use.
func (e *pooled) sftp() (*sftp.Client, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sc != nil {
		return e.sc, nil
	}
	sc, err := sftp.NewClient(e.conn.Client, sftp.UseConcurrentWrites(true), sftp.MaxConcurrentRequestsPerFile(32))
	if err != nil {
		return nil, fmt.Errorf("cannot start SFTP: %w", err)
	}
	e.sc = sc
	return sc, nil
}

// prompter answers connection prompts without a person. Secrets must be
// saved in the profile. A host seen for the first time is trusted if the
// settings allow it, since the user saved its profile, but a changed host
// key is always refused: it may be a man-in-the-middle and needs a person
// to review it in the app.
type prompter struct {
	trustNew bool

	mu      sync.Mutex
	refused string
}

func (p *prompter) refuse(why string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.refused == "" {
		p.refused = why
	}
}

func (p *prompter) reason() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.refused
}

func (p *prompter) HostKey(host, keyType, fingerprint string, changed bool) bool {
	if changed {
		p.refuse(fmt.Sprintf("the host key of %s has changed (now %s %s); this may be a man-in-the-middle attack. Ask the user to connect in NLR Shell and review the key", host, keyType, fingerprint))
		return false
	}
	if !p.trustNew {
		p.refuse(fmt.Sprintf("the host key of %s (%s %s) is not known yet; ask the user to connect once in NLR Shell, or to allow new host keys in %s", host, keyType, fingerprint, settingsHint))
		return false
	}
	return true
}

func (p *prompter) Password(title string) (string, bool, bool) {
	p.refuse("no working password is saved for " + title + "; ask the user to connect once in NLR Shell and choose to remember the password")
	return "", false, false
}

func (p *prompter) Passphrase(keyPath string) (string, bool) {
	p.refuse("the private key " + keyPath + " needs a passphrase that is not saved in the profile; ask the user to save it in NLR Shell")
	return "", false
}

func (p *prompter) Interactive(title, instruction string, questions []string, echo []bool) ([]string, bool) {
	p.refuse(fmt.Sprintf("%s asks for interactive authentication (%s), which needs the user; ask them to connect in NLR Shell instead", title, strings.Join(questions, " / ")))
	return nil, false
}
