package mcpsrv

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/mcp/basicauth"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/mcp/registry"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/mcp/upstream"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	pool, err := upstream.New(upstream.Config{HostsSpec: "x=http://127.0.0.1:1|http://127.0.0.1:2", WSMUser: "u", WSMPass: "p"})
	if err != nil {
		t.Fatal(err)
	}
	auth, err := basicauth.New("wsm-mcp", "a-password-of-sixteen-chars-plus")
	if err != nil {
		t.Fatal(err)
	}
	return &Server{Reg: registry.New(pool), Auth: auth, Version: "test"}
}

func post(t *testing.T, s *Server, authHeader string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(b))
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	return w
}

// TestNoAuthorizationHeaderIsRejected asserts a request with no
// Authorization header at all gets 401, not treated as anonymous.
func TestNoAuthorizationHeaderIsRejected(t *testing.T) {
	s := testServer(t)
	w := post(t, s, "", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

// TestWrongPasswordIsRejected asserts a syntactically valid but wrong
// credential also gets 401.
func TestWrongPasswordIsRejected(t *testing.T) {
	s := testServer(t)
	w := post(t, s, basicauth.Header("wsm-mcp", "a-totally-different-password-16"), map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func validAuth() string { return basicauth.Header("wsm-mcp", "a-password-of-sixteen-chars-plus") }

// TestOriginHeaderIsRejectedEvenWithValidAuth is the DNS-rebinding guard
// the MCP spec asks for: a browser-originated request (any Origin header
// at all) is refused regardless of credentials.
func TestOriginHeaderIsRejectedEvenWithValidAuth(t *testing.T) {
	s := testServer(t)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)))
	req.Header.Set("Authorization", validAuth())
	req.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a request carrying Origin, got %d", w.Code)
	}
}

// TestInitializeNegotiatesAProtocolVersion exercises the handshake a real
// MCP client performs first.
func TestInitializeNegotiatesAProtocolVersion(t *testing.T) {
	s := testServer(t)
	w := post(t, s, validAuth(), map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-06-18"}})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response did not decode: %v (%s)", err, w.Body.String())
	}
	if resp.Result.ProtocolVersion != "2025-06-18" {
		t.Fatalf("expected protocolVersion 2025-06-18, got %q", resp.Result.ProtocolVersion)
	}
}

// TestANotificationGetsA202WithNoBody covers notifications/initialized:
// a request with no "id" is a notification per JSON-RPC and gets a bare
// 202, never a JSON-RPC response envelope.
func TestANotificationGetsA202WithNoBody(t *testing.T) {
	s := testServer(t)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)))
	req.Header.Set("Authorization", validAuth())
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("expected an empty body for a notification response, got %q", w.Body.String())
	}
}

// TestToolsListReturnsEveryRegisteredTool pins the count against the live
// registry rather than a hardcoded number, so it tracks the registry
// automatically (and still fails if tools/list itself breaks).
func TestToolsListReturnsEveryRegisteredTool(t *testing.T) {
	s := testServer(t)
	w := post(t, s, validAuth(), map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	var resp struct {
		Result struct {
			Tools []map[string]any `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response did not decode: %v (%s)", err, w.Body.String())
	}
	want := len(s.Reg.List())
	if len(resp.Result.Tools) != want {
		t.Fatalf("expected %d tools, got %d", want, len(resp.Result.Tools))
	}
}

// TestCallingADangerToolWithTheGateOffReturnsAnIsErrorResult asserts a
// danger-gated tool call never reaches Run while WSM_MCP_ALLOW_DANGEROUS
// is unset, and that the refusal surfaces as a readable tool result
// (isError:true), not a transport-level failure — the spec's own
// recommendation for a tool the model can retry or explain to a user.
func TestCallingADangerToolWithTheGateOffReturnsAnIsErrorResult(t *testing.T) {
	s := testServer(t)
	w := post(t, s, validAuth(), map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "wsm_session_delete", "arguments": map[string]any{"session_id": "x"}}})
	var resp struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response did not decode: %v (%s)", err, w.Body.String())
	}
	if !resp.Result.IsError {
		t.Fatalf("expected isError:true for a danger-gated tool with the gate off, got %s", w.Body.String())
	}
}

// TestAnUnknownJSONRPCMethodReturnsMinus32601 pins the JSON-RPC error code
// a client would branch on.
func TestAnUnknownJSONRPCMethodReturnsMinus32601(t *testing.T) {
	s := testServer(t)
	w := post(t, s, validAuth(), map[string]any{"jsonrpc": "2.0", "id": 1, "method": "totally/unknown"})
	var resp struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response did not decode: %v (%s)", err, w.Body.String())
	}
	if resp.Error.Code != -32601 {
		t.Fatalf("expected error code -32601, got %d", resp.Error.Code)
	}
}
