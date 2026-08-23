package wa

import (
	"context"
	"path/filepath"
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
	m := NewManager(context.Background(), fake, newStubSessions(), newStubMsgs(),
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
}

func (f *fakeBackend) Name() string { return f.name }
func (f *fakeBackend) StartPairing(context.Context, *ManagedSession, PairMode) error {
	return ErrUnsupported
}
func (f *fakeBackend) Connect(context.Context, *ManagedSession) error    { return ErrUnsupported }
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
func (f *fakeBackend) Live(*ManagedSession) bool { return false }
func (f *fakeBackend) Close()                    { f.closeCalled = true }
