package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHealthReportsBackendReachabilityWithoutCallingWaxum is Phase 8 §3's
// required test: GET /api/v1/health is unauthenticated and IS the container
// healthcheck (docker-compose.yml), probed on every liveness interval — it
// must read Manager.CachedBackendHealth's cached verdict and must NEVER
// call GatewayHealth.Health (a live waxum request), or every liveness probe
// would put a network hop on waxum and feed its own rate limiter (CLAUDE.md's
// waxum rate-limit hazard note). healthFn below fails the test outright if
// GetHealth ever reaches it — the only way GetHealth can pass is by calling
// Reachable() instead, which does no I/O by construction.
func TestHealthReportsBackendReachabilityWithoutCallingWaxum(t *testing.T) {
	backend := &fakeOpsBackend{
		fakeGatewayBackend: &fakeGatewayBackend{name: "waxum"},
		healthFn: func() error {
			t.Fatal("GET /api/v1/health called GatewayHealth.Health — it must read the cached " +
				"Reachable() verdict instead, never a live call")
			return nil
		},
		reachableFn: func() (bool, bool) { return true, true },
	}
	srv, _, _, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend})

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Backend struct {
			Name      string `json:"name"`
			Reachable *bool  `json:"reachable"`
		} `json:"backend"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if out.Backend.Name != "waxum" {
		t.Errorf("backend.name = %q, want %q", out.Backend.Name, "waxum")
	}
	if out.Backend.Reachable == nil || !*out.Backend.Reachable {
		t.Errorf("backend.reachable = %v, want true", out.Backend.Reachable)
	}
}

// TestHealthOmitsReachableWhenNoVerdictHasBeenRecordedYet proves the
// "unknown" case reads as an OMITTED field, not a guessed false — see
// wa.GatewayHealth.Reachable's doc comment on why ok=false must not be
// collapsed into "unreachable".
func TestHealthOmitsReachableWhenNoVerdictHasBeenRecordedYet(t *testing.T) {
	backend := &fakeOpsBackend{
		fakeGatewayBackend: &fakeGatewayBackend{name: "waxum"},
		reachableFn:        func() (bool, bool) { return false, false },
	}
	srv, _, _, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend})

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	var backendOut map[string]json.RawMessage
	if err := json.Unmarshal(out["backend"], &backendOut); err != nil {
		t.Fatalf("decode backend %s: %v", out["backend"], err)
	}
	if _, present := backendOut["reachable"]; present {
		t.Errorf(`"reachable" present in %s, want it omitted when no verdict has been recorded yet`, out["backend"])
	}
}

// TestHealthOmitsBackendFieldOnABackendWithNoGatewayHealth proves a
// whatsmeow-shaped backend (no GatewayHealth at all) gets no "backend"
// field rather than a zero-valued/misleading one.
func TestHealthOmitsBackendFieldOnABackendWithNoGatewayHealth(t *testing.T) {
	backend := &fakeGatewayBackend{name: "whatsmeow"}
	srv, _, _, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend})

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if _, present := out["backend"]; present {
		t.Errorf(`"backend" present in %s, want it omitted for a backend with no GatewayHealth`, rec.Body.String())
	}
}
