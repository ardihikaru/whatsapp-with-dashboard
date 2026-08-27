package wa

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/waxum"
)

// newTestWhatsmeowBackend builds a real whatsmeow Backend on a throwaway
// SQLite store — the same construction TestWhatsmeowBackendIsTheOnlyBackendWiredToday
// (backend_test.go) uses, factored out here since several tests in this
// file need one.
func newTestWhatsmeowBackend(t *testing.T) Backend {
	t.Helper()
	pool, err := db.Open(filepath.Join(t.TempDir(), "wa.db"), db.SQLiteDialect)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	backend, err := NewWhatsmeowBackend(context.Background(), pool, db.SQLiteDialect)
	if err != nil {
		t.Fatalf("NewWhatsmeowBackend: %v", err)
	}
	t.Cleanup(backend.Close)
	return backend
}

// TestWhatsmeowBackendReportsMediaAndCallsUnsupported guards
// backend_meow_unsupported.go: whatsmeow has no in-process story for either
// capability (verified against the vendored module — see that file's doc
// comment), so all four Backend methods added alongside media/calls must
// answer ErrUnsupported rather than panicking on the nil *whatsmeow.Client
// these calls would otherwise dereference, or silently no-opping.
func TestWhatsmeowBackendReportsMediaAndCallsUnsupported(t *testing.T) {
	backend := newTestWhatsmeowBackend(t)
	m := newWaxumTestManager(t, backend)
	s, err := m.Create(context.Background(), "gw", "+62 812-0000-1111")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := backend.SendMedia(context.Background(), s, EmptyJID, OutboundMedia{}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("SendMedia error = %v, want ErrUnsupported", err)
	}
	if _, err := backend.CallTTS(context.Background(), s, EmptyJID, "hi", ""); !errors.Is(err, ErrUnsupported) {
		t.Errorf("CallTTS error = %v, want ErrUnsupported", err)
	}
	if _, err := backend.CallPlay(context.Background(), s, EmptyJID, "https://example.com/a.mp3"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("CallPlay error = %v, want ErrUnsupported", err)
	}
	if err := backend.CallTerminate(context.Background(), s, EmptyJID, "call-1", ""); !errors.Is(err, ErrUnsupported) {
		t.Errorf("CallTerminate error = %v, want ErrUnsupported", err)
	}
}

// TestTheWaxumBackendSatisfiesEveryCapabilityInterface guards the whole
// point of capabilities.go: waxumBackend must implement GatewayHealth,
// MediaStore, SessionOps and WebhookAdmin, all four, or a gateway-admin
// HTTP handler built against one of them (a later phase) would compile
// against the interface and then get ErrUnsupported from every real waxum
// deployment.
func TestTheWaxumBackendSatisfiesEveryCapabilityInterface(t *testing.T) {
	client, _ := newWaxumFakeServer(t, waxumFakeOpts{})
	backend := NewWaxumBackend(client, WaxumConfig{})

	if _, ok := backend.(GatewayHealth); !ok {
		t.Error("waxumBackend does not satisfy GatewayHealth")
	}
	if _, ok := backend.(MediaStore); !ok {
		t.Error("waxumBackend does not satisfy MediaStore")
	}
	if _, ok := backend.(SessionOps); !ok {
		t.Error("waxumBackend does not satisfy SessionOps")
	}
	if _, ok := backend.(WebhookAdmin); !ok {
		t.Error("waxumBackend does not satisfy WebhookAdmin")
	}
}

// TestIsPausedReadsTheStatusEndpointWithoutMutatingAnything proves IsPaused
// answers waxum's GET .../status "paused" field directly, rather than
// deriving it from — or having any side effect through — Pause/Resume.
func TestIsPausedReadsTheStatusEndpointWithoutMutatingAnything(t *testing.T) {
	client, calls := newWaxumFakeServer(t, waxumFakeOpts{
		statusFn: func(n int) waxum.Status { return waxum.Status{Status: "connected", Paused: true} },
	})
	backend := NewWaxumBackend(client, WaxumConfig{})
	ops := backend.(SessionOps)

	paused, err := ops.IsPaused(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("IsPaused: %v", err)
	}
	if !paused {
		t.Error("IsPaused = false, want true")
	}
	if got := calls.status.Load(); got != 1 {
		t.Errorf("status endpoint called %d times, want exactly 1", got)
	}
}

// TestTheWhatsmeowBackendSatisfiesNoCapabilityInterface is the negative
// half of the test above: without it, a stray stub method added to
// meowBackend by mistake (e.g. copy-pasting a waxumBackend method) would
// silently enable a gateway-administration HTTP path — pause, webhook
// registration, trust tokens — on a backend that has nothing behind it,
// and the type assertion in manager_capabilities.go would happily route
// requests there instead of returning ErrUnsupported.
func TestTheWhatsmeowBackendSatisfiesNoCapabilityInterface(t *testing.T) {
	backend := newTestWhatsmeowBackend(t)

	if _, ok := backend.(GatewayHealth); ok {
		t.Error("meowBackend unexpectedly satisfies GatewayHealth")
	}
	if _, ok := backend.(MediaStore); ok {
		t.Error("meowBackend unexpectedly satisfies MediaStore")
	}
	if _, ok := backend.(SessionOps); ok {
		t.Error("meowBackend unexpectedly satisfies SessionOps")
	}
	if _, ok := backend.(WebhookAdmin); ok {
		t.Error("meowBackend unexpectedly satisfies WebhookAdmin")
	}
}

// TestSessionOpsIsUnavailableOnTheWhatsmeowBackend, TestMediaStoreIsUnavailableOnTheWhatsmeowBackend
// and TestWebhookAdminIsUnavailableOnTheWhatsmeowBackend each guard the
// SAME claim as the negative test above, but through Manager's own
// accessor (manager_capabilities.go) — the actual path a handler would
// use, not a bare type assertion. Both layers are worth asserting: the
// type-assertion test catches a stray method on meowBackend itself; these
// catch the accessor failing to return ErrUnsupported even when the
// assertion correctly fails.
func TestSessionOpsIsUnavailableOnTheWhatsmeowBackend(t *testing.T) {
	m := newWaxumTestManager(t, newTestWhatsmeowBackend(t))
	if _, err := m.SessionOps(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("SessionOps() error = %v, want ErrUnsupported", err)
	}
}

func TestMediaStoreIsUnavailableOnTheWhatsmeowBackend(t *testing.T) {
	m := newWaxumTestManager(t, newTestWhatsmeowBackend(t))
	if _, err := m.MediaStore(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("MediaStore() error = %v, want ErrUnsupported", err)
	}
}

func TestWebhookAdminIsUnavailableOnTheWhatsmeowBackend(t *testing.T) {
	m := newWaxumTestManager(t, newTestWhatsmeowBackend(t))
	if _, err := m.WebhookAdmin(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("WebhookAdmin() error = %v, want ErrUnsupported", err)
	}
}

// TestGatewayHealthIsUnavailableOnTheWhatsmeowBackend rounds out the four
// accessors — GatewayHealth returns a plain error, not an (X, error) pair,
// so it is not covered by the same table as the three above.
func TestGatewayHealthIsUnavailableOnTheWhatsmeowBackend(t *testing.T) {
	m := newWaxumTestManager(t, newTestWhatsmeowBackend(t))
	if err := m.GatewayHealth(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("GatewayHealth() error = %v, want ErrUnsupported", err)
	}
}

// TestManagerCapabilityAccessorsDoNotPanicOnANilBackend guards the same
// nil-guard Snapshot already relies on (session.go's "mgr.backend is nil
// only in tests that never activate a session"): dozens of existing tests
// build a Manager via newTestManager (stubs_test.go:117), which passes a
// nil Backend. Every accessor here must return ErrUnsupported, not panic
// on a nil interface type assertion or a nil method call.
func TestManagerCapabilityAccessorsDoNotPanicOnANilBackend(t *testing.T) {
	m := newTestManager(newStubSessions(), newStubMsgs())

	if err := m.GatewayHealth(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Errorf("GatewayHealth() error = %v, want ErrUnsupported", err)
	}
	if _, err := m.MediaStore(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("MediaStore() error = %v, want ErrUnsupported", err)
	}
	if _, err := m.SessionOps(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("SessionOps() error = %v, want ErrUnsupported", err)
	}
	if _, err := m.WebhookAdmin(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("WebhookAdmin() error = %v, want ErrUnsupported", err)
	}
}

// TestMapErrTurnsA422IntoErrRejectedByBackend guards the fix to mapErr:
// before it, a 422 from waxum's own payload validation (a bad media
// descriptor, an unknown webhook event, …) fell through mapErr's default
// case as a bare *waxum.APIError, which the handler layer has no case for
// and would report as a generic 500 — "internal server error" for a
// problem that was actually the request.
func TestMapErrTurnsA422IntoErrRejectedByBackend(t *testing.T) {
	got := mapErr(&waxum.APIError{StatusCode: http.StatusUnprocessableEntity, Body: "bad media descriptor"})
	if !errors.Is(got, ErrRejectedByBackend) {
		t.Fatalf("mapErr(422) = %v, want ErrRejectedByBackend", got)
	}
}

// TestMapErrAlsoTurnsA400And415IntoErrRejectedByBackend covers the other
// two statuses mapErr's new case handles — a bad mimetype (415) and a
// malformed request body waxum's own validation rejects outright (400).
func TestMapErrAlsoTurnsA400And415IntoErrRejectedByBackend(t *testing.T) {
	for _, code := range []int{http.StatusBadRequest, http.StatusUnsupportedMediaType} {
		got := mapErr(&waxum.APIError{StatusCode: code})
		if !errors.Is(got, ErrRejectedByBackend) {
			t.Errorf("mapErr(%d) = %v, want ErrRejectedByBackend", code, got)
		}
	}
}

// TestMapErrNamesTheActualCauseWhenWaxumsBodyCarriesOne proves mapErr's
// 400/415/422 case surfaces waxum's own error message (the SSRF guard's
// "resolves to a private/reserved address", for one) rather than only the
// generic ErrRejectedByBackend text — errors.Is must still match (the
// handler layer's writeWAError keys off it), but Error() must NAME the
// cause, per the phase brief's "surface an operator-facing message that
// names the actual cause" requirement for webhook registration.
func TestMapErrNamesTheActualCauseWhenWaxumsBodyCarriesOne(t *testing.T) {
	got := mapErr(&waxum.APIError{
		StatusCode: http.StatusBadRequest,
		Body:       `{"success":false,"error":{"code":400,"message":"Callback URL resolves to a private/reserved address"}}`,
	})
	if !errors.Is(got, ErrRejectedByBackend) {
		t.Fatalf("mapErr(...) = %v, want it to still match ErrRejectedByBackend", got)
	}
	if !strings.Contains(got.Error(), "private/reserved address") {
		t.Fatalf("mapErr(...).Error() = %q, want it to name the actual cause (a private/reserved address)", got.Error())
	}
}

// TestMapErrLeavesOtherAPIErrorStatusesUntranslated guards against the 422
// case being too broad — a genuinely unexpected status (500 here) must stay
// a bare *waxum.APIError, not get folded into ErrRejectedByBackend, which
// would misrepresent a backend-side failure as a bad request.
func TestMapErrLeavesOtherAPIErrorStatusesUntranslated(t *testing.T) {
	got := mapErr(&waxum.APIError{StatusCode: http.StatusInternalServerError, Body: "boom"})
	if errors.Is(got, ErrRejectedByBackend) {
		t.Fatalf("mapErr(500) = %v, must not be ErrRejectedByBackend", got)
	}
	var apiErr *waxum.APIError
	if !errors.As(got, &apiErr) || apiErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("mapErr(500) = %v, want it to remain a *waxum.APIError{StatusCode: 500}", got)
	}
}

// TestMapErrStillMapsThePreExistingSentinelsAfterThe422Addition guards
// against the 422 addition's default-case reshuffle accidentally shadowing
// one of the switch's existing cases (404/409/429/503) — every one of
// those is matched by errors.Is BEFORE the new *waxum.APIError branch is
// ever reached, and this pins that order.
func TestMapErrStillMapsThePreExistingSentinelsAfterThe422Addition(t *testing.T) {
	cases := []struct {
		name string
		in   error
		want error
	}{
		{"404 not found", waxum.ErrNotFound, ErrNotFound},
		{"409 conflict", waxum.ErrConflict, ErrNotConnected},
		{"429 rate limited", waxum.ErrRateLimited, ErrBackendBusy},
		{"503 client not connected", waxum.ErrClientNotConnected, ErrNotConnected},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mapErr(tc.in); !errors.Is(got, tc.want) {
				t.Errorf("mapErr(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
