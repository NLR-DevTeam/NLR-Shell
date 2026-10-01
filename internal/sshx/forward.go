package sshx

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"

	"nlrshell/internal/store"
)

// ForwardInfo is the state of one forwarding rule for display.
type ForwardInfo struct {
	store.Forward
	Active bool
	Conns  int
	Err    string
}

type forward struct {
	rule  store.Forward
	ln    net.Listener
	conns atomic.Int32
	err   string

	mu     sync.Mutex
	open   map[net.Conn]struct{}
	closed bool
}

// add registers an accepted connection; it reports false if the forward
// has been stopped in the meantime.
func (fw *forward) add(c net.Conn) bool {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	if fw.closed {
		return false
	}
	if fw.open == nil {
		fw.open = map[net.Conn]struct{}{}
	}
	fw.open[c] = struct{}{}
	return true
}

func (fw *forward) remove(c net.Conn) {
	fw.mu.Lock()
	delete(fw.open, c)
	fw.mu.Unlock()
}

// close stops listening and drops established connections.
func (fw *forward) close() {
	if fw.ln != nil {
		fw.ln.Close()
	}
	fw.mu.Lock()
	fw.closed = true
	for c := range fw.open {
		c.Close()
	}
	fw.mu.Unlock()
}

// Forwards manages port forwarding for one session.
type Forwards struct {
	s  *Session
	mu sync.Mutex
	m  map[string]*forward
}

func newForwards(s *Session) *Forwards {
	return &Forwards{s: s, m: map[string]*forward{}}
}

// List returns the session's forwarding rules (saved in the profile) with
// their live state.
func (f *Forwards) List() []ForwardInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ForwardInfo, 0, len(f.s.Profile.Forwards))
	for _, r := range f.s.Profile.Forwards {
		fi := ForwardInfo{Forward: r}
		if fw, ok := f.m[r.ID]; ok {
			fi.Active = fw.ln != nil
			fi.Conns = int(fw.conns.Load())
			fi.Err = fw.err
		}
		out = append(out, fi)
	}
	return out
}

func listenAddr(r store.Forward) string {
	host := r.ListenHost
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, strconv.Itoa(r.ListenPort))
}

// Start activates a rule.
func (f *Forwards) Start(r store.Forward) error {
	client := f.s.Client()
	if client == nil {
		return errors.New("未连接")
	}
	f.Stop(r.ID)
	fw := &forward{rule: r}
	var err error
	switch r.Kind {
	case store.ForwardLocal, store.ForwardDynamic:
		fw.ln, err = net.Listen("tcp", listenAddr(r))
	case store.ForwardRemote:
		fw.ln, err = client.Listen("tcp", listenAddr(r))
	default:
		err = fmt.Errorf("未知的转发类型 %q", r.Kind)
	}
	f.mu.Lock()
	f.m[r.ID] = fw
	if err != nil {
		fw.err = friendlyListen(err)
		f.mu.Unlock()
		return errors.New(fw.err)
	}
	f.mu.Unlock()

	target := net.JoinHostPort(r.TargetHost, strconv.Itoa(r.TargetPort))
	go func() {
		for {
			conn, err := fw.ln.Accept()
			if err != nil {
				return
			}
			if !fw.add(conn) {
				conn.Close()
				return
			}
			go func() {
				fw.conns.Add(1)
				f.s.notify()
				defer func() {
					conn.Close()
					fw.remove(conn)
					fw.conns.Add(-1)
					f.s.notify()
				}()
				switch r.Kind {
				case store.ForwardLocal:
					if rc, err := client.Dial("tcp", target); err == nil {
						pipe(conn, rc)
					}
				case store.ForwardRemote:
					if rc, err := net.Dial("tcp", target); err == nil {
						pipe(conn, rc)
					}
				case store.ForwardDynamic:
					socks5(conn, func(addr string) (net.Conn, error) { return client.Dial("tcp", addr) })
				}
			}()
		}
	}()
	return nil
}

func friendlyListen(err error) string {
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Op == "listen" {
		return "端口监听失败（可能已被占用）"
	}
	return err.Error()
}

// Stop deactivates a rule.
func (f *Forwards) Stop(id string) {
	f.mu.Lock()
	fw := f.m[id]
	delete(f.m, id)
	f.mu.Unlock()
	if fw != nil {
		fw.close()
	}
}

func (f *Forwards) stopAll() {
	f.mu.Lock()
	m := f.m
	f.m = map[string]*forward{}
	f.mu.Unlock()
	for _, fw := range m {
		fw.close()
	}
}

func pipe(a, b net.Conn) {
	defer b.Close()
	done := make(chan struct{}, 2)
	go func() { io.Copy(a, b); done <- struct{}{} }()
	go func() { io.Copy(b, a); done <- struct{}{} }()
	<-done
	a.Close()
	b.Close()
	<-done
}

// socks5 serves one SOCKS5 CONNECT request on conn.
func socks5(conn net.Conn, dial func(addr string) (net.Conn, error)) {
	buf := make([]byte, 262)
	if _, err := io.ReadFull(conn, buf[:2]); err != nil || buf[0] != 5 {
		return
	}
	if _, err := io.ReadFull(conn, buf[:int(buf[1])]); err != nil {
		return
	}
	if _, err := conn.Write([]byte{5, 0}); err != nil {
		return
	}
	if _, err := io.ReadFull(conn, buf[:4]); err != nil || buf[0] != 5 {
		return
	}
	if buf[1] != 1 {
		conn.Write([]byte{5, 7, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	var host string
	switch buf[3] {
	case 1:
		if _, err := io.ReadFull(conn, buf[:4]); err != nil {
			return
		}
		host = net.IP(buf[:4]).String()
	case 3:
		if _, err := io.ReadFull(conn, buf[:1]); err != nil {
			return
		}
		n := int(buf[0])
		if _, err := io.ReadFull(conn, buf[:n]); err != nil {
			return
		}
		host = string(buf[:n])
	case 4:
		if _, err := io.ReadFull(conn, buf[:16]); err != nil {
			return
		}
		host = net.IP(buf[:16]).String()
	default:
		conn.Write([]byte{5, 8, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	if _, err := io.ReadFull(conn, buf[:2]); err != nil {
		return
	}
	port := binary.BigEndian.Uint16(buf[:2])
	rc, err := dial(net.JoinHostPort(host, strconv.Itoa(int(port))))
	if err != nil {
		conn.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	if _, err := conn.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		rc.Close()
		return
	}
	pipe(conn, rc)
}
