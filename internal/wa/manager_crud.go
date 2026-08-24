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
	//
	// A failed Logout does NOT block the local delete — matching
	// meowBackend.Logout's own "log and force local cleanup" posture
	// (backend_meow.go) — the WSM row is removed regardless. But the
	// failure MUST be surfaced, not silently dropped: during a live
	// throttling incident, DELETE calls to waxum were rate-limited (429),
	// the error was discarded here, and wa_sessions went to 0 rows while
	// waxum still held 3 sessions — two with real WhatsApp credentials —
	// permanently orphaned with nothing in the UI or logs to show it. A
	// transient 429 no longer reaches here at all in the common case
	// (internal/waxum.Client.do retries one with backoff before returning);
	// this is what happens once those retries are exhausted, or for any
	// other Logout failure. slog.Warn gives operational visibility;
	// s.ring is this package's own assertable per-session event log
	// (ring.go) — used elsewhere for exactly this "note that a test can
	// check without capturing slog output" purpose — see
	// TestDeletingASessionSurfacesAFailedBackendLogoutInsteadOfSilentlyOrphaningIt.
	if m.backend != nil {
		if err := m.backend.Logout(ctx, s); err != nil {
			slog.Warn("backend logout failed during delete; the WSM row is being removed anyway but the backend may still hold this session",
				"session", id, "backend", m.backend.Name(), "err", err)
			s.ring("delete: backend logout failed (possibly orphaned in " + m.backend.Name() + "): " + err.Error())
		}
	}
	return m.repo.Delete(ctx, id)
}

// ValidationError marks user-input failures so handlers can render them
// inline (400 / form error) instead of as server errors.
type ValidationError struct{ Field, Msg string }

func (e *ValidationError) Error() string { return e.Msg }
