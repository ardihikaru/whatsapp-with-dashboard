package wa

import (
	"context"
	"log/slog"
	"sync"
	"time"

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
	ID, Name, Phone string    // immutable after construction
	CreatedAt       time.Time // immutable; drives stable snapshot ordering
	LastSelectedAt  *time.Time

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
}

// Snapshot copies the live status. Connected asks the backend's Live (a
// cheap, non-I/O check — see Backend.Live) so a half-dead socket shows as its
// real state on the next poll. This runs on every 2s HTMX row poll AND on
// every send's load-balancing pick (load_balance.go), so it must stay O(1)
// no matter which backend answers.
func (s *ManagedSession) Snapshot() Snapshot {
	s.mu.Lock()
	snap := Snapshot{
		ID: s.ID, Name: s.Name, Phone: s.Phone, CreatedAt: s.CreatedAt,
		LastSelectedAt: cloneTime(s.LastSelectedAt),
		State:          s.state,
		QueueLen:       len(s.sendQ),
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

// setState commits a transition and persists last_status. Callers hold NO
// locks. Persistence uses persistCtx so it still lands during teardown.
func (s *ManagedSession) setState(st State) {
	s.mu.Lock()
	if s.state == st {
		s.mu.Unlock()
		return
	}
	s.state = st
	s.mu.Unlock()
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
	_ = s.mgr.repo.SetActive(s.mgr.persistCtx, s.ID, true)
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
	s.mu.Unlock()

	_ = s.mgr.backend.Disconnect(ctx, s)
	_ = s.mgr.repo.SetActive(s.mgr.persistCtx, s.ID, false)
	_ = s.mgr.repo.UpdateStatus(s.mgr.persistCtx, s.ID, string(StateDisconnected))
	return nil
}

// markDeviceGone handles E8: paired row without a whatsmeow device (DB file
// swapped/pruned). Terminal until the operator re-pairs. Called by the
// backend when its Connect finds no device for a paired jid.
func (s *ManagedSession) markDeviceGone() {
	s.setState(StateLoggedOut)
	_ = s.mgr.repo.SetActive(s.mgr.persistCtx, s.ID, false)
	s.ring("device record missing — re-pair required")
}
