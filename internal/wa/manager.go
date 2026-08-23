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

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/wasession"
)

// Config is the manager slice of the app configuration.
type Config struct {
	SendRate     float64 // token bucket refill, messages/second/session
	SendBurst    int     // token bucket capacity
	ReconcilePar int     // boot reconnection parallelism
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
}

// NewManager wires the supervisor. Call StartAll afterwards to reconcile
// persisted sessions, and Close (after http.Server.Shutdown) to tear down.
// backend may be nil in tests that never activate a session (see Snapshot's
// nil guard); production always passes one (wa.NewWhatsmeowBackend today).
func NewManager(rootCtx context.Context, backend Backend,
	repo wasession.Repository, msgs message.Repository, cfg Config) *Manager {
	return &Manager{
		sessions:   make(map[string]*ManagedSession),
		backend:    backend,
		repo:       repo,
		msgs:       msgs,
		cfg:        cfg,
		rootCtx:    rootCtx,
		persistCtx: context.WithoutCancel(rootCtx),
	}
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
