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
			got, ok := SelectSnapshotForSend(tt.in)
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
	if _, ok := SelectSnapshotForSend([]Snapshot{{ID: "a", Connected: false}}); ok {
		t.Fatal("expected no candidate")
	}
}
