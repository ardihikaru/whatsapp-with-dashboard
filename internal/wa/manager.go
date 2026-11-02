// Package wa supervises WhatsApp device sessions: one whatsmeow client per
// paired device, an explicit state machine, pairing pumps, and rate-limited
// send workers. Lock order is always Manager.mu → ManagedSession.mu, and
// Manager.mu protects only the map (docs/04 §2).
package wa

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/inbound"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/wasession"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/id"
)

// Config is the manager slice of the app configuration.
type Config struct {
	SendRate     float64 // token bucket refill, messages/second/session
	SendBurst    int     // token bucket capacity
	ReconcilePar int     // boot reconnection parallelism

	// Reconnect* tune the reconnect supervisor (reconnect.go) that restores,
	// for any backend, the role go.mau.fi/whatsmeow's own AutoReconnectHook
	// plays for the whatsmeow backend alone (see attachClientLocked in
	// backend_meow.go and reconnect.go's package doc comment for the
	// production incident this fixes). All three are optional: zero uses
	// reconnectDefault{ScanInterval,MinDelay,MaxDelay} (reconnect.go), which
	// is what every production call site gets today (cmd/app/main.go does
	// not set these) — tests set them small to run the supervisor loop
	// fast and deterministically instead of waiting on production-scale
	// real timers (house rule: inject intervals as parameters).
	ReconnectScanInterval time.Duration // how often the supervisor re-examines every session
	ReconnectMinDelay     time.Duration // backoff floor for the FIRST automatic reconnect after a session goes disconnected
	ReconnectMaxDelay     time.Duration // backoff ceiling

	// ReconnectStallThreshold bounds the supervisor's patience with a
	// session that keeps accepting reconnect attempts at the API level
	// without ever actually coming back up — see reconnect.go's package
	// doc comment for the production case this covers (waxum's /connect
	// returning 200 while socket_alive stays false for 2+ minutes, forever,
	// after an explicit disconnect). Optional: zero uses
	// reconnectDefaultStallThreshold (reconnect.go). Counts CONSECUTIVE
	// attempts since the session was last seen eligible or last manually
	// Activated — see ManagedSession.markStalled/Activate.
	ReconnectStallThreshold int

	// ReconcileInterval/ReconcileGrace tune the WSM-vs-backend reconciler
	// (manager_reconcile.go). Interval <= 0 disables it (the zero value every
	// test Config has); Grace <= 0 falls back to defaultReconcileGrace.
	ReconcileInterval time.Duration
	ReconcileGrace    time.Duration

	// Inbound* mirror internal/config's WSM_INBOUND_* group verbatim
	// (config.Config.Inbound*) — cmd/app/main.go copies them straight
	// across when building this Config, matching WaxumConfig's own
	// "plain values, not a config type from outside internal/wa"
	// convention. See inbound.go's ensureInboundPump/inboundPump for how
	// each is used; a zero InboundPollInterval falls back to a safe
	// default there rather than spinning a tight loop, but production
	// always gets a validated positive value from
	// internal/config.loadInbound.
	InboundEnabled      bool
	InboundPollInterval time.Duration
	InboundStampWindow  time.Duration
}

// Manager owns the id → ManagedSession map and every goroutine the package
// spawns. Its mutex guards ONLY the map — never hold it across whatsmeow or
// DB calls, or while taking a session lock.
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*ManagedSession

	backend Backend
	repo    wasession.Repository
	msgs    message.Repository
	cfg     Config

	// inbound is the customer-reply store the inbound pump (inbound.go)
	// writes to. Optional — nil means "no ingest": the pump (started from
	// the same Start(*Manager) hook the SSE event pump uses) checks it and
	// no-ops rather than requiring every existing Manager-building test to
	// wire one up. It cannot live on Backend/waxumBackend instead: newBackend
	// (backend construction) runs before appDB is open, and the waxum branch
	// there is explicit that it opens no store of its own — see that doc
	// comment. cmd/app/main.go is the only production call site that passes
	// a non-nil one.
	inbound inbound.Repository

	rootCtx    context.Context // signal ctx from main; parent of every session ctx
	persistCtx context.Context // rootCtx without cancelation: repo writes must land during teardown
	wg         sync.WaitGroup  // tracks EVERY goroutine this package spawns
	closed     atomic.Bool

	// supCtx/supCancel bound the reconnect supervisor's (reconnect.go) own
	// lifetime. It is NOT keyed to any one session's ctx — it is
	// manager-wide by design (see reconnect.go's doc comment) — so it needs
	// its own cancellation Close can reach for directly, the same way each
	// session's own s.cancel gives Close a way to stop THAT session's
	// goroutines. Always created (even when backend is nil and nothing
	// actually listens on supCtx) so Close can call supCancel
	// unconditionally without a nil check.
	supCtx    context.Context
	supCancel context.CancelFunc

	// sysRing is the app-wide diagnostic event log the system log page
	// (internal/handler's GetSystemLog) reads via RecentEvents — the
	// SAME Ring type as each ManagedSession's own per-session ring
	// (ring.go), just sized larger (sysRingSize) and never keyed to one
	// session. ManagedSession.ring/ringLevel append to both rings from
	// one call site, so every existing s.ring(...) call (state
	// transitions, pairing outcomes, status-poll failures, reconnect
	// attempts, send failures) reaches this automatically; RecordEvent
	// below is for the handful of call sites with no session in scope
	// yet (boot reconciliation, the reconnect supervisor's own scan-level
	// decisions). In-memory ONLY, by design — see RecentEvents' doc
	// comment and CLAUDE.md's shared-production-MySQL hazard note for why
	// this is not written to the app store. Always non-nil (unlike
	// backend, which may be nil in tests) so ManagedSession.ring never
	// needs a nil check.
	sysRing *Ring

	// recon is the reconciler's latest verdict — see manager_reconcile.go.
	recon reconcileState

	// inboundPumpOnce/inboundPoke/inboundLastSeen belong to inbound.go's
	// fleet-wide reply-ingest pump — see that file's package doc comment.
	// Allocated unconditionally (like sysRing above), even when the pump
	// itself never starts (m.inbound nil, or WSM_INBOUND_ENABLED=false),
	// so pokeInbound never needs a nil check on inboundPoke.
	inboundPumpOnce sync.Once
	inboundPoke     chan struct{}
	// inboundLastSeen is sessionID -> the highest waxum message row id
	// (waxum.InboundMessage.ID, its OWN keyset-cursor id, not our
	// wa_inbound_messages.id) this pump has already walked past — see
	// processSessionInbound's doc comment for how it bounds pagination. A
	// sync.Map, not a plain map+mutex, for the same reason waxumBackend's
	// own sessions field is one (backend_waxum.go): read/written by a
	// single pump goroutine per key in practice, but a sync.Map costs
	// nothing extra here and matches this package's existing convention
	// for an id-keyed map with no bulk-iteration need.
	inboundLastSeen sync.Map

	// stateChanges fans out every ManagedSession.setState transition to
	// internal/sessionalert's logout-alert collector — see
	// publishStateChange's doc comment. Always non-nil (same convention as
	// sysRing/inboundPoke above), buffered so a slow/absent consumer never
	// blocks a state transition.
	stateChanges chan StateChange
}

// StateChange is one ManagedSession.setState transition, published on
// Manager.StateChanges() for internal/sessionalert's logout-alert
// collector. Boot restore (manager_boot.go) assigns s.state DIRECTLY
// rather than calling setState, specifically so a restart never publishes
// one of these — see manager_boot.go's own comment at that assignment.
type StateChange struct {
	ID, Name, Phone string
	From, To        State
	At              time.Time
}

// stateChangeBuffer bounds StateChanges()'s channel. 64 comfortably covers
// the 2026-09-30 incident this exists to alert on (six sessions flipping
// within two minutes, see CLAUDE.md) with headroom; publishStateChange
// drops and logs rather than blocking if a consumer ever falls behind this
// far, because a state transition commit must never stall on alerting.
const stateChangeBuffer = 64

// publishStateChange is setState's ONLY side channel to
// internal/sessionalert (session.go calls this on every committed
// transition, old != new). Non-blocking: a full buffer means a very
// unusual pile-up with no consumer draining it, and the worse outcome by
// far would be the state machine itself backing up behind alerting that
// is, by design, best-effort.
func (m *Manager) publishStateChange(c StateChange) {
	select {
	case m.stateChanges <- c:
	default:
		slog.Warn("wa: state-change buffer full; dropped", "session", c.ID, "from", c.From, "to", c.To)
	}
}

// StateChanges exposes the state-transition feed for internal/sessionalert
// (or any other in-process consumer) to range over. The channel is never
// closed by this package; a consumer should stop reading via its own ctx
// instead.
func (m *Manager) StateChanges() <-chan StateChange {
	return m.stateChanges
}

// sysRingSize is the app-wide diagnostic ring's capacity — bigger than one
// session's own ringSize (256) because it fans in every session's events
// plus manager-level ones, so it wraps faster; 500 is small enough to stay
// O(1)-cheap to snapshot on a human's page load and comfortably covers a
// normal operator's "what just happened" window without being mistaken for
// durable history (see RecentEvents' doc comment).
const sysRingSize = 500

// NewManager wires the supervisor. Call StartAll afterwards to reconcile
// persisted sessions, and Close (after http.Server.Shutdown) to tear down.
// backend may be nil in tests that never activate a session (see Snapshot's
// nil guard); production always passes one (wa.NewWhatsmeowBackend today).
// inbound may ALSO be nil — see the Manager.inbound field's doc comment —
// and every existing caller that has no use for the inbound-message pump
// (every test in this package and internal/handler) passes nil for it.
func NewManager(rootCtx context.Context, backend Backend,
	repo wasession.Repository, msgs message.Repository, inb inbound.Repository, cfg Config) *Manager {
	supCtx, supCancel := context.WithCancel(rootCtx)
	m := &Manager{
		sessions:    make(map[string]*ManagedSession),
		backend:     backend,
		repo:        repo,
		msgs:        msgs,
		inbound:     inb,
		cfg:         cfg,
		rootCtx:     rootCtx,
		persistCtx:  context.WithoutCancel(rootCtx),
		supCtx:      supCtx,
		supCancel:   supCancel,
		sysRing:      NewRing(sysRingSize),
		inboundPoke:  make(chan struct{}, 1),
		stateChanges: make(chan StateChange, stateChangeBuffer),
	}
	// Nothing to reconnect without a backend (Snapshot/Close already guard
	// the same way) — every test that constructs a nil-backend Manager
	// (newTestManager and friends, dozens of call sites) would otherwise
	// gain a background goroutine with no purpose.
	if backend != nil {
		m.go1(func() { m.reconnectSupervisor(supCtx) })
		if _, ok := backend.(observingBackend); ok && cfg.ReconcileInterval > 0 {
			m.recon.report = ReconcileReport{Enabled: true, OK: true} // OK until a pass says otherwise; before the goroutine exists: nobody else holds m yet
			m.go1(func() { m.reconcileLoop(supCtx, cfg.ReconcileInterval) })
		}
		// backendStarter is an OPTIONAL lifecycle hook, the same
		// type-assertion pattern capabilities.go uses for gateway
		// administration — most Backend implementations (meowBackend)
		// don't need one. waxumBackend implements it to launch the
		// fleet-wide SSE event pump (backend_waxum_events.go, Phase 8) on
		// supCtx, the exact ctx the reconnectSupervisor line above already
		// uses — manager-wide, not keyed to any one session, and cancelled
		// by Close BEFORE per-session teardown and m.wg.Wait() (see
		// manager_boot.go's Close). Takes *Manager rather than
		// context.Context (which the plan this phase came from
		// originally specified): the event pump's handleEvent needs
		// m.Get and m.RecordEvent, and its own lifecycle needs m.go1 —
		// none of that is reachable from a bare context, and passing
		// *Manager costs nothing extra since this stays entirely
		// in-package.
		if starter, ok := backend.(interface{ Start(*Manager) }); ok {
			starter.Start(m)
		}
	}
	return m
}

// go1 spawns a wg-tracked goroutine so Close can join everything.
func (m *Manager) go1(f func()) {
	m.wg.Add(1)
	go func() { defer m.wg.Done(); f() }()
}

// NewID returns a 16-byte crypto/rand hex identifier (sessions and
// messages) — a thin wrapper over internal/id.New so every existing call
// site (wa.NewID(), all across this repo) keeps compiling unchanged while
// internal/selfconvo's own former duplicate (runner.go's old newID) now
// calls the same underlying implementation instead of copying it.
func NewID() string {
	return id.New()
}

// Get returns the live session for an ID.
func (m *Manager) Get(id string) (*ManagedSession, bool) {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	return s, ok
}

// Snapshots returns value-copy DTOs for every session, ordered by creation
// (ID ties broken by name) so HTMX row polling is stable frame to frame.
func (m *Manager) Snapshots() []Snapshot {
	m.mu.RLock()
	all := make([]*ManagedSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.mu.RUnlock()
	out := make([]Snapshot, 0, len(all))
	for _, s := range all {
		out = append(out, s.Snapshot())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

// GroupCodes returns every distinct non-empty routing group currently in
// use, lexically sorted — for populating a "route to group X" picker.
//
// This is derived from Snapshots(), i.e. from the in-memory
// Manager.sessions map, rather than a `SELECT DISTINCT group_code FROM
// wa_sessions` — deliberately: routing itself (EligibleForRoute) matches a
// Route's GroupCode against Snapshot.GroupCode in RAM, so a SQL query would
// be a second opinion about the same fact, one that could drift from it (a
// stale or since-cleared row) and offer an operator a code that currently
// routes nowhere. The app is also a hard singleton (CLAUDE.md) with no
// second process to disagree with this one's memory. The honest consequence
// worth keeping in mind: a code exists in this list only while at least one
// live session still carries it — clearing the last session's group_code
// makes that code vanish here even though nothing "deleted" it anywhere.
func (m *Manager) GroupCodes() []string {
	seen := map[string]bool{}
	for _, snap := range m.Snapshots() {
		for _, code := range snap.GroupCodes {
			seen[code] = true
		}
	}
	out := make([]string, 0, len(seen))
	for code := range seen {
		out = append(out, code)
	}
	sort.Strings(out)
	return out
}

// SetSelfReminder moves the self-reminder flag (see ManagedSession.
// isSelfReminder's doc comment) to id, clearing it from every other
// session, or just clears it when on is false. Exclusivity is enforced
// here, in the app, rather than by a DB index (see
// migrations/0015_session_groups_self_reminder.sql's header): the
// persisted write is a SINGLE statement
// (wasession.Repository.SetSelfReminder's CASE WHEN), and the in-memory
// mirror on every live ManagedSession is updated under m.mu so a
// concurrent Snapshot can never observe two sessions both flagged at once.
// Unknown id with on=true still clears every other row (the DB statement
// runs regardless) — this mirrors SetMaster/SetGroupCode's existing
// "unknown id persists as a repo call with no matching in-memory session"
// shape, which handlers already guard against with their own Get check.
func (m *Manager) SetSelfReminder(ctx context.Context, id string, on bool) error {
	if err := m.repo.SetSelfReminder(m.persistCtx, id, on); err != nil {
		return err
	}
	m.mu.RLock()
	all := make([]*ManagedSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.mu.RUnlock()
	for _, s := range all {
		s.mu.Lock()
		if s.ID == id {
			s.isSelfReminder = on
		} else if on {
			s.isSelfReminder = false
		}
		s.mu.Unlock()
	}
	if target, ok := m.Get(id); ok {
		if on {
			target.ring("flagged as the self-reminder session — default sender for session alerts")
		} else {
			target.ring("unflagged as the self-reminder session")
		}
	}
	m.RecordEvent("info", fmt.Sprintf("self-reminder session set to %q (on=%v)", id, on))
	return nil
}

// Counts returns (total, connected) for the health endpoint and the real
// dashboard KPI tile.
func (m *Manager) Counts() (total, connected int) {
	for _, snap := range m.Snapshots() {
		total++
		if snap.Connected {
			connected++
		}
	}
	return total, connected
}

// RecordEvent appends a manager-level diagnostic event — one with no single
// session in scope (boot reconciliation, backend-level errors) — to the
// app-wide ring. Session-scoped code should call ManagedSession.ring /
// ringLevel instead (session.go/backend_meow.go), which already reaches
// this same ring; this is the one exported entry point for the rest.
func (m *Manager) RecordEvent(level, note string) {
	m.sysRing.Append(Event{At: time.Now(), Level: level, Note: note})
}

// RecentEvents returns up to limit of the most recent app-wide diagnostic
// events, NEWEST FIRST (Ring.Snapshot is chronological/oldest-first; this
// reverses and truncates for the system log page). limit<=0 or larger than
// what's buffered returns everything available.
//
// This ring is IN-MEMORY ONLY and lost on every process restart — it is a
// high-frequency diagnostic log, not an audit trail, and deliberately never
// touches the app store: internal/handler's system log page renders both
// facts plainly so nobody mistakes it for one. wa_messages (see
// internal/domain/message) remains the durable audit log for sends. See
// CLAUDE.md's "Production MySQL is shared and is not a test target" note
// for why writing every state transition and reconnect attempt to that
// store would be the wrong trade.
func (m *Manager) RecentEvents(limit int) []Event {
	all := m.sysRing.Snapshot()
	if limit <= 0 || limit > len(all) {
		limit = len(all)
	}
	out := make([]Event, limit)
	for i := 0; i < limit; i++ {
		out[i] = all[len(all)-1-i]
	}
	return out
}
