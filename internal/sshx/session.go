package sshx

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"nlrshell/internal/monitor"
	"nlrshell/internal/store"
	"nlrshell/internal/term"
)

// State is the connection state of a session.
type State int

const (
	StateConnecting State = iota
	StateConnected
	StateClosed
)

// HistoryLen is the number of samples kept for the monitor charts.
const HistoryLen = 90

// MonitorData is a copy of the monitoring state for rendering.
type MonitorData struct {
	Ready       bool
	Unsupported bool
	Static      monitor.Static
	Snap        monitor.Snapshot
	CPU, Mem    []float64 // percent history, oldest first
	Rx, Tx      []float64 // bytes/s history
}

// Session is one SSH connection with its terminal and side channels.
type Session struct {
	ID      string
	Profile store.Profile
	Term    *term.Terminal

	st       *store.Store
	prompter Prompter
	notify   func()

	mu     sync.Mutex
	state  State
	err    error
	status string
	dialer *dialer
	client *ssh.Client
	chain  []*ssh.Client
	shell  *ssh.Session
	stdin  io.WriteCloser
	cols   int
	rows   int
	closed bool
	cwd    string
	cwdSeq int
	// markerOK is set when the server accepted LC_NLRSHELL for the shell.
	markerOK bool
	title    string
	bell     time.Time
	clip     string
	clipSeq  int
	connTime time.Time

	q *queue

	monMu    sync.Mutex
	mon      MonitorData
	monIn    io.WriteCloser
	monKick  chan struct{}
	interval time.Duration

	sftpMu  sync.Mutex
	sftpc   *sftp.Client
	sftpErr error
	names   *nameCache

	Transfers *Transfers
	Forwards  *Forwards
}

// NewSession creates a session for p. notify is called (from any goroutine)
// whenever something the UI displays has changed.
func NewSession(p store.Profile, st *store.Store, prompter Prompter, notify func()) *Session {
	s := &Session{
		ID:       store.NewID(),
		Profile:  p,
		st:       st,
		prompter: prompter,
		notify:   notify,
		cols:     80,
		rows:     24,
		q:        newQueue(),
		monKick:  make(chan struct{}, 1),
	}
	set := st.Settings()
	s.interval = time.Duration(set.MonitorInterval) * time.Second
	s.Term = term.New(s.cols, s.rows, set.Scrollback, term.Handler{
		Reply: func(b []byte) { s.Write(b) },
		Title: func(t string) { s.mu.Lock(); s.title = t; s.mu.Unlock() },
		Cwd:   func(d string) { s.setCwd(d) },
		Bell:  func() { s.mu.Lock(); s.bell = time.Now(); s.mu.Unlock() },
		Clipboard: func(text string) {
			s.mu.Lock()
			s.clip = text
			s.clipSeq++
			s.mu.Unlock()
		},
	})
	s.Transfers = newTransfers(s)
	s.Forwards = newForwards(s)
	return s
}

// Start begins connecting in the background.
func (s *Session) Start() {
	s.mu.Lock()
	s.state, s.err, s.closed = StateConnecting, nil, false
	s.status = "正在连接 …"
	d := &dialer{st: s.st, prompter: s.prompter, status: s.setStatus}
	s.dialer = d
	s.mu.Unlock()
	go s.run(d)
}

// Reconnect restarts a closed session, keeping the terminal contents.
func (s *Session) Reconnect() {
	s.mu.Lock()
	if s.state != StateClosed {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	if p, ok := s.st.Profile(s.Profile.ID); ok {
		s.Profile = p
	}
	s.q = newQueue()
	s.Term.Write([]byte("\r\n\x1b[0m\x1b[?25h\x1b[?1049l\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?2004l"))
	s.Start()
}

func (s *Session) setStatus(msg string) {
	s.mu.Lock()
	s.status = msg
	s.mu.Unlock()
	s.notify()
}

// Info returns the connection state for display.
func (s *Session) Info() (state State, status string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, s.status, s.err
}

// State returns the connection state.
func (s *Session) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Title returns the title set by the remote shell, if any.
func (s *Session) Title() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.title
}

// Cwd returns the last known working directory of the shell and a counter
// that increments every time it changes.
func (s *Session) Cwd() (string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cwd, s.cwdSeq
}

// Clipboard returns the last text the remote application placed on the
// clipboard (OSC 52) and a counter that increments with each request.
func (s *Session) Clipboard() (string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clip, s.clipSeq
}

// ConnectedAt returns when the connection was established.
func (s *Session) ConnectedAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connTime
}

func (s *Session) setCwd(dir string) {
	if dir == "" {
		return
	}
	s.mu.Lock()
	changed := dir != s.cwd
	if changed {
		s.cwd = dir
		s.cwdSeq++
	}
	s.mu.Unlock()
	if changed {
		s.notify()
	}
}

func (s *Session) fail(err error) {
	s.mu.Lock()
	if s.closed && err != nil {
		err = nil
	}
	s.state, s.err = StateClosed, err
	chain := s.chain
	s.client, s.chain, s.shell, s.stdin = nil, nil, nil, nil
	s.mu.Unlock()
	closeClients(chain)
	s.q.close()
	s.closeSFTP()
	s.Forwards.stopAll()
	s.Transfers.abortAll()
	s.notify()
}

func (s *Session) run(d *dialer) {
	chain, err := d.dial(s.Profile, 0)
	if err != nil {
		s.fail(err)
		return
	}
	client := chain[0]

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		closeClients(chain)
		s.fail(nil)
		return
	}
	s.client, s.chain = client, chain
	cols, rows := s.cols, s.rows
	s.mu.Unlock()
	s.setStatus("正在打开终端 …")

	sh, err := client.NewSession()
	if err != nil {
		s.fail(friendly(err))
		return
	}
	// The marker lets the monitor find this shell among the remote
	// processes. Servers that do not accept LC_* variables refuse it; the
	// monitor then falls back to guessing from the process tree.
	markerOK := sh.Setenv("LC_NLRSHELL", s.ID) == nil
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.IUTF8: 1, ssh.TTY_OP_ISPEED: 115200, ssh.TTY_OP_OSPEED: 115200}
	if err := sh.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		s.fail(friendly(err))
		return
	}
	stdin, _ := sh.StdinPipe()
	stdout, _ := sh.StdoutPipe()
	stderr, _ := sh.StderrPipe()
	if err := sh.Shell(); err != nil {
		s.fail(friendly(err))
		return
	}

	s.mu.Lock()
	s.shell, s.stdin = sh, stdin
	s.markerOK = markerOK
	s.state, s.status = StateConnected, ""
	s.connTime = time.Now()
	// The window may have been resized while we were connecting.
	resized := s.cols != cols || s.rows != rows
	cols, rows = s.cols, s.rows
	s.mu.Unlock()
	if resized {
		sh.WindowChange(rows, cols)
	}
	if s.Profile.ID != "" {
		s.st.Touch(s.Profile.ID)
	}
	s.notify()

	go s.q.run()
	go s.keepAlive(client)
	go s.runMonitor(client)
	for _, f := range s.Profile.Forwards {
		if f.AutoStart {
			s.Forwards.Start(f)
		}
	}

	var wg sync.WaitGroup
	pump := func(r io.Reader) {
		defer wg.Done()
		buf := make([]byte, 32*1024)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				s.Term.Write(buf[:n])
				s.notify()
			}
			if err != nil {
				return
			}
		}
	}
	wg.Add(2)
	go pump(stdout)
	go pump(stderr)
	werr := sh.Wait()
	wg.Wait()

	var exitErr *ssh.ExitError
	var missing *ssh.ExitMissingError
	switch {
	case werr == nil, errors.As(werr, &exitErr):
		s.fail(nil)
	case errors.As(werr, &missing), errors.Is(werr, io.EOF):
		s.fail(errors.New("连接已断开"))
	default:
		s.fail(friendly(werr))
	}
}

func (s *Session) keepAlive(c *ssh.Client) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	done := make(chan struct{})
	go func() { c.Wait(); close(done) }()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			if _, _, err := c.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				c.Close()
				return
			}
		}
	}
}

// Close terminates the session.
func (s *Session) Close() {
	s.mu.Lock()
	s.closed = true
	d, chain := s.dialer, s.chain
	s.mu.Unlock()
	if d != nil {
		d.abort()
	}
	closeClients(chain)
}

// Write sends input to the remote shell.
func (s *Session) Write(b []byte) {
	if len(b) == 0 {
		return
	}
	s.mu.Lock()
	w := s.stdin
	s.mu.Unlock()
	if w == nil {
		return
	}
	data := append([]byte(nil), b...)
	s.q.push(func() { w.Write(data) })
}

// Resize changes the terminal size.
func (s *Session) Resize(cols, rows int) {
	s.mu.Lock()
	if cols == s.cols && rows == s.rows {
		s.mu.Unlock()
		return
	}
	s.cols, s.rows = cols, rows
	sh := s.shell
	s.mu.Unlock()
	s.Term.Resize(cols, rows)
	if sh != nil {
		s.q.push(func() { sh.WindowChange(rows, cols) })
	}
}

// Client returns the SSH client, or nil when not connected.
func (s *Session) Client() *ssh.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client
}

// Exec runs a command on the remote host and returns its combined output.
func (s *Session) Exec(cmd string) (string, error) {
	c := s.Client()
	if c == nil {
		return "", errors.New("未连接")
	}
	sess, err := c.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	var out bytes.Buffer
	sess.Stdout, sess.Stderr = &out, &out
	err = sess.Run(cmd)
	return strings.TrimSpace(out.String()), err
}

// ---- Monitor ----------------------------------------------------------

func (s *Session) runMonitor(c *ssh.Client) {
	sess, err := c.NewSession()
	if err != nil {
		return
	}
	defer sess.Close()
	stdin, _ := sess.StdinPipe()
	stdout, _ := sess.StdoutPipe()
	if err := sess.Start("exec sh"); err != nil {
		s.monMu.Lock()
		s.mon.Unsupported = true
		s.monMu.Unlock()
		return
	}
	if _, err := io.WriteString(stdin, monitor.Script); err != nil {
		return
	}

	p := monitor.NewParser()
	s.mu.Lock()
	if s.markerOK {
		p.Marker = s.ID
	}
	s.mu.Unlock()
	var pmu sync.Mutex
	ready := make(chan struct{})
	stop := make(chan struct{})
	defer close(stop)

	s.monMu.Lock()
	s.mon = MonitorData{}
	s.monIn = stdin
	s.monMu.Unlock()

	go func() {
		select {
		case <-ready:
		case <-stop:
			return
		}
		tick := func() bool {
			pmu.Lock()
			line := p.TickLine()
			pmu.Unlock()
			_, err := io.WriteString(stdin, line)
			return err == nil
		}
		// Two quick samples so that rates are available right away.
		if !tick() {
			return
		}
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		for {
			select {
			case <-stop:
				return
			case <-s.monKick:
			case <-timer.C:
			}
			if !tick() {
				return
			}
			s.monMu.Lock()
			d := s.interval
			s.monMu.Unlock()
			if d < time.Second {
				d = time.Second
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(d)
		}
	}()

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	signaled := false
	for sc.Scan() {
		pmu.Lock()
		snap, ok := p.Feed(sc.Text())
		isReady := p.Ready
		static := p.Static
		pmu.Unlock()
		if isReady && !signaled {
			signaled = true
			close(ready)
			s.monMu.Lock()
			s.mon.Static = static
			s.monMu.Unlock()
		}
		if !ok {
			continue
		}
		s.monMu.Lock()
		m := &s.mon
		m.Ready = true
		m.Unsupported = !snap.Supported
		m.Snap = *snap
		memPct := 0.0
		if snap.MemTotal > 0 {
			memPct = float64(snap.MemUsed) / float64(snap.MemTotal) * 100
		}
		m.CPU = pushHist(m.CPU, snap.CPU)
		m.Mem = pushHist(m.Mem, memPct)
		m.Rx = pushHist(m.Rx, snap.RxRate)
		m.Tx = pushHist(m.Tx, snap.TxRate)
		s.monMu.Unlock()
		if snap.Cwd != "" {
			s.setCwd(snap.Cwd)
		}
		s.notify()
	}
	if !signaled {
		s.monMu.Lock()
		s.mon.Unsupported = true
		s.monMu.Unlock()
		s.notify()
	}
}

func pushHist(h []float64, v float64) []float64 {
	if len(h) >= HistoryLen {
		copy(h, h[1:])
		h = h[:len(h)-1]
	}
	return append(h, v)
}

// Monitor returns a copy of the current monitoring data.
func (s *Session) Monitor() MonitorData {
	s.monMu.Lock()
	defer s.monMu.Unlock()
	m := s.mon
	m.CPU = append([]float64(nil), m.CPU...)
	m.Mem = append([]float64(nil), m.Mem...)
	m.Rx = append([]float64(nil), m.Rx...)
	m.Tx = append([]float64(nil), m.Tx...)
	return m
}

// SetMonitorInterval changes the sampling interval.
func (s *Session) SetMonitorInterval(d time.Duration) {
	s.monMu.Lock()
	s.interval = d
	s.monMu.Unlock()
	s.RefreshMonitor()
}

// RefreshMonitor requests a sample right away.
func (s *Session) RefreshMonitor() {
	select {
	case s.monKick <- struct{}{}:
	default:
	}
}

// ---- Serial task queue ------------------------------------------------

// queue runs functions one at a time on a dedicated goroutine so that
// callers (the UI thread) never block on the network.
type queue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	items  []func()
	closed bool
}

func newQueue() *queue {
	q := &queue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *queue) push(f func()) {
	q.mu.Lock()
	if !q.closed {
		q.items = append(q.items, f)
		q.cond.Signal()
	}
	q.mu.Unlock()
}

func (q *queue) close() {
	q.mu.Lock()
	q.closed = true
	q.items = nil
	q.cond.Signal()
	q.mu.Unlock()
}

func (q *queue) run() {
	for {
		q.mu.Lock()
		for len(q.items) == 0 && !q.closed {
			q.cond.Wait()
		}
		if q.closed {
			q.mu.Unlock()
			return
		}
		items := q.items
		q.items = nil
		q.mu.Unlock()
		for _, f := range items {
			f()
		}
	}
}
