package wa

import (
	"context"
	"fmt"
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
			// active hydrates from the persisted intent regardless of whether
			// the boot-time reconnect below (toConnect) actually succeeds —
			// see reconnect.go's eligibility check. If it does not (e.g. the
			// backend errors on this very first Connect attempt), the
			// reconnect supervisor still needs to see this session as
			// eligible on its own next scan tick; gating this on the boot
			// Activate call's own success (which only flips it true itself)
			// would otherwise strand a row.Active==true session that failed
			// to reconnect at boot exactly the way it stranded post-boot.
			active: row.Active,
			// isMaster hydrates from the persisted routing flag — without
			// this, the flag survives in the database but every restart
			// silently un-flags the session in memory, failing OPEN and
			// putting the master back into automatic-selection rotation.
			isMaster: row.IsMaster,
			// groupCode hydrates RAW from the stored row — deliberately NOT
			// re-normalized through NormalizeGroupCode here. A hand-edited
			// row (e.g. "Group X", written directly against the database)
			// then matches no Route's GroupCode and stays OUT of the
			// automatic pool: it fails CLOSED, and the raw value is still
			// visible in the UI so an operator can fix it. Re-normalizing
			// and silently dropping an invalid value on error would instead
			// move that session INTO the ungrouped pool — the fail-OPEN
			// direction, and a worse mistake here than missing is_master's
			// hydration above: an unhydrated is_master merely lets a master
			// numbers get picked, but an unhydrated/mis-normalized
			// group_code silently returns every scoped handset in the
			// fleet to the default automatic-routing pool on every
			// restart.
			groupCode: row.GroupCode,
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

	// Manager-level event, not tied to any one session — the boot
	// reconciliation itself is what the system log page (RecordEvent's own
	// doc comment) exists to show alongside every session's own events.
	m.RecordEvent("info", fmt.Sprintf("boot reconciliation: %d session(s) loaded, %d queued for reconnect (parallelism %d)",
		len(rows), len(toConnect), m.cfg.ReconcilePar))

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
	m.supCancel() // stop the reconnect supervisor (reconnect.go) FIRST — it is not keyed to
	// any one session's ctx (it is manager-wide, on purpose — see its doc
	// comment), so nothing below would otherwise ever tell its scan loop to
	// stop, and m.wg.Wait() below would hang forever on it.
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
