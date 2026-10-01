package sshx_test

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"nlrshell/internal/sshx"
	"nlrshell/internal/sshx/sshtest"
	"nlrshell/internal/store"
)

type prompter struct {
	mu        sync.Mutex
	hostKeys  int
	passwords int
	password  string
	trust     bool
}

func (p *prompter) HostKey(host, keyType, fp string, changed bool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hostKeys++
	return p.trust
}

func (p *prompter) Password(title string) (string, bool, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.passwords++
	return p.password, true, p.password != ""
}

func (p *prompter) Passphrase(string) (string, bool) { return "", false }

func (p *prompter) Interactive(string, string, []string, []bool) ([]string, bool) {
	return nil, false
}

var current *sshx.Session

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	if current != nil {
		t.Logf("screen:\n%s", screenText(current))
	}
	t.Fatalf("timed out waiting for %s", what)
}

func screenText(s *sshx.Session) string {
	s.Term.Lock()
	defer s.Term.Unlock()
	var sb strings.Builder
	_, rows := s.Term.Size()
	for i := 0; i < s.Term.HistLen()+rows; i++ {
		sb.WriteString(s.Term.LineAt(i).Text())
		sb.WriteByte('\n')
	}
	return sb.String()
}

func setup(t *testing.T) (*sshtest.Server, *store.Store, store.Profile) {
	t.Helper()
	srv, err := sshtest.Start("127.0.0.1:0", "demo", "demo")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	srv.Populate()
	st := store.Open(t.TempDir())
	p := st.SaveProfile(store.Profile{Name: "test", Host: "127.0.0.1", Port: srv.Port(), User: "demo"})
	return srv, st, p
}

func connect(t *testing.T, st *store.Store, p store.Profile, pr *prompter) *sshx.Session {
	t.Helper()
	s := sshx.NewSession(p, st, pr, func() {})
	current = s
	s.Start()
	t.Cleanup(s.Close)
	waitFor(t, "connect", func() bool { return s.State() != sshx.StateConnecting })
	return s
}

func TestConnectShellAndPasswordSave(t *testing.T) {
	_, st, p := setup(t)

	// Rejecting the host key aborts the connection.
	pr := &prompter{password: "demo"}
	s := connect(t, st, p, pr)
	if _, _, err := s.Info(); s.State() != sshx.StateClosed || err == nil {
		t.Fatalf("expected failure, got state=%v err=%v", s.State(), err)
	}

	// Wrong password, then cancel.
	pr = &prompter{trust: true, password: "wrong"}
	s = connect(t, st, p, pr)
	if _, _, err := s.Info(); s.State() != sshx.StateClosed || err == nil || !strings.Contains(err.Error(), "认证失败") {
		t.Fatalf("expected auth failure, got %v", err)
	}
	if pr.hostKeys != 1 {
		t.Fatalf("host key prompts: %d", pr.hostKeys)
	}

	// Correct password is prompted once and then remembered.
	pr = &prompter{trust: true, password: "demo"}
	s = connect(t, st, p, pr)
	if s.State() != sshx.StateConnected {
		_, _, err := s.Info()
		t.Fatalf("not connected: %v", err)
	}
	if pr.hostKeys != 0 || pr.passwords != 1 {
		t.Fatalf("prompts: hostKeys=%d passwords=%d", pr.hostKeys, pr.passwords)
	}
	saved, _ := st.Profile(p.ID)
	if store.Decrypt(saved.Password) != "demo" || saved.LastUsed == 0 {
		t.Fatalf("password not saved: %+v", saved)
	}

	waitFor(t, "prompt", func() bool { return strings.Contains(screenText(s), "demo@nlr-demo") })
	s.Resize(100, 30)
	s.Write([]byte("echo hi 中文\r"))
	waitFor(t, "echo output", func() bool { return strings.Contains(screenText(s), "\nhi 中文\n") })
	s.Write([]byte("size\r"))
	waitFor(t, "resize to reach the remote side", func() bool { return strings.Contains(screenText(s), "100 cols x 30 rows") })
	if s.Title() != "demo@nlr-demo: ~" {
		t.Fatalf("title = %q", s.Title())
	}

	// Saved password: no prompts at all.
	pr2 := &prompter{}
	s2 := connect(t, st, saved, pr2)
	if s2.State() != sshx.StateConnected || pr2.passwords != 0 {
		t.Fatalf("saved password not used: state=%v prompts=%d", s2.State(), pr2.passwords)
	}

	// Remote exit ends the session cleanly.
	s.Write([]byte("exit\r"))
	waitFor(t, "close", func() bool { return s.State() == sshx.StateClosed })
	if _, _, err := s.Info(); err != nil {
		t.Fatalf("clean exit reported error: %v", err)
	}

	// Reconnect works on the same session object.
	s.Reconnect()
	waitFor(t, "reconnect", func() bool { return s.State() == sshx.StateConnected })
}

func TestMonitorAndCwd(t *testing.T) {
	_, st, p := setup(t)
	st.SetPassword(p.ID, "demo")
	p, _ = st.Profile(p.ID)
	s := connect(t, st, p, &prompter{trust: true})
	if s.State() != sshx.StateConnected {
		t.Fatal("not connected")
	}
	waitFor(t, "monitor", func() bool {
		m := s.Monitor()
		return m.Ready && len(m.CPU) >= 2 && m.Snap.CPU > 0
	})
	m := s.Monitor()
	if m.Unsupported || m.Static.Host != "nlr-demo" || m.Static.NCPU != 4 || len(m.Snap.Cores) != 4 {
		t.Fatalf("static/cores: %+v cores=%d", m.Static, len(m.Snap.Cores))
	}
	if m.Snap.MemTotal == 0 || len(m.Snap.Disks) != 3 || m.Snap.RxRate <= 0 || len(m.Snap.Procs) == 0 {
		t.Fatalf("snapshot: %+v", m.Snap)
	}
	if cwd, _ := s.Cwd(); cwd != "/home/demo" {
		t.Fatalf("cwd = %q", cwd)
	}
	s.Write([]byte("cd /var/log\r"))
	s.RefreshMonitor()
	waitFor(t, "cwd follow", func() bool {
		s.RefreshMonitor()
		cwd, _ := s.Cwd()
		return cwd == "/var/log"
	})
}

func TestFilesAndTransfers(t *testing.T) {
	srv, st, p := setup(t)
	st.SetPassword(p.ID, "demo")
	p, _ = st.Profile(p.ID)
	s := connect(t, st, p, &prompter{trust: true})

	home, err := s.Home()
	if err != nil || home != "/home/demo" {
		t.Fatalf("home = %q, %v", home, err)
	}
	dir, entries, err := s.List(home)
	if err != nil || dir != "/home/demo" {
		t.Fatalf("list: %q %v", dir, err)
	}
	byName := map[string]sshx.Entry{}
	for _, e := range entries {
		byName[e.Name] = e
	}
	if !byName["projects"].IsDir || byName["notes.md"].IsDir || !byName["www"].IsLink || !byName["www"].IsDir {
		t.Fatalf("entries: %+v", entries)
	}
	if !entries[0].IsDir {
		t.Fatal("directories should sort first")
	}

	if err := s.Mkdir("/home/demo/new"); err != nil {
		t.Fatal(err)
	}
	if err := s.Touch("/home/demo/new/a.txt"); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteText("/home/demo/new/a.txt", "你好\n"); err != nil {
		t.Fatal(err)
	}
	if txt, err := s.ReadText("/home/demo/new/a.txt"); err != nil || txt != "你好\n" {
		t.Fatalf("read: %q %v", txt, err)
	}
	if _, err := s.ReadText("/opt/app/bin/app"); err == nil {
		t.Fatal("binary file should be refused")
	}
	if err := s.Rename("/home/demo/new/a.txt", "/home/demo/new/b.txt"); err != nil {
		t.Fatal(err)
	}
	if err := s.Rename("/home/demo/new/b.txt", "/home/demo/notes.md"); err == nil {
		t.Fatal("rename over existing file should fail")
	}

	// Upload a directory tree and a large file, download them back.
	local := t.TempDir()
	os.MkdirAll(filepath.Join(local, "up", "sub"), 0o755)
	big := bytes.Repeat([]byte("0123456789abcdef"), 300000) // 4.8 MB
	os.WriteFile(filepath.Join(local, "up", "big.bin"), big, 0o644)
	os.WriteFile(filepath.Join(local, "up", "sub", "x.txt"), []byte("x"), 0o644)

	s.Transfers.Upload(filepath.Join(local, "up"), "/home/demo/new")
	waitFor(t, "upload", func() bool { return s.Transfers.Active() == 0 })
	list := s.Transfers.List()
	if len(list) != 1 || list[0].State != sshx.TransferDone || list[0].Done != int64(len(big))+1 || list[0].Total != list[0].Done {
		t.Fatalf("upload: %+v", list)
	}
	fs := srv.FS()
	defer fs.Close()
	f, err := fs.Open("/home/demo/new/up/big.bin")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(f)
	f.Close()
	if !bytes.Equal(got, big) {
		t.Fatalf("uploaded content differs: %d bytes", len(got))
	}

	dl := filepath.Join(local, "dl")
	os.MkdirAll(dl, 0o755)
	p1 := s.Transfers.Download("/home/demo/new/up", dl)
	p2 := s.Transfers.Download("/home/demo/notes.md", dl)
	p3 := s.Transfers.Download("/home/demo/notes.md", dl)
	waitFor(t, "download", func() bool { return s.Transfers.Active() == 0 })
	for _, ti := range s.Transfers.List() {
		if ti.State != sshx.TransferDone {
			t.Fatalf("transfer failed: %+v", ti)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(p1, "big.bin")); !bytes.Equal(b, big) {
		t.Fatal("downloaded big.bin differs")
	}
	if b, _ := os.ReadFile(filepath.Join(p1, "sub", "x.txt")); string(b) != "x" {
		t.Fatal("downloaded sub/x.txt differs")
	}
	if p2 == p3 || !strings.HasSuffix(p3, "notes (1).md") {
		t.Fatalf("download names: %q %q", p2, p3)
	}

	if err := s.Remove([]string{"/home/demo/new"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Stat("/home/demo/new"); err == nil {
		t.Fatal("directory still exists after remove")
	}
	s.Transfers.Clear()
	if len(s.Transfers.List()) != 0 {
		t.Fatal("clear did not remove finished transfers")
	}

	// Temporary downloads leave the list once complete; failed ones stay.
	var done []sshx.TransferInfo
	var mu sync.Mutex
	s.Transfers.OnDone = func(ti sshx.TransferInfo) { mu.Lock(); done = append(done, ti); mu.Unlock() }
	tmp := filepath.Join(local, "tmp-notes.md")
	s.Transfers.DownloadTemp("/home/demo/notes.md", tmp)
	s.Transfers.DownloadTemp("/home/demo/missing", filepath.Join(local, "tmp-missing"))
	waitFor(t, "temp downloads", func() bool { mu.Lock(); defer mu.Unlock(); return len(done) == 2 })
	if b, err := os.ReadFile(tmp); err != nil || !strings.Contains(string(b), "部署记录") {
		t.Fatalf("temp download content: %q %v", b, err)
	}
	left := s.Transfers.List()
	if len(left) != 1 || left[0].State != sshx.TransferFailed || !left[0].Temp {
		t.Fatalf("after temp downloads the list should hold only the failed one: %+v", left)
	}
	if s.Transfers.Seq() < 6 {
		t.Fatalf("seq = %d", s.Transfers.Seq())
	}
}

func TestForwards(t *testing.T) {
	_, st, p := setup(t)
	st.SetPassword(p.ID, "demo")
	p, _ = st.Profile(p.ID)

	// A local HTTP server stands in for a service on the remote network.
	web, _ := net.Listen("tcp", "127.0.0.1:0")
	defer web.Close()
	go http.Serve(web, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "pong") }))
	webPort := web.Addr().(*net.TCPAddr).Port

	free := func() int {
		l, _ := net.Listen("tcp", "127.0.0.1:0")
		defer l.Close()
		return l.Addr().(*net.TCPAddr).Port
	}
	lp, rp, dp := free(), free(), free()
	p.Forwards = []store.Forward{
		{ID: "l", Kind: store.ForwardLocal, ListenPort: lp, TargetHost: "127.0.0.1", TargetPort: webPort, AutoStart: true},
		{ID: "r", Kind: store.ForwardRemote, ListenPort: rp, TargetHost: "127.0.0.1", TargetPort: webPort},
		{ID: "d", Kind: store.ForwardDynamic, ListenPort: dp},
	}
	p = st.SaveProfile(p)
	s := connect(t, st, p, &prompter{trust: true})
	if s.State() != sshx.StateConnected {
		t.Fatal("not connected")
	}
	get := func(port int) string {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
		if err != nil {
			return "ERR " + err.Error()
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	waitFor(t, "auto-started local forward", func() bool { return get(lp) == "pong" })
	if err := s.Forwards.Start(p.Forwards[1]); err != nil {
		t.Fatal(err)
	}
	if got := get(rp); got != "pong" {
		t.Fatalf("remote forward: %q", got)
	}
	if err := s.Forwards.Start(p.Forwards[2]); err != nil {
		t.Fatal(err)
	}
	// Minimal SOCKS5 client.
	c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", dp))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte{5, 1, 0})
	io.ReadFull(c, make([]byte, 2))
	c.Write(append([]byte{5, 1, 0, 3, 9}, append([]byte("127.0.0.1"), byte(webPort>>8), byte(webPort))...))
	rep := make([]byte, 10)
	if _, err := io.ReadFull(c, rep); err != nil || rep[1] != 0 {
		t.Fatalf("socks reply: %v %v", rep, err)
	}
	fmt.Fprintf(c, "GET / HTTP/1.0\r\n\r\n")
	body, _ := io.ReadAll(c)
	if !strings.HasSuffix(string(body), "pong") {
		t.Fatalf("socks body: %q", body)
	}

	active := 0
	for _, f := range s.Forwards.List() {
		if f.Active {
			active++
		}
	}
	if active != 3 {
		t.Fatalf("active forwards: %d", active)
	}
	s.Forwards.Stop("l")
	if got := get(lp); !strings.HasPrefix(got, "ERR") {
		t.Fatalf("forward still alive after stop: %q", got)
	}
	// Starting on a busy port reports a friendly error.
	if err := s.Forwards.Start(store.Forward{ID: "x", Kind: store.ForwardLocal, ListenPort: webPort, TargetHost: "127.0.0.1", TargetPort: 1}); err == nil {
		t.Fatal("expected listen error")
	}
}
