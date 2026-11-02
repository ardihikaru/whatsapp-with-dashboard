package wa

import (
	"context"
	"testing"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/wasession"
)

// TestSetStateEmitsAStateChangeOnLoggedOut proves setState publishes on
// Manager.StateChanges() for every committed transition, including the
// logged_out one internal/sessionalert's collector cares about —
// red without the publishStateChange call session.go's setState makes.
func TestSetStateEmitsAStateChangeOnLoggedOut(t *testing.T) {
	m := newTestManager(newStubSessions(), newStubMsgs())
	s := &ManagedSession{ID: "s1", name: "Gateway", Phone: "628120000",
		mgr: m, state: StateDisconnected, events: NewRing(ringSize)}
	m.sessions[s.ID] = s

	s.setState(StateLoggedOut)

	select {
	case c := <-m.StateChanges():
		if c.ID != "s1" || c.From != StateDisconnected || c.To != StateLoggedOut {
			t.Fatalf("StateChange = %+v, want {ID:s1 From:disconnected To:logged_out ...}", c)
		}
	case <-time.After(time.Second):
		t.Fatal("no StateChange published within 1s")
	}
}

// TestSetStateEmitsNothingForANoOpTransition proves the "old == st" early
// return in setState also means NO StateChange — the collector must never
// see a same-to-same transition.
func TestSetStateEmitsNothingForANoOpTransition(t *testing.T) {
	m := newTestManager(newStubSessions(), newStubMsgs())
	s := &ManagedSession{ID: "s1", name: "Gateway", Phone: "628120000",
		mgr: m, state: StateConnected, events: NewRing(ringSize)}
	m.sessions[s.ID] = s

	s.setState(StateConnected) // same state

	select {
	case c := <-m.StateChanges():
		t.Fatalf("unexpected StateChange published for a no-op transition: %+v", c)
	case <-time.After(50 * time.Millisecond):
		// expected: nothing published
	}
}

// TestBootRestoreDoesNotPublishAStateChange proves StartAll's direct
// s.state assignment (manager_boot.go) — as opposed to calling setState —
// never fires an alert on every restart. This is the exact guarantee
// CLAUDE.md's "declaring logged_out after 3 consecutive polls" incident
// needs: a restored logged_out row must not re-alert just because the
// process started.
func TestBootRestoreDoesNotPublishAStateChange(t *testing.T) {
	repo := newStubSessions(
		wasession.Session{ID: "g", Name: "g", Phone: "62899", JID: "6289900000:1@s.whatsapp.net",
			LastStatus: string(StateLoggedOut), Active: false},
	)
	m := newTestManager(repo, newStubMsgs())
	if err := m.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	select {
	case c := <-m.StateChanges():
		t.Fatalf("StartAll published a StateChange despite restoring state directly: %+v", c)
	case <-time.After(100 * time.Millisecond):
		// expected: nothing published
	}
}
