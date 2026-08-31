package wa

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
)

// TestWhatsmeowBackendIsTheOnlyBackendWiredToday pins whatsmeow's own half
// of the Backend seam: NewWhatsmeowBackend builds something satisfying
// Backend and named "whatsmeow". A second implementation (waxumBackend,
// backend_waxum.go — see its own tests in backend_waxum_test.go) exists
// alongside it now, selected by WSM_WA_BACKEND at boot
// (cmd/app/main.go's newBackend, proven by
// TestNewBackendNeverOpensTheWhatsmeowStoreUnderWaxum /
// TestNewBackendOpensTheWhatsmeowStoreByDefault in cmd/app/main_test.go) —
// this test's claim is scoped to whatsmeow alone so it does not need
// touching every time the OTHER backend changes.
func TestWhatsmeowBackendIsTheOnlyBackendWiredToday(t *testing.T) {
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

	if got := backend.Name(); got != "whatsmeow" {
		t.Fatalf("Name() = %q, want %q", got, "whatsmeow")
	}
	// Compile-time-ish guard, made a runtime assertion so it shows up in test
	// output: NewWhatsmeowBackend's return type IS the seam — anything that
	// stops satisfying Backend fails to compile, not just this check.
	var _ Backend = backend
}

// TestManagerHoldsWhateverBackendItWasGiven guards NewManager's own half of
// the seam: the manager must not special-case or reach past the Backend
// interface to whatsmeow internals — Delete/Close route exclusively through
// backend.Logout/backend.Close (see manager_crud.go, manager_boot.go). A
// fake here proves the wiring without needing a real whatsmeow store.
func TestManagerHoldsWhateverBackendItWasGiven(t *testing.T) {
	fake := &fakeBackend{name: "fake-for-test"}
	m := NewManager(context.Background(), fake, newStubSessions(), newStubMsgs(), nil,
		Config{SendRate: 1, SendBurst: 3, ReconcilePar: 2})

	s, err := m.Create(context.Background(), "gw", "+62 812-0000-1111")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
	if !fake.logoutCalled {
		t.Fatal("Manager.Delete did not route through Backend.Logout")
	}

	m.Close()
	if !fake.closeCalled {
		t.Fatal("Manager.Close did not route through Backend.Close")
	}
}

// fakeBackend is a minimal Backend used only to prove Manager calls through
// the interface rather than reaching into a concrete whatsmeow type.
type fakeBackend struct {
	name         string
	logoutCalled bool
	closeCalled  bool

	// sendTextID, when non-empty, makes SendText succeed and return it
	// instead of the default ErrUnsupported — used by trySend/sender tests
	// that need a Backend.SendText call to actually succeed.
	sendTextID string

	// connectFn, when set, overrides Connect's default ErrUnsupported —
	// used by the reconnect supervisor tests (reconnect_test.go), which
	// need Connect to either succeed (proving a reconnect attempt actually
	// reached the backend) or block on ctx (proving Manager.Close's
	// cancellation reaches an in-flight auto-reconnect attempt). Every
	// call is counted in connectCalls regardless of connectFn, so a test
	// can assert "attempted N times" without racing on a bespoke counter.
	connectFn    func(ctx context.Context, s *ManagedSession) error
	connectCalls atomic.Int32

	// sendMediaID, when non-empty, makes SendMedia succeed and return it
	// instead of the default ErrUnsupported — the media analogue of
	// sendTextID above, for later phases' sender tests exercising a media
	// job.
	sendMediaID string

	// mediaMu guards sentMedia and sendMediaCalls, the record of every
	// OutboundMedia that has crossed SendMedia — a later phase's sender
	// tests need to assert on the exact media that reached the backend,
	// the same way sendTextID alone cannot prove text content.
	mediaMu        sync.Mutex
	sentMedia      []OutboundMedia
	sendMediaCalls int

	// sendMediaFn, when set, is called (NOT under mediaMu — it must not
	// itself call back into this fake while holding it, but IS free to read
	// mediaMu-guarded fields via its own locking) before sendMediaID/
	// ErrUnsupported is applied. A non-nil return overrides that default,
	// letting a test vary the outcome per call — e.g.
	// TestAMediaSendIsRetriedWithoutReUploading fails the first two calls
	// with a retryable error and succeeds the third, to prove trySend's
	// retry loop re-sends rather than re-uploads.
	sendMediaFn func(to JID, m OutboundMedia) error
}

func (f *fakeBackend) Name() string { return f.name }
func (f *fakeBackend) StartPairing(context.Context, *ManagedSession, PairMode) error {
	return ErrUnsupported
}
func (f *fakeBackend) Connect(ctx context.Context, s *ManagedSession) error {
	f.connectCalls.Add(1)
	if f.connectFn != nil {
		return f.connectFn(ctx, s)
	}
	return ErrUnsupported
}
func (f *fakeBackend) Disconnect(context.Context, *ManagedSession) error { return nil }
func (f *fakeBackend) Logout(_ context.Context, _ *ManagedSession) error {
	f.logoutCalled = true
	return nil
}
func (f *fakeBackend) SendText(context.Context, *ManagedSession, JID, string) (string, error) {
	if f.sendTextID != "" {
		return f.sendTextID, nil
	}
	return "", ErrUnsupported
}
func (f *fakeBackend) JoinedGroups(context.Context, *ManagedSession) ([]GroupInfo, error) {
	return nil, ErrUnsupported
}
func (f *fakeBackend) SendMedia(ctx context.Context, s *ManagedSession, to JID, m OutboundMedia) (string, error) {
	f.mediaMu.Lock()
	f.sendMediaCalls++
	f.sentMedia = append(f.sentMedia, m)
	fn := f.sendMediaFn
	f.mediaMu.Unlock()
	if fn != nil {
		if err := fn(to, m); err != nil {
			return "", err
		}
	}
	if f.sendMediaID != "" {
		return f.sendMediaID, nil
	}
	return "", ErrUnsupported
}
func (f *fakeBackend) CallTTS(context.Context, *ManagedSession, JID, string, string) (CallOutcome, error) {
	return CallOutcome{}, ErrUnsupported
}
func (f *fakeBackend) CallPlay(context.Context, *ManagedSession, JID, string) (CallOutcome, error) {
	return CallOutcome{}, ErrUnsupported
}
func (f *fakeBackend) CallTerminate(context.Context, *ManagedSession, JID, string, string) error {
	return ErrUnsupported
}
func (f *fakeBackend) Live(*ManagedSession) bool { return false }
func (f *fakeBackend) Close()                    { f.closeCalled = true }
