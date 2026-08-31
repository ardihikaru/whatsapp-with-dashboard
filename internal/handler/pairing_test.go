package handler

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	wsm "github.com/ardihikaru/whatsapp-with-dashboard"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/wasession"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/renderer"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// fakeStartPairingBackend is a minimal wa.Backend whose StartPairing answers
// a fixed error — everything else is unused by the tests in this file and
// panics if called, so an accidental extra call fails loudly instead of
// silently returning a zero value.
type fakeStartPairingBackend struct{ err error }

func (fakeStartPairingBackend) Name() string { return "fake-start-pairing" }
func (b fakeStartPairingBackend) StartPairing(context.Context, *wa.ManagedSession, wa.PairMode) error {
	return b.err
}
func (fakeStartPairingBackend) Connect(context.Context, *wa.ManagedSession) error {
	panic("not used by this test")
}
func (fakeStartPairingBackend) Disconnect(context.Context, *wa.ManagedSession) error {
	panic("not used by this test")
}
func (fakeStartPairingBackend) Logout(context.Context, *wa.ManagedSession) error {
	panic("not used by this test")
}
func (fakeStartPairingBackend) SendText(context.Context, *wa.ManagedSession, wa.JID, string) (string, error) {
	panic("not used by this test")
}
func (fakeStartPairingBackend) JoinedGroups(context.Context, *wa.ManagedSession) ([]wa.GroupInfo, error) {
	panic("not used by this test")
}
func (fakeStartPairingBackend) SendMedia(context.Context, *wa.ManagedSession, wa.JID, wa.OutboundMedia) (string, error) {
	panic("not used by this test")
}
func (fakeStartPairingBackend) CallTTS(context.Context, *wa.ManagedSession, wa.JID, string, string) (wa.CallOutcome, error) {
	panic("not used by this test")
}
func (fakeStartPairingBackend) CallPlay(context.Context, *wa.ManagedSession, wa.JID, string) (wa.CallOutcome, error) {
	panic("not used by this test")
}
func (fakeStartPairingBackend) CallTerminate(context.Context, *wa.ManagedSession, wa.JID, string, string) error {
	panic("not used by this test")
}
func (fakeStartPairingBackend) Live(*wa.ManagedSession) bool { return false }
func (fakeStartPairingBackend) Close()                       {}

// newPairingTestHandlers wires a real SQLite-backed wa.Manager around
// backend — the house pattern (newAuthTestServer, api_auth_test.go) minus
// the auth/API-client plumbing PostPairStart does not touch (it is
// page-mounted, driven directly here, no middleware in the chain).
func newPairingTestHandlers(t *testing.T, backend wa.Backend) (*handlers, *wa.ManagedSession) {
	t.Helper()
	pool, err := db.Open(filepath.Join(t.TempDir(), "test.db"), db.SQLiteDialect)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	if err := db.EnsureSchema(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	webFS, err := fs.Sub(wsm.FS, "web")
	if err != nil {
		t.Fatal(err)
	}
	rend, err := renderer.New(webFS, "")
	if err != nil {
		t.Fatal(err)
	}
	mgr := wa.NewManager(context.Background(), backend, wasession.NewRepository(pool),
		message.NewRepository(pool), nil, wa.Config{SendRate: 1, SendBurst: 3, ReconcilePar: 2})
	s, err := mgr.Create(context.Background(), "gw", "+62 812-0000-9999")
	if err != nil {
		t.Fatal(err)
	}
	return &handlers{Deps: Deps{Renderer: rend, Manager: mgr}}, s
}

func postPairStart(h *handlers, sessionID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/sessions/"+sessionID+"/pair/start", nil)
	req.SetPathValue("id", sessionID)
	rec := httptest.NewRecorder()
	h.PostPairStart(rec, req)
	return rec
}

// TestPostPairStartAnswers503WhenTheBackendIsRateLimiting is the regression
// test for the production misdiagnosis this whole change addresses: a bare
// 429 from waxum's CreateSession/Connect call used to surface as a fatal
// 500 ("could not start pairing"), having been mapped onto the misleading
// "send queue full" sentinel. wa.ErrBackendBusy must answer 503 with a
// Retry-After header — a transient, worth-retrying condition, not a server
// bug and not the client's own request rate.
func TestPostPairStartAnswers503WhenTheBackendIsRateLimiting(t *testing.T) {
	h, s := newPairingTestHandlers(t, fakeStartPairingBackend{err: wa.ErrBackendBusy})

	rec := postPairStart(h, s.ID)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Fatal("no Retry-After header on a 503 for a rate-limited backend")
	}
}

// TestPostPairStartStill500sOnAnUnrecognizedBackendError guards the OTHER
// side of the same switch: an error PostPairStart does not specifically
// recognize (neither ErrWrongState nor ErrBackendBusy) must still answer
// 500, not silently succeed or leak a 200. This is what the 503 case above
// was carved out OF, so it is worth pinning down alongside it.
func TestPostPairStartStill500sOnAnUnrecognizedBackendError(t *testing.T) {
	h, s := newPairingTestHandlers(t, fakeStartPairingBackend{err: errUnrecognizedForTest})

	rec := postPairStart(h, s.ID)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
}

// TestPostPairStartAnswers409WhenAlreadyPairing pins the pre-existing
// ErrWrongState mapping alongside the two above, so this file documents the
// whole switch's contract in one place rather than half of it.
func TestPostPairStartAnswers409WhenAlreadyPairing(t *testing.T) {
	h, s := newPairingTestHandlers(t, fakeStartPairingBackend{err: wa.ErrWrongState})

	rec := postPairStart(h, s.ID)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusConflict, rec.Body.String())
	}
}

var errUnrecognizedForTest = &pairingTestError{"boom"}

type pairingTestError struct{ s string }

func (e *pairingTestError) Error() string { return e.s }
