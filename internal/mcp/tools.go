package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"nlrshell/internal/sshx"
	"nlrshell/internal/store"
)

type tool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations annotations    `json:"annotations"`

	// writes marks tools that change remote hosts (see MCPSettings.ReadOnly).
	writes bool
	run    func(ctx context.Context, tc *toolCall, args []byte) (string, error)
}

// toolCall is what a tool works with: the store and settings as they were
// when the call came in.
type toolCall struct {
	s   *Server
	st  *store.Store
	cfg store.MCPSettings
}

// toolsFor returns the tools offered under cfg.
func toolsFor(cfg store.MCPSettings) []*tool {
	var ts []*tool
	for _, t := range allTools {
		if !cfg.ReadOnly || !t.writes {
			ts = append(ts, t)
		}
	}
	return ts
}

// settingsHint tells where the user changes what assistants may do.
const settingsHint = "NLR Shell (click the logo at the top left → MCP 服务)"

type annotations struct {
	ReadOnly    bool `json:"readOnlyHint"`
	Destructive bool `json:"destructiveHint"`
	Idempotent  bool `json:"idempotentHint"`
	OpenWorld   bool `json:"openWorldHint"`
}

// Limits on what a tool returns, so that one call cannot flood the
// assistant's context.
const (
	outputLimit    = 32 << 10 // per stream of run_command
	readLimit      = 128 << 10
	listLimit      = 1000
	defaultTimeout = 60
	maxTimeout     = 1800
)

func object(required []string, props map[string]any) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func prop(typ, desc string) map[string]any {
	return map[string]any{"type": typ, "description": desc}
}

var serverProp = prop("string", "Server id from list_servers (a unique name or user@host also works)")

var allTools = []*tool{
	{
		Name:        "list_servers",
		Title:       "List saved servers",
		Description: "List the SSH servers saved in NLR Shell with their id, name, group and address. Call this first to find the server to use.",
		InputSchema: object(nil, map[string]any{}),
		Annotations: annotations{ReadOnly: true, Idempotent: true},
		run:         listServers,
	},
	{
		Name:  "run_command",
		Title: "Run command",
		Description: "Run a shell command on a server and return its exit code, stdout and stderr. " +
			"The command runs non-interactively without a TTY in the login shell of the user; " +
			"long output is shortened to its beginning and end.",
		InputSchema: object([]string{"server", "command"}, map[string]any{
			"server":          serverProp,
			"command":         prop("string", "Shell command line to run"),
			"cwd":             prop("string", "Directory to run the command in (default: the home directory)"),
			"stdin":           prop("string", "Text to send to the command's standard input"),
			"timeout_seconds": prop("integer", fmt.Sprintf("Kill the command after this many seconds (default %d, at most %d)", defaultTimeout, maxTimeout)),
		}),
		Annotations: annotations{Destructive: true, OpenWorld: true},
		writes:      true,
		run:         runCommand,
	},
	{
		Name:        "list_directory",
		Title:       "List directory",
		Description: "List a remote directory over SFTP: type, permissions, size, modification time and name of each entry, directories first.",
		InputSchema: object([]string{"server"}, map[string]any{
			"server": serverProp,
			"path":   prop("string", "Directory to list (default: the home directory)"),
		}),
		Annotations: annotations{ReadOnly: true, Idempotent: true},
		run:         listDirectory,
	},
	{
		Name:        "read_file",
		Title:       "Read file",
		Description: fmt.Sprintf("Read a remote text file over SFTP. Returns at most %d KiB per call; use offset to read further. Binary files are refused (use download_file).", readLimit>>10),
		InputSchema: object([]string{"server", "path"}, map[string]any{
			"server":    serverProp,
			"path":      prop("string", "File to read"),
			"offset":    prop("integer", "Byte offset to start reading at (default 0)"),
			"max_bytes": prop("integer", fmt.Sprintf("Number of bytes to read (default and maximum %d)", readLimit)),
		}),
		Annotations: annotations{ReadOnly: true, Idempotent: true},
		run:         readFile,
	},
	{
		Name:        "write_file",
		Title:       "Write file",
		Description: "Write text to a remote file over SFTP, replacing its contents (an existing file keeps its permissions) or appending to it.",
		InputSchema: object([]string{"server", "path", "content"}, map[string]any{
			"server":      serverProp,
			"path":        prop("string", "File to write"),
			"content":     prop("string", "Text to write"),
			"append":      prop("boolean", "Append to the file instead of replacing it"),
			"create_dirs": prop("boolean", "Create missing parent directories"),
		}),
		Annotations: annotations{Destructive: true},
		writes:      true,
		run:         writeFile,
	},
	{
		Name:        "upload_file",
		Title:       "Upload file",
		Description: "Copy a file from this computer to a server over SFTP. If remote_path is an existing directory the file keeps its name.",
		InputSchema: object([]string{"server", "local_path", "remote_path"}, map[string]any{
			"server":      serverProp,
			"local_path":  prop("string", "Absolute path of the local file"),
			"remote_path": prop("string", "Remote file or directory to copy to"),
		}),
		Annotations: annotations{Destructive: true},
		writes:      true,
		run:         uploadFile,
	},
	{
		Name:        "download_file",
		Title:       "Download file",
		Description: "Copy a file from a server to this computer over SFTP. If local_path is an existing directory the file keeps its name.",
		InputSchema: object([]string{"server", "remote_path", "local_path"}, map[string]any{
			"server":      serverProp,
			"remote_path": prop("string", "Remote file to copy"),
			"local_path":  prop("string", "Absolute local file or directory to copy to"),
		}),
		Annotations: annotations{Idempotent: true},
		run:         downloadFile,
	},
	{
		Name:  "system_status",
		Title: "System status",
		Description: "Sample the status of a Linux server, as in NLR Shell's monitor panel: OS and kernel, CPU usage per core, " +
			"load, memory and swap, uptime, network rates, disk usage and the busiest processes. Takes about a second.",
		InputSchema: object([]string{"server"}, map[string]any{
			"server": serverProp,
		}),
		Annotations: annotations{ReadOnly: true},
		run:         systemStatus,
	},
}

func (s *Server) callTool(ctx context.Context, params []byte) (any, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "invalid params: " + err.Error()}
	}
	var t *tool
	for _, c := range allTools {
		if c.Name == p.Name {
			t = c
		}
	}
	if t == nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "unknown tool: " + p.Name}
	}
	args := []byte(p.Arguments)
	if len(args) == 0 || string(args) == "null" {
		args = []byte("{}")
	}
	st, cfg := s.opt.Store, s.config()
	var (
		text string
		err  error
	)
	switch {
	case t.writes && cfg.ReadOnly:
		err = fmt.Errorf("%s is not available: NLR Shell's MCP service is in read-only mode (see %s)", t.Name, settingsHint)
	default:
		text, err = t.run(ctx, &toolCall{s: s, st: st, cfg: cfg}, args)
	}
	if err != nil {
		text = err.Error()
	}
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": strings.ToValidUTF8(text, "�")}},
		"isError": err != nil,
	}, nil
}

func decode(args []byte, v any) error {
	if err := json.Unmarshal(args, v); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}

// allowed returns the profiles the settings open to assistants.
func (tc *toolCall) allowed() []store.Profile {
	var ps []store.Profile
	for _, p := range tc.st.Profiles() {
		if tc.cfg.Allows(p.ID) {
			ps = append(ps, p)
		}
	}
	return ps
}

// connect finds the saved profile named by server and returns a connection
// to it. The caller must release it.
func (tc *toolCall) connect(ctx context.Context, server string) (*pooled, error) {
	p, err := findProfile(tc.allowed(), server)
	if err != nil {
		if q, e := findProfile(tc.st.Profiles(), server); e == nil {
			return nil, fmt.Errorf("%s is not open to AI assistants; the user can allow it in %s", q.Title(), settingsHint)
		}
		return nil, err
	}
	return tc.s.pool.get(ctx, p, tc.st, tc.cfg.TrustNewHosts)
}

func (tc *toolCall) release(e *pooled) { tc.s.pool.release(e) }

// findProfile resolves a server reference: an id, or else a name or
// address that matches exactly one profile.
func findProfile(ps []store.Profile, ref string) (store.Profile, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return store.Profile{}, errors.New(`"server" is required; call list_servers to see the saved servers`)
	}
	for _, p := range ps {
		if p.ID == ref {
			return p, nil
		}
	}
	var hits []store.Profile
	for _, p := range ps {
		for _, name := range []string{p.Title(), p.Addr(), p.User + "@" + p.Host, p.Host} {
			if strings.EqualFold(name, ref) {
				hits = append(hits, p)
				break
			}
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return store.Profile{}, fmt.Errorf("no saved server matches %q; call list_servers to see the saved servers", ref)
	}
	var ids []string
	for _, p := range hits {
		ids = append(ids, fmt.Sprintf("%s (%s, id %s)", p.Title(), p.Addr(), p.ID))
	}
	return store.Profile{}, fmt.Errorf("%q matches several servers, pass the id instead: %s", ref, strings.Join(ids, "; "))
}

// ---- list_servers -----------------------------------------------------

func listServers(ctx context.Context, tc *toolCall, args []byte) (string, error) {
	st := tc.st
	ps := tc.allowed()
	if len(ps) == 0 {
		if len(st.Profiles()) > 0 {
			return "No saved server is open to AI assistants. Ask the user to choose servers in " + settingsHint + ".", nil
		}
		return "No servers are saved in NLR Shell yet. Ask the user to add a connection in the app first.", nil
	}
	type server struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Group    string `json:"group,omitempty"`
		Address  string `json:"address"`
		Auth     string `json:"auth"`
		Jump     string `json:"jumpHost,omitempty"`
		Proxy    string `json:"proxy,omitempty"`
		LastUsed string `json:"lastUsed,omitempty"`
	}
	out := make([]server, 0, len(ps))
	for _, p := range ps {
		v := server{ID: p.ID, Name: p.Title(), Group: p.Group, Address: p.Addr(), Auth: p.Auth}
		if p.JumpID != "" {
			if jp, ok := st.Profile(p.JumpID); ok {
				v.Jump = jp.Title()
			}
		}
		if p.ProxyType != store.ProxyNone {
			v.Proxy = p.ProxyType + "://" + p.ProxyAddr
		}
		if p.LastUsed > 0 {
			v.LastUsed = time.Unix(p.LastUsed, 0).Format(time.RFC3339)
		}
		out = append(out, v)
	}
	b, err := json.MarshalIndent(out, "", "  ")
	return string(b), err
}

// ---- run_command ------------------------------------------------------

func runCommand(ctx context.Context, tc *toolCall, args []byte) (string, error) {
	var a struct {
		Server  string `json:"server"`
		Command string `json:"command"`
		Cwd     string `json:"cwd"`
		Stdin   string `json:"stdin"`
		Timeout int    `json:"timeout_seconds"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Command) == "" {
		return "", errors.New(`"command" is required`)
	}
	timeout := a.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	timeout = min(timeout, maxTimeout)
	e, err := tc.connect(ctx, a.Server)
	if err != nil {
		return "", err
	}
	defer tc.release(e)

	cmd := a.Command
	if a.Cwd != "" {
		cmd = "cd -- " + shellQuote(a.Cwd) + " && " + cmd
	}
	sess, err := e.conn.NewSession()
	if err != nil {
		return "", fmt.Errorf("cannot open a channel: %w", err)
	}
	defer sess.Close()
	stdout, stderr := &capture{limit: outputLimit}, &capture{limit: outputLimit}
	sess.Stdout, sess.Stderr = stdout, stderr
	if a.Stdin != "" {
		sess.Stdin = strings.NewReader(a.Stdin)
	}
	if err := sess.Start(cmd); err != nil {
		return "", fmt.Errorf("cannot start the command: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- sess.Wait() }()

	var stopped string
	timer := time.NewTimer(time.Duration(timeout) * time.Second)
	defer timer.Stop()
	select {
	case err = <-done:
	case <-timer.C:
		stopped = fmt.Sprintf("the command did not finish within %d seconds and was killed", timeout)
	case <-ctx.Done():
		stopped = "the command was canceled"
	}
	if stopped != "" {
		sess.Signal(ssh.SIGKILL)
		sess.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}

	var sb strings.Builder
	var exitErr *ssh.ExitError
	var missing *ssh.ExitMissingError
	switch {
	case stopped != "":
		sb.WriteString(stopped + "\n")
	case err == nil:
		sb.WriteString("exit code: 0\n")
	case errors.As(err, &exitErr):
		fmt.Fprintf(&sb, "exit code: %d", exitErr.ExitStatus())
		if sig := exitErr.Signal(); sig != "" {
			fmt.Fprintf(&sb, " (signal %s)", sig)
		}
		sb.WriteByte('\n')
	case errors.As(err, &missing):
		sb.WriteString("exit code: unknown (the server did not report one)\n")
	default:
		return "", fmt.Errorf("the command failed: %w", err)
	}
	if out := stdout.String(); out != "" {
		sb.WriteString("--- stdout ---\n" + out)
		if !strings.HasSuffix(out, "\n") {
			sb.WriteByte('\n')
		}
	}
	if out := stderr.String(); out != "" {
		sb.WriteString("--- stderr ---\n" + out)
	}
	if stopped != "" {
		return "", errors.New(strings.TrimRight(sb.String(), "\n"))
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// capture keeps the beginning and the end of a stream that may be too long
// to return whole.
type capture struct {
	limit      int
	head, tail []byte
	total      int64
}

func (c *capture) Write(b []byte) (int, error) {
	n := len(b)
	c.total += int64(n)
	if room := c.limit/2 - len(c.head); room > 0 {
		k := min(room, len(b))
		c.head = append(c.head, b[:k]...)
		b = b[k:]
	}
	if len(b) > 0 {
		c.tail = append(c.tail, b...)
		if over := len(c.tail) - c.limit/2; over > 0 {
			c.tail = append(c.tail[:0], c.tail[over:]...)
		}
	}
	return n, nil
}

func (c *capture) String() string {
	kept := int64(len(c.head) + len(c.tail))
	if c.total <= kept {
		return string(c.head) + string(c.tail)
	}
	return fmt.Sprintf("%s\n… [%d bytes omitted] …\n%s", c.head, c.total-kept, c.tail)
}

// ---- Files ------------------------------------------------------------

func listDirectory(ctx context.Context, tc *toolCall, args []byte) (string, error) {
	var a struct {
		Server string `json:"server"`
		Path   string `json:"path"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	e, err := tc.connect(ctx, a.Server)
	if err != nil {
		return "", err
	}
	defer tc.release(e)
	c, err := e.sftp()
	if err != nil {
		return "", err
	}
	dir := a.Path
	if dir == "" {
		dir = "."
	}
	real, err := c.RealPath(dir)
	if err != nil {
		return "", fmt.Errorf("%s: %w", dir, err)
	}
	infos, err := c.ReadDir(real)
	if err != nil {
		return "", fmt.Errorf("%s: %w", real, err)
	}
	type entry struct {
		info   os.FileInfo
		isDir  bool
		target string
	}
	entries := make([]entry, 0, len(infos))
	for _, fi := range infos {
		en := entry{info: fi, isDir: fi.IsDir()}
		if fi.Mode()&os.ModeSymlink != 0 {
			full := path.Join(real, fi.Name())
			en.target, _ = c.ReadLink(full)
			if ti, err := c.Stat(full); err == nil {
				en.isDir = ti.IsDir()
			}
		}
		entries = append(entries, en)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].isDir != entries[j].isDir {
			return entries[i].isDir
		}
		return strings.ToLower(entries[i].info.Name()) < strings.ToLower(entries[j].info.Name())
	})

	var sb strings.Builder
	fmt.Fprintf(&sb, "%s (%d entries)\n", real, len(entries))
	for i, en := range entries {
		if i == listLimit {
			fmt.Fprintf(&sb, "… %d more entries not shown\n", len(entries)-listLimit)
			break
		}
		fi := en.info
		name := fi.Name()
		if en.isDir {
			name += "/"
		}
		if en.target != "" {
			name += " -> " + en.target
		}
		fmt.Fprintf(&sb, "%s %10d  %s  %s\n", fi.Mode(), fi.Size(), fi.ModTime().Format("2006-01-02 15:04"), name)
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

func readFile(ctx context.Context, tc *toolCall, args []byte) (string, error) {
	var a struct {
		Server   string `json:"server"`
		Path     string `json:"path"`
		Offset   int64  `json:"offset"`
		MaxBytes int64  `json:"max_bytes"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if a.Path == "" {
		return "", errors.New(`"path" is required`)
	}
	n := a.MaxBytes
	if n <= 0 || n > readLimit {
		n = readLimit
	}
	e, err := tc.connect(ctx, a.Server)
	if err != nil {
		return "", err
	}
	defer tc.release(e)
	c, err := e.sftp()
	if err != nil {
		return "", err
	}
	// Some servers refuse to open a directory, so look before opening.
	fi, err := c.Stat(a.Path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", a.Path, err)
	}
	if fi.IsDir() {
		return "", fmt.Errorf("%s is a directory; use list_directory", a.Path)
	}
	f, err := c.Open(a.Path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", a.Path, err)
	}
	defer f.Close()
	if a.Offset > 0 {
		if _, err := f.Seek(a.Offset, io.SeekStart); err != nil {
			return "", fmt.Errorf("%s: %w", a.Path, err)
		}
	}
	b, err := io.ReadAll(io.LimitReader(f, n))
	if err != nil {
		return "", fmt.Errorf("%s: %w", a.Path, err)
	}
	if bytes.IndexByte(b, 0) >= 0 {
		return "", fmt.Errorf("%s looks like a binary file; use download_file to copy it", a.Path)
	}
	text := string(b)
	if end := a.Offset + int64(len(b)); end < fi.Size() || a.Offset > 0 {
		text += fmt.Sprintf("\n[bytes %d-%d of %d", a.Offset, end, fi.Size())
		if end < fi.Size() {
			text += fmt.Sprintf("; pass offset %d to read on", end)
		}
		text += "]"
	}
	return text, nil
}

func writeFile(ctx context.Context, tc *toolCall, args []byte) (string, error) {
	var a struct {
		Server     string `json:"server"`
		Path       string `json:"path"`
		Content    string `json:"content"`
		Append     bool   `json:"append"`
		CreateDirs bool   `json:"create_dirs"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if a.Path == "" {
		return "", errors.New(`"path" is required`)
	}
	e, err := tc.connect(ctx, a.Server)
	if err != nil {
		return "", err
	}
	defer tc.release(e)
	c, err := e.sftp()
	if err != nil {
		return "", err
	}
	if a.CreateDirs {
		if dir := path.Dir(a.Path); dir != "." && dir != "/" {
			if err := c.MkdirAll(dir); err != nil {
				return "", fmt.Errorf("%s: %w", dir, err)
			}
		}
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if a.Append {
		flags = os.O_WRONLY | os.O_CREATE | os.O_APPEND
	}
	f, err := c.OpenFile(a.Path, flags)
	if err != nil {
		return "", fmt.Errorf("%s: %w", a.Path, err)
	}
	if a.Append {
		// Not every server honors the append flag.
		if _, err := f.Seek(0, io.SeekEnd); err != nil {
			f.Close()
			return "", fmt.Errorf("%s: %w", a.Path, err)
		}
	}
	if _, err := f.Write([]byte(a.Content)); err != nil {
		f.Close()
		return "", fmt.Errorf("%s: %w", a.Path, err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("%s: %w", a.Path, err)
	}
	verb := "Wrote"
	if a.Append {
		verb = "Appended"
	}
	return fmt.Sprintf("%s %d bytes to %s", verb, len(a.Content), a.Path), nil
}

// ctxReader and ctxWriter stop a copy when the tool call is canceled.
type ctxReader struct {
	ctx  context.Context
	r    io.Reader
	size int64
}

func (c ctxReader) Read(b []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(b)
}

// Size lets the SFTP client upload with concurrent writes.
func (c ctxReader) Size() int64 { return c.size }

type ctxWriter struct {
	ctx context.Context
	w   io.Writer
}

func (c ctxWriter) Write(b []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.w.Write(b)
}

func uploadFile(ctx context.Context, tc *toolCall, args []byte) (string, error) {
	var a struct {
		Server string `json:"server"`
		Local  string `json:"local_path"`
		Remote string `json:"remote_path"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if !filepath.IsAbs(a.Local) {
		return "", errors.New(`"local_path" must be an absolute path`)
	}
	if a.Remote == "" {
		return "", errors.New(`"remote_path" is required`)
	}
	lf, err := os.Open(a.Local)
	if err != nil {
		return "", err
	}
	defer lf.Close()
	lfi, err := lf.Stat()
	if err != nil {
		return "", err
	}
	if lfi.IsDir() {
		return "", fmt.Errorf("%s is a directory; only single files can be uploaded", a.Local)
	}

	e, err := tc.connect(ctx, a.Server)
	if err != nil {
		return "", err
	}
	defer tc.release(e)
	c, err := e.sftp()
	if err != nil {
		return "", err
	}
	remote := a.Remote
	if fi, err := c.Stat(remote); err == nil && fi.IsDir() || strings.HasSuffix(remote, "/") {
		remote = path.Join(remote, filepath.Base(a.Local))
	}
	rf, err := c.OpenFile(remote, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return "", fmt.Errorf("%s: %w", remote, err)
	}
	n, err := rf.ReadFrom(ctxReader{ctx, lf, lfi.Size()})
	if cerr := rf.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("upload to %s failed after %d bytes: %w", remote, n, err)
	}
	return fmt.Sprintf("Uploaded %s to %s (%d bytes)", a.Local, remote, n), nil
}

func downloadFile(ctx context.Context, tc *toolCall, args []byte) (string, error) {
	var a struct {
		Server string `json:"server"`
		Remote string `json:"remote_path"`
		Local  string `json:"local_path"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if !filepath.IsAbs(a.Local) {
		return "", errors.New(`"local_path" must be an absolute path`)
	}
	if a.Remote == "" {
		return "", errors.New(`"remote_path" is required`)
	}
	e, err := tc.connect(ctx, a.Server)
	if err != nil {
		return "", err
	}
	defer tc.release(e)
	c, err := e.sftp()
	if err != nil {
		return "", err
	}
	if fi, err := c.Stat(a.Remote); err != nil {
		return "", fmt.Errorf("%s: %w", a.Remote, err)
	} else if fi.IsDir() {
		return "", fmt.Errorf("%s is a directory; only single files can be downloaded", a.Remote)
	}
	rf, err := c.Open(a.Remote)
	if err != nil {
		return "", fmt.Errorf("%s: %w", a.Remote, err)
	}
	defer rf.Close()

	local := a.Local
	if fi, err := os.Stat(local); err == nil && fi.IsDir() {
		local = filepath.Join(local, sshx.SafeName(path.Base(a.Remote)))
	}
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		return "", err
	}
	lf, err := os.Create(local)
	if err != nil {
		return "", err
	}
	n, err := rf.WriteTo(ctxWriter{ctx, lf})
	if cerr := lf.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(local)
		return "", fmt.Errorf("download of %s failed after %d bytes: %w", a.Remote, n, err)
	}
	return fmt.Sprintf("Downloaded %s to %s (%d bytes)", a.Remote, local, n), nil
}
