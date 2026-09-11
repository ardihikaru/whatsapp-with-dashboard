package wa

import (
	"context"
	"sort"
	"time"
)

// Route is one automatic-routing request's candidate scope. It is the single
// input to EligibleForRoute and therefore to every automatic pick this
// package makes — SelectConnected, SelectMaster and the group-scoped paths
// added alongside it are all thin wrappers around Route + SelectForRoute.
type Route struct {
	// GroupCode selects the routing scope: "" is the UNGROUPED pool, not
	// "any group" — a session carrying a non-empty group_code is invisible
	// to a Route with GroupCode == "" and vice versa (see EligibleForRoute).
	// Callers must pass an already-normalized value (wa.NormalizeGroupCode).
	GroupCode string
	// MasterOnly restricts the scope (GroupCode's pool, ungrouped or named)
	// to sessions flagged is_master, and overrides AllowMaster — it is a
	// different axis from AllowMaster, not a stronger version of it. See
	// SelectMaster's doc comment for why "only the master" cannot be
	// expressed by any value of AllowMaster.
	MasterOnly bool
	// AllowMaster widens the scope to include masters ALONGSIDE ordinary
	// sessions, rather than picking only the master. Ignored when
	// MasterOnly is set.
	AllowMaster bool
}

// EligibleForRoute is the SINGLE eligibility predicate every automatic pick
// in this package now funnels through. It replaces what used to be two
// separate, subtly different inline filters (SelectConnected's
// `allowMaster || !snap.IsMaster` and SelectMaster's `!snap.IsMaster`) with
// one rule that also knows about group scope:
//
//  1. the session must be connected;
//  2. its group_code must match the route's scope EXACTLY — "" only matches
//     "", a named group only matches that same name; there is no fallback
//     between scopes in either direction;
//  3. within that scope, MasterOnly requires IsMaster and ignores
//     AllowMaster; otherwise AllowMaster widens the pool to include masters,
//     and its absence excludes them.
func EligibleForRoute(snap Snapshot, rt Route) bool {
	if !snap.Connected || snap.GroupCode != rt.GroupCode {
		return false
	}
	if rt.MasterOnly {
		return snap.IsMaster
	}
	return rt.AllowMaster || !snap.IsMaster
}

// SelectForRoute is the general-purpose automatic pick: it filters every
// live session through EligibleForRoute for the given Route, ranks survivors
// with SelectSnapshotForRoute's four-key tiebreak, and records the
// selection. SelectConnected and SelectMaster below are documented, signature
// -stable wrappers over this for their existing call sites.
//
// Lock order: copy m.sessions under m.mu.RLock, release it, THEN call
// Snapshot() on each — never hold m.mu across a ManagedSession.Snapshot call
// (docs/04 §2's Manager.mu → ManagedSession.mu order). There is only one
// filter pass now (EligibleForRoute), so — unlike the old SelectConnected —
// there is no second, unreachable-in-practice guard to apologize for: every
// candidate handed to SelectSnapshotForRoute already satisfies the same
// predicate the ranking step re-derives its candidate set from, and either
// both pools agree or something is broken in a way that guard could never
// have caught either.
func (m *Manager) SelectForRoute(ctx context.Context, rt Route) (*ManagedSession, Snapshot, bool) {
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
		if EligibleForRoute(snap, rt) {
			candidates = append(candidates, candidate{session: s, snap: snap})
		}
	}
	if len(candidates) == 0 {
		return nil, Snapshot{}, false
	}
	snaps := make([]Snapshot, 0, len(candidates))
	for _, c := range candidates {
		snaps = append(snaps, c.snap)
	}
	best, ok := SelectSnapshotForRoute(snaps, rt)
	if !ok {
		return nil, Snapshot{}, false
	}
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

// SelectConnected chooses the best live session for automatic routing from
// the UNGROUPED pool (group_code == ""): lowest queue length, then oldest
// last selection, then oldest created row, then session id. It records the
// selection before returning so concurrent callers distribute ties
// predictably within this single-process app.
//
// allowMaster gates whether a session flagged is_master (see
// ManagedSession.isMaster) is eligible for this AUTOMATIC pick — it is
// always still reachable by naming its session id explicitly. Callers must
// pass an explicit answer (no default): false unless the caller has an
// opt-in signal (e.g. a request's allow_master flag) to route through one.
//
// Thin wrapper over SelectForRoute — kept so existing call sites and
// load_balance_test.go/select_master_test.go's coverage of this exact
// signature stay untouched and serve as proof the wrapper preserves
// behaviour.
func (m *Manager) SelectConnected(ctx context.Context, allowMaster bool) (*ManagedSession, Snapshot, bool) {
	return m.SelectForRoute(ctx, Route{AllowMaster: allowMaster})
}

// SelectMaster chooses the connected MASTER session from the UNGROUPED pool,
// and nothing else. It is the routing mode for traffic that must always
// leave from one known handset — ops alerts, where "which number paged me at
// 3am" is part of reading the page.
//
// This is deliberately NOT expressible through SelectConnected's allowMaster
// flag, and the difference is easy to misread: allowMaster WIDENS the candidate
// set (allowMaster || !snap.IsMaster) so a master becomes eligible ALONGSIDE
// every ordinary session. There is no value of it that means "only the master".
//
// A session in a named group is never returned here even if it is a master:
// this call is Route{MasterOnly: true} with GroupCode left at "", so it is
// pinned to the ungrouped pool exactly like SelectConnected above — a
// grouped master must be reached via SelectForRoute(Route{GroupCode: "x",
// MasterOnly: true}) instead. Closing that leak (a global master_only pick
// silently reaching into a group) is the whole reason EligibleForRoute checks
// group scope before it ever looks at MasterOnly.
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
// Ranking is now the SAME four-key ranking SelectConnected uses (QueueLen,
// then oldest LastSelectedAt with "never selected" winning ties, then oldest
// CreatedAt, then ID) — unifying SelectMaster and SelectConnected onto one
// ranking function (SelectSnapshotForRoute) is what makes this comment true;
// before this change SelectMaster ranked on (QueueLen, ID) only, which this
// same doc comment already claimed was untrue. It matters only in the
// pathological case of several rows flagged is_master; the UI enforces one,
// but nothing in the schema does, so this still picks deterministically
// rather than at map order — see TestSelectMasterRotatesBetweenSeveralConnectedMasters
// for the case where the two rankings actually disagree.
func (m *Manager) SelectMaster(ctx context.Context) (*ManagedSession, Snapshot, bool) {
	return m.SelectForRoute(ctx, Route{MasterOnly: true})
}

// SelectSnapshotForRoute is the pure ranking helper SelectForRoute wraps —
// see EligibleForRoute for the filter and Route for the scope it filters by.
// Exported so handlers/tests can exercise ranking/exclusion against a
// literal []Snapshot without a live Manager.
func SelectSnapshotForRoute(snaps []Snapshot, rt Route) (Snapshot, bool) {
	candidates := make([]Snapshot, 0, len(snaps))
	for _, snap := range snaps {
		if EligibleForRoute(snap, rt) {
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

// SelectSnapshotForSend is SelectSnapshotForRoute restricted to the
// UNGROUPED pool — see that function's doc comment for the ranking, and
// Route's doc comment for why GroupCode: "" is a scope, not "no scope".
// Thin wrapper kept for load_balance_test.go's existing coverage of this
// exact signature.
func SelectSnapshotForSend(snaps []Snapshot, allowMaster bool) (Snapshot, bool) {
	return SelectSnapshotForRoute(snaps, Route{AllowMaster: allowMaster})
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
