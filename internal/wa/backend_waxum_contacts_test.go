package wa

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/waxum"
)

// TestCheckContactsMapsWaxumErrorsOntoTheWASentinels mirrors
// TestWaxumBackendMapsClientErrorsOntoTheWAErrSentinels for the
// ContactDirectory capability: a session waxum reports as gone or not
// connected (503 — waxum's get_client maps both "unknown session" and "not
// connected" to the same status) must read as ErrNotConnected, and its
// rate limiter (429) must read as ErrBackendBusy, NOT some generic error —
// mapErr is shared code, but CheckContacts is new code that has to
// actually CALL mapErr rather than return the raw waxum error, and this is
// the test that would catch a missing mapErr call.
func TestCheckContactsMapsWaxumErrorsOntoTheWASentinels(t *testing.T) {
	cases := []struct {
		name string
		code int
		want error
	}{
		{"503 (session unknown or not connected) maps to ErrNotConnected", http.StatusServiceUnavailable, ErrNotConnected},
		{"429 (waxum's tower_governor limiter) maps to ErrBackendBusy", http.StatusTooManyRequests, ErrBackendBusy},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.code)
			}))
			t.Cleanup(srv.Close)
			client := waxum.New(srv.URL, "test-token", time.Second,
				waxum.WithRetryPolicy(1, func(int, string) time.Duration { return time.Millisecond }))
			b := &waxumBackend{client: client}

			_, err := b.CheckContacts(context.Background(), "sess-1", []string{"559999999999"})
			if !errors.Is(err, c.want) {
				t.Fatalf("CheckContacts err = %v, want errors.Is(err, %v)", err, c.want)
			}
		})
	}
}

// TestCheckContactsReturnsWaxumsDecodedResultsOnSuccess guards
// CheckContacts actually translating waxum's response into []ContactCheck
// (phone/jid/is_registered) rather than discarding it, the same class of
// bug TestReportSpamDecodesTheReportIDResponse guards in the waxum package
// itself.
func TestCheckContactsReturnsWaxumsDecodedResultsOnSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"results":[
			{"phone":"559999999999","jid":"559999999999@s.whatsapp.net","is_registered":true},
			{"phone":"551234567890","jid":null,"is_registered":false}
		]}`))
	}))
	t.Cleanup(srv.Close)
	client := waxum.New(srv.URL, "test-token", time.Second)
	b := &waxumBackend{client: client}

	got, err := b.CheckContacts(context.Background(), "sess-1", []string{"559999999999", "551234567890"})
	if err != nil {
		t.Fatalf("CheckContacts: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(got))
	}
	if !got[0].IsRegistered || got[0].JID != "559999999999@s.whatsapp.net" {
		t.Errorf("results[0] = %+v, want a registered number with its jid", got[0])
	}
	if got[1].IsRegistered || got[1].JID != "" {
		t.Errorf("results[1] = %+v, want an unregistered number with an empty jid", got[1])
	}
}

// TestContactDirectoryIsUnavailableOnTheWhatsmeowBackend guards
// Manager.ContactDirectory returning ErrUnsupported for meowBackend, the
// same accessor-level assertion capabilities_test.go already makes for
// SessionOps/MediaStore/WebhookAdmin.
func TestContactDirectoryIsUnavailableOnTheWhatsmeowBackend(t *testing.T) {
	m := newWaxumTestManager(t, newTestWhatsmeowBackend(t))
	if _, err := m.ContactDirectory(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("ContactDirectory() error = %v, want ErrUnsupported", err)
	}
}
