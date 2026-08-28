package wa

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/wasession"
)

func TestCreateValidation(t *testing.T) {
	m := newTestManager(newStubSessions(), newStubMsgs())
	if _, err := m.Create(context.Background(), " ", "+62 812-0000-0000"); err == nil {
		t.Fatal("empty name accepted")
	}
	if _, err := m.Create(context.Background(), "gw", "0812"); err == nil {
		t.Fatal("local-format phone accepted")
	}
	var verr *ValidationError
	_, err := m.Create(context.Background(), "gw", "0812-3456-7890")
	if !errorsAs(err, &verr) {
		t.Fatalf("err = %T, want *ValidationError", err)
	}
}

func errorsAs(err error, target any) bool {
	verr, ok := target.(**ValidationError)
	if !ok {
		return false
	}
	v, ok := err.(*ValidationError)
	if ok {
		*verr = v
	}
	return ok
}

func TestCreateDeleteLifecycle(t *testing.T) {
	repo := newStubSessions()
	m := newTestManager(repo, newStubMsgs())
	s, err := m.Create(context.Background(), "gw", "+62 812-0000-1111")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Get(s.ID); !ok {
		t.Fatal("created session not in map")
	}
	if err := m.Delete(context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Get(s.ID); ok {
		t.Fatal("deleted session still reachable — map removal must come first")
	}
	if err := m.Delete(context.Background(), s.ID); err != ErrNotFound {
		t.Fatalf("second delete err = %v, want ErrNotFound", err)
	}
}

// TestConcurrentDeletes: exactly one caller wins the map removal; everyone
// else sees ErrNotFound (no double-teardown).
func TestConcurrentDeletes(t *testing.T) {
	m := newTestManager(newStubSessions(), newStubMsgs())
	s, _ := m.Create(context.Background(), "gw", "+62 812-0000-1111")
	var wg sync.WaitGroup
	wins := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wins <- m.Delete(context.Background(), s.ID)
		}()
	}
	wg.Wait()
	close(wins)
	var ok, notFound int
	for err := range wins {
		switch err {
		case nil:
			ok++
		case ErrNotFound:
			notFound++
		default:
			t.Fatalf("unexpected delete error: %v", err)
		}
	}
	if ok != 1 || notFound != 7 {
		t.Fatalf("wins = %d, losers = %d; want 1/7", ok, notFound)
	}
}

func TestActivateUnpaired(t *testing.T) {
	m := newTestManager(newStubSessions(), newStubMsgs())
	s, _ := m.Create(context.Background(), "gw", "+62 812-0000-1111")
	if err := s.Activate(context.Background()); err != ErrNotPaired {
		t.Fatalf("err = %v, want ErrNotPaired", err)
	}
}

func TestSetStatePersistsAndDedups(t *testing.T) {
	repo := newStubSessions()
	m := newTestManager(repo, newStubMsgs())
	s, _ := m.Create(context.Background(), "gw", "+62 812-0000-1111")
	s.setState(StateDisconnected)
	s.setState(StateDisconnected) // no-op: same state persists once
	if got := repo.lastStatus(s.ID); got != "disconnected" {
		t.Fatalf("persisted = %q, want disconnected", got)
	}
	if n := len(repo.statuses[s.ID]); n != 1 {
		t.Fatalf("persist calls = %d, want 1 (dedup)", n)
	}
}

func TestPermanentDownStopsSender(t *testing.T) {
	repo := newStubSessions()
	m := newTestManager(repo, newStubMsgs())
	s, _ := m.Create(context.Background(), "gw", "+62 812-0000-1111")
	s.mu.Lock()
	s.state = StateConnected
	s.sendQ = make(chan sendJob, 1) // pretend a sender is running (no worker needed)
	s.mu.Unlock()

	s.permanentDown("stream replaced")

	snap := s.Snapshot()
	if snap.State != StateLoggedOut {
		t.Fatalf("state = %s, want logged_out", snap.State)
	}
	s.mu.Lock()
	q := s.sendQ
	s.mu.Unlock()
	if q != nil {
		t.Fatal("sendQ still open after permanent disconnect")
	}
	act := repo.active[s.ID]
	if len(act) == 0 || act[len(act)-1] {
		t.Fatalf("active history = %v, want trailing false", act)
	}
}

// TestStartAllReconciliation: unpaired rows come back as new, crashed-pairing
// rows are reset (jid cleared), logged_out rows stay terminal, and nothing
// tries to connect without a container (Active only on safe rows).
func TestStartAllReconciliation(t *testing.T) {
	repo := newStubSessions(
		wasession.Session{ID: "a", Name: "a", Phone: "62811", JID: "", LastStatus: "new"},
		wasession.Session{ID: "b", Name: "b", Phone: "62812", JID: "62812@s.whatsapp.net", LastStatus: "pairing"},
		wasession.Session{ID: "c", Name: "c", Phone: "62813", JID: "62813@s.whatsapp.net", LastStatus: "logged_out"},
	)
	m := newTestManager(repo, newStubMsgs())
	if err := m.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := map[string]State{"a": StateNew, "b": StateNew, "c": StateLoggedOut}
	for id, st := range want {
		s, ok := m.Get(id)
		if !ok {
			t.Fatalf("session %s missing after StartAll", id)
		}
		snap := s.Snapshot()
		if snap.State != st {
			t.Errorf("session %s state = %s, want %s", id, snap.State, st)
		}
		if id == "b" && snap.JID != "" {
			t.Errorf("session b jid = %q, want cleared after crashed pairing", snap.JID)
		}
	}
	if got := repo.lastStatus("b"); got != "new" {
		t.Errorf("session b persisted status = %q, want new", got)
	}
	m.Close() // must join cleanly with zero goroutines spawned
}

// TestStartAllHydratesTheMasterFlagFromTheStoredRow proves the boot
// reconciliation carries wa_sessions.is_master into the in-memory
// ManagedSession. Losing this hydration fails OPEN: the flag would still be
// true in the database, but every restart would silently un-flag the
// session in memory and put the master back into automatic-selection
// rotation.
func TestStartAllHydratesTheMasterFlagFromTheStoredRow(t *testing.T) {
	repo := newStubSessions(
		wasession.Session{ID: "m", Name: "m", Phone: "62899", JID: "", LastStatus: "new", IsMaster: true},
	)
	m := newTestManager(repo, newStubMsgs())
	if err := m.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, ok := m.Get("m")
	if !ok {
		t.Fatal("session m missing after StartAll")
	}
	if snap := s.Snapshot(); !snap.IsMaster {
		t.Error("Snapshot().IsMaster = false, want true (StartAll must hydrate is_master from the stored row)")
	}
	m.Close()
}

func TestNewIDShapeAndUniqueness(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := NewID()
		if len(id) != 32 || strings.ToLower(id) != id {
			t.Fatalf("id %q not 32-char lowercase hex", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}
