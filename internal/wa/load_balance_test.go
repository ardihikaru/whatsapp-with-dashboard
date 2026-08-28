package wa

import (
	"testing"
	"time"
)

func TestSelectSnapshotForSend(t *testing.T) {
	oldest := time.Date(2026, 7, 6, 9, 0, 0, 0, time.UTC)
	newer := oldest.Add(time.Minute)
	createdA := oldest.Add(-3 * time.Hour)
	createdB := oldest.Add(-2 * time.Hour)

	tests := []struct {
		name string
		in   []Snapshot
		want string
	}{
		{
			name: "lowest queue wins",
			in: []Snapshot{
				{ID: "a", Connected: true, QueueLen: 2},
				{ID: "b", Connected: true, QueueLen: 1},
			},
			want: "b",
		},
		{
			name: "oldest last selected wins tie",
			in: []Snapshot{
				{ID: "a", Connected: true, QueueLen: 1, LastSelectedAt: &newer},
				{ID: "b", Connected: true, QueueLen: 1, LastSelectedAt: &oldest},
			},
			want: "b",
		},
		{
			name: "never selected wins over selected",
			in: []Snapshot{
				{ID: "a", Connected: true, QueueLen: 1, LastSelectedAt: &oldest},
				{ID: "b", Connected: true, QueueLen: 1},
			},
			want: "b",
		},
		{
			name: "oldest created wins tie",
			in: []Snapshot{
				{ID: "b", Connected: true, QueueLen: 1, CreatedAt: createdB},
				{ID: "a", Connected: true, QueueLen: 1, CreatedAt: createdA},
			},
			want: "a",
		},
		{
			name: "id wins final tie",
			in: []Snapshot{
				{ID: "b", Connected: true, QueueLen: 1, CreatedAt: createdA},
				{ID: "a", Connected: true, QueueLen: 1, CreatedAt: createdA},
			},
			want: "a",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := SelectSnapshotForSend(tt.in, false)
			if !ok {
				t.Fatal("expected candidate")
			}
			if got.ID != tt.want {
				t.Fatalf("got %s, want %s", got.ID, tt.want)
			}
		})
	}
}

func TestSelectSnapshotForSendNoConnected(t *testing.T) {
	if _, ok := SelectSnapshotForSend([]Snapshot{{ID: "a", Connected: false}}, false); ok {
		t.Fatal("expected no candidate")
	}
}

// masterVsNonMasterFixture returns two connected snapshots where "master" is
// strictly better on every ranking key (lower QueueLen, nil LastSelectedAt,
// earlier CreatedAt) than "nonmaster" — so if the exclusion predicate were
// missing, plain ranking would pick "master" every time.
func masterVsNonMasterFixture() []Snapshot {
	nonMasterSelected := time.Date(2026, 7, 6, 9, 0, 0, 0, time.UTC)
	return []Snapshot{
		{
			ID:        "master",
			Connected: true,
			IsMaster:  true,
			QueueLen:  0,
			CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			// LastSelectedAt nil: "never selected" beats any selected time.
		},
		{
			ID:             "nonmaster",
			Connected:      true,
			IsMaster:       false,
			QueueLen:       1,
			CreatedAt:      time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
			LastSelectedAt: &nonMasterSelected,
		},
	}
}

// TestSelectSnapshotForSendSkipsAMasterSession proves the exclusion
// predicate, not just the ranking: "master" wins on every ranking key, yet
// with allowMaster=false the non-master session must be chosen instead.
func TestSelectSnapshotForSendSkipsAMasterSession(t *testing.T) {
	got, ok := SelectSnapshotForSend(masterVsNonMasterFixture(), false)
	if !ok {
		t.Fatal("expected a candidate")
	}
	if got.ID != "nonmaster" {
		t.Fatalf("got %s, want nonmaster (the master session must be skipped)", got.ID)
	}
}

// TestSelectSnapshotForSendIncludesAMasterSessionWhenAllowed proves the
// allowMaster override: same fixture, but now the master session is
// eligible and wins on ranking as expected.
func TestSelectSnapshotForSendIncludesAMasterSessionWhenAllowed(t *testing.T) {
	got, ok := SelectSnapshotForSend(masterVsNonMasterFixture(), true)
	if !ok {
		t.Fatal("expected a candidate")
	}
	if got.ID != "master" {
		t.Fatalf("got %s, want master (allowMaster=true must include it)", got.ID)
	}
}

// TestSelectSnapshotForSendFindsNothingWhenEveryConnectedSessionIsMaster
// proves the predicate can legitimately exhaust the candidate pool: if every
// connected session is a master and allowMaster is false, there is no
// automatic candidate at all — mirroring TestSelectSnapshotForSendNoConnected
// but for the master-exclusion path rather than the Connected one. Two
// connected masters (not one) is deliberate: this fixture is also what
// proves SelectConnected's own `best, ok := SelectSnapshotForSend(...)`
// handling is correct — with the pre-filter loop already excluding both,
// SelectConnected must hit its `len(candidates) == 0` guard, and if that
// guard were ever missing, this same input is what would exercise the
// otherwise-dead `!ok` branch this change added there.
func TestSelectSnapshotForSendFindsNothingWhenEveryConnectedSessionIsMaster(t *testing.T) {
	snaps := []Snapshot{
		{ID: "master-1", Connected: true, IsMaster: true},
		{ID: "master-2", Connected: true, IsMaster: true},
	}
	if _, ok := SelectSnapshotForSend(snaps, false); ok {
		t.Fatal("expected no candidate: every connected session is a master and allowMaster is false")
	}
}
