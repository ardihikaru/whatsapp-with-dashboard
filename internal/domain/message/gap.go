package message

import (
	"context"
	"time"
)

// GapStats summarises the spacing between consecutive SUCCESSFUL sends on one
// session, computed from the real sent_at timestamps already sitting in
// wa_messages. This is an INFERENCE from observed outcomes — the closest
// durable evidence of how internal/wa/sender_limits.go's per-session token
// bucket actually paced real traffic — not a trace of the limiter's internal
// decisions; a burst can legitimately produce a gap narrower than the
// configured floor without anything being wrong. See
// internal/handler/outgoing_chat.go's doc comment for how this is presented to
// an operator.
type GapStats struct {
	SessionID string
	// Count is the number of gaps observed (samples - 1). 0 means fewer than
	// two successful sends were found, so Min/Max/Avg are all zero.
	Count         int
	Min, Max, Avg time.Duration
	LastSentAt    *time.Time
}

// GapStats reads the last `sample` successful sends for one session — newest
// first, reversed to chronological order — and computes the MIN/MAX/AVERAGE
// gap between consecutive sends in Go, the same shape Latency (series.go)
// uses for its own per-bucket percentile math and for the same reason: this
// delta computation is awkward and non-portable in SQL. Bounding by a sample
// COUNT rather than a time window (mirroring Latency's own choice) means a
// low-volume session still gets a meaningful sample and a high-volume one
// never triggers an unbounded scan.
//
// Failed sends never set sent_at (MarkFailed, repository.go) and so are
// correctly excluded here — a retry/backoff delay on a failed attempt is a
// different signal (Attempts on the row), not folded into the successful-send
// cadence.
func (r *SQLRepository) GapStats(ctx context.Context, sessionID string, sample int) (GapStats, error) {
	if sample <= 0 || sample > 1000 {
		sample = 200
	}
	rows, err := r.pool.QueryContext(ctx,
		`SELECT sent_at FROM wa_messages
		 WHERE session_id = ? AND status = ? AND sent_at IS NOT NULL
		 ORDER BY sent_at DESC LIMIT ?`, sessionID, StatusSent, sample)
	if err != nil {
		return GapStats{}, err
	}
	defer rows.Close()
	var times []time.Time
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return GapStats{}, err
		}
		t, perr := time.Parse(timeLayout, raw)
		if perr != nil {
			continue
		}
		times = append(times, t)
	}
	if err := rows.Err(); err != nil {
		return GapStats{}, err
	}

	out := GapStats{SessionID: sessionID}
	if len(times) == 0 {
		return out, nil
	}
	// times arrived newest-first (ORDER BY sent_at DESC); reverse to
	// chronological order so consecutive deltas are actual send-to-send gaps.
	for i, j := 0, len(times)-1; i < j; i, j = i+1, j-1 {
		times[i], times[j] = times[j], times[i]
	}
	last := times[len(times)-1]
	out.LastSentAt = &last
	if len(times) < 2 {
		return out, nil
	}

	out.Min = times[1].Sub(times[0])
	out.Max = out.Min
	var total time.Duration
	for i := 1; i < len(times); i++ {
		gap := times[i].Sub(times[i-1])
		if gap < out.Min {
			out.Min = gap
		}
		if gap > out.Max {
			out.Max = gap
		}
		total += gap
	}
	out.Count = len(times) - 1
	out.Avg = total / time.Duration(out.Count)
	return out, nil
}
