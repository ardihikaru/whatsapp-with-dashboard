package wa

import (
	"context"
	"strings"
	"testing"
)

func newNamedSession(t *testing.T) (*stubSessions, *ManagedSession) {
	t.Helper()
	repo := newStubSessions()
	m := newTestManager(repo, newStubMsgs())
	s := &ManagedSession{ID: "s1", name: "old", Phone: "628120000",
		mgr: m, state: StateDisconnected, events: NewRing(ringSize)}
	m.sessions[s.ID] = s
	return repo, s
}

func TestSetNameUpdatesTheSnapshotAndPersists(t *testing.T) {
	repo, s := newNamedSession(t)
	if err := s.SetName(context.Background(), "  New Name  "); err != nil {
		t.Fatal(err)
	}
	if got := s.Snapshot().Name; got != "New Name" {
		t.Errorf("Snapshot().Name = %q, want trimmed %q", got, "New Name")
	}
	if got := s.Name(); got != "New Name" {
		t.Errorf("Name() = %q, want %q", got, "New Name")
	}
	if calls := repo.names["s1"]; len(calls) != 1 || calls[0] != "New Name" {
		t.Errorf("repo.SetName calls = %v, want [New Name]", calls)
	}
}

func TestSetNameRejectsEmptyAndTooLongWithoutTouchingState(t *testing.T) {
	repo, s := newNamedSession(t)
	for label, in := range map[string]string{"empty": "   ", "too long": strings.Repeat("x", 191)} {
		err := s.SetName(context.Background(), in)
		verr, ok := err.(*ValidationError)
		if !ok || verr.Field != "name" {
			t.Fatalf("%s: err = %#v, want *ValidationError{Field:name}", label, err)
		}
	}
	if got := s.Snapshot().Name; got != "old" {
		t.Errorf("Name = %q after rejected renames, want unchanged", got)
	}
	if calls := repo.names["s1"]; len(calls) != 0 {
		t.Errorf("repo.SetName called %d time(s) for invalid input, want 0", len(calls))
	}
	if err := s.SetName(context.Background(), strings.Repeat("x", 190)); err != nil {
		t.Errorf("190-char name rejected: %v", err)
	}
}
