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
//
// allowMaster gates whether a session flagged is_master (see
// ManagedSession.isMaster) is eligible for this AUTOMATIC pick — it is
// always still reachable by naming its session id explicitly. Callers must
// pass an explicit answer (no default): false unless the caller has an
// opt-in signal (e.g. a request's allow_master flag) to route through one.
func (m *Manager) SelectConnected(ctx context.Context, allowMaster bool) (*ManagedSession, Snapshot, bool) {
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
		if snap.Connected && (allowMaster || !snap.IsMaster) {
			candidates = append(candidates, candidate{session: s, snap: snap})
		}
	}
	if len(candidates) == 0 {
		return nil, Snapshot{}, false
	}
	best, ok := SelectSnapshotForSend(func() []Snapshot {
		out := make([]Snapshot, 0, len(candidates))
		for _, c := range candidates {
			out = append(out, c.snap)
		}
		return out
	}(), allowMaster)
	if !ok {
		// candidates is non-empty (checked above) but SelectSnapshotForSend's
		// own Connected/master filter found nothing — every entry in
		// `candidates` was already required to be Connected and (allowMaster
		// || !IsMaster) by the pre-filter loop above, so this should be
		// unreachable in practice; treat it the same as "nothing to select"
		// rather than falling through with a zero-value best.
		return nil, Snapshot{}, false
	}
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

// SelectMaster chooses the connected MASTER session, and nothing else. It is
// the routing mode for traffic that must always leave from one known handset —
// ops alerts, where "which number paged me at 3am" is part of reading the page.
//
// This is deliberately NOT expressible through SelectConnected's allowMaster
// flag, and the difference is easy to misread: allowMaster WIDENS the candidate
// set (allowMaster || !snap.IsMaster) so a master becomes eligible ALONGSIDE
// every ordinary session. There is no value of it that means "only the master".
//
// ⚠ THIS MODE HAS NO FALLBACK, BY DESIGN AND BY REQUEST. If the master handset
// is disconnected this returns false and the caller answers 409 — it does not
// quietly route through another session, because doing so would defeat the only
// reason to ask for this mode. That makes the master a single point of failure
// for whoever opts in: on 2026-09-11 the production master (6287748383890) was
// disconnected while three other sessions were live, so every master_only send
// would have 409'd. Callers that use this for alerting must therefore monitor
// the master's own connectivity — see clino-watchdog's watchdog_*_master_down
// alarms in the yuza-ai-gaming-workshop repo, which exist for exactly this.
//
// Ranking is the same as SelectConnected's and matters only in the pathological
// case of several rows flagged is_master; the UI enforces one, but nothing in
// the schema does, so this picks deterministically rather than at map order.
func (m *Manager) SelectMaster(ctx context.Context) (*ManagedSession, Snapshot, bool) {
	m.mu.RLock()
	all := make([]*ManagedSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.mu.RUnlock()
	var chosen *ManagedSession
	var best Snapshot
	for _, s := range all {
		snap := s.Snapshot()
		if !snap.Connected || !snap.IsMaster {
			continue
		}
		if chosen == nil || snap.QueueLen < best.QueueLen ||
			(snap.QueueLen == best.QueueLen && snap.ID < best.ID) {
			chosen, best = s, snap
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

// SelectSnapshotForSend is the pure ranking helper SelectConnected wraps —
// see its doc comment for allowMaster's meaning. Exported so handlers can
// exercise the ranking/exclusion logic directly against a literal
// []Snapshot without a live Manager (see load_balance_test.go).
func SelectSnapshotForSend(snaps []Snapshot, allowMaster bool) (Snapshot, bool) {
	candidates := make([]Snapshot, 0, len(snaps))
	for _, snap := range snaps {
		if snap.Connected && (allowMaster || !snap.IsMaster) {
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
