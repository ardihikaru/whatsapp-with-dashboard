package wa

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// State is the live session state: new → pairing → connected ⇄ disconnected,
// with logged_out terminal until re-pair (docs/04 §3).
type State string

const (
	StateNew          State = "new"
	StatePairing      State = "pairing"
	StateConnected    State = "connected"
	StateDisconnected State = "disconnected"
	StateLoggedOut    State = "logged_out"
)

// ManagedSession pairs one wa_sessions row with at most one live backend
// connection. s.mu guards the mutable fields; hold it for state reads/writes
// and cheap calls only — never across Connect/SendMessage/Logout/PairPhone
// (docs/04 §2).
//
// client/jid stay whatsmeow's own types rather than this package's JID (see
// jid.go): jid is the session's OWN paired identity, which can carry a
// nonzero whatsmeow Device (a linked companion device address) that round-
// trips through wa_sessions.jid on every reconnect via GetDevice — dropping
// it would pick the wrong device row. client is whatsmeow-specific full
// stop. Both are read directly by backend_meow.go (same package) under s.mu;
// keeping them here (rather than behind an opaque `any` slot) is what lets
// transport-agnostic code — Enqueue's admission check, trySend's connected
// check, Snapshot — stay simple, single-package, lock-scoped reads instead of
// routing every field access through the Backend interface. The trade a
// second backend pays for that is backend_meow.go reaching into fields it
// doesn't "own" in a strict sense; see backend.go for the fuller rationale.
type ManagedSession struct {
	ID, Phone      string    // immutable after construction
	CreatedAt      time.Time // immutable; drives stable snapshot ordering
	LastSelectedAt *time.Time

	// name is operator-editable (SetName), so unlike ID/Phone it is guarded -- by
	// its OWN lock, not mu: ring/ringLevel read it and may run with mu held.
	nameMu sync.RWMutex
	name   string

	mgr *Manager

	mu      sync.Mutex
	state   State
	jid     types.JID          // zero until paired; whatsmeow AD-JID, see comment above
	client  *whatsmeow.Client  // nil unless attached (backend_meow.go owns creation)
	cancel  context.CancelFunc // per-session ctx (child of mgr.rootCtx); see ensureCtxLocked
	ctx     context.Context
	pairing *PairingState // non-nil once pairing starts; replaced on restart
	sendQ   chan sendJob  // non-nil only while the sender runs
	events  *Ring         // never nil

	// active mirrors wa_sessions.active (operator/pairing-success INTENT
	// that this session stay connected) in memory, alongside the repo write
	// every site below already makes — Snapshot/state alone cannot answer
	// "does the operator want this reconnected", because a session can be
	// legitimately StateDisconnected either because it was deliberately
	// deactivated (Deactivate) or because the socket dropped out from under
	// an otherwise-desired-active session (the production bug reconnect.go
	// fixes). Set true by Activate and by a successful pairing
	// (onPaired/finishPairingSuccess — both persist active=1 via
	// SetPaired/SetActive already; this just keeps the in-memory copy in
	// sync so reconnectEligible below needs no repo round trip on its hot,
	// no-I/O path). Set false by Deactivate, markDeviceGone and
	// permanentDown, each of which already persists active=0.
	active bool

	// isMaster mirrors wa_sessions.is_master: ROUTING intent, distinct from
	// active's CONNECTION intent above. It excludes this session from
	// automatic selection (load_balance.go's SelectConnected /
	// SelectSnapshotForSend) unless a caller explicitly opts in; it does
	// NOT affect whether the session connects, reconnects, or can be sent
	// through directly by naming its id. Set by SetMaster below, which
	// touches only this field and its persisted column — never state, the
	// socket, or active.
	isMaster bool

	// groupCodes mirrors wa_session_groups (0015) — the FULL routing-scope
	// set, parallel to isMaster above and not a second exclusion flag.
	// nil/empty is the ungrouped default pool; a non-empty set removes this
	// session from default automatic selection (load_balance.go's
	// EligibleForRoute) entirely — including a global master_only pick —
	// and it becomes reachable by a Route naming ANY of these codes, not
	// just the first. Always kept sorted and de-duplicated by
	// SetGroupCodes, so Snapshot's "first code" projection (see
	// Snapshot.GroupCode) is deterministic. Set by SetGroupCode/
	// SetGroupCodes below, which touch only this field and its persisted
	// column(s) — never state, the socket, or active — exactly like
	// SetMaster.
	groupCodes []string

	// isSelfReminder mirrors wa_sessions.is_self_reminder (0015): the one
	// session, per host, every alert this app sends (internal/
	// sessionalert) defaults to posting FROM. Exclusivity across every
	// ManagedSession is enforced by Manager.SetSelfReminder, which is the
	// ONLY writer of this field — never set directly here, the same
	// division of labour isMaster has with SetMaster, except the "clear
	// every OTHER row" step requires the manager's map, not just this
	// session.
	isSelfReminder bool

	// stalled/stalledReason mark "needs operator attention" — the
	// reconnect supervisor (reconnect.go) has called Backend.Connect
	// ReconnectStallThreshold times in a row without the session ever
	// reaching StateConnected. This is NOT a new State: state stays
	// whatever it already was (StateDisconnected in every case the
	// supervisor produces today — see reconnectEligible), because
	// "stalled" answers a different question than the state machine does.
	// StateLoggedOut already means "terminal, re-pair required"; a stalled
	// session has NOT been told by the backend that it's logged out — it
	// is credentialed and, as far as WSM can tell, should reconnect on its
	// own, but repeated attempts have not brought the socket up (the
	// documented waxum-side limitation in reconnect.go's package doc
	// comment). Recasting that as StateLoggedOut would send an operator to
	// re-pair a device that may not need it at all, and would wrongly make
	// reconnectEligible (which excludes StateLoggedOut on purpose) stop
	// seeing it forever, even after a fix on waxum's side. A plain second
	// bool alongside `active` (both cheap, no-I/O, s.mu-guarded reads) is
	// the smallest change that lets the state machine stay exactly as
	// documented while still giving handlers something to render — see
	// Snapshot.Stalled/StalledReason below and markStalled/isStalled in
	// reconnect.go. Cleared only by a manual Activate (see Activate below)
	// — the operator must always be able to try again.
	stalled       bool
	stalledReason string
}

// Snapshot is the value-copy DTO handed to HTTP handlers — no locks or
// pointers escape the package.
type Snapshot struct {
	ID, Name, Phone string
	JID             string
	State           State
	Connected       bool
	QueueLen        int
	CreatedAt       time.Time
	LastSelectedAt  *time.Time
	LastEvents      []Event

	// Stalled/StalledReason mirror ManagedSession.stalled/stalledReason —
	// see that field's doc comment for why this is a flag alongside State
	// rather than a new State value. StalledReason is worded for an
	// operator to act on directly (see markStalled, reconnect.go), not a
	// Go error string.
	Stalled       bool
	StalledReason string

	// IsMaster mirrors ManagedSession.isMaster — see that field's doc
	// comment. Read by load_balance.go's exclusion predicate.
	IsMaster bool

	// GroupCode is the FIRST (lexically) code in GroupCodes below, or "" —
	// kept for every existing template/API consumer that reads a single
	// code (session_rows.html, apiSession.GroupCode). Prefer GroupCodes for
	// anything that must see the whole set.
	GroupCode string
	// GroupCodes mirrors ManagedSession.groupCodes — see that field's doc
	// comment. Read by load_balance.go's EligibleForRoute as the scope
	// match (membership, not equality).
	GroupCodes []string
	// SelfReminder mirrors ManagedSession.isSelfReminder — see that
	// field's doc comment.
	SelfReminder bool
}

// Snapshot copies the live status. Connected asks the backend's Live (a
// cheap, non-I/O check — see Backend.Live) so a half-dead socket shows as its
// real state on the next poll. This runs on every 2s HTMX row poll AND on
// every send's load-balancing pick (load_balance.go), so it must stay O(1)
// no matter which backend answers.
func (s *ManagedSession) Snapshot() Snapshot {
	s.mu.Lock()
	snap := Snapshot{
		ID: s.ID, Name: s.Name(), Phone: s.Phone, CreatedAt: s.CreatedAt,
		LastSelectedAt: cloneTime(s.LastSelectedAt),
		State:          s.state,
		QueueLen:       len(s.sendQ),
		Stalled:        s.stalled,
		StalledReason:  s.stalledReason,
		IsMaster:       s.isMaster,
		GroupCode:      firstGroupCode(s.groupCodes),
		GroupCodes:     append([]string(nil), s.groupCodes...),
		SelfReminder:   s.isSelfReminder,
	}
	if !s.jid.IsEmpty() {
		snap.JID = s.jid.String()
	}
	// mgr.backend is nil only in tests that never activate a session; guard
	// rather than require every stub to provide one.
	snap.Connected = s.state == StateConnected && s.mgr.backend != nil && s.mgr.backend.Live(s)
	s.mu.Unlock()
	snap.LastEvents = s.events.Snapshot()
	return snap
}

func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := *t
	return &v
}

// RingWarn appends a warn-level diagnostic note to this session's event ring
// (and the fleet-wide system ring) via ringLevel — the exact mechanism
// finishJob (sender.go) already uses for a failed send. Exported so callers
// OUTSIDE this package (internal/handler) can record a diagnostic note
// against a specific session for a failure that is not itself a send
// outcome or a state transition — e.g. Phase 6's best-effort MinIO archive
// write, which must never fail the send but should still be visible on the
// session's event ring and the system log page.
func (s *ManagedSession) RingWarn(note string) {
	s.ringLevel("warn", note)
}

// setState commits a transition and persists last_status. Callers hold NO
// locks. Persistence uses persistCtx so it still lands during teardown.
//
// Every transition is logged (slog) AND rung (s.ring) — this package's own
// production incident (reconnect.go's doc comment) was invisible from WSM's
// logs alone: waxum kept a session alive and authenticated for ten more
// minutes after WSM's last "disconnected" write, and nothing here recorded
// that the transition ever happened, let alone why. slog gives operational
// (container-log) visibility; the ring buffer is this package's own
// assertable per-session event log that ALSO reaches the operator directly
// (Snapshot.LastEvents, rendered on the sessions page) without them having
// to read container logs at all.
func (s *ManagedSession) setState(st State) {
	s.mu.Lock()
	old := s.state
	if old == st {
		s.mu.Unlock()
		return
	}
	s.state = st
	s.mu.Unlock()
	slog.Info("wa: state transition", "session", s.ID, "from", old, "to", st)
	s.ring(fmt.Sprintf("state: %s -> %s", old, st))
	if err := s.mgr.repo.UpdateStatus(s.mgr.persistCtx, s.ID, string(st)); err != nil {
		slog.Warn("persist status", "session", s.ID, "state", st, "err", err)
	}
}

// ensureCtxLocked lazily establishes the per-session ctx (child of
// mgr.rootCtx) the first time it's needed. Transport-agnostic: both backends
// share one cancellation lifecycle per session — startSenderLocked and the
// pairing pump both depend on s.ctx existing — independent of whether or how
// a backend represents a "client". Only client CREATION is backend-owned
// (attachClientLocked in backend_meow.go); this is not. Called under s.mu.
func (s *ManagedSession) ensureCtxLocked() context.Context {
	if s.ctx == nil {
		s.ctx, s.cancel = context.WithCancel(s.mgr.rootCtx)
	}
	return s.ctx
}

// Activate (re)connects an inactive-but-paired session and records the user
// intent (active=1). Idempotent while connected or pairing. The transport
// work (attach/reuse a client, dial) is Backend.Connect; this is the
// transport-agnostic policy around it: the state-machine guard and the
// active=1 persist stay here so a second backend gets them for free.
//
// A successful call ALSO clears stalled/stalledReason (see the field's doc
// comment on ManagedSession and reconnect.go's markStalled): this is the
// one path both the operator's UI/API "Activate" button and a manually
// retried automatic reconnect go through, so it is the natural place for
// "the operator asked to try again" to reset the supervisor's patience,
// regardless of whether THIS particular call actually revives the socket —
// see TestAManualActivateClearsTheStalledStateAndResumesRetrying
// (reconnect_test.go). Idempotent/harmless for a session that was never
// stalled.
func (s *ManagedSession) Activate(ctx context.Context) error {
	s.mu.Lock()
	if s.state == StateConnected || s.state == StatePairing {
		s.mu.Unlock()
		return nil
	}
	if s.jid.IsEmpty() {
		s.mu.Unlock()
		return ErrNotPaired
	}
	s.mu.Unlock()

	if err := s.mgr.backend.Connect(ctx, s); err != nil {
		return err
	}
	s.mu.Lock()
	s.active = true
	wasStalled := s.stalled
	s.stalled = false
	s.stalledReason = ""
	s.mu.Unlock()
	_ = s.mgr.repo.SetActive(s.mgr.persistCtx, s.ID, true)
	if wasStalled {
		s.ring("manual activate cleared the stalled flag; automatic reconnect attempts resume")
	}
	return nil
}

// Deactivate drops the socket and keeps credentials. Self-disconnect emits no
// backend event, so the transition happens here. The socket teardown itself
// is Backend.Disconnect; the sender stop, state transition and persistence
// are transport-agnostic and stay here.
func (s *ManagedSession) Deactivate(ctx context.Context) error {
	s.mu.Lock()
	if s.jid.IsEmpty() && s.state != StateConnected {
		s.mu.Unlock()
		return ErrNotPaired
	}
	s.stopSenderLocked() // close(sendQ): worker drains, fails leftovers, exits
	s.state = StateDisconnected
	s.active = false // operator intent: stop reconnecting — see reconnect.go's eligibility check
	s.mu.Unlock()

	_ = s.mgr.backend.Disconnect(ctx, s)
	_ = s.mgr.repo.SetActive(s.mgr.persistCtx, s.ID, false)
	_ = s.mgr.repo.UpdateStatus(s.mgr.persistCtx, s.ID, string(StateDisconnected))
	return nil
}

// SetMaster flips the ROUTING-intent flag (see isMaster's doc comment) and
// persists it. Unlike Activate/Deactivate this never touches state, the
// socket, or active — a master session stays connected and remains directly
// sendable; the flag only removes it from automatic selection's candidate
// pool.
func (s *ManagedSession) SetMaster(ctx context.Context, master bool) error {
	s.mu.Lock()
	s.isMaster = master
	s.mu.Unlock()
	if err := s.mgr.repo.SetMaster(s.mgr.persistCtx, s.ID, master); err != nil {
		return err
	}
	if master {
		s.ring("flagged as a master session — excluded from automatic send routing")
	} else {
		s.ring("unflagged as a master session — eligible for automatic send routing again")
	}
	return nil
}

// SetGroupCode assigns or clears this session's routing scope to a SINGLE
// code — a thin wrapper over SetGroupCodes kept for every existing caller
// that only ever dealt with one (PostSessionSetGroupCode's list/ops
// editors, PutAPISessionGroupCode, PostAPISessions). "" clears every group.
func (s *ManagedSession) SetGroupCode(ctx context.Context, code string) error {
	code = strings.TrimSpace(code)
	if code == "" {
		return s.SetGroupCodes(ctx, nil)
	}
	return s.SetGroupCodes(ctx, []string{code})
}

// firstGroupCode returns codes[0], or "" for an empty set — codes is
// assumed already sorted (SetGroupCodes' contract), so this is also the
// lexically-first code, matching Snapshot.GroupCode's doc comment.
func firstGroupCode(codes []string) string {
	if len(codes) == 0 {
		return ""
	}
	return codes[0]
}

// SetGroupCodes assigns or clears this session's FULL routing-scope set
// (see groupCodes' doc comment). Every code is normalized and de-duplicated
// FIRST, before any state or DB write — the same "validate everything, then
// change nothing on failure" contract SetGroupCode's callers already rely
// on. An empty/nil codes clears every group (back to the ungrouped pool).
// Like SetMaster this never touches state, the socket, or active.
func (s *ManagedSession) SetGroupCodes(ctx context.Context, codes []string) error {
	seen := map[string]bool{}
	var normalized []string
	for _, raw := range codes {
		code, err := NormalizeGroupCode(raw)
		if err != nil {
			return &ValidationError{Field: "group_codes", Msg: err.Error()}
		}
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		normalized = append(normalized, code)
	}
	sort.Strings(normalized)

	s.mu.Lock()
	s.groupCodes = normalized
	s.mu.Unlock()
	if err := s.mgr.repo.SetGroupCodes(s.mgr.persistCtx, s.ID, normalized); err != nil {
		return err
	}
	if len(normalized) > 0 {
		s.ring(fmt.Sprintf("routing groups set to %s — excluded from ungrouped automatic routing", strings.Join(normalized, ", ")))
	} else {
		s.ring("routing groups cleared — back in the ungrouped automatic routing pool")
	}
	return nil
}

// maxSessionNameLen mirrors the wa_sessions.name column (VARCHAR(190)).
const maxSessionNameLen = 190

// Name returns the session's display name. It is guarded by nameMu, not mu,
// because ring/ringLevel read it from paths that may already hold mu.
func (s *ManagedSession) Name() string {
	s.nameMu.RLock()
	defer s.nameMu.RUnlock()
	return s.name
}

// SetName renames the session (display name only). It validates BEFORE any
// state or DB write -- trimmed, non-empty, at most maxSessionNameLen runes --
// and returns a *ValidationError{Field:"name"} otherwise, leaving the session
// untouched. waxum keeps the name it was given at CreateSession: it has no
// rename call, and WSM's name is purely cosmetic there.
func (s *ManagedSession) SetName(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return &ValidationError{Field: "name", Msg: "name is required"}
	}
	if utf8.RuneCountInString(name) > maxSessionNameLen {
		return &ValidationError{Field: "name", Msg: fmt.Sprintf("name must be at most %d characters", maxSessionNameLen)}
	}
	if err := s.mgr.repo.SetName(s.mgr.persistCtx, s.ID, name); err != nil {
		return err
	}
	s.nameMu.Lock()
	s.name = name
	s.nameMu.Unlock()
	return nil
}

// markDeviceGone handles E8: paired row without a whatsmeow device (DB file
// swapped/pruned). Terminal until the operator re-pairs. Called by the
// backend when its Connect finds no device for a paired jid.
func (s *ManagedSession) markDeviceGone() {
	s.setState(StateLoggedOut)
	s.mu.Lock()
	s.active = false // terminal: re-pair required, never auto-reconnected — see reconnect.go
	s.mu.Unlock()
	_ = s.mgr.repo.SetActive(s.mgr.persistCtx, s.ID, false)
	s.ring("device record missing — re-pair required")
}

// reconnectEligible reports whether the reconnect supervisor (reconnect.go)
// should treat s as a candidate this scan tick: the operator (or a
// successful pairing) still wants it connected (active), it has
// credentials (jid non-empty — paired), and the backend currently reports
// it down in a way that is NOT terminal. StateDisconnected only —
// deliberately excluding StateLoggedOut (needs a re-pair, not a reconnect;
// see mapWaxumStatus's doc comment in backend_waxum.go for how that state
// is now inferred), StateNew/StatePairing (no credential yet, or already
// mid-handshake) and StateConnected (nothing to do). No I/O; cheap enough
// to call every scan tick for every session.
func (s *ManagedSession) reconnectEligible() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active && !s.jid.IsEmpty() && s.state == StateDisconnected
}

// isStalled reports whether reconnect.go's supervisor has already given up
// automatically retrying s (see the stalled field's doc comment). Checked
// by the supervisor's scan loop as a SEPARATE gate from reconnectEligible,
// not folded into it — reconnectEligible answers "does the state machine
// say a reconnect makes sense", which stays true for a stalled session (see
// TestStalledIsNotConfusedWithLoggedOut); this answers "has the supervisor
// already decided retrying is futile for now".
func (s *ManagedSession) isStalled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stalled
}

// markStalled records that the supervisor is giving up automatic retries
// for s until a manual Activate (see that method's doc comment) resets it.
// reason must be worded for an operator to act on (see reconnect.go's call
// site) — this is the ONLY place that text is composed, so a vague reason
// here is a vague reason everywhere it is rendered.
func (s *ManagedSession) markStalled(reason string) {
	s.mu.Lock()
	s.stalled = true
	s.stalledReason = reason
	s.mu.Unlock()
	slog.Warn("wa: reconnect stalled — needs operator attention", "session", s.ID, "reason", reason)
	s.ringLevel("warn", "stalled: "+reason)
}
