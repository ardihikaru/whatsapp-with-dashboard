package selfconvo

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/wasession"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// fakeGatewayWABackend is a minimal wa.Backend fake for this file's tests —
// copying the shape of internal/handler/media_calls_test_backend_test.go's
// fakeGatewayBackend (wa's own fakeBackend is test-private, in-package-only)
// rather than exercising any real transport. Every method this file's
// tests do not specifically drive answers wa.ErrUnsupported.
type fakeGatewayWABackend struct{}

func (fakeGatewayWABackend) Name() string { return "fake" }
func (fakeGatewayWABackend) StartPairing(context.Context, *wa.ManagedSession, wa.PairMode) error {
	return wa.ErrUnsupported
}
func (fakeGatewayWABackend) Connect(context.Context, *wa.ManagedSession) error {
	return wa.ErrUnsupported
}
func (fakeGatewayWABackend) Disconnect(context.Context, *wa.ManagedSession) error { return nil }
func (fakeGatewayWABackend) Logout(context.Context, *wa.ManagedSession) error     { return nil }
func (fakeGatewayWABackend) SendText(context.Context, *wa.ManagedSession, wa.JID, string) (string, error) {
	return "", wa.ErrUnsupported
}
func (fakeGatewayWABackend) JoinedGroups(context.Context, *wa.ManagedSession) ([]wa.GroupInfo, error) {
	return nil, wa.ErrUnsupported
}
func (fakeGatewayWABackend) SendMedia(context.Context, *wa.ManagedSession, wa.JID, wa.OutboundMedia) (string, error) {
	return "", wa.ErrUnsupported
}
func (fakeGatewayWABackend) CallTTS(context.Context, *wa.ManagedSession, wa.JID, string, string) (wa.CallOutcome, error) {
	return wa.CallOutcome{}, wa.ErrUnsupported
}
func (fakeGatewayWABackend) CallPlay(context.Context, *wa.ManagedSession, wa.JID, string) (wa.CallOutcome, error) {
	return wa.CallOutcome{}, wa.ErrUnsupported
}
func (fakeGatewayWABackend) CallTerminate(context.Context, *wa.ManagedSession, wa.JID, string, string) error {
	return wa.ErrUnsupported
}
func (fakeGatewayWABackend) Live(*wa.ManagedSession) bool { return false }
func (fakeGatewayWABackend) Close()                       {}

var _ wa.Backend = fakeGatewayWABackend{}

// newGatewayTestManager builds a real *wa.Manager over a temp SQLite pool —
// the plan's requirement that this file's tests exercise the real
// wa.Manager/message.Repository, not a wa-package stub — and returns the
// message repository alongside it so tests can read back the audit row
// Send leaves.
func newGatewayTestManager(t *testing.T) (*wa.Manager, *message.SQLRepository) {
	t.Helper()
	pool, err := db.Open(filepath.Join(t.TempDir(), "t.db"), db.SQLiteDialect)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	if err := db.EnsureSchema(context.Background(), pool); err != nil {
		t.Fatalf("db.EnsureSchema: %v", err)
	}
	msgs := message.NewRepository(pool)
	mgr := wa.NewManager(context.Background(), fakeGatewayWABackend{}, wasession.NewRepository(pool),
		msgs, nil, wa.Config{SendRate: 100, SendBurst: 5, ReconcilePar: 2})
	return mgr, msgs
}

func TestManagerGatewayReportsAnUnknownSessionAsAbsent(t *testing.T) {
	mgr, msgs := newGatewayTestManager(t)
	gw := NewManagerGateway(mgr, msgs)

	_, ok := gw.Participant("does-not-exist")
	if ok {
		t.Fatal("Participant reported ok=true for an unknown session id")
	}
}

// TestManagerGatewayHealthyReportsAnUnknownSessionByName pins Healthy's
// not-found branch against a REAL Manager: an id with no session at all
// must fail with a reason naming that id, not silently pass.
func TestManagerGatewayHealthyReportsAnUnknownSessionByName(t *testing.T) {
	mgr, msgs := newGatewayTestManager(t)
	gw := NewManagerGateway(mgr, msgs)

	sessionID, reason, ok := gw.Healthy([]string{"does-not-exist"})
	if ok {
		t.Fatal("Healthy([\"does-not-exist\"]) ok = true, want false")
	}
	if sessionID != "does-not-exist" {
		t.Errorf("sessionID = %q, want %q", sessionID, "does-not-exist")
	}
	if !strings.Contains(reason, "does-not-exist") {
		t.Errorf("reason = %q, want it to name the session", reason)
	}
}

// TestManagerGatewayHealthyReportsAFreshlyCreatedSessionAsUnhealthy pins
// Healthy against a REAL Manager/Session that exists but was never
// connected (state=new): Connected is false the same way Participant
// reports it, so Healthy must refuse it too and name the session.
func TestManagerGatewayHealthyReportsAFreshlyCreatedSessionAsUnhealthy(t *testing.T) {
	mgr, msgs := newGatewayTestManager(t)
	gw := NewManagerGateway(mgr, msgs)

	s, err := mgr.Create(context.Background(), "session a", "+62 812-0000-1111")
	if err != nil {
		t.Fatalf("mgr.Create: %v", err)
	}

	sessionID, reason, ok := gw.Healthy([]string{s.ID})
	if ok {
		t.Fatalf("Healthy([%q]) ok = true, want false (a freshly created session was never connected)", s.ID)
	}
	if sessionID != s.ID {
		t.Errorf("sessionID = %q, want %q", sessionID, s.ID)
	}
	if !strings.Contains(reason, s.ID) {
		t.Errorf("reason = %q, want it to name the session", reason)
	}
}

func TestManagerGatewaySendStampsSelfConvoCategoryAndTheRunIdAndFailsTheRowWhenNotConnected(t *testing.T) {
	mgr, msgs := newGatewayTestManager(t)
	gw := NewManagerGateway(mgr, msgs)

	s, err := mgr.Create(context.Background(), "session a", "+62 812-0000-1111")
	if err != nil {
		t.Fatalf("mgr.Create: %v", err)
	}

	const runID = "run-123"
	_, err = gw.Send(context.Background(), SendRequest{
		SessionID: s.ID,
		ToDigits:  "62812000022",
		Text:      "halo",
		RunID:     runID,
	})
	if !errors.Is(err, wa.ErrNotConnected) {
		t.Fatalf("Send err = %v, want wa.ErrNotConnected (session is state=new, never connected)", err)
	}

	rows, _, err := msgs.List(context.Background(), message.Filter{SessionID: s.ID}, "", 10)
	if err != nil {
		t.Fatalf("msgs.List: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows for session %s, want exactly 1", len(rows), s.ID)
	}
	row := rows[0]
	if row.Category != message.CategorySelfConvo {
		t.Fatalf("Category = %q, want %q", row.Category, message.CategorySelfConvo)
	}
	if row.ClientRef != runID {
		t.Fatalf("ClientRef = %q, want %q", row.ClientRef, runID)
	}
	if row.Status != message.StatusFailed {
		t.Fatalf("Status = %q, want %q", row.Status, message.StatusFailed)
	}
}

// TestManagerGatewayUsesTheCallersMessageIdForTheAuditRow pins item 3 of the
// runner review: the runner mints the message id and stores it on the turn
// (ClaimTurn) BEFORE ever calling Send, so Send must use THAT id for the
// wa_messages row rather than minting its own (wa.NewID()) — otherwise a
// turn cut off mid-send would have no audit row to reconcile against on the
// next boot (see runner.go's Recover).
func TestManagerGatewayUsesTheCallersMessageIdForTheAuditRow(t *testing.T) {
	mgr, msgs := newGatewayTestManager(t)
	gw := NewManagerGateway(mgr, msgs)

	s, err := mgr.Create(context.Background(), "session a", "+62 812-0000-1111")
	if err != nil {
		t.Fatalf("mgr.Create: %v", err)
	}

	const callerMessageID = "caller-minted-id-0001"
	_, err = gw.Send(context.Background(), SendRequest{
		SessionID: s.ID,
		ToDigits:  "62812000022",
		Text:      "halo",
		RunID:     "run-456",
		MessageID: callerMessageID,
	})
	if !errors.Is(err, wa.ErrNotConnected) {
		t.Fatalf("Send err = %v, want wa.ErrNotConnected (session is state=new, never connected)", err)
	}

	row, err := msgs.GetByID(context.Background(), callerMessageID)
	if err != nil {
		t.Fatalf("GetByID(%q): %v — Send must have inserted the row under the caller's own id", callerMessageID, err)
	}
	if row.Status != message.StatusFailed {
		t.Fatalf("Status = %q, want %q", row.Status, message.StatusFailed)
	}
}
