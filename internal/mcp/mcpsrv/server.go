// Package mcpsrv serves wsm-mcp's tool registry as a standard MCP server
// over Streamable HTTP (spec 2025-06-18, stateless: every POST /mcp is one
// JSON-RPC message answered with one JSON response; no SSE stream, no
// session id). Same shape as hikaru-mcp's internal/mcpsrv.
package mcpsrv

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/mcp/basicauth"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/mcp/registry"
)

// protocols this server speaks, newest first.
var protocols = []string{"2025-06-18", "2025-03-26"}

// Server is the HTTP handler.
type Server struct {
	Reg     *registry.Registry
	Auth    *basicauth.Checker
	Version string
	Log     *slog.Logger
	// MaxBodyBytes caps a single request body; defaults to 1 MiB raised to
	// WSM_MCP_MAX_BODY_BYTES for tool calls that carry base64 media.
	MaxBodyBytes int64
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const defaultMaxBodyBytes = 1 << 20

// Handler mounts /mcp (POST only; GET answers 405 — no server-initiated
// stream) and /healthz (no auth, {"ok":true}).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", s.serve)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "version": s.Version})
	})
	return mux
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != "" { // browsers send Origin; BE-to-BE callers don't (DNS-rebinding guard, spec)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if !s.Auth.Check(r.Header.Get("Authorization")) {
		w.Header().Set("WWW-Authenticate", `Basic realm="wsm-mcp"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	max := s.MaxBodyBytes
	if max <= 0 {
		max = defaultMaxBodyBytes
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, max))
	if err != nil {
		reply(w, nil, nil, &rpcError{-32700, "body too large or unreadable"})
		return
	}
	var req request
	if err := json.Unmarshal(body, &req); err != nil || req.JSONRPC != "2.0" || req.Method == "" {
		reply(w, nil, nil, &rpcError{-32600, "invalid JSON-RPC request (batches are not supported)"})
		return
	}
	if len(req.ID) == 0 { // notification (e.g. notifications/initialized): accepted, no body
		w.WriteHeader(http.StatusAccepted)
		return
	}
	res, rerr := s.dispatch(r, req)
	reply(w, req.ID, res, rerr)
}

func (s *Server) dispatch(r *http.Request, req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		v := protocols[0]
		for _, known := range protocols {
			if p.ProtocolVersion == known {
				v = known
			}
		}
		return map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "wsm-mcp", "title": "WSM MCP", "version": s.Version},
			"instructions": "wsm-mcp wraps WSM's /api/v1 and waxum's client API as tools over a failover pool of " +
				"hosts. Session-scoped tools accept session_id OR phone (the same phone has a different session_id " +
				"per host); pass target to pin one host. Danger-gated tools refuse unless the operator has set " +
				"WSM_MCP_ALLOW_DANGEROUS=true. See mcp/TUTORIAL.md for worked examples.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		list := []map[string]any{}
		for _, t := range s.Reg.List() {
			list = append(list, map[string]any{"name": t.Name, "description": t.Desc, "inputSchema": t.InputSchema(),
				"annotations": t.Annotations()})
		}
		return map[string]any{"tools": list}, nil
	case "tools/call":
		return s.call(r, req.Params)
	default:
		return nil, &rpcError{-32601, "method not found: " + req.Method}
	}
}

func (s *Server) call(r *http.Request, raw json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || p.Name == "" {
		return nil, &rpcError{-32602, "params.name is required"}
	}
	if !s.Reg.Has(p.Name) {
		return nil, &rpcError{-32602, "unknown tool: " + p.Name}
	}
	start := time.Now()
	out, err := s.Reg.Call(r.Context(), p.Name, p.Arguments)
	ok := err == nil
	if s.Log != nil {
		s.Log.Info("tool", "door", "mcp-http", "tool", p.Name, "ms", time.Since(start).Milliseconds(), "ok", ok)
	}
	if err != nil { // tool errors are results the model can read (spec), not protocol errors
		return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": err.Error()}}}, nil
	}
	b, _ := json.Marshal(out)
	return map[string]any{"isError": false, "content": []map[string]any{{"type": "text", "text": string(b)}},
		"structuredContent": out}, nil
}

func reply(w http.ResponseWriter, id json.RawMessage, result any, e *rpcError) {
	w.Header().Set("Content-Type", "application/json")
	msg := map[string]any{"jsonrpc": "2.0", "id": id}
	if len(id) == 0 {
		msg["id"] = nil
	}
	if e != nil {
		msg["error"] = e
	} else {
		msg["result"] = result
	}
	json.NewEncoder(w).Encode(msg)
}
