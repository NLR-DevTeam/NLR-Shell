package sshtest

import (
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// shell is a tiny line-oriented fake of an interactive bash session. It
// understands just enough commands to exercise the terminal emulator.
func (s *Server) shell(sc *ssh.ServerConn, ch ssh.Channel, cols, rows int, resize <-chan [2]int) {
	out := func(format string, a ...any) {
		ch.Write([]byte(strings.ReplaceAll(fmt.Sprintf(format, a...), "\n", "\r\n")))
	}
	cwd := func() string {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.cwd[sc]
	}
	prompt := func() {
		d := cwd()
		if d == "/home/demo" {
			d = "~"
		} else if strings.HasPrefix(d, "/home/demo/") {
			d = "~" + d[len("/home/demo"):]
		}
		out("\x1b]0;demo@nlr-demo: %s\x07\x1b[1;32mdemo@nlr-demo\x1b[0m:\x1b[1;34m%s\x1b[0m$ ", d, d)
	}
	out("Welcome to Ubuntu 24.04.1 LTS (GNU/Linux 6.8.0-45-generic x86_64)\n\n")
	out(" * Documentation:  https://help.ubuntu.com\n * Management:     https://landscape.canonical.com\n\n")
	out("  System load:  0.52               Processes:             %d\n  Usage of /:   40.1%% of 78.6GB    Users logged in:       1\n  Memory usage: 49%%                IPv4 address for eth0: 10.0.0.11\n\n", 118)
	out("Last login: %s from 10.0.0.2\n", time.Now().Add(-3*time.Hour).Format("Mon Jan _2 15:04:05 2006"))
	prompt()

	fs := s.FS()
	defer fs.Close()

	var line []rune
	buf := make([]byte, 4096)
	var pending []byte
	for {
		n, err := ch.Read(buf)
		if err != nil {
			return
		}
		select {
		case sz := <-resize:
			cols, rows = sz[0], sz[1]
		default:
		}
		pending = append(pending, buf[:n]...)
		rs := []rune(string(pending))
		pending = pending[:0]
		for i := 0; i < len(rs); i++ {
			r := rs[i]
			switch {
			case r == '\r' || r == '\n':
				out("\n")
				cmd := strings.TrimSpace(string(line))
				line = line[:0]
				if cmd == "exit" || cmd == "logout" {
					out("logout\n")
					return
				}
				s.run(sc, fs, cmd, out, cols, rows)
				prompt()
			case r == 0x7f || r == 0x08:
				if len(line) > 0 {
					w := 1
					if line[len(line)-1] >= 0x2e80 {
						w = 2
					}
					line = line[:len(line)-1]
					out("%s", strings.Repeat("\b \b", w))
				}
			case r == 0x03:
				out("^C\n")
				line = line[:0]
				prompt()
			case r == 0x04:
				if len(line) == 0 {
					out("logout\n")
					return
				}
			case r == 0x0c:
				out("\x1b[H\x1b[2J")
				prompt()
				out("%s", string(line))
			case r == 0x1b:
				// Swallow escape sequences (arrow keys etc).
				for i+1 < len(rs) && !(rs[i+1] >= 0x40 && rs[i+1] <= 0x7e && rs[i+1] != '[' && rs[i+1] != 'O') {
					i++
				}
				i++
			case r >= 0x20:
				line = append(line, r)
				out("%s", string(r))
			}
		}
	}
}

func (s *Server) run(sc *ssh.ServerConn, fs interface {
	ReadDir(string) ([]os.FileInfo, error)
	Stat(string) (os.FileInfo, error)
}, cmd string, out func(string, ...any), cols, rows int) {
	args := strings.Fields(cmd)
	if len(args) == 0 {
		return
	}
	s.mu.Lock()
	cwd := s.cwd[sc]
	s.mu.Unlock()
	abs := func(p string) string {
		switch {
		case p == "" || p == "~":
			return "/home/demo"
		case strings.HasPrefix(p, "~/"):
			return path.Join("/home/demo", p[2:])
		case path.IsAbs(p):
			return path.Clean(p)
		}
		return path.Join(cwd, p)
	}
	switch args[0] {
	case "cd":
		target := ""
		if len(args) > 1 {
			target = args[1]
		}
		d := abs(target)
		if fi, err := fs.Stat(d); err != nil || !fi.IsDir() {
			out("-bash: cd: %s: No such file or directory\n", target)
			return
		}
		s.mu.Lock()
		s.cwd[sc] = d
		s.mu.Unlock()
	case "pwd":
		out("%s\n", cwd)
	case "whoami":
		out("demo\n")
	case "echo":
		out("%s\n", strings.Join(args[1:], " "))
	case "clear":
		out("\x1b[H\x1b[2J\x1b[3J")
	case "uname":
		out("Linux nlr-demo 6.8.0-45-generic #45-Ubuntu SMP x86_64 GNU/Linux\n")
	case "ls", "ll":
		long := args[0] == "ll"
		dir := cwd
		for _, a := range args[1:] {
			if strings.HasPrefix(a, "-") {
				long = long || strings.Contains(a, "l")
			} else {
				dir = abs(a)
			}
		}
		infos, err := fs.ReadDir(dir)
		if err != nil {
			out("ls: cannot access '%s': No such file or directory\n", dir)
			return
		}
		sort.Slice(infos, func(i, j int) bool { return infos[i].Name() < infos[j].Name() })
		name := func(fi os.FileInfo) string {
			switch {
			case fi.IsDir():
				return "\x1b[1;34m" + fi.Name() + "\x1b[0m"
			case fi.Mode()&os.ModeSymlink != 0:
				return "\x1b[1;36m" + fi.Name() + "\x1b[0m"
			case fi.Mode()&0o111 != 0:
				return "\x1b[1;32m" + fi.Name() + "\x1b[0m"
			case strings.HasSuffix(fi.Name(), ".gz"):
				return "\x1b[1;31m" + fi.Name() + "\x1b[0m"
			}
			return fi.Name()
		}
		if long {
			out("total %d\n", len(infos)*4)
			for _, fi := range infos {
				out("%s 1 demo demo %8d %s %s\n", fi.Mode().String(), fi.Size(), fi.ModTime().Format("Jan _2 15:04"), name(fi))
			}
			return
		}
		var parts []string
		for _, fi := range infos {
			parts = append(parts, name(fi))
		}
		out("%s\n", strings.Join(parts, "  "))
	case "colors":
		for i := 0; i < 16; i++ {
			out("\x1b[48;5;%dm  ", i)
		}
		out("\x1b[0m\n")
		for i := 16; i < 232; i++ {
			out("\x1b[48;5;%dm ", i)
			if (i-15)%36 == 0 {
				out("\x1b[0m\n")
			}
		}
		for i := 232; i < 256; i++ {
			out("\x1b[48;5;%dm ", i)
		}
		out("\x1b[0m\n")
		for i := 0; i < cols && i < 72; i++ {
			out("\x1b[48;2;%d;%d;%dm ", 255-i*3, i*3, 128)
		}
		out("\x1b[0m\n\x1b[1mbold\x1b[0m \x1b[3mitalic\x1b[0m \x1b[4munderline\x1b[0m \x1b[9mstrike\x1b[0m \x1b[7mreverse\x1b[0m \x1b[2mfaint\x1b[0m 中文宽字符 ｆｕｌｌ\n")
	case "box":
		out("\x1b(0lqqqqqqqqwqqqqqqqqk\x1b(B\n\x1b(0x\x1b(B  name  \x1b(0x\x1b(B  size  \x1b(0x\x1b(B\n\x1b(0tqqqqqqqqnqqqqqqqqu\x1b(B\n\x1b(0x\x1b(B app    \x1b(0x\x1b(B  12 MB \x1b(0x\x1b(B\n\x1b(0mqqqqqqqqvqqqqqqqqj\x1b(B\n")
		out("┌──────────┬──────────┐\n│ ▁▂▃▄▅▆▇█ │ ░▒▓█▀▄▌▐ │\n└──────────┴──────────┘\n")
	case "seq":
		n := 100
		if len(args) > 1 {
			fmt.Sscanf(args[1], "%d", &n)
		}
		var b strings.Builder
		for i := 1; i <= n; i++ {
			fmt.Fprintf(&b, "%d\n", i)
		}
		out("%s", b.String())
	case "size":
		out("%d cols x %d rows\n", cols, rows)
	default:
		out("%s: command not found\n", args[0])
	}
}
