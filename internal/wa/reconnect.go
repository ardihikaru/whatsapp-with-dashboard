package wa

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/backoff"
)

// This file restores, for EVERY backend, the role go.mau.fi/whatsmeow's own
// AutoReconnectHook plays for the whatsmeow backend alone
// (attachClientLocked in backend_meow.go): when a session's socket drops
// out from under it, something must actually try to bring it back.
//
// The production incident this fixes: an operator paired a device
// (StatePaired committed, correct jid persisted). Seconds later waxum's
// process logged the session Connected and kept delivering message/receipt
// events for it for ten more minutes — the socket was alive and
// authenticated the entire time. WSM's own record, meanwhile, wrote
// last_status=disconnected once and never changed it again: statusPump
// (backend_waxum.go) only ever READS /status and records what it sees — it
// never calls Connect again. whatsmeow never had this problem because
// cli.AutoReconnectHook redials for it, in-process, with no equivalent
// anywhere in this package for a REST-backed session. This was a functional
// regression introduced when the waxum backend was added, not something
// waxum itself did wrong.
//
// Default timing intentionally mirrors waxum's own operational posture (see
// CLAUDE.md's waxum rate-limit hazard note, and backend_waxum.go's pairPump
// doc comment, point 5, for where RECONNECT_WATCHDOG_POLL_MS is already
// documented in this codebase): waxum runs its OWN reconnect watchdog
// against a downed socket, polling every 30s (its own default) for up to 10
// attempts, and separately rate-limits every endpoint per source IP. A
// tight WSM-side retry loop
// would not just be redundant with that — it would actively fight it,
// piling our own /connect calls on top of waxum's simultaneous attempts
// against the exact same peer, and risks 429-ing waxum's own reconnect
// calls along with ours. reconnectDefaultMinDelay (30s) and
// reconnectDefaultMaxDelay (5m) below are chosen to be deliberately LOOSER
// than that cadence, not tighter.
const (
	reconnectDefaultScanInterval = 5 * time.Second
	reconnectDefaultMinDelay     = 30 * time.Second // >= waxum's RECONNECT_WATCHDOG_POLL_MS (30s) — see doc comment above
	reconnectDefaultMaxDelay     = 5 * time.Minute

	// reconnectDefaultStallThreshold: after this many CONSECUTIVE
	// automatic reconnect attempts for one session all succeed at the API
	// level (Backend.Connect/Activate returns nil) without the session
	// ever reaching StateConnected again, the supervisor stops retrying it
	// and marks it stalled (ManagedSession.markStalled) instead of firing
	// a next attempt. 5 is chosen, not derived: it is enough attempts,
	// spread across reconnectDelay's exponential-backoff schedule (30s,
	// 60s, 120s, 240s, 300s-capped — several minutes total), to rule out a
	// single transient blip while waxum's own reconnect watchdog is still
	// mid-cycle, but low enough that an operator is told "this needs you"
	// well before the incident this file's package doc comment describes
	// (socket dead for 2+ minutes and climbing) turns into an
	// indefinitely-silent one — the whole point of this feature is to stop
	// burning attempts against a peer that has already shown, repeatedly,
	// that /connect succeeding does not mean the socket comes up.
	reconnectDefaultStallThreshold = 5
)

// reconnectDelay returns how long the supervisor must wait before its
// (attempt+1)'th automatic reconnect for one session: an exponential
// backoff+jitter schedule floored at min and capped at max, built on
// internal/backoff's shared DelayFrom/Jitter (see that package's doc
// comment for why the formula itself must not be duplicated here — this
// package already has one caller of it, sender_limits.go's send retry
// loop, and a second, independently-tuned copy would be exactly the
// mistake internal/backoff exists to prevent). attempt is 0-based: attempt
// 0 is the delay before the FIRST automatic reconnect since a session was
// last seen eligible (see reconnectSupervisor's doc comment for why that
// first delay, not just later ones, must also respect min).
func reconnectDelay(attempt int, min, max time.Duration) time.Duration {
	return backoff.DelayFrom(attempt, min, max) + backoff.Jitter()
}

// reconnectSupervisor is Manager's one manager-wide background goroutine
// that periodically re-examines every session and redials the ones that
// need it. Started once, from NewManager, for the Manager's whole lifetime
// (see NewManager's nil-backend guard) and stopped by Close via supCancel —
// never started or stopped per session.
//
// ONE manager-wide goroutine, not N per-session goroutines, by design:
//
//   - No per-session lifecycle wiring. A per-session version would need a
//     start call added at every place a session can BECOME eligible
//     (Activate's success path, a successful pairing) and a matching stop
//     call added at every place it can STOP being eligible (Deactivate,
//     markDeviceGone, permanentDown, session deletion) — the same policy
//     duplicated across five-plus call sites, each an independent
//     goroutine-lifetime bug surface (a missed stop leaks a goroutine per
//     session forever; a missed start silently reintroduces this exact
//     production bug for one session while looking fixed everywhere else).
//     One loop that reads current state on every tick needs none of that:
//     eligibility is recomputed fresh each scan, from state that already
//     exists (ManagedSession.active/jid/state), not tracked separately.
//   - One shared, easily-unit-tested backoff schedule. attempts/nextAttempt
//     below are two plain maps in one function's closure, not a field
//     bolted onto ManagedSession (which would grow this package's central,
//     heavily load-bearing struct for a concern only this file needs) or
//     N independent timers that would each need their own test.
//   - The scan itself never blocks on I/O — see reconnectOne below — so one
//     slow or hanging waxum peer can never delay this loop from noticing or
//     retrying every OTHER disconnected session, which a per-session
//     goroutine blocked inside its own Connect call would not obviously
//     guarantee without the same care taken here anyway.
//
// Lock discipline: this reads Manager.mu (RLock, to copy the session slice)
// and then EACH session's own ManagedSession.reconnectEligible (which takes
// s.mu itself, briefly) — never both at once, matching the documented lock
// order (Manager.mu -> ManagedSession.mu) and never holding either across
// the network call, which runs in its own goroutine entirely outside both.
func (m *Manager) reconnectSupervisor(ctx context.Context) {
	scan := m.cfg.ReconnectScanInterval
	if scan <= 0 {
		scan = reconnectDefaultScanInterval
	}
	minDelay := m.cfg.ReconnectMinDelay
	if minDelay <= 0 {
		minDelay = reconnectDefaultMinDelay
	}
	maxDelay := m.cfg.ReconnectMaxDelay
	if maxDelay <= 0 {
		maxDelay = reconnectDefaultMaxDelay
	}
	stallThreshold := m.cfg.ReconnectStallThreshold
	if stallThreshold <= 0 {
		stallThreshold = reconnectDefaultStallThreshold
	}

	attempts := map[string]int{}          // session id -> reconnect attempts made since it was last seen eligible
	nextAttempt := map[string]time.Time{} // session id -> earliest time worth trying again

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(scan):
		}

		m.mu.RLock()
		all := make([]*ManagedSession, 0, len(m.sessions))
		for _, s := range m.sessions {
			all = append(all, s)
		}
		m.mu.RUnlock()

		now := time.Now()
		for _, s := range all {
			if s.isStalled() {
				// Already given up on (markStalled below, a previous scan):
				// do NOT touch attempts/nextAttempt here. reconnectEligible
				// (checked next) would still say yes — state stays
				// StateDisconnected, see the stalled field's doc comment on
				// ManagedSession — so without this check the block below
				// would just resume firing attempts on its own, defeating
				// the whole point of stalling. Only a manual Activate
				// (session.go) clears s.stalled; when it does, this session
				// hits the "not seen" branch below on its NEXT tick (its
				// map entries were deleted when it stalled) and starts a
				// fresh curve at attempt 0 — see
				// TestAManualActivateClearsTheStalledStateAndResumesRetrying.
				continue
			}
			if !s.reconnectEligible() {
				// Not eligible right now — either it was never disconnected,
				// it already recovered, the operator deactivated it, or it
				// went terminal (logged out). Forget any backoff state so a
				// FUTURE disconnect starts a fresh curve at attempt 0/min,
				// not wherever a stale curve left off.
				delete(attempts, s.ID)
				delete(nextAttempt, s.ID)
				continue
			}

			due, seen := nextAttempt[s.ID]
			if !seen {
				// First tick this session has been observed eligible: SCHEDULE
				// (do not fire) the first attempt no sooner than minDelay from
				// now. Firing immediately here — the natural reading of "no
				// entry yet, so nothing to wait for" — is exactly the bug
				// TestReconnectBackoffDoesNotOutpaceWaxumsOwnWatchdog and the
				// production incident this file's doc comment describes both
				// guard against: the very first automatic reconnect must
				// already respect the floor, not just the second and later
				// ones.
				nextAttempt[s.ID] = now.Add(reconnectDelay(0, minDelay, maxDelay))
				continue
			}
			if now.Before(due) {
				continue
			}

			// attempts[s.ID] only ever grows while s stays reconnectEligible
			// (StateDisconnected) — the moment a reconnect actually works,
			// the backend moves it to StateConnected, reconnectEligible goes
			// false, and the branch above resets it to 0. So reaching
			// stallThreshold here already MEANS stallThreshold consecutive
			// attempts each got an API-level answer (Activate/Backend.Connect
			// returned without ever making the session reconnectEligible=false)
			// but the socket never actually came back — exactly the
			// "succeeds at the API level but socket_alive stays false"
			// condition this feature exists for (see reconnectDefaultStallThreshold's
			// doc comment and reconnect.go's package doc comment for the
			// production case). Stop here instead of firing attempt
			// stallThreshold+1.
			if attempts[s.ID] >= stallThreshold {
				reason := fmt.Sprintf(
					"waxum accepted /connect %d consecutive times but this session never reached socket_alive; "+
						"restarting the waxum container or re-pairing this device are the two known remedies "+
						"(see reconnect.go's package doc comment) — automatic reconnect has stopped for now, "+
						"use Activate to try again", attempts[s.ID])
				s.markStalled(reason)
				delete(attempts, s.ID)
				delete(nextAttempt, s.ID)
				continue
			}

			n := attempts[s.ID] + 1
			attempts[s.ID] = n
			nextAttempt[s.ID] = now.Add(reconnectDelay(n, minDelay, maxDelay))

			reason := fmt.Sprintf("session active, paired, and observed disconnected (attempt %d/%d)", n, stallThreshold)
			slog.Info("wa: auto-reconnect attempt", "session", s.ID, "attempt", n, "reason", reason)
			s.ring("auto-reconnect: " + reason)
			m.go1(func() { m.reconnectOne(ctx, s, n) })
		}
	}
}

// reconnectOne makes exactly one reconnect attempt for s, outside any lock,
// via the SAME path an operator's "Activate" button uses (Activate already
// carries the correct idempotent guard for connected/pairing sessions and
// persists active=1 — see session.go). It is spawned per attempt (not
// inline in the scan loop above) so a slow or hanging call for one session
// cannot delay the scan loop from considering every OTHER session on its
// own next tick.
func (m *Manager) reconnectOne(ctx context.Context, s *ManagedSession, attempt int) {
	if err := s.Activate(ctx); err != nil {
		slog.Warn("wa: auto-reconnect failed", "session", s.ID, "attempt", attempt, "err", err)
		s.ring(fmt.Sprintf("auto-reconnect attempt %d failed: %s", attempt, err))
	}
}
