// Package store persists connection profiles and settings as JSON under the
// user's config directory. Secrets are encrypted with Windows DPAPI so they
// can only be read by the same Windows user.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Auth methods.
const (
	AuthPassword = "password"
	AuthKey      = "key"
	AuthAgent    = "agent"
)

// Forward kinds.
const (
	ForwardLocal   = "local"
	ForwardRemote  = "remote"
	ForwardDynamic = "dynamic"
)

// Forward describes one port forwarding rule.
type Forward struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	ListenHost string `json:"listenHost"`
	ListenPort int    `json:"listenPort"`
	TargetHost string `json:"targetHost,omitempty"`
	TargetPort int    `json:"targetPort,omitempty"`
	AutoStart  bool   `json:"autoStart"`
}

// Profile is a saved connection.
type Profile struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Group string `json:"group,omitempty"`
	Host  string `json:"host"`
	Port  int    `json:"port"`
	User  string `json:"user"`
	Auth  string `json:"auth"`
	// Password and Passphrase are DPAPI-encrypted, base64 encoded.
	Password   string    `json:"password,omitempty"`
	KeyPath    string    `json:"keyPath,omitempty"`
	Passphrase string    `json:"passphrase,omitempty"`
	JumpID     string    `json:"jumpId,omitempty"`
	Forwards   []Forward `json:"forwards,omitempty"`
	LastUsed   int64     `json:"lastUsed,omitempty"`
}

// Title returns the display name of the profile.
func (p *Profile) Title() string {
	if p.Name != "" {
		return p.Name
	}
	return p.Host
}

// Addr returns user@host[:port].
func (p *Profile) Addr() string {
	s := p.Host
	if p.User != "" {
		s = p.User + "@" + s
	}
	if p.Port != 0 && p.Port != 22 {
		s += ":" + itoa(p.Port)
	}
	return s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// Snippet is a saved command.
type Snippet struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Command string `json:"command"`
}

// Settings are user preferences and remembered layout.
type Settings struct {
	FontFamily      string  `json:"fontFamily"`
	FontSize        float32 `json:"fontSize"`
	Scrollback      int     `json:"scrollback"`
	SidebarWidth    int     `json:"sidebarWidth"`
	FilesHeight     int     `json:"filesHeight"`
	ShowSidebar     bool    `json:"showSidebar"`
	ShowFiles       bool    `json:"showFiles"`
	ShowHidden      bool    `json:"showHidden"`
	FollowCwd       bool    `json:"followCwd"`
	CopyOnSelect    bool    `json:"copyOnSelect"`
	RightClickPaste bool    `json:"rightClickPaste"`
	DownloadDir     string  `json:"downloadDir"`
	MonitorInterval int     `json:"monitorInterval"`
	WindowWidth     int     `json:"windowWidth"`
	WindowHeight    int     `json:"windowHeight"`
	Maximized       bool    `json:"maximized"`
}

// DefaultSettings returns the settings used on first run.
func DefaultSettings() Settings {
	return Settings{
		FontFamily:      "Cascadia Mono",
		FontSize:        14,
		Scrollback:      10000,
		SidebarWidth:    264,
		FilesHeight:     260,
		ShowSidebar:     true,
		ShowFiles:       true,
		FollowCwd:       true,
		MonitorInterval: 2,
		WindowWidth:     1280,
		WindowHeight:    800,
	}
}

type fileData struct {
	Profiles []Profile `json:"profiles"`
	Snippets []Snippet `json:"snippets"`
	History  []string  `json:"history,omitempty"`
	Settings Settings  `json:"settings"`
}

// Store holds the persisted state.
type Store struct {
	mu   sync.Mutex
	dir  string
	data fileData
}

// Dir returns the application data directory, creating it if needed. It is
// %APPDATA%\NLR Shell unless the NLRSHELL_DATA environment variable points
// elsewhere (useful for a portable install).
func Dir() string {
	dir := os.Getenv("NLRSHELL_DATA")
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			base = "."
		}
		dir = filepath.Join(base, "NLR Shell")
	}
	os.MkdirAll(dir, 0o700)
	return dir
}

// Open loads the store from dir (see Dir).
func Open(dir string) *Store {
	s := &Store{dir: dir}
	s.data.Settings = DefaultSettings()
	if b, err := os.ReadFile(s.path()); err == nil {
		json.Unmarshal(b, &s.data)
	}
	st := &s.data.Settings
	def := DefaultSettings()
	if st.FontSize < 6 || st.FontSize > 72 {
		st.FontSize = def.FontSize
	}
	if st.FontFamily == "" {
		st.FontFamily = def.FontFamily
	}
	if st.Scrollback <= 0 {
		st.Scrollback = def.Scrollback
	}
	if st.MonitorInterval <= 0 {
		st.MonitorInterval = def.MonitorInterval
	}
	if st.SidebarWidth < 120 {
		st.SidebarWidth = def.SidebarWidth
	}
	if st.FilesHeight < 80 {
		st.FilesHeight = def.FilesHeight
	}
	if st.DownloadDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			st.DownloadDir = filepath.Join(home, "Downloads")
		}
	}
	return s
}

func (s *Store) path() string { return filepath.Join(s.dir, "config.json") }

// KnownHostsPath returns the path of the known_hosts file.
func (s *Store) KnownHostsPath() string { return filepath.Join(s.dir, "known_hosts") }

func (s *Store) saveLocked() error {
	b, err := json.MarshalIndent(&s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path())
}

// NewID returns a random identifier.
func NewID() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Profiles returns a copy of all profiles, most recently used first.
func (s *Store) Profiles() []Profile {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]Profile(nil), s.data.Profiles...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].LastUsed != out[j].LastUsed {
			return out[i].LastUsed > out[j].LastUsed
		}
		return strings.ToLower(out[i].Title()) < strings.ToLower(out[j].Title())
	})
	return out
}

// Profile returns the profile with the given id.
func (s *Store) Profile(id string) (Profile, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.data.Profiles {
		if p.ID == id {
			return p, true
		}
	}
	return Profile{}, false
}

// SaveProfile inserts or updates a profile and returns it with its id set.
func (s *Store) SaveProfile(p Profile) Profile {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.Port == 0 {
		p.Port = 22
	}
	if p.Auth == "" {
		p.Auth = AuthPassword
	}
	if p.ID == "" {
		p.ID = NewID()
		s.data.Profiles = append(s.data.Profiles, p)
	} else {
		found := false
		for i := range s.data.Profiles {
			if s.data.Profiles[i].ID == p.ID {
				s.data.Profiles[i] = p
				found = true
			}
		}
		if !found {
			s.data.Profiles = append(s.data.Profiles, p)
		}
	}
	s.saveLocked()
	return p
}

// DeleteProfile removes a profile.
func (s *Store) DeleteProfile(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.data.Profiles[:0]
	for _, p := range s.data.Profiles {
		if p.ID != id {
			if p.JumpID == id {
				p.JumpID = ""
			}
			out = append(out, p)
		}
	}
	s.data.Profiles = out
	s.saveLocked()
}

// Touch records that a profile was just used.
func (s *Store) Touch(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Profiles {
		if s.data.Profiles[i].ID == id {
			s.data.Profiles[i].LastUsed = time.Now().Unix()
		}
	}
	s.saveLocked()
}

// SetPassword stores an encrypted password for a profile.
func (s *Store) SetPassword(id, plain string) {
	enc := Encrypt(plain)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Profiles {
		if s.data.Profiles[i].ID == id {
			s.data.Profiles[i].Password = enc
		}
	}
	s.saveLocked()
}

// Groups returns the distinct group names in use.
func (s *Store) Groups() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, p := range s.data.Profiles {
		if p.Group != "" && !seen[p.Group] {
			seen[p.Group] = true
			out = append(out, p.Group)
		}
	}
	sort.Strings(out)
	return out
}

// Settings returns the current settings.
func (s *Store) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Settings
}

// UpdateSettings applies f to the settings and saves them.
func (s *Store) UpdateSettings(f func(*Settings)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(&s.data.Settings)
	s.saveLocked()
}

// Snippets returns the saved commands.
func (s *Store) Snippets() []Snippet {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Snippet(nil), s.data.Snippets...)
}

// SaveSnippet inserts or updates a snippet.
func (s *Store) SaveSnippet(sn Snippet) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sn.ID == "" {
		sn.ID = NewID()
		s.data.Snippets = append(s.data.Snippets, sn)
	} else {
		for i := range s.data.Snippets {
			if s.data.Snippets[i].ID == sn.ID {
				s.data.Snippets[i] = sn
			}
		}
	}
	s.saveLocked()
}

// DeleteSnippet removes a snippet.
func (s *Store) DeleteSnippet(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.data.Snippets[:0]
	for _, sn := range s.data.Snippets {
		if sn.ID != id {
			out = append(out, sn)
		}
	}
	s.data.Snippets = out
	s.saveLocked()
}

// History returns the command bar history, oldest first.
func (s *Store) History() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.data.History...)
}

// AddHistory appends a command to the command bar history.
func (s *Store) AddHistory(cmd string) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.data.History[:0]
	for _, h := range s.data.History {
		if h != cmd {
			out = append(out, h)
		}
	}
	out = append(out, cmd)
	if len(out) > 200 {
		out = out[len(out)-200:]
	}
	s.data.History = out
	s.saveLocked()
}
