// Package wa supervises WhatsApp device sessions: one whatsmeow client per
// paired device, an explicit state machine, pairing pumps, and rate-limited
// send workers. Lock order is always Manager.mu → ManagedSession.mu, and
// Manager.mu protects only the map (docs/04 §2).
package wa

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/wasession"
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
func NewManager(rootCtx context.Context, backend Backend,
	repo wasession.Repository, msgs message.Repository, cfg Config) *Manager {
	supCtx, supCancel := context.WithCancel(rootCtx)
	m := &Manager{
		sessions:   make(map[string]*ManagedSession),
		backend:    backend,
		repo:       repo,
		msgs:       msgs,
		cfg:        cfg,
		rootCtx:    rootCtx,
		persistCtx: context.WithoutCancel(rootCtx),
		supCtx:     supCtx,
		supCancel:  supCancel,
		sysRing:    NewRing(sysRingSize),
	}
	// Nothing to reconnect without a backend (Snapshot/Close already guard
	// the same way) — every test that constructs a nil-backend Manager
	// (newTestManager and friends, dozens of call sites) would otherwise
	// gain a background goroutine with no purpose.
	if backend != nil {
		m.go1(func() { m.reconnectSupervisor(supCtx) })
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

// NewID returns a 16-byte crypto/rand hex identifier (sessions and messages).
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return hex.EncodeToString(b[:])
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
