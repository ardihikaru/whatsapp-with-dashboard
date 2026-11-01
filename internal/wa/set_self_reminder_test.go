package wa

import (
	"context"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/wasession"
)

// selfReminderTestManager builds a Manager with n plain sessions, none
// flagged self-reminder, for TestManager.SetSelfReminder's exclusivity
// tests below. Each id is ALSO seeded as a stub repository row (not just an
// in-memory ManagedSession) so repo.All can be asserted against too.
func selfReminderTestManager(t *testing.T, ids ...string) (*stubSessions, *Manager) {
	t.Helper()
	var rows []wasession.Session
	for _, id := range ids {
		rows = append(rows, wasession.Session{ID: id, Name: id, Phone: "628120000", LastStatus: "new"})
	}
	repo := newStubSessions(rows...)
	m := newTestManager(repo, newStubMsgs())
	for _, id := range ids {
		m.sessions[id] = &ManagedSession{ID: id, name: id, Phone: "628120000",
			mgr: m, state: StateDisconnected, events: NewRing(ringSize)}
	}
	return repo, m
}

// TestSetSelfReminderMovesTheFlagAndClearsEveryOtherSession proves the
// exclusivity contract end to end: setting the flag on "b" after it was on
// "a" leaves exactly one session flagged, in memory AND in the repository's
// view of the row.
func TestSetSelfReminderMovesTheFlagAndClearsEveryOtherSession(t *testing.T) {
	repo, m := selfReminderTestManager(t, "a", "b", "c")

	if err := m.SetSelfReminder(context.Background(), "a", true); err != nil {
		t.Fatal(err)
	}
	assertOnlySelfReminder(t, m, "a")

	if err := m.SetSelfReminder(context.Background(), "b", true); err != nil {
		t.Fatal(err)
	}
	assertOnlySelfReminder(t, m, "b")

	// The repository's own row-level view must also end with exactly one
	// row set — not just the in-memory mirror.
	rows, err := repo.All(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, row := range rows {
		if row.IsSelfReminder {
			n++
			if row.ID != "b" {
				t.Errorf("repo row %q flagged self-reminder, want only %q", row.ID, "b")
			}
		}
	}
	if n != 1 {
		t.Fatalf("repo rows flagged self-reminder = %d, want exactly 1", n)
	}
}

// TestSetSelfReminderFalseOnlyClearsTheNamedSession proves on=false does not
// disturb a DIFFERENT session already carrying the flag — only the named id
// is touched.
func TestSetSelfReminderFalseOnlyClearsTheNamedSession(t *testing.T) {
	_, m := selfReminderTestManager(t, "a", "b")
	if err := m.SetSelfReminder(context.Background(), "a", true); err != nil {
		t.Fatal(err)
	}
	if err := m.SetSelfReminder(context.Background(), "b", false); err != nil {
		t.Fatal(err)
	}
	assertOnlySelfReminder(t, m, "a")
}

func assertOnlySelfReminder(t *testing.T, m *Manager, want string) {
	t.Helper()
	for _, snap := range m.Snapshots() {
		got := snap.SelfReminder
		wantFlag := snap.ID == want
		if got != wantFlag {
			t.Errorf("session %s: SelfReminder = %v, want %v", snap.ID, got, wantFlag)
		}
	}
}
