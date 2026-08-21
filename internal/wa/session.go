package wa

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
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

// ManagedSession pairs one wa_sessions row with at most one whatsmeow client.
// s.mu guards the mutable fields; hold it for state reads/writes and cheap
// calls only — never across Connect/SendMessage/Logout/PairPhone (docs/04 §2).
type ManagedSession struct {
	ID, Name, Phone string    // immutable after construction
	CreatedAt       time.Time // immutable; drives stable snapshot ordering
	LastSelectedAt  *time.Time

	mgr *Manager

	mu      sync.Mutex
	state   State
	jid     types.JID          // zero until paired
	client  *whatsmeow.Client  // nil unless attached
	cancel  context.CancelFunc // per-session ctx (child of mgr.rootCtx)
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

// Snapshot copies the live status. Connected cross-checks IsConnected so a
// half-dead socket shows as its real state on the next poll.
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
	snap.Connected = s.client != nil && s.client.IsConnected() && s.state == StateConnected
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

// attachClientLocked enforces the one-Client-per-Device invariant (hard rule
// 3). Called under s.mu; dev comes from container.GetDevice or NewDevice.
func (s *ManagedSession) attachClientLocked(dev *store.Device) (*whatsmeow.Client, error) {
	if s.client != nil {
		return nil, fmt.Errorf("session %s already has a client", s.ID)
	}
	cli := whatsmeow.NewClient(dev, newWhatsAppLogger(s.ID))
	cli.AddEventHandler(s.handleEvent) // registered once, never removed
	cli.AutoReconnectHook = func(error) bool {
		s.mu.Lock()
		dead := s.state == StateLoggedOut || s.client != cli
		s.mu.Unlock()
		return !dead // stop retrying once this client is discarded or logged out
	}
	ctx, cancel := context.WithCancel(s.mgr.rootCtx)
	s.client, s.ctx, s.cancel = cli, ctx, cancel
	return cli, nil
}

// detachClientLocked discards the client whole (the handler stays registered
// on the dead client — we never call RemoveEventHandler). Called under s.mu.
func (s *ManagedSession) detachClientLocked() {
	if s.cancel != nil {
		s.cancel()
	}
	s.client, s.ctx, s.cancel = nil, nil, nil
}

// Activate (re)connects an inactive-but-paired session and records the user
// intent (active=1). Idempotent while connected or pairing.
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
	cli := s.client
	if cli == nil {
		dev, err := s.mgr.container.GetDevice(ctx, s.jid)
		if err != nil || dev == nil {
			s.mu.Unlock()
			s.markDeviceGone()
			return ErrDeviceGone
		}
		if cli, err = s.attachClientLocked(dev); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	s.startSenderLocked()
	sctx := s.ctx
	s.mu.Unlock()

	if err := cli.ConnectContext(sctx); err != nil { // I/O unlocked; Connected event → setState
		s.setState(StateDisconnected) // whatsmeow auto-reconnect keeps trying
		return err
	}
	_ = s.mgr.repo.SetActive(s.mgr.persistCtx, s.ID, true)
	return nil
}

// Deactivate drops the socket and keeps credentials. Self-disconnect emits no
// whatsmeow event, so the transition happens here.
func (s *ManagedSession) Deactivate(ctx context.Context) error {
	s.mu.Lock()
	if s.jid.IsEmpty() && s.state != StateConnected {
		s.mu.Unlock()
		return ErrNotPaired
	}
	s.stopSenderLocked() // close(sendQ): worker drains, fails leftovers, exits
	cli := s.client
	s.state = StateDisconnected
	s.mu.Unlock()

	if cli != nil {
		cli.Disconnect()
	}
	_ = s.mgr.repo.SetActive(s.mgr.persistCtx, s.ID, false)
	_ = s.mgr.repo.UpdateStatus(s.mgr.persistCtx, s.ID, string(StateDisconnected))
	return nil
}

// markDeviceGone handles E8: paired row without a whatsmeow device (DB file
// swapped/pruned). Terminal until the operator re-pairs.
func (s *ManagedSession) markDeviceGone() {
	s.setState(StateLoggedOut)
	_ = s.mgr.repo.SetActive(s.mgr.persistCtx, s.ID, false)
	s.ring("device record missing — re-pair required")
}
