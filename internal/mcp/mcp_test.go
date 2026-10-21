package mcp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nlrshell/internal/monitor"
	"nlrshell/internal/sshx/sshtest"
	"nlrshell/internal/store"
)

type client struct {
	t   *testing.T
	url string
	id  int
}

// start runs the service on a free port, as the app does.
func start(t *testing.T, st *store.Store) *client {
	t.Helper()
	sv := NewService(Options{Version: "test", Store: st})
	if err := sv.Start(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sv.Stop)
	running, url, err := sv.State()
	if !running || err != nil {
		t.Fatalf("service state: %v %q %v", running, url, err)
	}
	return &client{t: t, url: url}
}

// post sends raw as the body of a POST and returns the status and body.
func (c *client) post(raw string, header map[string]string) (int, []byte) {
	c.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, c.url, strings.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (c *client) request(method string, params any) map[string]any {
	c.t.Helper()
	c.id++
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": c.id, "method": method, "params": params})
	status, body := c.post(string(b), nil)
	if status != http.StatusOK {
		c.t.Fatalf("%s: status %d %s", method, status, body)
	}
	var r map[string]any
	if err := json.Unmarshal(body, &r); err != nil {
		c.t.Fatalf("bad response %q: %v", body, err)
	}
	if id, _ := r["id"].(float64); int(id) != c.id {
		c.t.Fatalf("response id %v, want %d", r["id"], c.id)
	}
	return r
}

// call runs a tool and returns its text and whether it reported an error.
func (c *client) call(name string, args map[string]any) (string, bool) {
	c.t.Helper()
	r := c.request("tools/call", map[string]any{"name": name, "arguments": args})
	res, ok := r["result"].(map[string]any)
	if !ok {
		c.t.Fatalf("%s: no result in %v", name, r)
	}
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	return text, res["isError"].(bool)
}

func errorCode(r map[string]any) int {
	e, ok := r["error"].(map[string]any)
	if !ok {
		return 0
	}
	return int(e["code"].(float64))
}

func toolNames(r map[string]any) []string {
	var names []string
	for _, t := range r["result"].(map[string]any)["tools"].([]any) {
		names = append(names, t.(map[string]any)["name"].(string))
	}
	return names
}

// configure changes the MCP settings, as saving the app's dialog does.
func configure(st *store.Store, f func(*store.MCPSettings)) {
	st.UpdateSettings(func(s *store.Settings) { f(&s.MCP) })
}

func TestProtocol(t *testing.T) {
	c := start(t, store.Open(t.TempDir()))

	r := c.request("initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t", "version": "1"}})
	res := r["result"].(map[string]any)
	if res["protocolVersion"] != "2025-06-18" {
		t.Fatalf("negotiated %v", res["protocolVersion"])
	}
	if res["serverInfo"].(map[string]any)["version"] != "test" {
		t.Fatalf("serverInfo %v", res["serverInfo"])
	}
	if _, ok := res["capabilities"].(map[string]any)["tools"]; !ok {
		t.Fatalf("no tools capability: %v", res["capabilities"])
	}
	r = c.request("initialize", map[string]any{"protocolVersion": "1999-01-01"})
	if v := r["result"].(map[string]any)["protocolVersion"]; v != protocolVersions[0] {
		t.Fatalf("unknown version negotiated to %v", v)
	}

	// Notifications are accepted without a body.
	if status, body := c.post(`{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil); status != http.StatusAccepted || len(body) != 0 {
		t.Fatalf("notification: %d %q", status, body)
	}
	if r := c.request("ping", nil); r["result"] == nil {
		t.Fatalf("ping: %v", r)
	}
	if r := c.request("nope", nil); errorCode(r) != codeMethodNotFound {
		t.Fatalf("unknown method: %v", r)
	}
	if r := c.request("tools/call", map[string]any{"name": "nope"}); errorCode(r) != codeInvalidParams {
		t.Fatalf("unknown tool: %v", r)
	}

	names := toolNames(c.request("tools/list", nil))
	if len(names) != len(allTools) || names[0] != "list_servers" {
		t.Fatalf("tools: %v", names)
	}

	status, body := c.post(`{not json`, nil)
	var perr map[string]any
	json.Unmarshal(body, &perr)
	if status != http.StatusBadRequest || errorCode(perr) != codeParse || perr["id"] != nil {
		t.Fatalf("parse error: %d %s", status, body)
	}

	status, body = c.post(`[{"jsonrpc":"2.0","id":"a","method":"ping"},{"jsonrpc":"2.0","method":"notifications/initialized"},{"jsonrpc":"2.0","id":"b","method":"ping"}]`, nil)
	var batch []any
	if json.Unmarshal(body, &batch); status != http.StatusOK || len(batch) != 2 {
		t.Fatalf("batch: %d %s", status, body)
	}

	text, isErr := c.call("list_servers", nil)
	if isErr || !strings.Contains(text, "No servers") {
		t.Fatalf("empty list_servers: %v %q", isErr, text)
	}
}

// TestHTTPGuards checks the defenses against web pages and DNS rebinding.
func TestHTTPGuards(t *testing.T) {
	c := start(t, store.Open(t.TempDir()))
	ping := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	for _, tc := range []struct {
		name   string
		header map[string]string
		want   int
	}{
		{"plain", nil, http.StatusOK},
		{"local page", map[string]string{"Origin": "http://localhost:6274"}, http.StatusOK},
		{"other site", map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		{"rebound host", map[string]string{"Host": "evil.example:14559"}, http.StatusForbidden},
		{"form post", map[string]string{"Content-Type": "text/plain"}, http.StatusUnsupportedMediaType},
	} {
		req, _ := http.NewRequest(http.MethodPost, c.url, strings.NewReader(ping))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range tc.header {
			if k == "Host" {
				req.Host = v
			} else {
				req.Header.Set(k, v)
			}
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s: status %d, want %d", tc.name, resp.StatusCode, tc.want)
		}
	}
	resp, err := http.Get(c.url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET: status %d", resp.StatusCode)
	}
	resp, err = http.Post(strings.TrimSuffix(c.url, "/mcp")+"/other", "application/json", bytes.NewReader([]byte(ping)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("other path: status %d", resp.StatusCode)
	}
}

func TestServiceRestart(t *testing.T) {
	st := store.Open(t.TempDir())
	sv := NewService(Options{Store: st})
	if err := sv.Start(0); err != nil {
		t.Fatal(err)
	}
	_, url, _ := sv.State()
	// A second service cannot take the same port.
	port := strings.TrimSuffix(strings.TrimPrefix(url, "http://127.0.0.1:"), "/mcp")
	other := NewService(Options{Store: st})
	var n int
	json.Unmarshal([]byte(port), &n)
	if err := other.Start(n); err == nil || !strings.Contains(err.Error(), "已被占用") {
		t.Fatalf("second service on %d: %v", n, err)
	}
	if running, _, err := other.State(); running || err == nil {
		t.Fatalf("failed service state: %v %v", running, err)
	}
	sv.Stop()
	if running, _, _ := sv.State(); running {
		t.Fatal("stopped service still running")
	}
	if err := other.Start(n); err != nil {
		t.Fatalf("port not freed by Stop: %v", err)
	}
	other.Stop()
}

func TestFindProfile(t *testing.T) {
	ps := []store.Profile{
		{ID: "a1", Name: "Web", Host: "10.0.0.1", Port: 22, User: "root"},
		{ID: "b2", Name: "db", Host: "10.0.0.2", Port: 2222, User: "admin"},
		{ID: "c3", Name: "db", Host: "10.0.0.3", Port: 22, User: "admin"},
	}
	for ref, want := range map[string]string{"a1": "a1", "web": "a1", "root@10.0.0.1": "a1", "admin@10.0.0.2:2222": "b2", "10.0.0.3": "c3"} {
		p, err := findProfile(ps, ref)
		if err != nil || p.ID != want {
			t.Errorf("findProfile(%q) = %q, %v; want %q", ref, p.ID, err, want)
		}
	}
	for _, ref := range []string{"", "db", "nope"} {
		if _, err := findProfile(ps, ref); err == nil {
			t.Errorf("findProfile(%q) succeeded", ref)
		}
	}
}

func TestCapture(t *testing.T) {
	c := &capture{limit: 10}
	io.WriteString(c, "hello")
	if c.String() != "hello" {
		t.Fatalf("short: %q", c.String())
	}
	io.WriteString(c, "0123456789")
	io.WriteString(c, "world")
	if got := c.String(); got != "hello\n… [10 bytes omitted] …\nworld" {
		t.Fatalf("long: %q", got)
	}
	if shellQuote("it's") != `'it'\''s'` {
		t.Fatalf("shellQuote: %s", shellQuote("it's"))
	}
}

func TestTools(t *testing.T) {
	srv, err := sshtest.Start("127.0.0.1:0", "demo", "demo")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	srv.Populate()
	dir := t.TempDir()
	st := store.Open(dir)
	p := st.SaveProfile(store.Profile{Name: "demo", Host: "127.0.0.1", Port: srv.Port(), User: "demo"})
	st.SetPassword(p.ID, "demo")
	st.SaveProfile(store.Profile{Name: "nopass", Host: "127.0.0.1", Port: srv.Port(), User: "demo"})
	c := start(t, st)
	expect := func(name string, args map[string]any, wantErr bool, want ...string) string {
		t.Helper()
		text, isErr := c.call(name, args)
		if isErr != wantErr {
			t.Fatalf("%s %v: isError=%v\n%s", name, args, isErr, text)
		}
		for _, w := range want {
			if !strings.Contains(text, w) {
				t.Fatalf("%s %v: missing %q in\n%s", name, args, w, text)
			}
		}
		return text
	}

	expect("list_servers", nil, false, p.ID, `"name": "demo"`, "nopass")
	expect("run_command", map[string]any{"server": "nopass", "command": "echo hi"}, true, "no working password")
	expect("run_command", map[string]any{"server": "missing", "command": "echo hi"}, true, "no saved server")

	expect("run_command", map[string]any{"server": p.ID, "command": "echo hello"}, false, "exit code: 0", "--- stdout ---\nhello")
	expect("run_command", map[string]any{"server": "demo", "command": "frobnicate"}, false, "exit code: 127", "--- stderr ---", "command not found")

	expect("list_directory", map[string]any{"server": "demo", "path": "/home/demo"}, false,
		"/home/demo (", "projects/", "notes.md", "www/ -> /var/www/html")
	expect("list_directory", map[string]any{"server": "demo", "path": "/nope"}, true)

	expect("read_file", map[string]any{"server": "demo", "path": "/home/demo/notes.md"}, false, "部署记录")
	expect("read_file", map[string]any{"server": "demo", "path": "/etc/hostname", "max_bytes": 3}, false, "nlr\n[bytes 0-3 of 9; pass offset 3")
	expect("read_file", map[string]any{"server": "demo", "path": "/etc/hostname", "offset": 3}, false, "-demo\n\n[bytes 3-9 of 9]")
	expect("read_file", map[string]any{"server": "demo", "path": "/opt/app/bin/app"}, true, "binary")
	expect("read_file", map[string]any{"server": "demo", "path": "/home/demo"}, true, "directory")

	expect("write_file", map[string]any{"server": "demo", "path": "/tmp/new/a.txt", "content": "one\n", "create_dirs": true}, false, "Wrote 4 bytes")
	expect("write_file", map[string]any{"server": "demo", "path": "/tmp/new/a.txt", "content": "two\n", "append": true}, false, "Appended 4 bytes")
	if text := expect("read_file", map[string]any{"server": "demo", "path": "/tmp/new/a.txt"}, false); text != "one\ntwo\n" {
		t.Fatalf("after append: %q", text)
	}

	local := t.TempDir()
	src := filepath.Join(local, "up.txt")
	os.WriteFile(src, []byte("uploaded"), 0o644)
	expect("upload_file", map[string]any{"server": "demo", "local_path": src, "remote_path": "/tmp"}, false, "/tmp/up.txt (8 bytes)")
	if text := expect("read_file", map[string]any{"server": "demo", "path": "/tmp/up.txt"}, false); text != "uploaded" {
		t.Fatalf("uploaded file: %q", text)
	}
	expect("upload_file", map[string]any{"server": "demo", "local_path": "up.txt", "remote_path": "/tmp"}, true, "absolute")

	expect("download_file", map[string]any{"server": "demo", "remote_path": "/etc/hostname", "local_path": local}, false, "(9 bytes)")
	if b, err := os.ReadFile(filepath.Join(local, "hostname")); err != nil || string(b) != "nlr-demo\n" {
		t.Fatalf("downloaded file: %q %v", b, err)
	}

	expect("system_status", map[string]any{"server": "demo"}, false,
		"Host: nlr-demo", "Ubuntu 24.04.1 LTS", "CPU:", "Memory:", "Disks:", "Processes:")
}

func TestSettings(t *testing.T) {
	srv, err := sshtest.Start("127.0.0.1:0", "demo", "demo")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	st := store.Open(dir)
	web := st.SaveProfile(store.Profile{Name: "web", Host: "127.0.0.1", Port: srv.Port(), User: "demo"})
	st.SetPassword(web.ID, "demo")
	db := st.SaveProfile(store.Profile{Name: "db", Host: "localhost", Port: srv.Port(), User: "demo"})
	st.SetPassword(db.ID, "demo")

	c := start(t, st)
	expect := func(name string, args map[string]any, wantErr bool, want string) {
		t.Helper()
		text, isErr := c.call(name, args)
		if isErr != wantErr || !strings.Contains(text, want) {
			t.Fatalf("%s %v: isError=%v, want %v and %q in\n%s", name, args, isErr, wantErr, want, text)
		}
	}
	echo := map[string]any{"server": "web", "command": "echo hi"}

	// New host keys refused unless allowed.
	configure(st, func(m *store.MCPSettings) { m.TrustNewHosts = false })
	expect("run_command", echo, true, "not known yet")
	configure(st, func(m *store.MCPSettings) { m.TrustNewHosts = true })
	expect("run_command", echo, false, "hi")
	// Once known, the key is accepted without the setting.
	configure(st, func(m *store.MCPSettings) { m.TrustNewHosts = false })
	expect("run_command", echo, false, "hi")

	// Read-only mode from the settings.
	configure(st, func(m *store.MCPSettings) { m.ReadOnly = true })
	if names := strings.Join(toolNames(c.request("tools/list", nil)), " "); strings.Contains(names, "run_command") {
		t.Fatalf("read-only settings offer run_command: %s", names)
	}
	expect("run_command", echo, true, "read-only")
	configure(st, func(m *store.MCPSettings) { m.ReadOnly = false })

	// Only the chosen servers.
	configure(st, func(m *store.MCPSettings) { m.AllServers, m.Servers = false, []string{web.ID} })
	text, _ := c.call("list_servers", nil)
	if !strings.Contains(text, web.ID) || strings.Contains(text, db.ID) {
		t.Fatalf("list_servers with one server open:\n%s", text)
	}
	expect("run_command", map[string]any{"server": "db", "command": "echo hi"}, true, "not open to AI assistants")
	configure(st, func(m *store.MCPSettings) { m.Servers = nil })
	expect("list_servers", nil, false, "No saved server is open")
}

func TestBusiest(t *testing.T) {
	all := []monitor.Proc{
		{PID: 1, Name: "systemd", CPU: 0, Mem: 14 << 20},
		{PID: 2, Name: "kworker/0:1", CPU: 3, Mem: 0},
		{PID: 3, Name: "java", CPU: 0, Mem: 350 << 20},
		{PID: 4, Name: "qq", CPU: 1, Mem: 190 << 20},
		{PID: 5, Name: "nginx", CPU: 1, Mem: 20 << 20},
	}
	var names []string
	for _, p := range busiest(all, 3) {
		names = append(names, p.Name)
	}
	if got := strings.Join(names, " "); got != "qq nginx java" {
		t.Fatalf("busiest: %s", got)
	}
	if n := len(busiest(all, statusProcs)); n != 4 {
		t.Fatalf("kernel threads kept: %d processes", n)
	}
}
