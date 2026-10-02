package sshx_test

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"nlrshell/internal/sshx"
	"nlrshell/internal/store"
)

// testProxy runs a proxy that counts the tunnels it opened.
type testProxy struct {
	ln      net.Listener
	tunnels atomic.Int32
	targets chan string
}

func startProxy(t *testing.T, serve func(*testProxy, net.Conn)) *testProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &testProxy{ln: ln, targets: make(chan string, 8)}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serve(p, c)
		}
	}()
	return p
}

func (p *testProxy) addr() string { return p.ln.Addr().String() }

// pipe connects the client to target. first, if set, is written to the
// client before the target's bytes.
func (p *testProxy) pipe(c net.Conn, target string, first []byte) {
	defer c.Close()
	p.targets <- target
	rc, err := net.Dial("tcp", strings.Replace(target, "localhost", "127.0.0.1", 1))
	if err != nil {
		return
	}
	defer rc.Close()
	p.tunnels.Add(1)
	go io.Copy(rc, c)
	if first != nil {
		// Send the reply together with the server's banner, so that the
		// client finds SSH bytes buffered behind the reply.
		buf := make([]byte, 256)
		n, _ := rc.Read(buf)
		c.Write(append(first, buf[:n]...))
	}
	io.Copy(c, rc)
}

func serveHTTP(p *testProxy, c net.Conn) {
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil || req.Method != http.MethodConnect {
		c.Close()
		return
	}
	if strings.HasPrefix(req.Host, "denied") {
		io.WriteString(c, "HTTP/1.1 403 Forbidden\r\n\r\n")
		c.Close()
		return
	}
	p.pipe(c, req.Host, []byte("HTTP/1.1 200 Connection established\r\n\r\n"))
}

func serveSOCKS5(p *testProxy, c net.Conn) {
	b := make([]byte, 262)
	if _, err := io.ReadFull(c, b[:2]); err != nil || b[0] != 5 {
		c.Close()
		return
	}
	io.ReadFull(c, b[:b[1]])
	c.Write([]byte{5, 0})
	io.ReadFull(c, b[:4])
	var host string
	switch b[3] {
	case 1:
		io.ReadFull(c, b[:4])
		host = net.IP(b[:4]).String()
	case 3:
		io.ReadFull(c, b[:1])
		n := int(b[0])
		io.ReadFull(c, b[:n])
		host = string(b[:n])
	}
	io.ReadFull(c, b[:2])
	target := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(b[:2]))))
	if strings.HasPrefix(host, "denied") {
		c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		c.Close()
		return
	}
	c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
	p.pipe(c, target, nil)
}

func TestProxy(t *testing.T) {
	_, st, p := setup(t)
	httpProxy := startProxy(t, serveHTTP)
	socksProxy := startProxy(t, serveSOCKS5)

	for _, tc := range []struct {
		kind  string
		proxy *testProxy
	}{{store.ProxyHTTP, httpProxy}, {store.ProxySOCKS5, socksProxy}} {
		t.Run(tc.kind, func(t *testing.T) {
			// A host name goes to the proxy unresolved.
			q := p
			q.Host, q.ProxyType, q.ProxyAddr = "localhost", tc.kind, "http://"+tc.proxy.addr()+"/"
			s := connect(t, st, q, &prompter{trust: true, password: "demo"})
			if s.State() != sshx.StateConnected {
				_, _, err := s.Info()
				t.Fatalf("not connected through %s proxy: %v", tc.kind, err)
			}
			if got, want := <-tc.proxy.targets, "localhost:"+strconv.Itoa(p.Port); got != want {
				t.Fatalf("proxy asked for %s, want %s", got, want)
			}
			s.Write([]byte("echo via proxy\r"))
			waitFor(t, "output through the proxy", func() bool { return strings.Contains(screenText(s), "\nvia proxy\n") })

			// A refusal by the proxy is reported as such.
			q.Host = "denied.example"
			s = connect(t, st, q, &prompter{trust: true, password: "demo"})
			if _, _, err := s.Info(); err == nil || !strings.Contains(err.Error(), "代理") {
				t.Fatalf("refused tunnel: %v", err)
			}
		})
	}

	// A proxy that is not running is not mistaken for the SSH server.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := ln.Addr().String()
	ln.Close()
	q := p
	q.ProxyType, q.ProxyAddr = store.ProxySOCKS5, dead
	s := connect(t, st, q, &prompter{trust: true, password: "demo"})
	if _, _, err := s.Info(); err == nil || !strings.Contains(err.Error(), "无法连接代理") {
		t.Fatalf("dead proxy: %v", err)
	}
}

func TestParseProxyAddr(t *testing.T) {
	for in, want := range map[string]string{
		"127.0.0.1:7890":         "127.0.0.1:7890",
		" http://127.0.0.1:7890": "127.0.0.1:7890",
		"socks5://[::1]:1080/":   "[::1]:1080",
		"proxy.lan:8080":         "proxy.lan:8080",
		"127.0.0.1":              "",
		"user:pw@127.0.0.1:7890": "",
		"127.0.0.1:70000":        "",
	} {
		got, err := sshx.ParseProxyAddr(in)
		if want == "" && err == nil || want != "" && got != want {
			t.Errorf("ParseProxyAddr(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}
