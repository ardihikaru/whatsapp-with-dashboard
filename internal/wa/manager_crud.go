package wa

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/wasession"
)

// Create validates, persists (state=new, active=0), and registers a session.
func (m *Manager) Create(ctx context.Context, name, phone string) (*ManagedSession, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, &ValidationError{Field: "name", Msg: "name is required"}
	}
	digits, err := NormalizePhone(phone)
	if err != nil {
		return nil, &ValidationError{Field: "phone", Msg: err.Error()}
	}
	row := &wasession.Session{
		ID: NewID(), Name: name, Phone: digits, LastStatus: string(StateNew),
	}
	if err := m.repo.Insert(ctx, row); err != nil {
		return nil, err
	}
	s := &ManagedSession{
		ID: row.ID, Name: name, Phone: digits, CreatedAt: time.Now().UTC(),
		mgr: m, state: StateNew, events: NewRing(ringSize),
	}
	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()
	return s, nil
}

// Delete is the full teardown: map removal FIRST (no new HTTP work can reach
// the session), then sender stop + ctx cancel, then unlink from the phone
// (Logout deletes the whatsmeow store rows but emits no events.LoggedOut),
// and finally the app row — wa_messages cascade via FK.
func (m *Manager) Delete(ctx context.Context, id string) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()
	if !ok {
		return ErrNotFound
	}

	s.mu.Lock()
	s.stopSenderLocked()
	if s.cancel != nil {
		s.cancel() // ends pairing pump / pending I/O
	}
	cli := s.client
	s.client = nil
	paired := cli != nil && cli.Store.ID != nil
	s.mu.Unlock()

	if cli != nil {
		if paired {
			if err := cli.Logout(ctx); err != nil {
				slog.Warn("logout failed; forcing local cleanup", "session", id, "err", err)
				cli.Disconnect()
				if err := m.container.DeleteDevice(ctx, cli.Store); err != nil {
					slog.Warn("device purge failed", "session", id, "err", err)
				}
			}
		} else {
			cli.Disconnect()
		}
	}
	return m.repo.Delete(ctx, id)
}

// ValidationError marks user-input failures so handlers can render them
// inline (400 / form error) instead of as server errors.
type ValidationError struct{ Field, Msg string }

func (e *ValidationError) Error() string { return e.Msg }
