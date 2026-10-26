package wa

import "testing"

// routeSpec is a {id, connected, isMaster, groupCode} tuple for
// routeTestManager below — masterSpec (select_master_test.go) has no group
// field, so this is its own tiny fixture type rather than widening that one
// and touching its existing callers.
type routeSpec struct {
	id                  string
	connected, isMaster bool
	groupCode           string
}

// routeTestManager builds a Manager holding the given sessions, reusing
// alwaysLiveBackend (select_master_test.go) for the same reason that file
// documents: Snapshot gates Connected on backend.Live, so the stock
// fakeBackend would read every case here as "disconnected" regardless of
// the `connected` spec.
func routeTestManager(t *testing.T, specs ...routeSpec) *Manager {
	t.Helper()
	m := NewManager(t.Context(), alwaysLiveBackend{&fakeBackend{name: "fake"}},
		newStubSessions(), newStubMsgs(), nil,
		Config{SendRate: 1, SendBurst: 3, ReconcilePar: 2})
	for _, sp := range specs {
		state := StateDisconnected
		if sp.connected {
			state = StateConnected
		}
		m.sessions[sp.id] = &ManagedSession{ID: sp.id, name: sp.id, Phone: "628120000",
			mgr: m, state: state, isMaster: sp.isMaster, groupCode: sp.groupCode, events: NewRing(ringSize)}
	}
	return m
}

// TestSelectForRouteHonoursAGroupScopeEndToEnd drives Manager.SelectForRoute
// through a real Manager (not the pure SelectSnapshotForRoute helper) for
// every row of the layer-1 plan's semantics table, proving the map-copy,
// filter, rank and selection-recording plumbing in SelectForRoute composes
// correctly with EligibleForRoute end to end.
func TestSelectForRouteHonoursAGroupScopeEndToEnd(t *testing.T) {
	tests := []struct {
		name  string
		specs []routeSpec
		rt    Route
		want  string // "" == expect no route
	}{
		{
			name: "nothing: default pool picks the ungrouped non-master",
			specs: []routeSpec{
				{id: "u", connected: true},
				{id: "m", connected: true, isMaster: true},
				{id: "g", connected: true, groupCode: "alpha"},
			},
			rt:   Route{},
			want: "u",
		},
		{
			name: "allow_master:true widens the ungrouped pool to include its master",
			specs: []routeSpec{
				{id: "m", connected: true, isMaster: true},
				{id: "g", connected: true, groupCode: "alpha"},
			},
			rt:   Route{AllowMaster: true},
			want: "m",
		},
		{
			name: "master_only:true picks the ungrouped master only",
			specs: []routeSpec{
				{id: "u", connected: true},
				{id: "m", connected: true, isMaster: true},
				{id: "gm", connected: true, isMaster: true, groupCode: "alpha"},
			},
			rt:   Route{MasterOnly: true},
			want: "m",
		},
		{
			name: `group_code:"alpha" picks alpha's non-master`,
			specs: []routeSpec{
				{id: "u", connected: true},
				{id: "g", connected: true, groupCode: "alpha"},
				{id: "gm", connected: true, isMaster: true, groupCode: "alpha"},
			},
			rt:   Route{GroupCode: "alpha"},
			want: "g",
		},
		{
			name: `group_code:"alpha" + allow_master:true widens to include alpha's master too`,
			specs: []routeSpec{
				{id: "gm", connected: true, isMaster: true, groupCode: "alpha"},
			},
			rt:   Route{GroupCode: "alpha", AllowMaster: true},
			want: "gm",
		},
		{
			name: `group_code:"alpha" + master_only:true picks alpha's master, never the ungrouped one`,
			specs: []routeSpec{
				{id: "m", connected: true, isMaster: true},
				{id: "gm", connected: true, isMaster: true, groupCode: "alpha"},
			},
			rt:   Route{GroupCode: "alpha", MasterOnly: true},
			want: "gm",
		},
		{
			name: "an empty group has no route and never falls back to the ungrouped pool",
			specs: []routeSpec{
				{id: "u", connected: true},
			},
			rt:   Route{GroupCode: "alpha"},
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := routeTestManager(t, tc.specs...)
			s, snap, ok := m.SelectForRoute(t.Context(), tc.rt)
			if tc.want == "" {
				if ok {
					t.Fatalf("SelectForRoute: ok = true (picked %q), want no route", snap.ID)
				}
				return
			}
			if !ok {
				t.Fatalf("SelectForRoute: ok = false, want %q", tc.want)
			}
			if s.ID != tc.want || snap.ID != tc.want {
				t.Fatalf("SelectForRoute = %q/%q, want %q", s.ID, snap.ID, tc.want)
			}
		})
	}
}
