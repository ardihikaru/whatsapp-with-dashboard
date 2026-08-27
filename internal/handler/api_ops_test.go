package handler

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// opsRouteCases is every Phase 6b §1 SessionOps route, shared by the 501
// test and the auth-table test — one place to keep them in sync with
// routes_api.go.
func opsRouteCases(sessionID string) []struct{ name, method, path, body string } {
	return []struct{ name, method, path, body string }{
		{"operations", http.MethodGet, "/api/v1/sessions/" + sessionID + "/operations", ""},
		{"pause", http.MethodPost, "/api/v1/sessions/" + sessionID + "/pause", ""},
		{"resume", http.MethodPost, "/api/v1/sessions/" + sessionID + "/resume", ""},
		{"get auto-reconnect", http.MethodGet, "/api/v1/sessions/" + sessionID + "/auto-reconnect", ""},
		{"put auto-reconnect", http.MethodPut, "/api/v1/sessions/" + sessionID + "/auto-reconnect", `{"enabled":true}`},
		{"get history-sync", http.MethodGet, "/api/v1/sessions/" + sessionID + "/history-sync", ""},
		{"put history-sync", http.MethodPut, "/api/v1/sessions/" + sessionID + "/history-sync", `{"skip":true}`},
		{"appstate resync", http.MethodPost, "/api/v1/sessions/" + sessionID + "/appstate/resync", `{"collections":["critical_block"],"mode":"full"}`},
		{"spam report", http.MethodPost, "/api/v1/sessions/" + sessionID + "/spam-report", `{"message_id":"m1","message_timestamp":1234567890}`},
		{"issue trust tokens", http.MethodPost, "/api/v1/sessions/" + sessionID + "/trust-tokens", `{"jids":["628123456789@s.whatsapp.net"]}`},
		{"list trust tokens", http.MethodGet, "/api/v1/sessions/" + sessionID + "/trust-tokens", ""},
		{"get trust token", http.MethodGet, "/api/v1/sessions/" + sessionID + "/trust-tokens/628123456789@s.whatsapp.net", ""},
		{"prune trust tokens", http.MethodDelete, "/api/v1/sessions/" + sessionID + "/trust-tokens/expired", ""},
	}
}

// webhookRouteCases is every Phase 6b §2 WebhookAdmin route (session-scoped
// only — the fleet-level reenable-all is exercised separately since it
// needs no session id).
func webhookRouteCases(sessionID string) []struct{ name, method, path, body string } {
	return []struct{ name, method, path, body string }{
		{"list webhooks", http.MethodGet, "/api/v1/sessions/" + sessionID + "/webhooks", ""},
		{"register webhook", http.MethodPost, "/api/v1/sessions/" + sessionID + "/webhooks", `{"url":"https://example.com/hook","events":["message"],"secret":"shh"}`},
		{"delete webhook", http.MethodDelete, "/api/v1/sessions/" + sessionID + "/webhooks/wh-1", ""},
		{"enable webhook", http.MethodPost, "/api/v1/sessions/" + sessionID + "/webhooks/wh-1/enable", ""},
		{"webhook dlq", http.MethodGet, "/api/v1/sessions/" + sessionID + "/webhooks/dlq", ""},
		{"webhook dlq replay", http.MethodPost, "/api/v1/sessions/" + sessionID + "/webhooks/dlq/entry-1/replay", ""},
	}
}

// TestOperationEndpointsAnswer501OnTheWhatsmeowBackend proves every
// SessionOps/WebhookAdmin/gateway-health route answers 501 on a backend
// that does not implement the corresponding capability interface —
// fakeGatewayBackend (media_calls_test_backend_test.go), which is shaped
// like whatsmeow: no SessionOps, no WebhookAdmin, no GatewayHealth at all.
func TestOperationEndpointsAnswer501OnTheWhatsmeowBackend(t *testing.T) {
	backend := &fakeGatewayBackend{name: "fake"}
	srv, s, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend})
	token := apiToken(t, srv, apiClients, "ops-501")

	cases := opsRouteCases(s.ID)
	cases = append(cases, webhookRouteCases(s.ID)...)
	cases = append(cases,
		struct{ name, method, path, body string }{"reenable all webhooks", http.MethodPost, "/api/v1/webhooks/reenable-all", ""},
		struct{ name, method, path, body string }{"gateway health", http.MethodGet, "/api/v1/gateway/health", ""},
	)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postJSON(srv, token, tc.method, tc.path, tc.body)
			if rec.Code != http.StatusNotImplemented {
				t.Fatalf("%s %s: status = %d, want %d; body = %s", tc.method, tc.path, rec.Code, http.StatusNotImplemented, rec.Body.String())
			}
		})
	}
}

// TestPauseAndResumeDoNotRequireALiveSocket proves pause/resume answer 200
// on a session that was created but never activated (no live socket) —
// the one exception documented in api_ops.go's PostAPISessionPause: waxum
// resolves these two WITHOUT get_installed_client requiring a connection,
// unlike every other SessionOps endpoint.
func TestPauseAndResumeDoNotRequireALiveSocket(t *testing.T) {
	backend := &fakeOpsBackend{fakeGatewayBackend: &fakeGatewayBackend{name: "fake"}}
	srv, s, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend})
	token := apiToken(t, srv, apiClients, "ops-pause-resume")

	if s.Snapshot().Connected {
		t.Fatal("test setup: session must not be connected for this to prove anything")
	}

	pauseRec := postJSON(srv, token, http.MethodPost, "/api/v1/sessions/"+s.ID+"/pause", "")
	if pauseRec.Code != http.StatusOK {
		t.Fatalf("pause on a disconnected session: status = %d, want 200; body = %s", pauseRec.Code, pauseRec.Body.String())
	}
	resumeRec := postJSON(srv, token, http.MethodPost, "/api/v1/sessions/"+s.ID+"/resume", "")
	if resumeRec.Code != http.StatusOK {
		t.Fatalf("resume on a disconnected session: status = %d, want 200; body = %s", resumeRec.Code, resumeRec.Body.String())
	}
}

// TestOperationsSnapshotReportsPausedAutoReconnectAndHistorySync proves GET
// .../operations aggregates all three states into one response, matching
// what each individual endpoint would answer on its own.
func TestOperationsSnapshotReportsPausedAutoReconnectAndHistorySync(t *testing.T) {
	backend := &fakeOpsBackend{
		fakeGatewayBackend: &fakeGatewayBackend{name: "fake"},
		isPausedFn:         func() (bool, error) { return true, nil },
		autoReconnectFn:    func() (wa.AutoReconnectState, error) { return wa.AutoReconnectState{Enabled: true, ErrorCount: 2}, nil },
		historySyncFn:      func() (bool, error) { return true, nil },
	}
	srv, s, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend})
	token := apiToken(t, srv, apiClients, "ops-snapshot")

	rec := postJSON(srv, token, http.MethodGet, "/api/v1/sessions/"+s.ID+"/operations", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Paused        bool `json:"paused"`
		AutoReconnect struct {
			Enabled    bool   `json:"enabled"`
			ErrorCount uint32 `json:"error_count"`
		} `json:"auto_reconnect"`
		HistorySync struct {
			Skip bool `json:"skip"`
		} `json:"history_sync"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Paused {
		t.Error("Paused = false, want true")
	}
	if !got.AutoReconnect.Enabled || got.AutoReconnect.ErrorCount != 2 {
		t.Errorf("AutoReconnect = %+v, want Enabled=true ErrorCount=2", got.AutoReconnect)
	}
	if !got.HistorySync.Skip {
		t.Error("HistorySync.Skip = false, want true")
	}
}
