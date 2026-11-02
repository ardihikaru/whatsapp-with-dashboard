package sessionalert

import "context"

// MembershipChecker is the narrow seam the FINAL "any connected session"
// fallback tier uses to confirm a candidate is actually a member of the
// target group — see chooseSender's doc comment for why this check is
// scoped to that tier alone. Gateway embeds it, so the production path
// (managerGateway, gateway_wa.go) and every test fake share one method.
type MembershipChecker interface {
	// Member reports whether sessionID's device currently belongs to
	// groupJID. An error is treated as "not a member" by chooseSender —
	// "membership unknown" must never fail OPEN into a non-member send.
	Member(ctx context.Context, sessionID, groupJID string) (bool, error)
}

// chooseSender picks which session sends an alert, in order:
//
//  1. the configured id, if connected and not excluded;
//  2. whichever session carries SelfReminder, if connected and not excluded;
//  3. any OTHER connected, non-excluded session that is a confirmed member
//     of groupJID.
//
// Tiers 1 and 2 are NEVER membership-checked — root-caused live 2026-11-02
// (CLAUDE.md): an operator who names a sender, or flags a session
// self-reminder, has already made that choice deliberately, and the
// membership check exists for exactly the opposite situation — an
// automatic fallback with no such deliberate choice behind it. Checking
// membership there too would also cost an extra waxum round trip (even if
// cached) for the overwhelmingly common case where the operator's choice
// is obviously already correct.
//
// Tier 3 is where the 2026-11-02 incident lived: "any connected session"
// with no membership filter picked a session that had never joined the
// target group, waxum answered 403 forbidden, and the alert the self-
// reminder session's own logout was supposed to report was silently lost.
// mc.Member is called ONLY here, and only for candidates that already
// passed every other filter — see Gateway's own doc comment for why the
// production implementation must cache this (a tiny waxum rate-limit
// burst, CLAUDE.md) rather than call it per candidate per alert.
//
// excluded is the set of session ids being reported on (the logout batch,
// or the daily report's non-connected list) — a sender must never be one
// of them, though in practice a reported session is never Connected
// anyway and so would be filtered out regardless; excluded makes that
// guarantee explicit rather than incidental.
func chooseSender(ctx context.Context, mc MembershipChecker, snaps []Snapshot, configuredID, groupJID string, excluded map[string]bool) (Snapshot, bool) {
	if configuredID != "" {
		for _, sn := range snaps {
			if sn.ID == configuredID && sn.Connected && !excluded[sn.ID] {
				return sn, true
			}
		}
	}
	for _, sn := range snaps {
		if sn.SelfReminder && sn.Connected && !excluded[sn.ID] {
			return sn, true
		}
	}
	for _, sn := range snaps {
		if !sn.Connected || excluded[sn.ID] {
			continue
		}
		member, err := mc.Member(ctx, sn.ID, groupJID)
		if err != nil || !member {
			continue
		}
		return sn, true
	}
	return Snapshot{}, false
}
