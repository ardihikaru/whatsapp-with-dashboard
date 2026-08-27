package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/call"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

func postJSON(srv http.Handler, token, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// TestCallEndpointsAnswer501WhenCallsAreDisabled proves the
// WSM_CALLS_ENABLED=false default (Deps.CallsEnabled zero value) makes
// every one of the four /calls/* routes answer 501 WITHOUT placing
// anything — the backend's call hooks are never even called.
func TestCallEndpointsAnswer501WhenCallsAreDisabled(t *testing.T) {
	called := false
	backend := &fakeGatewayBackend{
		name: "fake",
		ttsFn: func(to wa.JID, text, voice string) (wa.CallOutcome, error) {
			called = true
			return wa.CallOutcome{}, nil
		},
		playFn: func(to wa.JID, audioURL string) (wa.CallOutcome, error) { called = true; return wa.CallOutcome{}, nil },
		termFn: func(peer wa.JID, callID, reason string) error { called = true; return nil },
	}
	srv, s, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend, callsEnabled: false})
	token := apiToken(t, srv, apiClients, "calls-disabled")

	cases := []struct {
		name, method, path, body string
	}{
		{"tts", http.MethodPost, "/api/v1/sessions/" + s.ID + "/calls/tts", `{"to":"628123456789","text":"hi"}`},
		{"play", http.MethodPost, "/api/v1/sessions/" + s.ID + "/calls/play", `{"to":"628123456789","audio_url":"https://example.invalid/a.mp3"}`},
		{"terminate", http.MethodPost, "/api/v1/sessions/" + s.ID + "/calls/terminate", `{"peer":"628123456789","call_id":"call-1"}`},
		{"list", http.MethodGet, "/api/v1/sessions/" + s.ID + "/calls", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postJSON(srv, token, tc.method, tc.path, tc.body)
			if rec.Code != http.StatusNotImplemented {
				t.Fatalf("%s: status = %d, want %d; body = %s", tc.name, rec.Code, http.StatusNotImplemented, rec.Body.String())
			}
		})
	}
	if called {
		t.Fatal("a call-placing hook was invoked while calls are disabled")
	}
}

// TestCallTTSRecordsARowInWaCalls proves a successful TTS call (calls
// enabled) writes a wa_calls row and marks it placed with waxum's call id.
func TestCallTTSRecordsARowInWaCalls(t *testing.T) {
	backend := &fakeGatewayBackend{
		name: "fake",
		ttsFn: func(to wa.JID, text, voice string) (wa.CallOutcome, error) {
			return wa.CallOutcome{CallID: "call-42", To: to.String()}, nil
		},
	}
	srv, s, apiClients, _, calls := newMediaTestServer(t, mediaTestConfig{backend: backend, callsEnabled: true})
	token := apiToken(t, srv, apiClients, "calls-tts-row")

	rec := postJSON(srv, token, http.MethodPost, "/api/v1/sessions/"+s.ID+"/calls/tts",
		`{"to":"628123456789","text":"your otp is 1234","voice":"id-ID-ArdiNeural"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}

	rows, err := calls.Recent(context.Background(), s.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("wa_calls has %d rows, want 1", len(rows))
	}
	row := rows[0]
	if row.Status != call.StatusPlaced {
		t.Errorf("Status = %q, want %q", row.Status, call.StatusPlaced)
	}
	if row.CallID != "call-42" {
		t.Errorf("CallID = %q, want %q", row.CallID, "call-42")
	}
	if row.Kind != call.KindTTS {
		t.Errorf("Kind = %q, want %q", row.Kind, call.KindTTS)
	}
}

// TestAFailedCallIsMarkedFailedWithItsError proves a backend error placing
// the call is recorded on the row (MarkFailed) and surfaced through
// writeWAError, not silently dropped.
func TestAFailedCallIsMarkedFailedWithItsError(t *testing.T) {
	backend := &fakeGatewayBackend{
		name: "fake",
		ttsFn: func(to wa.JID, text, voice string) (wa.CallOutcome, error) {
			return wa.CallOutcome{}, wa.ErrRejectedByBackend
		},
	}
	srv, s, apiClients, _, calls := newMediaTestServer(t, mediaTestConfig{backend: backend, callsEnabled: true})
	token := apiToken(t, srv, apiClients, "calls-failed-row")

	rec := postJSON(srv, token, http.MethodPost, "/api/v1/sessions/"+s.ID+"/calls/tts",
		`{"to":"628123456789","text":"hi"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusUnprocessableEntity, rec.Body.String())
	}

	rows, err := calls.Recent(context.Background(), s.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("wa_calls has %d rows, want 1", len(rows))
	}
	row := rows[0]
	if row.Status != call.StatusFailed {
		t.Errorf("Status = %q, want %q", row.Status, call.StatusFailed)
	}
	if row.Error == "" {
		t.Error("Error is empty, want the backend's rejection recorded")
	}
}

// TestCallPlayRecordsARowAndTerminateSucceeds is a light smoke test on the
// two call endpoints not otherwise exercised above.
func TestCallPlayRecordsARowAndTerminateSucceeds(t *testing.T) {
	backend := &fakeGatewayBackend{
		name: "fake",
		playFn: func(to wa.JID, audioURL string) (wa.CallOutcome, error) {
			return wa.CallOutcome{CallID: "call-99"}, nil
		},
		termFn: func(peer wa.JID, callID, reason string) error { return nil },
	}
	srv, s, apiClients, _, calls := newMediaTestServer(t, mediaTestConfig{backend: backend, callsEnabled: true})
	token := apiToken(t, srv, apiClients, "calls-play-terminate")

	rec := postJSON(srv, token, http.MethodPost, "/api/v1/sessions/"+s.ID+"/calls/play",
		`{"to":"628123456789","audio_url":"https://example.invalid/a.mp3"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("play status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	rows, err := calls.Recent(context.Background(), s.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Kind != call.KindPlay {
		t.Fatalf("wa_calls rows = %+v, want exactly one play row", rows)
	}

	termRec := postJSON(srv, token, http.MethodPost, "/api/v1/sessions/"+s.ID+"/calls/terminate",
		`{"peer":"628123456789","call_id":"call-99"}`)
	if termRec.Code != http.StatusNoContent {
		t.Fatalf("terminate status = %d, want 204; body = %s", termRec.Code, termRec.Body.String())
	}
}

// TestTerminatingACallMarksTheRowTerminated is the Phase 6b §0 gap-fix:
// before MarkTerminatedByCallID existed, a terminated call's wa_calls row
// stayed "placed" forever, because the terminate handler was only ever
// handed waxum's call_id and had no lookup keyed on it. Places a call
// (placed, with waxum's call-42 id), terminates it, then asserts the row
// itself — not just the 204 — actually moved to terminated with EndedAt
// set.
func TestTerminatingACallMarksTheRowTerminated(t *testing.T) {
	backend := &fakeGatewayBackend{
		name: "fake",
		ttsFn: func(to wa.JID, text, voice string) (wa.CallOutcome, error) {
			return wa.CallOutcome{CallID: "call-42"}, nil
		},
		termFn: func(peer wa.JID, callID, reason string) error { return nil },
	}
	srv, s, apiClients, _, calls := newMediaTestServer(t, mediaTestConfig{backend: backend, callsEnabled: true})
	token := apiToken(t, srv, apiClients, "calls-terminate-marks-row")

	placeRec := postJSON(srv, token, http.MethodPost, "/api/v1/sessions/"+s.ID+"/calls/tts",
		`{"to":"628123456789","text":"hi"}`)
	if placeRec.Code != http.StatusOK {
		t.Fatalf("place status = %d, want 200; body = %s", placeRec.Code, placeRec.Body.String())
	}

	before := time.Now().UTC()
	termRec := postJSON(srv, token, http.MethodPost, "/api/v1/sessions/"+s.ID+"/calls/terminate",
		`{"peer":"628123456789","call_id":"call-42"}`)
	if termRec.Code != http.StatusNoContent {
		t.Fatalf("terminate status = %d, want 204; body = %s", termRec.Code, termRec.Body.String())
	}

	rows, err := calls.Recent(context.Background(), s.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("wa_calls has %d rows, want 1", len(rows))
	}
	row := rows[0]
	if row.Status != call.StatusTerminated {
		t.Fatalf("Status = %q, want %q (the row must not stay %q forever)", row.Status, call.StatusTerminated, call.StatusPlaced)
	}
	if row.EndedAt == nil {
		t.Fatal("EndedAt is nil, want a timestamp")
	}
	if row.EndedAt.Before(before.Add(-2 * time.Second)) {
		t.Errorf("EndedAt = %v, want at/after %v", row.EndedAt, before)
	}
}

// TestGetAPISessionCallsReturnsSnakeCaseJSON pins apiCall's json tags —
// call.Call itself carries none, so this is the only thing standing between
// the API and Go's default field-name marshaling.
func TestGetAPISessionCallsReturnsSnakeCaseJSON(t *testing.T) {
	backend := &fakeGatewayBackend{
		name: "fake",
		ttsFn: func(to wa.JID, text, voice string) (wa.CallOutcome, error) {
			return wa.CallOutcome{CallID: "call-1"}, nil
		},
	}
	srv, s, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend, callsEnabled: true})
	token := apiToken(t, srv, apiClients, "calls-snake-case")
	postJSON(srv, token, http.MethodPost, "/api/v1/sessions/"+s.ID+"/calls/tts", `{"to":"628123456789","text":"hi"}`)

	rec := postJSON(srv, token, http.MethodGet, "/api/v1/sessions/"+s.ID+"/calls", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if _, ok := rows[0]["to_phone"]; !ok {
		t.Fatalf("row missing snake_case %q key: %v", "to_phone", rows[0])
	}
}
