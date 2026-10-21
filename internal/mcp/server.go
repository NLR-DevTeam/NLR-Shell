// Package mcp serves the connections saved in NLR Shell to AI assistants
// over the Model Context Protocol. The app runs it on a local URL (see
// Service); the tools run commands, move files and read system status on
// saved hosts, and passwords and keys never leave the store.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"sync"

	"nlrshell/internal/store"
)

// Options configures the server.
type Options struct {
	// Version is reported to clients as the server version.
	Version string
	// Store holds the saved connections and the MCP settings. It is read
	// on every request, so changes made in the app apply right away.
	Store *store.Store
}

// protocolVersions are the MCP revisions the server speaks, newest first.
var protocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

const instructions = `NLR Shell gives access to the SSH servers the user saved in the NLR Shell app.
Call list_servers first, then pass a server's id (or its unique name) as "server" to the other tools.
Connections are kept open between calls. Passwords and keys stay inside NLR Shell; a server whose credentials are not saved there cannot be used.
The user decides in NLR Shell (logo menu → MCP 服务) which servers are open and whether access is read-only.
run_command runs a non-interactive command (no TTY); avoid commands that wait for input.`

// JSON-RPC error codes.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Server answers MCP messages. It keeps no per-client state apart from the
// tool calls in flight, so every request stands on its own.
type Server struct {
	opt  Options
	pool *pool

	mu       sync.Mutex
	inflight map[string]context.CancelFunc
}

func newServer(opt Options) *Server {
	return &Server{opt: opt, pool: newPool(), inflight: map[string]context.CancelFunc{}}
}

// handle answers the body of one POST: a message or, from clients older
// than 2025-06-18, a batch. It returns nil when there is nothing to answer,
// that is for notifications and responses.
func (s *Server) handle(ctx context.Context, body []byte) any {
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '[' {
		var batch []json.RawMessage
		if err := json.Unmarshal(body, &batch); err != nil {
			return errorResponse(nil, codeParse, "parse error")
		}
		var out []*response
		for _, raw := range batch {
			if r := s.process(ctx, raw); r != nil {
				out = append(out, r)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	if r := s.process(ctx, body); r != nil {
		return r
	}
	return nil
}

func (s *Server) process(ctx context.Context, raw []byte) *response {
	var m message
	if err := json.Unmarshal(raw, &m); err != nil {
		return errorResponse(nil, codeParse, "parse error")
	}
	if m.Method == "" {
		if len(m.ID) == 0 {
			return errorResponse(nil, codeInvalidRequest, "invalid request")
		}
		// A response to a request of ours; the server sends none.
		return nil
	}
	if len(m.ID) == 0 {
		s.notification(m)
		return nil
	}
	if m.Method == "tools/call" {
		// Kept so that notifications/cancelled can stop the call.
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		key := string(m.ID)
		s.mu.Lock()
		s.inflight[key] = cancel
		s.mu.Unlock()
		defer func() {
			s.mu.Lock()
			delete(s.inflight, key)
			s.mu.Unlock()
		}()
	}
	result, err := s.dispatch(ctx, m)
	if err != nil {
		return &response{JSONRPC: "2.0", ID: m.ID, Error: err}
	}
	return &response{JSONRPC: "2.0", ID: m.ID, Result: result}
}

func (s *Server) dispatch(ctx context.Context, m message) (any, *rpcError) {
	switch m.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(m.Params, &p)
		v := protocolVersions[0]
		if slices.Contains(protocolVersions, p.ProtocolVersion) {
			v = p.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "nlrshell", "title": "NLR Shell", "version": s.opt.Version},
			"instructions":    instructions,
		}, nil
	case "ping":
		return struct{}{}, nil
	case "tools/list":
		return map[string]any{"tools": toolsFor(s.config())}, nil
	case "tools/call":
		return s.callTool(ctx, m.Params)
	}
	return nil, &rpcError{Code: codeMethodNotFound, Message: "method not found: " + m.Method}
}

// config returns the MCP settings as saved now.
func (s *Server) config() store.MCPSettings {
	return s.opt.Store.Settings().MCP
}

func (s *Server) notification(m message) {
	if m.Method != "notifications/cancelled" {
		return
	}
	var p struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(m.Params, &p) != nil {
		return
	}
	s.mu.Lock()
	cancel := s.inflight[string(p.RequestID)]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func errorResponse(id json.RawMessage, code int, msg string) *response {
	if id == nil {
		id = json.RawMessage("null")
	}
	return &response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}
