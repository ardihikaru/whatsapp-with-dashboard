package wa

import (
	"testing"
	"time"
)

// TestEligibleForRouteIsTheWholeScopeRule pins every row of the layer-1
// plan's semantics table (nothing / allow_master / master_only, each with
// and without a group_code) plus disconnected always losing regardless of
// scope — this predicate is the ONLY filter every automatic pick in this
// package now funnels through, so a gap here is a gap everywhere.
func TestEligibleForRouteIsTheWholeScopeRule(t *testing.T) {
	tests := []struct {
		name string
		snap Snapshot
		rt   Route
		want bool
	}{
		{
			name: "nothing: ungrouped non-master matches the default pool",
			snap: Snapshot{Connected: true, GroupCode: "", IsMaster: false},
			rt:   Route{},
			want: true,
		},
		{
			name: "nothing: ungrouped master is excluded from the default pool",
			snap: Snapshot{Connected: true, GroupCode: "", IsMaster: true},
			rt:   Route{},
			want: false,
		},
		{
			name: "nothing: a grouped session is invisible to the default pool",
			snap: Snapshot{Connected: true, GroupCode: "alpha", IsMaster: false},
			rt:   Route{},
			want: false,
		},
		{
			name: "allow_master:true widens the ungrouped pool to include its master",
			snap: Snapshot{Connected: true, GroupCode: "", IsMaster: true},
			rt:   Route{AllowMaster: true},
			want: true,
		},
		{
			name: "allow_master:true still excludes a grouped session",
			snap: Snapshot{Connected: true, GroupCode: "alpha", IsMaster: false},
			rt:   Route{AllowMaster: true},
			want: false,
		},
		{
			name: "master_only:true picks the ungrouped master",
			snap: Snapshot{Connected: true, GroupCode: "", IsMaster: true},
			rt:   Route{MasterOnly: true},
			want: true,
		},
		{
			name: "master_only:true excludes an ungrouped non-master",
			snap: Snapshot{Connected: true, GroupCode: "", IsMaster: false},
			rt:   Route{MasterOnly: true},
			want: false,
		},
		{
			name: "master_only:true never reaches into a group",
			snap: Snapshot{Connected: true, GroupCode: "alpha", IsMaster: true},
			rt:   Route{MasterOnly: true},
			want: false,
		},
		{
			name: `group_code:"X" matches a non-master session in that group`,
			snap: Snapshot{Connected: true, GroupCode: "alpha", IsMaster: false},
			rt:   Route{GroupCode: "alpha"},
			want: true,
		},
		{
			name: `group_code:"X" excludes a master in that same group`,
			snap: Snapshot{Connected: true, GroupCode: "alpha", IsMaster: true},
			rt:   Route{GroupCode: "alpha"},
			want: false,
		},
		{
			name: `group_code:"X" excludes a different group`,
			snap: Snapshot{Connected: true, GroupCode: "beta", IsMaster: false},
			rt:   Route{GroupCode: "alpha"},
			want: false,
		},
		{
			name: `group_code:"X" excludes the ungrouped pool`,
			snap: Snapshot{Connected: true, GroupCode: "", IsMaster: false},
			rt:   Route{GroupCode: "alpha"},
			want: false,
		},
		{
			name: `group_code:"X" + allow_master:true widens to that group's master`,
			snap: Snapshot{Connected: true, GroupCode: "alpha", IsMaster: true},
			rt:   Route{GroupCode: "alpha", AllowMaster: true},
			want: true,
		},
		{
			name: `group_code:"X" + master_only:true picks that group's master only`,
			snap: Snapshot{Connected: true, GroupCode: "alpha", IsMaster: true},
			rt:   Route{GroupCode: "alpha", MasterOnly: true},
			want: true,
		},
		{
			name: `group_code:"X" + master_only:true excludes that group's non-master`,
			snap: Snapshot{Connected: true, GroupCode: "alpha", IsMaster: false},
			rt:   Route{GroupCode: "alpha", MasterOnly: true},
			want: false,
		},
		{
			name: "disconnected is always false, regardless of scope",
			snap: Snapshot{Connected: false, GroupCode: "", IsMaster: false},
			rt:   Route{AllowMaster: true},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EligibleForRoute(tt.snap, tt.rt); got != tt.want {
				t.Fatalf("EligibleForRoute(%+v, %+v) = %v, want %v", tt.snap, tt.rt, got, tt.want)
			}
		})
	}
}

// groupedVsUngroupedFixture returns one grouped and one ungrouped connected
// snapshot where the grouped one wins on EVERY ranking key (QueueLen 0, nil
// LastSelectedAt, earliest CreatedAt, lowest ID) — so if group exclusion
// were missing, plain ranking would pick it regardless of which pool was
// asked for.
func groupedVsUngroupedFixture() []Snapshot {
	ungroupedSelected := time.Date(2026, 7, 6, 9, 0, 0, 0, time.UTC)
	return []Snapshot{
		{
			ID:        "a-grouped",
			Connected: true,
			GroupCode: "alpha",
			QueueLen:  0,
			CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			// LastSelectedAt nil: "never selected" beats any selected time.
		},
		{
			ID:             "z-ungrouped",
			Connected:      true,
			GroupCode:      "",
			QueueLen:       1,
			CreatedAt:      time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
			LastSelectedAt: &ungroupedSelected,
		},
	}
}

// TestAGroupCodedSessionIsInvisibleToTheDefaultPool proves EXCLUSION, not
// ranking: the grouped session wins on every ranking key, yet the default
// (ungrouped) route must still pick the ungrouped session.
func TestAGroupCodedSessionIsInvisibleToTheDefaultPool(t *testing.T) {
	got, ok := SelectSnapshotForRoute(groupedVsUngroupedFixture(), Route{})
	if !ok {
		t.Fatal("expected a candidate")
	}
	if got.ID != "z-ungrouped" {
		t.Fatalf("got %s, want z-ungrouped (the grouped session must be invisible to the default pool)", got.ID)
	}
}

// TestAnUngroupedSessionIsInvisibleToAGroupedRoute is the converse of the
// test above — catches a `GroupCode == "" matches anything` leniency bug in
// EligibleForRoute (e.g. `rt.GroupCode == "" || snap.GroupCode == rt.GroupCode`).
func TestAnUngroupedSessionIsInvisibleToAGroupedRoute(t *testing.T) {
	got, ok := SelectSnapshotForRoute(groupedVsUngroupedFixture(), Route{GroupCode: "alpha"})
	if !ok {
		t.Fatal("expected a candidate")
	}
	if got.ID != "a-grouped" {
		t.Fatalf("got %s, want a-grouped (the ungrouped session must be invisible to a grouped route)", got.ID)
	}
}

// TestAGroupedRouteNeverFallsBackToTheUngroupedPool proves there is no
// fallback: group "alpha" is present but disconnected, while the ungrouped
// pool is live and idle, yet Route{GroupCode: "alpha"} must still find
// nothing — exactly as master_only has no fallback today.
func TestAGroupedRouteNeverFallsBackToTheUngroupedPool(t *testing.T) {
	snaps := []Snapshot{
		{ID: "alpha-down", Connected: false, GroupCode: "alpha"},
		{ID: "ungrouped-live", Connected: true, GroupCode: ""},
	}
	if _, ok := SelectSnapshotForRoute(snaps, Route{GroupCode: "alpha"}); ok {
		t.Fatal("expected no candidate: alpha's only session is disconnected and there is no fallback to the ungrouped pool")
	}
}

// TestMasterOnlyWithinAGroupPicksThatGroupsMasterAndNeverAnother is the
// leak this feature closes: before group scoping, a global master_only pick
// (Route{MasterOnly: true}) could only ever mean "the one master flagged in
// the whole fleet" — now that masters can exist inside named groups too, a
// global master_only pick must still resolve to the UNGROUPED master, never
// reach into a group, while a group-scoped master_only pick resolves within
// that group.
func TestMasterOnlyWithinAGroupPicksThatGroupsMasterAndNeverAnother(t *testing.T) {
	snaps := []Snapshot{
		{ID: "alpha-master", Connected: true, GroupCode: "alpha", IsMaster: true},
		{ID: "ungrouped-master", Connected: true, GroupCode: "", IsMaster: true},
	}
	got, ok := SelectSnapshotForRoute(snaps, Route{GroupCode: "alpha", MasterOnly: true})
	if !ok || got.ID != "alpha-master" {
		t.Fatalf("Route{GroupCode:alpha,MasterOnly:true} = %q/%v, want alpha-master", got.ID, ok)
	}

	got, ok = SelectSnapshotForRoute(snaps, Route{MasterOnly: true})
	if !ok || got.ID != "ungrouped-master" {
		t.Fatalf("Route{MasterOnly:true} = %q/%v, want ungrouped-master (a global master_only pick must never reach into a group)", got.ID, ok)
	}
}

// TestAGroupMasterIsExcludedUnlessAllowMaster proves the two scopes compose:
// group "alpha" holds only a connected master. A plain group route must find
// nothing (masters are excluded within a group exactly as in the ungrouped
// pool); adding allow_master must then find it.
func TestAGroupMasterIsExcludedUnlessAllowMaster(t *testing.T) {
	snaps := []Snapshot{
		{ID: "alpha-master", Connected: true, GroupCode: "alpha", IsMaster: true},
	}
	if _, ok := SelectSnapshotForRoute(snaps, Route{GroupCode: "alpha"}); ok {
		t.Fatal("expected no candidate: alpha's only session is a master and allow_master is not set")
	}
	got, ok := SelectSnapshotForRoute(snaps, Route{GroupCode: "alpha", AllowMaster: true})
	if !ok || got.ID != "alpha-master" {
		t.Fatalf("Route{GroupCode:alpha,AllowMaster:true} = %q/%v, want alpha-master", got.ID, ok)
	}
}
