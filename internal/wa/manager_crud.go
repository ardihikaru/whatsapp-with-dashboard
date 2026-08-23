package wa

import (
	"context"
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
// (backend.Logout deletes the credentials but emits no events.LoggedOut),
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
	s.mu.Unlock()

	// backend is nil only in tests that never pair/attach a session (see
	// NewManager's doc comment); nothing for Logout to do in that case.
	if m.backend != nil {
		_ = m.backend.Logout(ctx, s) // errors are logged inside; delete proceeds regardless
	}
	return m.repo.Delete(ctx, id)
}

// ValidationError marks user-input failures so handlers can render them
// inline (400 / form error) instead of as server errors.
type ValidationError struct{ Field, Msg string }

func (e *ValidationError) Error() string { return e.Msg }
