// Package sshx implements the SSH side of NLR Shell: connecting and
// authenticating, the interactive shell, system monitoring, SFTP, file
// transfers and port forwarding. It has no UI dependencies; user interaction
// goes through the Prompter interface.
package sshx

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"

	"nlrshell/internal/store"
)

// ErrCanceled is returned when the user dismisses a prompt.
var ErrCanceled = errors.New("已取消")

// Prompter asks the user for decisions and secrets during connection. Its
// methods are called from connection goroutines and block until answered.
type Prompter interface {
	// HostKey asks whether to trust a host key. changed is true when a
	// different key for the host is already on record.
	HostKey(host, keyType, fingerprint string, changed bool) bool
	// Password asks for a password. save reports whether to remember it.
	Password(title string) (password string, save, ok bool)
	// Passphrase asks for the passphrase of a private key.
	Passphrase(keyPath string) (string, bool)
	// Interactive answers keyboard-interactive challenges.
	Interactive(title, instruction string, questions []string, echo []bool) ([]string, bool)
}

type dialer struct {
	st       *store.Store
	prompter Prompter
	status   func(string)

	mu      sync.Mutex
	conns   []net.Conn
	aborted bool
}

// abort closes any connection in progress.
func (d *dialer) abort() {
	d.mu.Lock()
	d.aborted = true
	for _, c := range d.conns {
		c.Close()
	}
	d.mu.Unlock()
}

func (d *dialer) track(c net.Conn) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.aborted {
		c.Close()
		return ErrCanceled
	}
	d.conns = append(d.conns, c)
	return nil
}

// dial connects to p, going through its jump host chain if configured. It
// returns the client for p followed by the jump clients (outermost last).
func (d *dialer) dial(p store.Profile, depth int) ([]*ssh.Client, error) {
	if depth > 4 {
		return nil, errors.New("跳板机嵌套过深")
	}
	addr := net.JoinHostPort(p.Host, strconv.Itoa(portOf(p)))
	var (
		conn  net.Conn
		chain []*ssh.Client
		err   error
	)
	if p.JumpID != "" {
		jp, ok := d.st.Profile(p.JumpID)
		if !ok {
			return nil, errors.New("找不到跳板机配置")
		}
		chain, err = d.dial(jp, depth+1)
		if err != nil {
			return nil, fmt.Errorf("跳板机 %s: %w", jp.Title(), err)
		}
		d.status("经由 " + jp.Title() + " 连接 " + addr + " …")
		conn, err = chain[0].Dial("tcp", addr)
	} else if p.ProxyType != store.ProxyNone {
		d.status("经由代理 " + p.ProxyAddr + " 连接 " + addr + " …")
		// Proxy errors already say what went wrong; friendly would take
		// a refusal for one by the SSH server.
		if conn, err = dialProxy(p.ProxyType, p.ProxyAddr, addr, 15*time.Second); err != nil {
			return nil, err
		}
	} else {
		d.status("正在连接 " + addr + " …")
		conn, err = net.DialTimeout("tcp", addr, 15*time.Second)
	}
	if err != nil {
		closeClients(chain)
		return nil, friendly(err)
	}
	if err := d.track(conn); err != nil {
		closeClients(chain)
		return nil, err
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(30 * time.Second)
	}

	a := &authState{d: d, p: p}
	cfg := &ssh.ClientConfig{
		User:            p.User,
		Auth:            a.methods(),
		HostKeyCallback: d.hostKeyCallback(),
		ClientVersion:   "SSH-2.0-NLRShell",
	}
	d.status("正在验证身份 …")
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		closeClients(chain)
		if a.canceled {
			return nil, ErrCanceled
		}
		return nil, friendly(err)
	}
	if a.save && a.last != "" && p.ID != "" {
		d.st.SetPassword(p.ID, a.last)
	}
	client := ssh.NewClient(c, chans, reqs)
	return append([]*ssh.Client{client}, chain...), nil
}

func portOf(p store.Profile) int {
	if p.Port <= 0 {
		return 22
	}
	return p.Port
}

func closeClients(cs []*ssh.Client) {
	for _, c := range cs {
		c.Close()
	}
}

// friendly rewrites common low-level errors into something a user can act on.
func friendly(err error) error {
	if err == nil {
		return nil
	}
	s := err.Error()
	var ne net.Error
	switch {
	case errors.Is(err, ErrCanceled):
		return ErrCanceled
	case strings.Contains(s, "unable to authenticate"):
		return errors.New("认证失败：用户名、密码或密钥不正确")
	case errors.As(err, &ne) && ne.Timeout():
		return errors.New("连接超时：主机无响应")
	case strings.Contains(s, "actively refused") || strings.Contains(s, "connection refused"):
		return errors.New("连接被拒绝：目标端口未开放 SSH 服务")
	case strings.Contains(s, "no such host"):
		return errors.New("无法解析主机名")
	case strings.Contains(s, "handshake failed: EOF") || strings.Contains(s, "forcibly closed"):
		return errors.New("连接被对端关闭")
	}
	return err
}

// ---- Host keys --------------------------------------------------------

var knownHostsMu sync.Mutex

func (d *dialer) hostKeyCallback() ssh.HostKeyCallback {
	path := d.st.KnownHostsPath()
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		knownHostsMu.Lock()
		defer knownHostsMu.Unlock()
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0o600); err == nil {
			f.Close()
		}
		check, err := knownhosts.New(path)
		if err != nil {
			return fmt.Errorf("读取 known_hosts 失败: %w", err)
		}
		err = check(hostname, remote, key)
		if err == nil {
			return nil
		}
		var ke *knownhosts.KeyError
		if !errors.As(err, &ke) {
			return err
		}
		changed := len(ke.Want) > 0
		if !d.prompter.HostKey(hostname, key.Type(), ssh.FingerprintSHA256(key), changed) {
			return ErrCanceled
		}
		if changed {
			drop := map[int]bool{}
			for _, k := range ke.Want {
				if k.Filename == path {
					drop[k.Line] = true
				}
			}
			if err := removeLines(path, drop); err != nil {
				return err
			}
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.WriteString(knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key) + "\n")
		return err
	}
}

func removeLines(path string, drop map[int]bool) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var out []string
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		if !drop[n] {
			out = append(out, sc.Text())
		}
	}
	s := strings.Join(out, "\n")
	if s != "" {
		s += "\n"
	}
	return os.WriteFile(path, []byte(s), 0o600)
}

// ---- Authentication ---------------------------------------------------

type authState struct {
	d *dialer
	p store.Profile

	triedSaved bool
	last       string
	save       bool
	canceled   bool
}

func (a *authState) title() string {
	return a.p.User + "@" + a.p.Host
}

func (a *authState) methods() []ssh.AuthMethod {
	var ms []ssh.AuthMethod
	switch a.p.Auth {
	case store.AuthAgent:
		ms = append(ms, ssh.PublicKeysCallback(a.agentSigners))
	case store.AuthKey:
		ms = append(ms, ssh.PublicKeysCallback(a.keySigners))
	}
	ms = append(ms,
		ssh.RetryableAuthMethod(ssh.PasswordCallback(a.password), 3),
		ssh.RetryableAuthMethod(ssh.KeyboardInteractive(a.interactive), 3),
	)
	return ms
}

func (a *authState) password() (string, error) {
	if !a.triedSaved {
		a.triedSaved = true
		if pw := store.Decrypt(a.p.Password); pw != "" {
			a.last = pw
			return pw, nil
		}
	}
	pw, save, ok := a.d.prompter.Password(a.title())
	if !ok {
		a.canceled = true
		return "", ErrCanceled
	}
	a.last, a.save = pw, save
	return pw, nil
}

func (a *authState) interactive(name, instruction string, questions []string, echos []bool) ([]string, error) {
	if len(questions) == 0 {
		return nil, nil
	}
	// A single hidden question is almost always the password.
	if len(questions) == 1 && !echos[0] {
		pw, err := a.password()
		if err != nil {
			return nil, err
		}
		return []string{pw}, nil
	}
	title := a.title()
	if name != "" {
		title = name
	}
	ans, ok := a.d.prompter.Interactive(title, instruction, questions, echos)
	if !ok {
		a.canceled = true
		return nil, ErrCanceled
	}
	return ans, nil
}

func (a *authState) keySigners() ([]ssh.Signer, error) {
	// path names the key in the passphrase prompt.
	var (
		path string
		pem  []byte
	)
	if a.p.KeyData != "" {
		path = "手动输入的私钥"
		if pem = []byte(store.Decrypt(a.p.KeyData)); len(pem) == 0 {
			return nil, errors.New("无法解密保存的私钥，请重新输入")
		}
	} else {
		path = expandHome(a.p.KeyPath)
		if path == "" {
			return nil, errors.New("未指定私钥文件")
		}
		var err error
		if pem, err = os.ReadFile(path); err != nil {
			return nil, fmt.Errorf("读取私钥失败: %w", err)
		}
	}
	signer, err := ssh.ParsePrivateKey(pem)
	if err == nil {
		return []ssh.Signer{signer}, nil
	}
	if err := CheckPrivateKey(pem); err != nil {
		return nil, err
	}
	var missing *ssh.PassphraseMissingError
	if !errors.As(err, &missing) {
		return nil, fmt.Errorf("解析私钥失败: %w", err)
	}
	if pp := store.Decrypt(a.p.Passphrase); pp != "" {
		if signer, err := ssh.ParsePrivateKeyWithPassphrase(pem, []byte(pp)); err == nil {
			return []ssh.Signer{signer}, nil
		}
	}
	for i := 0; i < 3; i++ {
		pp, ok := a.d.prompter.Passphrase(path)
		if !ok {
			a.canceled = true
			return nil, ErrCanceled
		}
		if signer, err := ssh.ParsePrivateKeyWithPassphrase(pem, []byte(pp)); err == nil {
			return []ssh.Signer{signer}, nil
		}
	}
	return nil, errors.New("私钥口令不正确")
}

// CheckPrivateKey reports whether pem holds a private key that can be
// used, possibly after asking for its passphrase.
func CheckPrivateKey(pem []byte) error {
	_, err := ssh.ParseRawPrivateKey(pem)
	var missing *ssh.PassphraseMissingError
	switch {
	case err == nil, errors.As(err, &missing):
		return nil
	case strings.HasPrefix(strings.TrimSpace(string(pem)), "PuTTY-User-Key-File"):
		return errors.New("不支持 PuTTY .ppk 格式，请用 PuTTYgen 导出为 OpenSSH 格式")
	}
	return fmt.Errorf("解析私钥失败: %w", err)
}

func (a *authState) agentSigners() ([]ssh.Signer, error) {
	conn, err := dialAgent()
	if err != nil {
		return nil, err
	}
	if err := a.d.track(conn); err != nil {
		return nil, err
	}
	return agent.NewClient(conn).Signers()
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimLeft(p[1:], `/\`))
		}
	}
	return p
}
