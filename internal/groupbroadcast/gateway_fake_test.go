package groupbroadcast

import (
	"context"
	"math/rand/v2"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
	gb "github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/groupbroadcast"
)

// fakeClock is a manually advanced clock, injected as Now so every test
// controls exactly what the planner/runner considers "now" — mirrors
// internal/selfconvo's own fakeClock.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{now: t} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fakeGateway mirrors internal/selfconvo's own fakeGateway — Participant/
// Send/Healthy doubles, with per-session unhealthy overrides and a
// configurable Send error.
type fakeGateway struct {
	mu           sync.Mutex
	participants map[string]Participant
	sends        []SendRequest
	sendErr      error
	unhealthy    map[string]string
}

func newFakeGateway() *fakeGateway {
	return &fakeGateway{participants: make(map[string]Participant)}
}

func (g *fakeGateway) seed(p Participant) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.participants[p.ID] = p
}

func (g *fakeGateway) Participant(id string) (Participant, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	p, ok := g.participants[id]
	return p, ok
}

func (g *fakeGateway) Send(ctx context.Context, req SendRequest) (SendReceipt, error) {
	g.mu.Lock()
	g.sends = append(g.sends, req)
	n := len(g.sends)
	sendErr := g.sendErr
	g.mu.Unlock()
	if sendErr != nil {
		return SendReceipt{}, sendErr
	}
	return SendReceipt{MessageID: "msg-" + req.OccurrenceID + "-" + strconv.Itoa(n)}, nil
}

func (g *fakeGateway) setUnhealthy(id, reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.unhealthy == nil {
		g.unhealthy = map[string]string{}
	}
	g.unhealthy[id] = reason
}

func (g *fakeGateway) clearUnhealthy(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.unhealthy, id)
}

func (g *fakeGateway) Healthy(ids []string) (sessionID, reason string, ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, id := range ids {
		if r, bad := g.unhealthy[id]; bad {
			return id, r, false
		}
	}
	return "", "", true
}

func (g *fakeGateway) sendCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.sends)
}

func newDeterministicRand() *rand.Rand {
	return rand.New(rand.NewPCG(1, 2))
}

func newTestPool(t *testing.T) *gbTestPool {
	t.Helper()
	pool, err := db.Open(filepath.Join(t.TempDir(), "t.db"), db.SQLiteDialect)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	if err := db.EnsureSchema(context.Background(), pool); err != nil {
		t.Fatalf("db.EnsureSchema: %v", err)
	}
	return &gbTestPool{repo: gb.NewRepository(pool)}
}

// gbTestPool is a thin holder so newTestPool can return just what tests
// need (the repository) without importing database/sql into this file's
// signature soup.
type gbTestPool struct {
	repo *gb.Repository
}

// newTestRunner wires a Runner over a fresh SQLite pool and fake gateway.
func newTestRunner(t *testing.T, clock *fakeClock) (*Runner, *gb.Repository, *fakeGateway) {
	t.Helper()
	p := newTestPool(t)
	gw := newFakeGateway()
	r := New(p.repo, gw, RunnerOptions{Now: clock.Now, PollInterval: 5 * time.Second})
	return r, p.repo, gw
}

// newTestPlanner wires a Planner over a fresh SQLite pool and fake gateway
// — AI is left nil (fallback-only mode), matching a deployment where
// WSM_CODEX_CLI_* is not configured.
func newTestPlanner(t *testing.T, clock *fakeClock) (*Planner, *gb.Repository, *fakeGateway) {
	t.Helper()
	p := newTestPool(t)
	gw := newFakeGateway()
	pl := NewPlanner(p.repo, gw, PlannerOptions{
		Now: clock.Now, Rand: newDeterministicRand(), PollInterval: time.Minute,
	})
	return pl, p.repo, gw
}

// newDailySchedule builds a minimal always-active daily schedule with a
// sender window early morning and a receiver window shortly after —
// mirrors internal/selfconvo's own newDailySchedule.
func newDailySchedule(id string, receiverIDs ...string) (*gb.Schedule, []gb.ScheduleReceiver) {
	sc := &gb.Schedule{
		ID: id, Name: id, SenderSessionID: "sess-sender", SenderName: "Sender", SenderPhone: "628110000001",
		GroupJID: "120363426585371342@g.us", GroupName: "Clino Team",
		Recurrence:          gb.RecurrenceDaily,
		SenderWindowStart:   "07:55",
		SenderWindowEnd:     "08:16",
		ReceiverWindowStart: "08:17",
		ReceiverWindowEnd:   "09:30",
		LateCutoff:          "08:30",
		LangIDWeight:        70,
		Timezone:            "UTC",
		StartDate:           "2026-09-01",
		State:               gb.ScheduleActive,
	}
	receivers := make([]gb.ScheduleReceiver, len(receiverIDs))
	for i, rid := range receiverIDs {
		receivers[i] = gb.ScheduleReceiver{SessionID: rid, SessionName: rid, Phone: "6281100000" + strconv.Itoa(i+2), Position: i}
	}
	return sc, receivers
}
