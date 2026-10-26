package wa

import (
	"context"
	"testing"
)

// TestSetGroupCodeRejectsAnInvalidCodeWithoutTouchingState proves
// SetGroupCode validates BEFORE writing anything — same contract SetMaster's
// doc comment and manager_crud.go's session creation already promise: an
// invalid code must leave the in-memory groupCode unchanged and never reach
// the repository at all.
func TestSetGroupCodeRejectsAnInvalidCodeWithoutTouchingState(t *testing.T) {
	repo := newStubSessions()
	m := newTestManager(repo, newStubMsgs())
	s := &ManagedSession{ID: "s1", name: "s1", Phone: "628120000",
		mgr: m, state: StateDisconnected, groupCode: "alpha", events: NewRing(ringSize)}
	m.sessions[s.ID] = s

	err := s.SetGroupCode(context.Background(), "kemang selatan")
	if err == nil {
		t.Fatal("SetGroupCode: err = nil, want a *ValidationError for an invalid code")
	}
	if _, ok := err.(*ValidationError); !ok {
		t.Fatalf("SetGroupCode err = %T, want *ValidationError", err)
	}
	if got := s.Snapshot().GroupCode; got != "alpha" {
		t.Fatalf("Snapshot().GroupCode = %q after a rejected SetGroupCode, want unchanged %q", got, "alpha")
	}
	if calls := repo.groups[s.ID]; len(calls) != 0 {
		t.Fatalf("repo.SetGroupCode called %d time(s), want 0 — an invalid code must never reach the repository", len(calls))
	}
}
