package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// DefaultPort is the port the app listens on unless the user picks another.
const DefaultPort = 14559

// maxBody bounds one request; write_file content arrives in a single one.
const maxBody = 64 << 20

// Service runs the MCP server on http://127.0.0.1:<port>/mcp while the app
// is open, using the "Streamable HTTP" transport. It answers every request
// with plain JSON and keeps no session, which the protocol allows.
//
// Nothing outside this computer can reach the port, but any web page open
// in a browser can send requests to it. So the server only accepts JSON
// requests (a page cannot send one without the browser asking first, and
// the server does not allow it) and refuses requests that name another
// host or come from another site, which also defeats DNS rebinding.
type Service struct {
	srv *Server

	mu   sync.Mutex
	hs   *http.Server
	port int
	err  error
}

// NewService returns a stopped service.
func NewService(opt Options) *Service {
	return &Service{srv: newServer(opt)}
}

// Start listens on port, stopping the server first if it runs. Port 0
// picks a free port.
func (sv *Service) Start(port int) error {
	sv.Stop()
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	sv.mu.Lock()
	defer sv.mu.Unlock()
	if err != nil {
		if isAddrInUse(err) {
			err = fmt.Errorf("端口 %d 已被占用", port)
		}
		sv.err = err
		return err
	}
	sv.port = ln.Addr().(*net.TCPAddr).Port
	sv.hs = &http.Server{Handler: sv.handler(sv.port), ReadHeaderTimeout: 10 * time.Second}
	sv.err = nil
	go sv.hs.Serve(ln)
	return nil
}

// Stop closes the port and the SSH connections the tools opened.
func (sv *Service) Stop() {
	sv.mu.Lock()
	hs := sv.hs
	sv.hs, sv.err = nil, nil
	sv.mu.Unlock()
	if hs != nil {
		hs.Close()
		sv.srv.pool.closeAll()
	}
}

// State reports whether the service runs, its URL when it does, and why
// it last failed to start.
func (sv *Service) State() (running bool, url string, err error) {
	sv.mu.Lock()
	defer sv.mu.Unlock()
	if sv.hs == nil {
		return false, "", sv.err
	}
	return true, URL(sv.port), nil
}

// URL is the address clients connect to for a service on port.
func URL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d/mcp", port)
}

func (sv *Service) handler(port int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		if !localHost(r.Host, port) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && !localOrigin(o) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodPost {
			// No server-initiated stream and no sessions to end.
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
			http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
		if err != nil {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return
		}
		out := sv.srv.handle(r.Context(), body)
		if out == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		status := http.StatusOK
		if r, ok := out.(*response); ok && r.Error != nil && string(r.ID) == "null" {
			status = http.StatusBadRequest
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(out)
	})
}

func isAddrInUse(err error) bool {
	var errno syscall.Errno
	return errors.As(err, &errno) && (errno == syscall.EADDRINUSE || errno == 10048) // WSAEADDRINUSE
}

func isLoopbackName(h string) bool {
	switch h {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

// localHost reports whether a Host header names this service.
func localHost(hostport string, port int) bool {
	h, p, err := net.SplitHostPort(hostport)
	return err == nil && isLoopbackName(h) && p == strconv.Itoa(port)
}

// localOrigin reports whether a page on origin is served from this
// computer, such as a local MCP inspector.
func localOrigin(origin string) bool {
	u, err := url.Parse(origin)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && isLoopbackName(u.Hostname())
}
