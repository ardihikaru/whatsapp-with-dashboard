package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/auth"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/call"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// postFormAsAdmin is login_test.go's postLogin pattern (url.Values +
// x-www-form-urlencoded), generalised to any authenticated POST — every
// browser form in this phase (calls, ops, webhooks) posts this way.
func postFormAsAdmin(t *testing.T, srv http.Handler, sessions *auth.Store, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	sess, err := sessions.Create("admin")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: sess.Token})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// TestCallsPagePlacesATTSCallAndRecordsItInWaCalls is calls_ui.go's
// behavioural proof: POST /calls/tts must actually call
// wa.ManagedSession.PlaceCallTTS (via wa.CallBackend/CallTTS) AND insert a
// wa_calls row, not just re-render an empty table.
func TestCallsPagePlacesATTSCallAndRecordsItInWaCalls(t *testing.T) {
	var gotText, gotVoice string
	backend := &fakeGatewayBackend{
		name: "waxum-shaped",
		ttsFn: func(to wa.JID, text, voice string) (wa.CallOutcome, error) {
			gotText, gotVoice = text, voice
			return wa.CallOutcome{CallID: "call-123"}, nil
		},
	}
	srv, sessions, s, calls := newSessionAdminTestServer(t, backend, true)

	rec := postFormAsAdmin(t, srv, sessions, "/calls/tts", url.Values{
		"routing": {"explicit"}, "session_id": {s.ID},
		"to": {"628123456789"}, "text": {"hello there"}, "voice": {"alto"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /calls/tts = %d: %s", rec.Code, rec.Body.String())
	}
	if gotText != "hello there" || gotVoice != "alto" {
		t.Fatalf("PlaceCallTTS got text=%q voice=%q, want %q/%q", gotText, gotVoice, "hello there", "alto")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "628123456789") || !strings.Contains(body, "tts") {
		t.Errorf("call_rows does not show the placed call; got:\n%s", body)
	}

	rows, err := calls.Recent(context.Background(), s.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != call.StatusPlaced || rows[0].CallID != "call-123" {
		t.Fatalf("wa_calls row = %+v, want one placed row with call_id=call-123", rows)
	}
}

// TestCallsPageTerminateEndsTheCallAndMarksTheRow proves the row's
// Terminate button (call_rows.html's hx-vals) round-trips call_id/peer
// correctly and that a successful terminate flips the row's status.
func TestCallsPageTerminateEndsTheCallAndMarksTheRow(t *testing.T) {
	var gotPeer, gotCallID string
	backend := &fakeGatewayBackend{
		name: "waxum-shaped",
		ttsFn: func(to wa.JID, text, voice string) (wa.CallOutcome, error) {
			return wa.CallOutcome{CallID: "call-999"}, nil
		},
		termFn: func(peer wa.JID, callID, reason string) error {
			gotPeer, gotCallID = peer.User, callID
			return nil
		},
	}
	srv, sessions, s, calls := newSessionAdminTestServer(t, backend, true)

	place := postFormAsAdmin(t, srv, sessions, "/calls/tts", url.Values{
		"routing": {"explicit"}, "session_id": {s.ID}, "to": {"628123456789"}, "text": {"hi"},
	})
	if place.Code != http.StatusOK {
		t.Fatalf("POST /calls/tts = %d: %s", place.Code, place.Body.String())
	}

	term := postFormAsAdmin(t, srv, sessions, "/calls/"+s.ID+"/terminate", url.Values{
		"call_id": {"call-999"}, "peer": {"628123456789"},
	})
	if term.Code != http.StatusOK {
		t.Fatalf("POST /calls/%s/terminate = %d: %s", s.ID, term.Code, term.Body.String())
	}
	if gotPeer != "628123456789" || gotCallID != "call-999" {
		t.Fatalf("TerminateCall got peer=%q call_id=%q, want 628123456789/call-999", gotPeer, gotCallID)
	}
	rows, err := calls.Recent(context.Background(), s.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != call.StatusTerminated {
		t.Fatalf("wa_calls row = %+v, want one terminated row", rows)
	}
}

// TestCallsPageAnswersAnExplanatoryStateWhenCallingIsDisabled proves
// callsGuardUI actually stops a POST, not just the page's own form
// visibility — a direct POST (curl, or a stale tab) must degrade the same
// way.
func TestCallsPageAnswersAnExplanatoryStateWhenCallingIsDisabled(t *testing.T) {
	backend := &fakeGatewayBackend{name: "waxum-shaped"}
	srv, sessions, s, _ := newSessionAdminTestServer(t, backend, false)

	rec := postFormAsAdmin(t, srv, sessions, "/calls/tts", url.Values{
		"routing": {"explicit"}, "session_id": {s.ID}, "to": {"628123456789"}, "text": {"hi"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /calls/tts = %d, want 200 with an inline explanation", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "disabled on this deployment") {
		t.Errorf("response does not explain calling is disabled; got:\n%s", rec.Body.String())
	}
}
