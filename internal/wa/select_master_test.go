package wa

import (
	"testing"
	"time"
)

// alwaysLiveBackend makes every registered session report Connected without a
// real statusPump — Snapshot gates Connected on backend.Live (session.go:157),
// so the stock fakeBackend (Live == false) would make every case below "no
// master found" for the wrong reason.
type alwaysLiveBackend struct{ *fakeBackend }

func (alwaysLiveBackend) Live(*ManagedSession) bool { return true }

// masterTestManager builds a Manager holding the given sessions. specs are
// {id, connected, isMaster} triples kept positional and tiny because the whole
// point of each case is which of the three flags differs.
func masterTestManager(t *testing.T, specs ...struct {
	id                  string
	connected, isMaster bool
}) *Manager {
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
			mgr: m, state: state, isMaster: sp.isMaster, events: NewRing(ringSize)}
	}
	return m
}

type masterSpec = struct {
	id                  string
	connected, isMaster bool
}

func TestSelectMaster(t *testing.T) {
	tests := []struct {
		name  string
		specs []masterSpec
		want  string // "" == expect no route
	}{{
		// The whole point of the mode: three ordinary sessions are live and
		// idle, and the send still leaves from the master.
		name: "picks the master and ignores every non-master",
		specs: []masterSpec{
			{"a", true, false}, {"b", true, false}, {"m", true, true},
		},
		want: "m",
	}, {
		// The 2026-09-11 production state exactly: master offline, three
		// others live. SelectConnected would happily route; this must NOT.
		name: "no fallback when the master is disconnected",
		specs: []masterSpec{
			{"a", true, false}, {"b", true, false}, {"m", false, true},
		},
		want: "",
	}, {
		name:  "no master configured at all is not a route",
		specs: []masterSpec{{"a", true, false}},
		want:  "",
	}, {
		// Nothing in the schema enforces a single master, so ranking must be
		// deterministic rather than Go map order.
		name: "several connected masters rank deterministically by id",
		specs: []masterSpec{
			{"m2", true, true}, {"m1", true, true},
		},
		want: "m1",
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := masterTestManager(t, tc.specs...)
			s, snap, ok := m.SelectMaster(t.Context())
			if tc.want == "" {
				if ok {
					t.Fatalf("SelectMaster: ok = true (picked %q), want no route", snap.ID)
				}
				return
			}
			if !ok {
				t.Fatalf("SelectMaster: ok = false, want %q", tc.want)
			}
			if s.ID != tc.want || snap.ID != tc.want {
				t.Fatalf("SelectMaster = %q/%q, want %q", s.ID, snap.ID, tc.want)
			}
			if !snap.IsMaster {
				t.Fatalf("SelectMaster returned a non-master snapshot: %+v", snap)
			}
		})
	}
}

// TestSelectMasterIsNotAllowMaster pins the distinction the two flags are most
// easily confused over: allow_master WIDENS SelectConnected's candidate set, so
// with it set an ordinary session may still win. Only SelectMaster is exclusive.
func TestSelectMasterIsNotAllowMaster(t *testing.T) {
	specs := []masterSpec{{"a", true, false}, {"m", true, true}}

	m := masterTestManager(t, specs...)
	if _, snap, ok := m.SelectConnected(t.Context(), true); !ok || snap.ID != "a" {
		t.Fatalf("SelectConnected(allowMaster=true) = %q/%v, want the ordinary session %q "+
			"(allow_master must widen, not pin)", snap.ID, ok, "a")
	}

	m = masterTestManager(t, specs...)
	if _, snap, ok := m.SelectMaster(t.Context()); !ok || snap.ID != "m" {
		t.Fatalf("SelectMaster = %q/%v, want %q", snap.ID, ok, "m")
	}
}

// TestSelectMasterRotatesBetweenSeveralConnectedMasters pins the one
// deliberate behaviour change in this layer: SelectMaster used to rank on
// (QueueLen, ID) only, ignoring LastSelectedAt/CreatedAt entirely, even
// though its own doc comment already claimed "ranking is the same as
// SelectConnected's" (false until this change unified both onto
// SelectSnapshotForRoute's four-key ranking). Two connected masters with
// equal QueueLen: the LOWER-ID one was selected recently, the HIGHER-ID one
// was never selected — "never selected" must win the tie, so m2 (not the
// old (QueueLen, ID) winner m1) is picked.
func TestSelectMasterRotatesBetweenSeveralConnectedMasters(t *testing.T) {
	m := masterTestManager(t, masterSpec{"m1", true, true}, masterSpec{"m2", true, true})
	recently := time.Now().UTC().Add(-time.Minute)
	m.sessions["m1"].LastSelectedAt = &recently
	// m2.LastSelectedAt stays nil: never selected.

	_, snap, ok := m.SelectMaster(t.Context())
	if !ok {
		t.Fatal("SelectMaster: ok = false, want a route")
	}
	if snap.ID != "m2" {
		t.Fatalf("SelectMaster = %q, want m2 (never-selected must win the tie under the unified four-key ranking; "+
			"m1 would win under the old (QueueLen, ID)-only ranking)", snap.ID)
	}
}
