package wa

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/backoff"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/waxum"
)

// This file is the fleet-wide consumer of waxum's Server-Sent-Events stream
// (GET /api/v1/events/tail, internal/waxum.Client.TailEvents — built in an
// earlier phase and, until now, never called from anywhere in the
// codebase). Phase 8 of the plan at
// .../please-implement-and-wire-foamy-shamir.md.
//
// THE STREAM IS A NUDGE, NOT A SECOND STATE MACHINE — this is the entire
// point of this file, so it is not simplified away anywhere below:
//
//   - applyStatus (backend_waxum.go) carries loggedOutHysteresisPolls'
//     hysteresis, calibrated in POLLS against a real production flap (see
//     that constant's doc comment). If an SSE event committed state
//     directly, three logged_out frames arriving within ~200ms (plausible —
//     waxum's own tail is explicitly lossy, see next point, and a burst
//     right after a reconnect is exactly when several would fire close
//     together) would collapse that evidence into a fifth of a second and
//     reopen the incident the hysteresis exists to prevent.
//   - waxum's tail is explicitly lossy: its own ring is bounded at 200
//     items, so a burst that produces more events than that inside a poll
//     window loses the middle ones, and the stream can be down for an
//     arbitrary stretch (this backend's own reconnect below, or a waxum
//     restart, which also empties waxum's ring). An event source with those
//     two properties cannot be an authority on its own.
//
// So handleEvent below NEVER calls applyStatus and NEVER constructs a
// State. It does exactly one state-relevant thing: a non-blocking send on
// the affected session's waxumSessionState.poke (backend_waxum.go), which
// only shortens statusPump's wait before its NEXT real GET /status — a
// request that goes through applyStatus completely unchanged, floored by
// pokeMinInterval so a burst of pokes cannot compress the hysteresis
// evidence window either (see that constant's doc comment for why THAT
// floor is also required). Net effect: one state authority, hysteresis
// intact on both axes (poll count AND wall-clock spacing), and perceived
// transition latency drops from up to pollInterval (60s by default) to
// ~0 for the common case of an idle session.
//
// Lifecycle: exactly ONE connection for the whole process — waxum's
// /events/tail has no per-session filter here (an unfiltered
// waxum.TailOptions{}), matching "fleet-wide, one stream" — guarded by
// eventPumpOnce so a second ensureEventPump call (there is only one caller,
// Start, but the guard costs nothing and matches ensureStatusPump's own
// CompareAndSwap-style idempotence). It runs on Manager.supCtx, NOT any one
// session's ctx: supCtx is manager-wide by construction (see manager.go's
// doc comment on that field — the reconnect supervisor uses the exact same
// slot), and Manager.Close cancels supCancel FIRST, before per-session
// teardown and before m.wg.Wait() (manager_boot.go) — so this pump's own
// ctx is already cancelled by the time Close starts waiting for it.
// Spawned via m.go1, the same wg every other tracked goroutine in this
// package uses: an UNTRACKED loop here would let Close's m.wg.Wait() return
// before this goroutine has actually exited, races Close's assumption that
// nothing backend-owned is still running when it calls backend.Close(), and
// — the specific hazard CLAUDE.md's WhatsApp-session-identity note
// documents — risks the process being SIGKILLed by compose's
// stop_grace_period with this goroutine still mid-flight. See
// TestManagerCloseTerminatesTheEventPump, "the single most important test
// in this phase."
type waxumEventEnvelope struct {
	SessionID string `json:"session_id"`
}

// pokeableEventKinds are the SSE "event:" kinds (waxum.Event.Type, with any
// dotted prefix like "session." stripped by eventKind below — the exact
// wire shape of waxum's real event names is not pinned down anywhere in
// this codebase yet, so both a bare "connected" and a "session.connected"
// are accepted) that warrant an early poke: every one of them is a
// connection-state-relevant transition per the phase brief. Anything else
// (a message/receipt event, for instance) is still rung — see handleEvent —
// but does not shorten statusPump's wait; there is no reason to.
var pokeableEventKinds = map[string]bool{
	"connected":      true,
	"disconnected":   true,
	"logged_out":     true,
	"account_locked": true,
	"qr_code":        true,
	"pair_code":      true,
	"stream_error":   true,
}

// eventKind strips an optional dotted namespace prefix off an SSE event
// type ("session.connected" -> "connected"), so pokeableEventKinds matches
// regardless of whether waxum's real deployment prefixes its event names —
// unverified against a live waxum, see this file's own doc comment and the
// final report for why.
func eventKind(evType string) string {
	if i := strings.LastIndex(evType, "."); i >= 0 {
		return evType[i+1:]
	}
	return evType
}

// eventPumpFailureForgetWindow: an attempt that stays open at least this
// long before dropping is treated as "the gateway was genuinely reachable
// for a while", resetting the reconnect failCount to 0 even if it never
// happened to deliver a real (non-keep-alive) event — waxum's own
// keep-alive cadence is ~15s (internal/waxum/events.go's doc comment), so a
// stream that outlives that by 4x has clearly proven connectivity. Without
// this, a stream that dies after ten quiet minutes would inherit whatever
// backoff its very first, much shorter-lived predecessor accumulated,
// instead of reconnecting promptly.
const eventPumpFailureForgetWindow = 60 * time.Second

// Start satisfies the optional Backend lifecycle hook NewManager checks for
// (manager.go's "if backend != nil" branch, the exact slot that already
// spawns reconnectSupervisor). It takes *Manager, not context.Context —
// see this package's manager.go for the interface definition and its doc
// comment for why: handleEvent needs m.Get (to ring an event against the
// right session) and m.RecordEvent (the fallback when it can't), and
// m.go1/m.supCtx are how this pump gets tracked and torn down — none of
// that is reachable from a bare ctx. A no-op when WSM_WAXUM_EVENTS=0
// (b.eventsEnabled false), leaving statusPump exactly as it behaved before
// this phase.
//
// Also starts inbound.go's reply-ingest pump (m.ensureInboundPump) — see
// that method's own doc comment for why it shares this hook rather than a
// bespoke one. ensureInboundPump is its own no-op when there is nothing
// for it to do (m.inbound nil, or WSM_INBOUND_ENABLED=false), so this is
// safe to call unconditionally, the same way ensureEventPump above already
// is.
func (b *waxumBackend) Start(m *Manager) {
	b.ensureEventPump(m)
	m.ensureInboundPump()
}

// ensureEventPump starts the fleet-wide SSE consumer exactly once.
func (b *waxumBackend) ensureEventPump(m *Manager) {
	if !b.eventsEnabled {
		return
	}
	b.eventPumpOnce.Do(func() {
		m.go1(func() { b.eventPump(m.supCtx, m) })
	})
}

// eventPump owns the reconnect loop around TailEvents: one attempt per
// iteration, backing off (the SAME internal/backoff schedule statusPump
// uses, capped at maxStatusPollBackoff — no new, independently-tuned curve)
// between attempts that end quickly, and forgetting that backoff once an
// attempt has proven itself (delivered an event, or simply stayed open past
// eventPumpFailureForgetWindow).
func (b *waxumBackend) eventPump(ctx context.Context, m *Manager) {
	failCount := 0
	for ctx.Err() == nil {
		attemptStart := time.Now()
		gotEvent := false

		err := b.client.TailEvents(ctx, waxum.TailOptions{}, func(ev waxum.Event) error {
			gotEvent = true
			b.gatewayReachable.Store(true)
			b.healthKnown.Store(true)
			b.handleEvent(m, ev)
			if ctx.Err() != nil {
				return ctx.Err() // stop the stream promptly on shutdown
			}
			return nil
		})
		if ctx.Err() != nil {
			return // shutting down — do not log/backoff/reconnect
		}

		// The connection is no longer up, whatever the reason — mark it so
		// GET /api/v1/health's cached field reflects reality promptly. See
		// GatewayHealth.Reachable's doc comment for the trade this makes
		// (a health probe hit between here and the next successful attempt
		// sees "unreachable", even though the very next attempt often
		// reconnects within a second or two).
		b.gatewayReachable.Store(false)
		b.healthKnown.Store(true)

		if gotEvent || time.Since(attemptStart) > eventPumpFailureForgetWindow {
			failCount = 0
		}
		failCount++
		wait := backoff.DelayFrom(failCount-1, time.Second, maxStatusPollBackoff) + backoff.Jitter()
		if err != nil {
			slog.Warn("wa: waxum event stream ended", "consecutive_failures", failCount, "err", err)
			m.RecordEvent("warn", fmt.Sprintf("waxum event stream ended (%d in a row): %s", failCount, err))
		} else {
			slog.Warn("wa: waxum event stream ended without error", "consecutive_failures", failCount)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// handleEvent decodes one SSE event's session_id and rings it — EVERY
// event, not just the ones that poke — so it lands on the system log page
// (Manager.RecentEvents / internal/handler's GetSystemLog) the same way
// every other session-scoped diagnostic already does (ManagedSession.ring's
// doc comment). A session_id that does not (or no longer) map to a live
// *ManagedSession still gets recorded, via m.RecordEvent, rather than
// silently dropped — a session deleted moments before its own
// disconnected/logged_out event arrives is exactly the case an operator
// most wants visible.
//
// Decoding is best-effort: json.Unmarshal's error is ignored deliberately.
// A malformed or session_id-less payload (a genuinely fleet-level event, if
// waxum ever sends one) still gets rung with whatever was decoded (an empty
// SessionID), it just never pokes anything — see the guard below.
func (b *waxumBackend) handleEvent(m *Manager, ev waxum.Event) {
	var env waxumEventEnvelope
	_ = json.Unmarshal(ev.Data, &env)

	note := "waxum event: " + ev.Type
	if env.SessionID != "" {
		if s, ok := m.Get(env.SessionID); ok {
			s.ring(note)
		} else {
			m.RecordEvent("info", note+" (session "+env.SessionID+", not tracked)")
		}
	} else {
		m.RecordEvent("info", note)
	}

	// A "message" event nudges the inbound-message pump (inbound.go) to
	// poll sooner than its own timer — a SEPARATE poke from pokeSession
	// below, on a SEPARATE, session-less channel (m.pokeInbound, not
	// waxumSessionState.poke): the inbound pump is fleet-wide, not
	// per-session, so there is no single session's poke channel to reuse
	// even if it wanted one. This does NOT call applyStatus, does NOT
	// construct a State, and does NOT poke statusPump — see this file's
	// package doc comment (lines 21-49) for why an event must never write
	// session state, and inbound.go's pokeInbound doc comment for why
	// this call is compatible with that rule despite living right next to
	// pokeSession.
	if eventKind(ev.Type) == "message" {
		m.pokeInbound()
	}

	if env.SessionID == "" || !pokeableEventKinds[eventKind(ev.Type)] {
		return
	}
	b.pokeSession(env.SessionID)
}

// Reachable satisfies GatewayHealth — O(1), no I/O, reading only the two
// atomics eventPump maintains. See that interface method's doc comment.
func (b *waxumBackend) Reachable() (reachable, ok bool) {
	return b.gatewayReachable.Load(), b.healthKnown.Load()
}

var _ GatewayHealth = (*waxumBackend)(nil)
