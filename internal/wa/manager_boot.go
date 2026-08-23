package wa

import (
	"context"
	"log/slog"

	"go.mau.fi/whatsmeow/types"
)

// StartAll is the boot reconciliation (docs/04 §6): rebuild the in-memory map
// from wa_sessions, reset rows that crashed mid-pairing (PairingState is
// memory-only), and reconnect active paired sessions with bounded
// parallelism. It never blocks boot — connections converge in background.
func (m *Manager) StartAll(ctx context.Context) error {
	rows, err := m.repo.All(ctx)
	if err != nil {
		return err
	}

	var toConnect []*ManagedSession
	for _, row := range rows {
		s := &ManagedSession{
			ID: row.ID, Name: row.Name, Phone: row.Phone, CreatedAt: row.CreatedAt,
			LastSelectedAt: row.LastSelectedAt,
			mgr:            m, state: StateDisconnected, events: NewRing(ringSize),
		}
		switch {
		case row.JID == "":
			s.state = StateNew
		default:
			jid, err := types.ParseJID(row.JID)
			if err != nil {
				slog.Warn("bad stored jid; treating as unpaired", "session", s.ID, "jid", row.JID)
				s.state = StateNew
				break
			}
			s.jid = jid
			if row.LastStatus == string(StatePairing) { // crashed mid-pairing: nothing was persisted
				s.state, s.jid = StateNew, types.EmptyJID
				_ = m.repo.UpdateStatus(ctx, s.ID, string(StateNew))
			}
			if row.LastStatus == string(StateLoggedOut) {
				s.state = StateLoggedOut
			}
		}
		if row.LastStatus == string(StatePairing) && row.JID == "" {
			_ = m.repo.UpdateStatus(ctx, s.ID, string(StateNew)) // stale pairing, never paired
		}
		m.sessions[s.ID] = s // boot is single-threaded here; no lock contention yet
		if row.Active && !s.jid.IsEmpty() && s.state != StateLoggedOut {
			toConnect = append(toConnect, s)
		}
	}

	sem := make(chan struct{}, m.cfg.ReconcilePar)
	for _, s := range toConnect {
		s := s
		sem <- struct{}{}
		m.go1(func() {
			defer func() { <-sem }()
			if err := s.Activate(m.rootCtx); err != nil {
				slog.Warn("boot reconnect failed", "session", s.ID, "err", err)
			}
		})
	}
	return nil
}

// Close tears everything down after http.Server.Shutdown: stop senders,
// cancel session contexts (ends pairing pumps), disconnect the backend's
// transport for each session, then join every tracked goroutine so main may
// close the DB safely afterwards. Finally releases backend-level resources
// (e.g. whatsmeow's sqlite container).
func (m *Manager) Close() {
	if !m.closed.CompareAndSwap(false, true) {
		return
	}
	m.mu.Lock()
	all := make([]*ManagedSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.mu.Unlock()

	for _, s := range all {
		s.mu.Lock()
		s.stopSenderLocked()
		if s.cancel != nil {
			s.cancel()
		}
		s.mu.Unlock()
		if m.backend != nil {
			_ = m.backend.Disconnect(m.persistCtx, s)
		}
	}
	m.wg.Wait()
	if m.backend != nil {
		m.backend.Close()
	}
}
