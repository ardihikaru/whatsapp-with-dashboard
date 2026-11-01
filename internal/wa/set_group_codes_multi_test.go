package wa

import (
	"context"
	"reflect"
	"testing"
)

// TestSetGroupCodesNormalizesSortsAndDeduplicates proves the multi-group
// writer trims/lowercases every code, drops duplicates and "" entries, and
// sorts the result — so Snapshot.GroupCode (the first element) is
// deterministic regardless of input order.
func TestSetGroupCodesNormalizesSortsAndDeduplicates(t *testing.T) {
	repo := newStubSessions()
	m := newTestManager(repo, newStubMsgs())
	s := &ManagedSession{ID: "s1", name: "s1", Phone: "628120000",
		mgr: m, state: StateDisconnected, events: NewRing(ringSize)}
	m.sessions[s.ID] = s

	if err := s.SetGroupCodes(context.Background(), []string{" Beta ", "alpha", "beta", ""}); err != nil {
		t.Fatal(err)
	}
	snap := s.Snapshot()
	if want := []string{"alpha", "beta"}; !reflect.DeepEqual(snap.GroupCodes, want) {
		t.Fatalf("GroupCodes = %v, want %v", snap.GroupCodes, want)
	}
	if snap.GroupCode != "alpha" {
		t.Errorf("GroupCode = %q, want first element %q", snap.GroupCode, "alpha")
	}
	if calls := repo.groupSets["s1"]; len(calls) != 1 || !reflect.DeepEqual(calls[0], []string{"alpha", "beta"}) {
		t.Fatalf("repo.SetGroupCodes calls = %v, want one call with [alpha beta]", calls)
	}
}

// TestSetGroupCodesRejectsAnyInvalidCodeWithoutAPartialWrite proves that one
// bad code among several good ones fails the WHOLE call before anything is
// written — in-memory state, the repository call, and the previously-stored
// groups must all stay exactly as they were.
func TestSetGroupCodesRejectsAnyInvalidCodeWithoutAPartialWrite(t *testing.T) {
	repo := newStubSessions()
	m := newTestManager(repo, newStubMsgs())
	s := &ManagedSession{ID: "s1", name: "s1", Phone: "628120000",
		mgr: m, state: StateDisconnected, groupCodes: []string{"alpha"}, events: NewRing(ringSize)}
	m.sessions[s.ID] = s

	err := s.SetGroupCodes(context.Background(), []string{"alpha", "kemang selatan"})
	if _, ok := err.(*ValidationError); !ok {
		t.Fatalf("err = %#v, want *ValidationError", err)
	}
	if got := s.Snapshot().GroupCodes; !reflect.DeepEqual(got, []string{"alpha"}) {
		t.Fatalf("GroupCodes = %v after a rejected call, want unchanged [alpha]", got)
	}
	if calls := repo.groupSets["s1"]; len(calls) != 0 {
		t.Fatalf("repo.SetGroupCodes called %d time(s), want 0 — an invalid code must never reach the repository", len(calls))
	}
}

// TestSetGroupCodesWithAnEmptySliceClearsEveryGroup proves the "ungrouped"
// path round-trips through the multi-group writer the same way
// SetGroupCode("") always has.
func TestSetGroupCodesWithAnEmptySliceClearsEveryGroup(t *testing.T) {
	repo := newStubSessions()
	m := newTestManager(repo, newStubMsgs())
	s := &ManagedSession{ID: "s1", name: "s1", Phone: "628120000",
		mgr: m, state: StateDisconnected, groupCodes: []string{"alpha", "beta"}, events: NewRing(ringSize)}
	m.sessions[s.ID] = s

	if err := s.SetGroupCodes(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	snap := s.Snapshot()
	if len(snap.GroupCodes) != 0 || snap.GroupCode != "" {
		t.Fatalf("GroupCodes/GroupCode = %v/%q after clearing, want empty", snap.GroupCodes, snap.GroupCode)
	}
}

// TestEligibleForRouteMatchesAnyOfSeveralGroups proves a session carrying
// several groups is reachable by a Route naming ANY of them, not just the
// first (Snapshot.GroupCode) — the core semantics change 0015 makes over
// the single-group model.
func TestEligibleForRouteMatchesAnyOfSeveralGroups(t *testing.T) {
	snap := Snapshot{Connected: true, GroupCodes: []string{"alpha", "beta"}}
	for _, code := range []string{"alpha", "beta"} {
		if !EligibleForRoute(snap, Route{GroupCode: code}) {
			t.Errorf("EligibleForRoute with GroupCodes=[alpha beta], Route{GroupCode:%q} = false, want true", code)
		}
	}
	if EligibleForRoute(snap, Route{GroupCode: "gamma"}) {
		t.Error("a session in [alpha beta] must not be eligible for an unrelated group")
	}
	if EligibleForRoute(snap, Route{}) {
		t.Error("a session with any groups must be invisible to the ungrouped (\"\") route")
	}
}

// TestEligibleForRouteTreatsNoGroupsAsUngrouped proves an empty GroupCodes
// slice is exactly the ungrouped pool — matching Route{} and nothing named.
func TestEligibleForRouteTreatsNoGroupsAsUngrouped(t *testing.T) {
	snap := Snapshot{Connected: true}
	if !EligibleForRoute(snap, Route{}) {
		t.Error("a session with no groups must be eligible for the ungrouped route")
	}
	if EligibleForRoute(snap, Route{GroupCode: "alpha"}) {
		t.Error("a session with no groups must not be eligible for a named group")
	}
}
