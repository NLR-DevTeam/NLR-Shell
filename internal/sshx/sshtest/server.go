// Package sshtest is an in-process SSH server used by tests and by the
// cmd/testsshd development tool. It offers password login, a small fake
// shell, a fake Linux monitor data source, an in-memory SFTP filesystem and
// TCP forwarding, so the whole client can be exercised without a real host.
package sshtest

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"math"
	mrand "math/rand"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Server is a running test server.
type Server struct {
	Addr     string
	User     string
	Password string
	// Interactive, if set, makes the server demand keyboard-interactive
	// authentication with these questions instead of a plain password.
	ln  net.Listener
	cfg *ssh.ServerConfig
	// authorized is the public key accepted for User, if any.
	authorized atomic.Pointer[ssh.PublicKey]
	handlers   sftp.Handlers
	start      time.Time

	mu      sync.Mutex
	cwd     map[*ssh.ServerConn]string
	forward map[string]net.Listener
}

// Start launches a server on addr (use "127.0.0.1:0" for a free port).
func Start(addr, user, password string) (*Server, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	s := &Server{User: user, Password: password, handlers: sftp.InMemHandler(), start: time.Now(),
		cwd: map[*ssh.ServerConn]string{}, forward: map[string]net.Listener{}}
	s.cfg = &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == s.User && string(pass) == s.Password {
				return nil, nil
			}
			return nil, fmt.Errorf("bad credentials")
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if k := s.authorized.Load(); k != nil && c.User() == s.User && bytes.Equal((*k).Marshal(), key.Marshal()) {
				return nil, nil
			}
			return nil, fmt.Errorf("unknown key")
		},
	}
	s.cfg.AddHostKey(signer)
	s.ln, err = net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s.Addr = s.ln.Addr().String()
	go s.accept()
	return s, nil
}

// Authorize makes the server accept key for its user.
func (s *Server) Authorize(key ssh.PublicKey) { s.authorized.Store(&key) }

// Close stops the server.
func (s *Server) Close() { s.ln.Close() }

// Port returns the listening port.
func (s *Server) Port() int {
	_, p, _ := net.SplitHostPort(s.Addr)
	n, _ := strconv.Atoi(p)
	return n
}

// FS returns an SFTP client connected directly to the server's in-memory
// filesystem, for populating and inspecting it.
func (s *Server) FS() *sftp.Client {
	c1, c2 := net.Pipe()
	go sftp.NewRequestServer(c1, s.handlers).Serve()
	c, err := sftp.NewClientPipe(c2, c2)
	if err != nil {
		panic(err)
	}
	return c
}

// Populate fills the in-memory filesystem with a small demo tree.
func (s *Server) Populate() {
	c := s.FS()
	defer c.Close()
	for _, d := range []string{"/etc/nginx/conf.d", "/home/demo/projects/api/src", "/home/demo/.ssh", "/var/log/nginx", "/var/www/html", "/root", "/tmp", "/opt/app/bin", "/usr/local/bin"} {
		c.MkdirAll(d)
	}
	files := map[string]string{
		"/etc/passwd":                       "root:x:0:0:root:/root:/bin/bash\ndemo:x:1000:1000:Demo:/home/demo:/bin/bash\nwww-data:x:33:33::/var/www:/usr/sbin/nologin\n",
		"/etc/group":                        "root:x:0:\ndemo:x:1000:\nwww-data:x:33:\n",
		"/etc/hostname":                     "nlr-demo\n",
		"/etc/hosts":                        "127.0.0.1 localhost\n10.0.0.12 db-1\n10.0.0.13 cache-1\n",
		"/etc/nginx/nginx.conf":             "user www-data;\nworker_processes auto;\n\nevents {\n    worker_connections 1024;\n}\n\nhttp {\n    include       mime.types;\n    sendfile      on;\n    keepalive_timeout 65;\n    include /etc/nginx/conf.d/*.conf;\n}\n",
		"/etc/nginx/conf.d/api.conf":        "server {\n    listen 80;\n    server_name api.example.com;\n    location / {\n        proxy_pass http://127.0.0.1:8080;\n    }\n}\n",
		"/home/demo/.bashrc":                "export PS1='\\u@\\h:\\w\\$ '\nalias ll='ls -alF'\n",
		"/home/demo/.ssh/authorized_keys":   "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample demo@laptop\n",
		"/home/demo/notes.md":               "# 部署记录\n\n- 2026-09-28 升级 nginx 到 1.27\n- 2026-09-29 调整 worker 数量\n",
		"/home/demo/deploy.sh":              "#!/bin/sh\nset -e\ncd /opt/app\ngit pull\nmake build\nsystemctl restart app\n",
		"/home/demo/projects/api/go.mod":    "module example.com/api\n\ngo 1.27\n",
		"/home/demo/projects/api/main.go":   "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n",
		"/home/demo/projects/api/README.md": "# api\n",
		"/var/log/nginx/access.log":         strings.Repeat("10.0.0.7 - - [30/Sep/2026:10:12:01 +0800] \"GET /health HTTP/1.1\" 200 2 \"-\" \"curl/8.5\"\n", 400),
		"/var/log/nginx/error.log":          "",
		"/var/log/syslog":                   strings.Repeat("Sep 30 10:12:01 nlr-demo systemd[1]: Started Session 42 of user demo.\n", 2000),
		"/var/www/html/index.html":          "<!doctype html>\n<title>It works</title>\n<h1>It works!</h1>\n",
		"/opt/app/bin/app":                  strings.Repeat("\x7fELF\x00\x01\x02", 30000),
		"/tmp/backup-2026-09-30.tar.gz":     strings.Repeat("\x1f\x8b\x08\x00binary", 90000),
	}
	for p, content := range files {
		if f, err := c.Create(p); err == nil {
			io.WriteString(f, content)
			f.Close()
		}
	}
	c.Chmod("/home/demo/deploy.sh", 0o755)
	c.Chmod("/opt/app/bin/app", 0o755)
	c.Chmod("/home/demo/.ssh", 0o700)
	c.Symlink("/var/www/html", "/home/demo/www")
}

func (s *Server) accept() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.serve(conn)
	}
}

func (s *Server) serve(conn net.Conn) {
	sc, chans, reqs, err := ssh.NewServerConn(conn, s.cfg)
	if err != nil {
		conn.Close()
		return
	}
	defer sc.Close()
	s.mu.Lock()
	s.cwd[sc] = "/home/demo"
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.cwd, sc)
		s.mu.Unlock()
	}()
	go s.global(sc, reqs)
	for nc := range chans {
		switch nc.ChannelType() {
		case "session":
			ch, r, err := nc.Accept()
			if err == nil {
				go s.session(sc, ch, r)
			}
		case "direct-tcpip":
			var d struct {
				Host     string
				Port     uint32
				OrigHost string
				OrigPort uint32
			}
			if ssh.Unmarshal(nc.ExtraData(), &d) != nil {
				nc.Reject(ssh.ConnectionFailed, "bad request")
				continue
			}
			rc, err := net.Dial("tcp", net.JoinHostPort(d.Host, strconv.Itoa(int(d.Port))))
			if err != nil {
				nc.Reject(ssh.ConnectionFailed, err.Error())
				continue
			}
			ch, r, err := nc.Accept()
			if err != nil {
				rc.Close()
				continue
			}
			go ssh.DiscardRequests(r)
			go func() {
				defer ch.Close()
				defer rc.Close()
				go io.Copy(rc, ch)
				io.Copy(ch, rc)
			}()
		default:
			nc.Reject(ssh.UnknownChannelType, "unsupported")
		}
	}
}

func (s *Server) global(sc *ssh.ServerConn, reqs <-chan *ssh.Request) {
	for r := range reqs {
		switch r.Type {
		case "tcpip-forward":
			var p struct {
				Addr string
				Port uint32
			}
			if ssh.Unmarshal(r.Payload, &p) != nil {
				r.Reply(false, nil)
				continue
			}
			ln, err := net.Listen("tcp", net.JoinHostPort(p.Addr, strconv.Itoa(int(p.Port))))
			if err != nil {
				r.Reply(false, nil)
				continue
			}
			port := uint32(ln.Addr().(*net.TCPAddr).Port)
			s.mu.Lock()
			s.forward[fmt.Sprintf("%p:%s:%d", sc, p.Addr, port)] = ln
			s.mu.Unlock()
			r.Reply(true, ssh.Marshal(struct{ Port uint32 }{port}))
			go func() {
				defer ln.Close()
				go func() { sc.Wait(); ln.Close() }()
				for {
					c, err := ln.Accept()
					if err != nil {
						return
					}
					orig := c.RemoteAddr().(*net.TCPAddr)
					payload := ssh.Marshal(struct {
						Addr     string
						Port     uint32
						OrigAddr string
						OrigPort uint32
					}{p.Addr, port, orig.IP.String(), uint32(orig.Port)})
					ch, cr, err := sc.OpenChannel("forwarded-tcpip", payload)
					if err != nil {
						c.Close()
						continue
					}
					go ssh.DiscardRequests(cr)
					go func() {
						defer ch.Close()
						defer c.Close()
						go io.Copy(c, ch)
						io.Copy(ch, c)
					}()
				}
			}()
		case "cancel-tcpip-forward":
			var p struct {
				Addr string
				Port uint32
			}
			ssh.Unmarshal(r.Payload, &p)
			key := fmt.Sprintf("%p:%s:%d", sc, p.Addr, p.Port)
			s.mu.Lock()
			if ln, ok := s.forward[key]; ok {
				ln.Close()
				delete(s.forward, key)
			}
			s.mu.Unlock()
			r.Reply(true, nil)
		default:
			r.Reply(r.Type == "keepalive@openssh.com", nil)
		}
	}
}

func (s *Server) session(sc *ssh.ServerConn, ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	cols, rows := 80, 24
	resize := make(chan [2]int, 8)
	exit := func(code uint32) {
		ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{code}))
	}
	for r := range reqs {
		switch r.Type {
		case "pty-req":
			var p struct {
				Term       string
				Cols, Rows uint32
				W, H       uint32
				Modes      string
			}
			ssh.Unmarshal(r.Payload, &p)
			cols, rows = int(p.Cols), int(p.Rows)
			r.Reply(true, nil)
		case "window-change":
			var p struct{ Cols, Rows, W, H uint32 }
			ssh.Unmarshal(r.Payload, &p)
			select {
			case resize <- [2]int{int(p.Cols), int(p.Rows)}:
			default:
			}
		case "env":
			r.Reply(true, nil)
		case "shell":
			r.Reply(true, nil)
			go func() {
				s.shell(sc, ch, cols, rows, resize)
				exit(0)
				ch.Close()
			}()
		case "exec":
			var p struct{ Command string }
			ssh.Unmarshal(r.Payload, &p)
			r.Reply(true, nil)
			go func() {
				code := s.exec(sc, ch, p.Command)
				exit(code)
				ch.Close()
			}()
		case "subsystem":
			var p struct{ Name string }
			ssh.Unmarshal(r.Payload, &p)
			if p.Name != "sftp" {
				r.Reply(false, nil)
				continue
			}
			r.Reply(true, nil)
			go func() {
				sftp.NewRequestServer(ch, s.handlers, sftp.WithStartDirectory("/home/demo")).Serve()
				exit(0)
				ch.Close()
			}()
		default:
			if r.WantReply {
				r.Reply(false, nil)
			}
		}
	}
}

func (s *Server) exec(sc *ssh.ServerConn, ch ssh.Channel, cmd string) uint32 {
	switch {
	case cmd == "exec sh":
		s.monitor(sc, ch)
		return 0
	case strings.HasPrefix(cmd, "kill "):
		return 0
	case strings.HasPrefix(cmd, "echo "):
		fmt.Fprintln(ch, strings.TrimPrefix(cmd, "echo "))
		return 0
	}
	fmt.Fprintf(ch.Stderr(), "sh: %s: command not found\n", cmd)
	return 127
}

// monitor imitates the output of monitor.Script on a small Linux VM.
func (s *Server) monitor(sc *ssh.ServerConn, ch ssh.Channel) {
	in := bufio.NewReader(ch)
	for {
		line, err := in.ReadString('\n')
		if err != nil {
			return
		}
		if strings.TrimSpace(line) == "done" {
			break
		}
	}
	fmt.Fprint(ch, "@@NLR static\npid 2001\nuname Linux 6.8.0-45-generic x86_64\nhost nlr-demo\nos Ubuntu 24.04.1 LTS\ncpu  Intel(R) Xeon(R) Gold 6338 CPU @ 2.00GHz\nncpu 4\ntick 100\npage 4096\nuser demo\n@@NLR ready\n")

	rng := mrand.New(mrand.NewSource(7))
	type proc struct {
		pid, ppid, tty int
		name           string
		ticks          float64
		rss            int
		load           float64
	}
	procs := []*proc{
		{1, 0, 0, "systemd", 900, 3100, 0.1},
		{412, 1, 0, "systemd-journal", 300, 9800, 0.2},
		{655, 1, 0, "sshd", 40, 2100, 0},
		{701, 1, 0, "nginx", 20, 1900, 0},
		{702, 701, 0, "nginx", 5200, 6400, 3},
		{703, 701, 0, "nginx", 4900, 6100, 2},
		{820, 1, 0, "mysqld", 88000, 148000, 9},
		{911, 1, 0, "redis-server", 9100, 5200, 1},
		{1033, 1, 0, "node", 41000, 61000, 14},
		{1290, 1, 0, "dockerd", 12000, 22000, 1},
		{1302, 1290, 0, "containerd", 8000, 12000, 0.5},
		{1500, 1, 0, "java", 230000, 310000, 22},
		{1999, 655, 0, "sshd", 3, 2400, 0},
		{2000, 1999, 34816, "bash", 12, 1300, 0},
		{2001, 1999, 0, "sh", 1, 400, 0.3},
		{1777, 1, 0, "cron", 9, 700, 0},
		{1801, 1, 0, "rsyslogd", 150, 1100, 0.1},
		{1850, 1, 0, "python3", 5200, 18000, 4},
	}
	var cpu [5][4]float64 // user, system, idle, iowait for total + 4 cores
	rx, tx := 8.1e9, 2.3e9
	t0 := time.Now()
	last := t0
	phase := 0.0
	for {
		if _, err := in.ReadString('\n'); err != nil {
			return
		}
		now := time.Now()
		dt := now.Sub(last).Seconds()
		last = now
		phase += dt
		busy := 0.22 + 0.16*math.Sin(phase/7) + rng.Float64()*0.08
		cpu[0] = [4]float64{}
		for i := 1; i <= 4; i++ {
			b := math.Min(math.Max(busy+(rng.Float64()-0.5)*0.3, 0.02), 0.97)
			cpu[i][0] += dt * 100 * b * 0.7
			cpu[i][1] += dt * 100 * b * 0.3
			cpu[i][2] += dt * 100 * (1 - b)
			for j := 0; j < 4; j++ {
				cpu[0][j] += cpu[i][j]
			}
		}
		rx += dt * (180e3 + 900e3*math.Abs(math.Sin(phase/5)) + rng.Float64()*120e3)
		tx += dt * (60e3 + 300e3*math.Abs(math.Cos(phase/6)) + rng.Float64()*40e3)

		var b strings.Builder
		b.WriteString("@@NLR begin\n@stat\n")
		for i := 0; i < 5; i++ {
			name := "cpu "
			if i > 0 {
				name = "cpu" + strconv.Itoa(i-1)
			}
			fmt.Fprintf(&b, "%s %d 0 %d %d %d 0 0 0 0 0\n", name, int64(cpu[i][0]), int64(cpu[i][1]), int64(cpu[i][2]), int64(cpu[i][3]))
		}
		avail := 4100000 + int(300000*math.Sin(phase/11))
		fmt.Fprintf(&b, "@mem\nMemTotal: 8123456 kB\nMemFree: 812345 kB\nMemAvailable: %d kB\nBuffers: 210000 kB\nCached: 2950000 kB\nSwapTotal: 2097148 kB\nSwapFree: 1887436 kB\nSReclaimable: 180000 kB\n", avail)
		fmt.Fprintf(&b, "@load\n%.2f %.2f %.2f 2/%d 2001\n", busy*4, busy*3.6, busy*3.2, len(procs))
		fmt.Fprintf(&b, "@uptime\n%.2f 0\n", 1987654+now.Sub(t0).Seconds())
		fmt.Fprintf(&b, "@net\nInter-| Receive | Transmit\n face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed\n    lo: 123456 100 0 0 0 0 0 0 123456 100 0 0 0 0 0 0\n  eth0: %d 100 0 0 0 0 0 0 %d 100 0 0 0 0 0 0\ndocker0: 1 1 0 0 0 0 0 0 1 1 0 0 0 0 0 0\n", int64(rx), int64(tx))
		b.WriteString("@df\nFilesystem 1024-blocks Used Available Capacity Mounted on\nudev 4000000 0 4000000 0% /dev\n/dev/vda1 82436780 31250000 47600000 40% /\ntmpfs 812344 1200 811144 1% /run\n/dev/vdb1 206292968 163290000 32500000 84% /data\n/dev/vda15 106858 6186 100673 6% /boot/efi\n")
		b.WriteString("@proc\n")
		for _, p := range procs {
			p.ticks += dt * p.load * (0.6 + rng.Float64()*0.8)
			tp := -1
			if p.tty != 0 {
				tp = p.pid
			}
			fmt.Fprintf(&b, "%d %d %d %d %d 0 %d %s\n", p.pid, p.ppid, p.tty, tp, int64(p.ticks), p.rss, p.name)
		}
		s.mu.Lock()
		cwd := s.cwd[sc]
		s.mu.Unlock()
		fmt.Fprintf(&b, "@cwd\n%s\n@@NLR end\n", cwd)
		if _, err := io.WriteString(ch, b.String()); err != nil {
			return
		}
	}
}
