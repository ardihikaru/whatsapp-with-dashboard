// Package backoff holds this codebase's one bounded exponential
// backoff+jitter schedule. It exists so a codebase with more than one
// retrying caller does not end up with more than one retry curve:
//
//   - internal/wa's per-session send retry loop
//     (internal/wa/sender_limits.go's backoffDelay/jitter) used it first.
//   - internal/waxum's HTTP client (client.go's retry-on-429 loop) needs the
//     same shape of schedule but cannot import internal/wa's copy directly:
//     internal/wa already imports internal/waxum (backend_waxum.go's
//     mapErr etc.), so the reverse import would be a cycle.
//
// Pulling the math out to a package below both breaks that cycle without
// duplicating the formula — see internal/waxum/client.go's retryAfterDelay
// and internal/wa/sender_limits.go's backoffDelay/jitter, both of which now
// just call this package.
package backoff

import (
	"math"
	"math/rand/v2"
	"time"
)

// Delay is the deterministic part of a retry schedule: min(30s, 1s·2ⁿ) for
// attempt n = 0,1,2… (jitter added separately via Jitter). Equivalent to
// DelayFrom(attempt, time.Second, 30*time.Second) — kept as its own
// exported function so the two existing call sites (internal/waxum's
// retry-on-429 loop, internal/wa's send retry loop) need no change.
func Delay(attempt int) time.Duration {
	return DelayFrom(attempt, time.Second, 30*time.Second)
}

// DelayFrom generalizes Delay to an arbitrary base and ceiling: min(max,
// base·2ⁿ) for attempt n = 0,1,2… It exists so a caller that needs a
// LOOSER curve than Delay's fixed 1s/30s — e.g. internal/wa's reconnect
// supervisor (reconnect.go), which must not retry sooner than waxum's own
// 30s reconnect-watchdog cadence — reuses this package's one formula
// instead of hand-rolling a second copy of it (see this file's package doc
// comment for why that duplication is the thing to avoid). base<=0 falls
// back to 1s (Delay's own base) and max<=0 or max<base is clamped to base,
// so a zero-value caller never divides by zero or returns a negative/zero
// ceiling.
func DelayFrom(attempt int, base, max time.Duration) time.Duration {
	if base <= 0 {
		base = time.Second
	}
	if max <= 0 || max < base {
		max = base
	}
	d := float64(base) * math.Exp2(float64(attempt))
	if d > float64(max) {
		return max
	}
	return time.Duration(d)
}

// Jitter decorrelates retries across concurrent callers (sessions, or
// concurrent waxum requests): U(0, 500ms).
func Jitter() time.Duration {
	return time.Duration(rand.Int64N(int64(500 * time.Millisecond)))
}
