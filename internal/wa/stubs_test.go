package wa

import (
	"context"
	"sync"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/wasession"
)

// stubSessions records every persistence call the manager makes.
type stubSessions struct {
	mu       sync.Mutex
	rows     []wasession.Session
	statuses map[string][]string
	active   map[string][]bool
	master   map[string][]bool
	deleted  []string
}

func newStubSessions(rows ...wasession.Session) *stubSessions {
	return &stubSessions{rows: rows,
		statuses: map[string][]string{}, active: map[string][]bool{}, master: map[string][]bool{}}
}

func (s *stubSessions) All(context.Context) ([]wasession.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]wasession.Session(nil), s.rows...), nil
}

func (s *stubSessions) Insert(_ context.Context, row *wasession.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, *row)
	return nil
}

func (s *stubSessions) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted = append(s.deleted, id)
	return nil
}

func (s *stubSessions) UpdateStatus(_ context.Context, id, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses[id] = append(s.statuses[id], status)
	return nil
}

func (s *stubSessions) SetActive(_ context.Context, id string, active bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active[id] = append(s.active[id], active)
	return nil
}

func (s *stubSessions) SetMaster(_ context.Context, id string, master bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.master[id] = append(s.master[id], master)
	return nil
}

func (s *stubSessions) MarkSelected(_ context.Context, id string, at time.Time) error {
	return nil
}

func (s *stubSessions) SetPaired(_ context.Context, id, jid string) error {
	return s.UpdateStatus(nil, id, "paired:"+jid)
}

func (s *stubSessions) lastStatus(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	hist := s.statuses[id]
	if len(hist) == 0 {
		return ""
	}
	return hist[len(hist)-1]
}

// stubMsgs records the send worker's audit-trail writes.
type stubMsgs struct {
	mu     sync.Mutex
	sent   map[string]string // msgID → waMessageID
	failed map[string]string // msgID → error text

	// failedAttempts is MarkFailed's attempts argument, keyed the same as
	// failed above — added for dispatch_test.go, which asserts Dispatch's
	// enqueue-failure path calls MarkFailed with attempts 0 (it fails before
	// any send attempt is ever made, unlike senderLoop's own MarkFailed
	// calls after ≤3 real attempts).
	failedAttempts map[string]int

	// insertedIDs/insertedCategories record every Insert call in order —
	// added for dispatch_test.go's
	// TestDispatchInsertsTheAuditRowBeforeEnqueueingAndReturnsTheTerminalResult,
	// which asserts the audit row exists (id + category) BEFORE Dispatch
	// ever reaches Enqueue. insertErr, when set, makes Insert fail instead —
	// unused today but kept for a future insert-failure test rather than
	// adding a second stub type.
	insertedIDs        []string
	insertedCategories map[string]string // msgID → message.Category
	insertErr          error
}

func newStubMsgs() *stubMsgs {
	return &stubMsgs{sent: map[string]string{}, failed: map[string]string{},
		failedAttempts: map[string]int{}, insertedCategories: map[string]string{}}
}

func (m *stubMsgs) Insert(_ context.Context, msg *message.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.insertErr != nil {
		return m.insertErr
	}
	m.insertedIDs = append(m.insertedIDs, msg.ID)
	m.insertedCategories[msg.ID] = msg.Category
	return nil
}

func (m *stubMsgs) MarkSent(_ context.Context, id, waID string, _ int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent[id] = waID
	return nil
}

func (m *stubMsgs) MarkFailed(_ context.Context, id, errMsg string, attempts int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failed[id] = errMsg
	m.failedAttempts[id] = attempts
	return nil
}

func (m *stubMsgs) Recent(context.Context, string, int) ([]message.Message, error) {
	return nil, nil
}

func (m *stubMsgs) failedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.failed)
}

// newTestManager builds a Manager without a whatsmeow container — enough for
// every lifecycle path that stops before pairing I/O.
func newTestManager(repo wasession.Repository, msgs message.Repository) *Manager {
	return NewManager(context.Background(), nil, repo, msgs, nil,
		Config{SendRate: 1, SendBurst: 3, ReconcilePar: 2})
}
