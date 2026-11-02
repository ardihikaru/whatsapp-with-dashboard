package sessionalert

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/wasession"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// countingGroupsBackend is a minimal wa.Backend fake — same shape as
// internal/selfconvo/gateway_wa_test.go's fakeGatewayWABackend (wa's own
// fakeBackend is test-private, in-package-only) — whose JoinedGroups
// counts every call, so this file's tests can prove managerGateway.Member
// actually caches rather than re-fetching on every call.
type countingGroupsBackend struct {
	mu     sync.Mutex
	calls  int
	groups []wa.GroupInfo
}

func (*countingGroupsBackend) Name() string { return "fake" }
func (*countingGroupsBackend) StartPairing(context.Context, *wa.ManagedSession, wa.PairMode) error {
	return wa.ErrUnsupported
}
func (*countingGroupsBackend) Connect(context.Context, *wa.ManagedSession) error {
	return wa.ErrUnsupported
}
func (*countingGroupsBackend) Disconnect(context.Context, *wa.ManagedSession) error { return nil }
func (*countingGroupsBackend) Logout(context.Context, *wa.ManagedSession) error     { return nil }
func (*countingGroupsBackend) SendText(context.Context, *wa.ManagedSession, wa.JID, string) (string, error) {
	return "", wa.ErrUnsupported
}
func (b *countingGroupsBackend) JoinedGroups(context.Context, *wa.ManagedSession) ([]wa.GroupInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	return append([]wa.GroupInfo(nil), b.groups...), nil
}
func (*countingGroupsBackend) SendMedia(context.Context, *wa.ManagedSession, wa.JID, wa.OutboundMedia) (string, error) {
	return "", wa.ErrUnsupported
}
func (*countingGroupsBackend) CallTTS(context.Context, *wa.ManagedSession, wa.JID, string, string) (wa.CallOutcome, error) {
	return wa.CallOutcome{}, wa.ErrUnsupported
}
func (*countingGroupsBackend) CallPlay(context.Context, *wa.ManagedSession, wa.JID, string) (wa.CallOutcome, error) {
	return wa.CallOutcome{}, wa.ErrUnsupported
}
func (*countingGroupsBackend) CallTerminate(context.Context, *wa.ManagedSession, wa.JID, string, string) error {
	return wa.ErrUnsupported
}
func (*countingGroupsBackend) Live(*wa.ManagedSession) bool { return false }
func (*countingGroupsBackend) Close()                       {}

func (b *countingGroupsBackend) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

var _ wa.Backend = (*countingGroupsBackend)(nil)

func newMemberTestManager(t *testing.T, backend *countingGroupsBackend) *wa.Manager {
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
	mgr := wa.NewManager(context.Background(), backend, wasession.NewRepository(pool),
		msgs, nil, wa.Config{SendRate: 100, SendBurst: 5, ReconcilePar: 2})
	return mgr
}

// TestManagerGatewayMemberCachesWithinTheTTL proves Member only calls the
// backend's JoinedGroups ONCE for repeated calls inside the TTL window —
// the requirement CLAUDE.md's rate-limit hazard exists for: an uncached
// membership check on every fallback-tier candidate, for every alert,
// could feed waxum's tiny (~3 request) burst.
func TestManagerGatewayMemberCachesWithinTheTTL(t *testing.T) {
	backend := &countingGroupsBackend{groups: []wa.GroupInfo{{JID: testGroupJID, Name: "Ops"}}}
	mgr := newMemberTestManager(t, backend)
	s, err := mgr.Create(context.Background(), "s1", "628120000000")
	if err != nil {
		t.Fatal(err)
	}

	gw := NewManagerGateway(mgr).(*managerGateway)
	clk := &clockBox{t: time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)}
	gw.now = clk.now
	gw.ttl = 10 * time.Minute

	for i := 0; i < 3; i++ {
		member, err := gw.Member(context.Background(), s.ID, testGroupJID)
		if err != nil {
			t.Fatal(err)
		}
		if !member {
			t.Fatalf("call %d: Member = false, want true", i)
		}
	}
	if got := backend.callCount(); got != 1 {
		t.Fatalf("backend.JoinedGroups called %d time(s) across 3 Member calls within the TTL, want 1", got)
	}

	// Past the TTL, Member re-fetches.
	clk.advance(11 * time.Minute)
	if _, err := gw.Member(context.Background(), s.ID, testGroupJID); err != nil {
		t.Fatal(err)
	}
	if got := backend.callCount(); got != 2 {
		t.Fatalf("backend.JoinedGroups called %d time(s) after the TTL expired, want 2", got)
	}
}

// TestManagerGatewayMemberReportsAnUnknownSessionAsAnError proves a
// session id this gateway does not know answers an ERROR, not a bare
// false — chooseSender's fail-closed contract (sender.go) depends on this.
func TestManagerGatewayMemberReportsAnUnknownSessionAsAnError(t *testing.T) {
	mgr := newMemberTestManager(t, &countingGroupsBackend{})
	gw := NewManagerGateway(mgr)
	if _, err := gw.Member(context.Background(), "does-not-exist", testGroupJID); err == nil {
		t.Fatal("Member for an unknown session = nil error, want an error")
	}
}
