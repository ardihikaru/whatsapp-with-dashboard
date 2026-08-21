package wa

import (
	"context"
	"sort"
	"time"
)

// SelectConnected chooses the best live session for automatic routing:
// lowest queue length, then oldest last selection, then oldest created row,
// then session id. It records the selection before returning so concurrent
// callers distribute ties predictably within this single-process app.
func (m *Manager) SelectConnected(ctx context.Context) (*ManagedSession, Snapshot, bool) {
	m.mu.RLock()
	all := make([]*ManagedSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.mu.RUnlock()
	type candidate struct {
		session *ManagedSession
		snap    Snapshot
	}
	candidates := make([]candidate, 0, len(all))
	for _, s := range all {
		snap := s.Snapshot()
		if snap.Connected {
			candidates = append(candidates, candidate{session: s, snap: snap})
		}
	}
	if len(candidates) == 0 {
		return nil, Snapshot{}, false
	}
	best, _ := SelectSnapshotForSend(func() []Snapshot {
		out := make([]Snapshot, 0, len(candidates))
		for _, c := range candidates {
			out = append(out, c.snap)
		}
		return out
	}())
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].snap.ID < candidates[j].snap.ID })
	var chosen *ManagedSession
	for _, c := range candidates {
		if c.snap.ID == best.ID {
			chosen = c.session
			break
		}
	}
	if chosen == nil {
		return nil, Snapshot{}, false
	}
	now := time.Now().UTC()
	chosen.mu.Lock()
	chosen.LastSelectedAt = &now
	chosen.mu.Unlock()
	_ = m.repo.MarkSelected(ctx, chosen.ID, now)
	return chosen, chosen.Snapshot(), true
}

func SelectSnapshotForSend(snaps []Snapshot) (Snapshot, bool) {
	candidates := make([]Snapshot, 0, len(snaps))
	for _, snap := range snaps {
		if snap.Connected {
			candidates = append(candidates, snap)
		}
	}
	if len(candidates) == 0 {
		return Snapshot{}, false
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.QueueLen != b.QueueLen {
			return a.QueueLen < b.QueueLen
		}
		if !sameSelectionTime(a.LastSelectedAt, b.LastSelectedAt) {
			if a.LastSelectedAt == nil {
				return true
			}
			if b.LastSelectedAt == nil {
				return false
			}
			return a.LastSelectedAt.Before(*b.LastSelectedAt)
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.ID < b.ID
	})
	return candidates[0], true
}

func sameSelectionTime(a, b *time.Time) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return a.Equal(*b)
	}
}
