package sshx

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/pkg/sftp"
)

// Entry is one remote directory entry.
type Entry struct {
	Name    string
	Size    int64
	Mode    os.FileMode
	ModTime time.Time
	IsDir   bool // true for directories and links to directories
	IsLink  bool
	Owner   string
	Group   string
}

// MaxEditSize is the largest file the built-in editor will open.
const MaxEditSize = 2 << 20

// SFTP returns the SFTP client for the session, opening it on first use.
func (s *Session) SFTP() (*sftp.Client, error) {
	s.sftpMu.Lock()
	defer s.sftpMu.Unlock()
	if s.sftpc != nil {
		return s.sftpc, nil
	}
	c := s.Client()
	if c == nil {
		return nil, errors.New("未连接")
	}
	sc, err := sftp.NewClient(c, sftp.UseConcurrentWrites(true), sftp.MaxConcurrentRequestsPerFile(32))
	if err != nil {
		return nil, fmt.Errorf("无法启动 SFTP: %w", err)
	}
	s.sftpc = sc
	s.names = &nameCache{}
	return sc, nil
}

func (s *Session) closeSFTP() {
	s.sftpMu.Lock()
	if s.sftpc != nil {
		s.sftpc.Close()
		s.sftpc = nil
	}
	s.sftpMu.Unlock()
}

// Home returns the directory an SFTP session starts in.
func (s *Session) Home() (string, error) {
	c, err := s.SFTP()
	if err != nil {
		return "", err
	}
	return c.Getwd()
}

// List reads a remote directory. It returns the canonical path of the
// directory and its entries, directories first.
func (s *Session) List(dir string) (string, []Entry, error) {
	c, err := s.SFTP()
	if err != nil {
		return dir, nil, err
	}
	if dir == "" {
		dir = "."
	}
	real, err := c.RealPath(dir)
	if err != nil {
		return dir, nil, fsErr(err)
	}
	infos, err := c.ReadDir(real)
	if err != nil {
		return real, nil, fsErr(err)
	}
	s.sftpMu.Lock()
	names := s.names
	s.sftpMu.Unlock()
	names.load(c)

	entries := make([]Entry, 0, len(infos))
	for _, fi := range infos {
		e := Entry{Name: fi.Name(), Size: fi.Size(), Mode: fi.Mode(), ModTime: fi.ModTime(), IsDir: fi.IsDir()}
		if st, ok := fi.Sys().(*sftp.FileStat); ok && st != nil {
			e.Owner, e.Group = names.user(st.UID), names.group(st.GID)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			e.IsLink = true
			if ti, err := c.Stat(path.Join(real, fi.Name())); err == nil {
				e.IsDir = ti.IsDir()
				e.Size = ti.Size()
			}
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return real, entries, nil
}

// Mkdir creates a directory.
func (s *Session) Mkdir(p string) error {
	c, err := s.SFTP()
	if err != nil {
		return err
	}
	return fsErr(c.Mkdir(p))
}

// Touch creates an empty file, failing if it already exists.
func (s *Session) Touch(p string) error {
	c, err := s.SFTP()
	if err != nil {
		return err
	}
	if _, err := c.Lstat(p); err == nil {
		return errors.New("文件已存在")
	}
	f, err := c.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return fsErr(err)
	}
	return f.Close()
}

// Rename moves a file or directory.
func (s *Session) Rename(from, to string) error {
	c, err := s.SFTP()
	if err != nil {
		return err
	}
	if _, err := c.Lstat(to); err == nil {
		return errors.New("目标已存在")
	}
	return fsErr(c.Rename(from, to))
}

// Remove deletes files and directories recursively.
func (s *Session) Remove(paths []string) error {
	c, err := s.SFTP()
	if err != nil {
		return err
	}
	for _, p := range paths {
		if p == "" || p == "/" {
			return errors.New("拒绝删除根目录")
		}
		fi, err := c.Lstat(p)
		if err != nil {
			return fsErr(err)
		}
		if fi.IsDir() {
			err = c.RemoveAll(p)
		} else {
			err = c.Remove(p)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", path.Base(p), fsErr(err))
		}
	}
	return nil
}

// Chmod changes permission bits.
func (s *Session) Chmod(p string, mode os.FileMode) error {
	c, err := s.SFTP()
	if err != nil {
		return err
	}
	return fsErr(c.Chmod(p, mode))
}

// ReadText reads a remote file for editing. It refuses files that are too
// large or look binary.
func (s *Session) ReadText(p string) (string, error) {
	c, err := s.SFTP()
	if err != nil {
		return "", err
	}
	fi, err := c.Stat(p)
	if err != nil {
		return "", fsErr(err)
	}
	if fi.IsDir() {
		return "", errors.New("这是一个目录")
	}
	if fi.Size() > MaxEditSize {
		return "", fmt.Errorf("文件过大（%d MB），请下载后编辑", fi.Size()>>20)
	}
	f, err := c.Open(p)
	if err != nil {
		return "", fsErr(err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxEditSize+1))
	if err != nil {
		return "", fsErr(err)
	}
	if bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b) {
		return "", errors.New("不是 UTF-8 文本文件，请下载后用本地程序打开")
	}
	return string(b), nil
}

// IsText reports whether b looks like editable UTF-8 text.
func IsText(b []byte) bool {
	return bytes.IndexByte(b, 0) < 0 && utf8.Valid(b)
}

// ReadFile reads a whole remote file, refusing files larger than limit.
func (s *Session) ReadFile(p string, limit int64) ([]byte, error) {
	c, err := s.SFTP()
	if err != nil {
		return nil, err
	}
	fi, err := c.Stat(p)
	if err != nil {
		return nil, fsErr(err)
	}
	if fi.IsDir() {
		return nil, errors.New("这是一个目录")
	}
	if fi.Size() > limit {
		return nil, fmt.Errorf("文件过大（%d MB）", fi.Size()>>20)
	}
	f, err := c.Open(p)
	if err != nil {
		return nil, fsErr(err)
	}
	defer f.Close()
	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		return nil, fsErr(err)
	}
	return buf.Bytes(), nil
}

// WriteText replaces the contents of a remote file, keeping its mode.
func (s *Session) WriteText(p, content string) error {
	c, err := s.SFTP()
	if err != nil {
		return err
	}
	f, err := c.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return fsErr(err)
	}
	if _, err := f.Write([]byte(content)); err != nil {
		f.Close()
		return fsErr(err)
	}
	return fsErr(f.Close())
}

func fsErr(err error) error {
	if err == nil {
		return nil
	}
	var se *sftp.StatusError
	switch {
	case errors.Is(err, os.ErrPermission):
		return errors.New("权限不足")
	case errors.Is(err, os.ErrNotExist):
		return errors.New("文件或目录不存在")
	case errors.As(err, &se) && se.Code == 4: // SSH_FX_FAILURE carries no detail
		return errors.New("操作失败（目录非空、已存在或不被允许）")
	}
	return err
}

// nameCache maps numeric ids to names using the remote passwd and group
// files, read once per SFTP session.
type nameCache struct {
	once   sync.Once
	users  map[uint32]string
	groups map[uint32]string
}

func (n *nameCache) load(c *sftp.Client) {
	if n == nil {
		return
	}
	n.once.Do(func() {
		n.users = readIDs(c, "/etc/passwd")
		n.groups = readIDs(c, "/etc/group")
	})
}

func readIDs(c *sftp.Client, p string) map[uint32]string {
	m := map[uint32]string{}
	f, err := c.Open(p)
	if err != nil {
		return m
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return m
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(line, ":")
		if len(f) < 3 {
			continue
		}
		if id, err := strconv.ParseUint(f[2], 10, 32); err == nil {
			if _, dup := m[uint32(id)]; !dup {
				m[uint32(id)] = f[0]
			}
		}
	}
	return m
}

func (n *nameCache) user(id uint32) string {
	if n != nil {
		if s, ok := n.users[id]; ok {
			return s
		}
	}
	return strconv.FormatUint(uint64(id), 10)
}

func (n *nameCache) group(id uint32) string {
	if n != nil {
		if s, ok := n.groups[id]; ok {
			return s
		}
	}
	return strconv.FormatUint(uint64(id), 10)
}

// lookup finds the id of a user or group name; a number is taken as is.
func lookup(m map[uint32]string, name string) (uint32, bool) {
	if n, err := strconv.ParseUint(name, 10, 32); err == nil {
		return uint32(n), true
	}
	for id, s := range m {
		if s == name {
			return id, true
		}
	}
	return 0, false
}

// Owners returns the user names of the remote host, sorted, so a UI can
// offer them as file owners.
func (s *Session) Owners() ([]string, error) {
	c, err := s.SFTP()
	if err != nil {
		return nil, err
	}
	s.sftpMu.Lock()
	names := s.names
	s.sftpMu.Unlock()
	names.load(c)
	out := make([]string, 0, len(names.users))
	for _, n := range names.users {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// SetAttrs sets the permission bits of p and, when owner is not empty, its
// owner. owner is "user" or "user:group", by name or number; a missing
// group keeps the current one. mode holds the permission bits and, if
// special is set, the setuid/setgid/sticky bits too; otherwise those bits
// are kept as they are. With recursive set, everything under a directory
// changes the same way; symbolic links are left alone.
func (s *Session) SetAttrs(p string, mode os.FileMode, special bool, owner string, recursive bool) error {
	c, err := s.SFTP()
	if err != nil {
		return err
	}
	s.sftpMu.Lock()
	names := s.names
	s.sftpMu.Unlock()
	names.load(c)

	var uid, gid uint32
	setUID, setGID := false, false
	if owner = strings.TrimSpace(owner); owner != "" {
		user, group, hasGroup := strings.Cut(owner, ":")
		if user != "" {
			if uid, setUID = lookup(names.users, user); !setUID {
				return fmt.Errorf("没有用户 %s", user)
			}
		}
		if hasGroup && group != "" {
			if gid, setGID = lookup(names.groups, group); !setGID {
				return fmt.Errorf("没有用户组 %s", group)
			}
		}
	}

	const specialBits = os.ModeSetuid | os.ModeSetgid | os.ModeSticky
	apply := func(path string, fi os.FileInfo) error {
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		m := mode & (os.ModePerm | specialBits)
		if !special {
			m = mode&os.ModePerm | fi.Mode()&specialBits
		}
		if err := c.Chmod(path, m); err != nil {
			return fmt.Errorf("%s: %w", path, fsErr(err))
		}
		if setUID || setGID {
			st, ok := fi.Sys().(*sftp.FileStat)
			if !ok {
				return fmt.Errorf("%s: 无法读取所有者", path)
			}
			u, g := st.UID, st.GID
			if setUID {
				u = uid
			}
			if setGID {
				g = gid
			}
			if err := c.Chown(path, int(u), int(g)); err != nil {
				return fmt.Errorf("%s: %w", path, fsErr(err))
			}
		}
		return nil
	}
	fi, err := c.Lstat(p)
	if err != nil {
		return fsErr(err)
	}
	if !recursive || !fi.IsDir() {
		return apply(p, fi)
	}
	w := c.Walk(p)
	for w.Step() {
		if err := w.Err(); err != nil {
			return fsErr(err)
		}
		if err := apply(w.Path(), w.Stat()); err != nil {
			return err
		}
	}
	return nil
}
