package message

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
)

// analytics_test exercises the read-side queries against the same SQLite engine the
// rest of the WSM unit suite uses, which is what proves the SUBSTR/COUNT(CASE ...)
// SQL is portable off MySQL.

func newTestRepo(t *testing.T) (*SQLRepository, context.Context) {
	t.Helper()
	pool, err := db.Open(filepath.Join(t.TempDir(), "test.db"), db.SQLiteDialect)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	ctx := context.Background()
	if err := db.EnsureSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"sess-1", "sess-2"} {
		if _, err := pool.ExecContext(ctx,
			`INSERT INTO wa_sessions (id, name, phone, created_at, updated_at)
			 VALUES (?, ?, '628000', '2026-07-01T00:00:00Z', '2026-07-01T00:00:00Z')`,
			s, s); err != nil {
			t.Fatal(err)
		}
	}
	return NewRepository(pool), ctx
}

var idSeq int

// seed returns the id it minted for m, so a test that needs to read the row
// back by id (GetByID) doesn't have to guess the msg-NNNN sequence itself.
func (r *SQLRepository) seed(t *testing.T, ctx context.Context, m Message) string {
	t.Helper()
	idSeq++
	m.ID = fmt.Sprintf("msg-%04d", idSeq)
	if m.SessionID == "" {
		m.SessionID = "sess-1"
	}
	if err := r.Insert(ctx, &m); err != nil {
		t.Fatal(err)
	}
	return m.ID
}

func day(offset int) time.Time { return time.Now().UTC().AddDate(0, 0, offset) }

func TestTotalsAndBreakdown(t *testing.T) {
	r, ctx := newTestRepo(t)
	r.seed(t, ctx, Message{Status: StatusSent, ClientRef: "clA", APIClientID: "c1", Category: CategoryNotification, CreatedAt: day(0)})
	r.seed(t, ctx, Message{Status: StatusSent, ClientRef: "clA", APIClientID: "c1", Category: CategoryNotification, CreatedAt: day(0)})
	r.seed(t, ctx, Message{Status: StatusFailed, ClientRef: "clA", APIClientID: "c1", Category: CategoryNotification, CreatedAt: day(0)})
	r.seed(t, ctx, Message{Status: StatusSent, ClientRef: "clB", CreatedAt: day(-1)}) // no consumer
	r.seed(t, ctx, Message{Status: StatusQueued, SessionID: "sess-2", CreatedAt: day(-1)})

	tot, err := r.Totals(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if tot.Total != 5 || tot.Sent != 3 || tot.Failed != 1 || tot.Queued != 1 {
		t.Fatalf("totals = %+v, want total5 sent3 failed1 queued1", tot)
	}

	byClient, err := r.Breakdown(ctx, Filter{}, "client_ref", 20)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]BreakdownRow{}
	for _, b := range byClient {
		got[b.Key] = b
	}
	if got["clA"].Total != 3 || got["clA"].Sent != 2 || got["clA"].Failed != 1 {
		t.Fatalf("clA breakdown = %+v", got["clA"])
	}
	if got["clB"].Total != 1 || got[""].Total != 1 { // "" = un-attributed (the queued sess-2 row)
		t.Fatalf("clB=%+v none=%+v", got["clB"], got[""])
	}

	// Filtering by status narrows every query.
	sent, _ := r.Totals(ctx, Filter{Status: StatusSent})
	if sent.Total != 3 {
		t.Fatalf("sent-filtered total = %d, want 3", sent.Total)
	}
	// Filtering by consumer.
	byC1, _ := r.Breakdown(ctx, Filter{APIClientID: "c1"}, "status", 20)
	if len(byC1) != 2 { // sent + failed
		t.Fatalf("c1 status groups = %d, want 2", len(byC1))
	}
}

func TestSeriesGapFill(t *testing.T) {
	r, ctx := newTestRepo(t)
	r.seed(t, ctx, Message{Status: StatusSent, CreatedAt: day(0)})
	r.seed(t, ctx, Message{Status: StatusFailed, CreatedAt: day(0)})
	r.seed(t, ctx, Message{Status: StatusSent, CreatedAt: day(-2)})

	pts, err := r.Series(ctx, Filter{}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 3 {
		t.Fatalf("series len = %d, want 3 (gap-filled)", len(pts))
	}
	last := pts[len(pts)-1]
	if last.Total != 2 || last.Sent != 1 || last.Failed != 1 {
		t.Fatalf("today point = %+v, want total2 sent1 failed1", last)
	}
	if pts[1].Total != 0 { // the middle day had no traffic
		t.Fatalf("gap day total = %d, want 0", pts[1].Total)
	}
}

func TestListKeysetPagination(t *testing.T) {
	r, ctx := newTestRepo(t)
	base := time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		r.seed(t, ctx, Message{Status: StatusSent, CreatedAt: base.Add(time.Duration(i) * time.Minute)})
	}
	first, next, err := r.List(ctx, Filter{}, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || next == "" {
		t.Fatalf("page1 len=%d next=%q, want 2 rows + a cursor", len(first), next)
	}
	// Newest first: the last-inserted minute leads.
	if !first[0].CreatedAt.After(first[1].CreatedAt) {
		t.Fatalf("page not ordered newest-first")
	}
	seen := map[string]bool{first[0].ID: true, first[1].ID: true}
	page, cur := first, next
	for cur != "" {
		page, cur, err = r.List(ctx, Filter{}, cur, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range page {
			if seen[row.ID] {
				t.Fatalf("row %s returned twice across pages", row.ID)
			}
			seen[row.ID] = true
		}
	}
	if len(seen) != 5 {
		t.Fatalf("paginated %d distinct rows, want 5", len(seen))
	}
}

func TestLatencySmoke(t *testing.T) {
	r, ctx := newTestRepo(t)
	var ids []string
	for i := 0; i < 3; i++ {
		idSeq++
		id := fmt.Sprintf("lat-%d", idSeq)
		ids = append(ids, id)
		m := &Message{ID: id, SessionID: "sess-1", ToPhone: "628", Body: "x",
			Status: StatusQueued, CreatedAt: time.Now().UTC().Add(-30 * time.Minute)}
		if err := r.Insert(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range ids {
		if err := r.MarkSent(ctx, id, "wamid", 1); err != nil {
			t.Fatal(err)
		}
	}
	pts, err := r.Latency(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 24 {
		t.Fatalf("latency buckets = %d, want 24", len(pts))
	}
	var nonZero bool
	for _, p := range pts {
		if p.P50 > 0 {
			nonZero = true
		}
	}
	if !nonZero {
		t.Fatalf("expected at least one bucket with a p50 latency")
	}
}
