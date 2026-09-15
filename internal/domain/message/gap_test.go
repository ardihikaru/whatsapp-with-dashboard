package message

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
)

func newGapTestRepo(t *testing.T) (*SQLRepository, context.Context) {
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
	if _, err := pool.ExecContext(ctx,
		`INSERT INTO wa_sessions (id, name, phone, created_at, updated_at)
		 VALUES ('sess-1','Gateway 1','628000','2026-07-01T00:00:00Z','2026-07-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	return NewRepository(pool), ctx
}

// TestGapStatsComputesMinMaxAvgFromRealSentAtTimestamps seeds four successful
// sends on one session at known, distinct gaps (1s, 3s, 2s apart) directly via
// sent_at and asserts GapStats' Count/Min/Max/Avg match the arithmetic
// exactly. This is the test that actually exercises the delta computation —
// it fails if the query's ORDER BY/LIMIT is wrong, if the newest-first rows
// are not reversed to chronological order before differencing, or if the
// Avg divisor is off by one.
func TestGapStatsComputesMinMaxAvgFromRealSentAtTimestamps(t *testing.T) {
	repo, ctx := newGapTestRepo(t)

	base := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	offsets := []time.Duration{0, 1 * time.Second, 4 * time.Second, 6 * time.Second} // gaps: 1s, 3s, 2s
	for i, d := range offsets {
		ts := base.Add(d).Format(timeLayout)
		if _, err := repo.pool.ExecContext(ctx,
			`INSERT INTO wa_messages (id, session_id, to_phone, body, status, created_at, sent_at)
			 VALUES (?, 'sess-1', '628111000', 'hi', 'sent', ?, ?)`,
			fmt.Sprintf("m%d", i), ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	// A failed send has no sent_at and must not be counted.
	if _, err := repo.pool.ExecContext(ctx,
		`INSERT INTO wa_messages (id, session_id, to_phone, body, status, created_at)
		 VALUES ('mfail', 'sess-1', '628222000', 'x', 'failed', ?)`, base.Format(timeLayout)); err != nil {
		t.Fatal(err)
	}

	stats, err := repo.GapStats(ctx, "sess-1", 200)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Count != 3 {
		t.Fatalf("Count = %d, want 3", stats.Count)
	}
	if stats.Min != time.Second {
		t.Fatalf("Min = %v, want 1s", stats.Min)
	}
	if stats.Max != 3*time.Second {
		t.Fatalf("Max = %v, want 3s", stats.Max)
	}
	if stats.Avg != 2*time.Second {
		t.Fatalf("Avg = %v, want 2s", stats.Avg)
	}
	want := base.Add(6 * time.Second)
	if stats.LastSentAt == nil || !stats.LastSentAt.Equal(want) {
		t.Fatalf("LastSentAt = %v, want %v", stats.LastSentAt, want)
	}
}

// TestGapStatsWithFewerThanTwoSuccessfulSendsHasNoGaps confirms a session
// with zero or exactly one successful send reports Count 0 (and a zero
// Min/Max/Avg) rather than dividing by zero or panicking on an empty slice.
func TestGapStatsWithFewerThanTwoSuccessfulSendsHasNoGaps(t *testing.T) {
	repo, ctx := newGapTestRepo(t)

	none, err := repo.GapStats(ctx, "sess-1", 200)
	if err != nil {
		t.Fatal(err)
	}
	if none.Count != 0 || none.LastSentAt != nil {
		t.Fatalf("zero sends: GapStats = %+v, want Count 0 and nil LastSentAt", none)
	}

	sentAt := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC).Format(timeLayout)
	if _, err := repo.pool.ExecContext(ctx,
		`INSERT INTO wa_messages (id, session_id, to_phone, body, status, created_at, sent_at)
		 VALUES ('m0', 'sess-1', '628111000', 'hi', 'sent', ?, ?)`, sentAt, sentAt); err != nil {
		t.Fatal(err)
	}
	one, err := repo.GapStats(ctx, "sess-1", 200)
	if err != nil {
		t.Fatal(err)
	}
	if one.Count != 0 {
		t.Fatalf("one send: Count = %d, want 0", one.Count)
	}
	if one.LastSentAt == nil {
		t.Fatal("one send: LastSentAt is nil, want the single sent_at")
	}
}

// TestGapStatsScopesToTheGivenSession proves the WHERE session_id=? clause
// actually filters — a session with a tight cadence must not see another
// session's sends bleed into its own Min/Max/Avg.
func TestGapStatsScopesToTheGivenSession(t *testing.T) {
	repo, ctx := newGapTestRepo(t)
	if _, err := repo.pool.ExecContext(ctx,
		`INSERT INTO wa_sessions (id, name, phone, created_at, updated_at)
		 VALUES ('sess-2','Gateway 2','629000','2026-07-01T00:00:00Z','2026-07-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	// sess-1: two sends, 10s apart.
	for i, d := range []time.Duration{0, 10 * time.Second} {
		ts := base.Add(d).Format(timeLayout)
		if _, err := repo.pool.ExecContext(ctx,
			`INSERT INTO wa_messages (id, session_id, to_phone, body, status, created_at, sent_at)
			 VALUES (?, 'sess-1', '628111000', 'hi', 'sent', ?, ?)`,
			fmt.Sprintf("s1-%d", i), ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	// sess-2: two sends, 1s apart — a much tighter cadence.
	for i, d := range []time.Duration{0, 1 * time.Second} {
		ts := base.Add(d).Format(timeLayout)
		if _, err := repo.pool.ExecContext(ctx,
			`INSERT INTO wa_messages (id, session_id, to_phone, body, status, created_at, sent_at)
			 VALUES (?, 'sess-2', '629111000', 'hi', 'sent', ?, ?)`,
			fmt.Sprintf("s2-%d", i), ts, ts); err != nil {
			t.Fatal(err)
		}
	}

	s1, err := repo.GapStats(ctx, "sess-1", 200)
	if err != nil {
		t.Fatal(err)
	}
	if s1.Avg != 10*time.Second {
		t.Fatalf("sess-1 Avg = %v, want 10s (sess-2's 1s gap must not leak in)", s1.Avg)
	}
}
