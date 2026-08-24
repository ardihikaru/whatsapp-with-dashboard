package wa

import (
	"context"
	"errors"
	"math"
	"time"

	"go.mau.fi/whatsmeow"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/backoff"
)

// bucket is the pure token-bucket core (docs/10): tokens = min(burst,
// tokens + Δt·rate); one token per send; when short, wait (1−tokens)/rate.
// Time is passed in so tests drive it without sleeping.
type bucket struct {
	rate   float64 // tokens per second
	burst  float64 // capacity
	tokens float64
	last   time.Time
}

func newBucket(rate float64, burst int, now time.Time) *bucket {
	if rate <= 0 {
		rate = 1
	}
	if burst < 1 {
		burst = 1
	}
	return &bucket{rate: rate, burst: float64(burst), tokens: float64(burst), last: now}
}

// take consumes one token if available and returns 0; otherwise it returns
// how long the caller must wait before trying again.
func (b *bucket) take(now time.Time) time.Duration {
	b.tokens = math.Min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rate)
	b.last = now
	if b.tokens < 1 {
		return time.Duration((1 - b.tokens) / b.rate * float64(time.Second))
	}
	b.tokens--
	return 0
}

// backoffDelay is the deterministic part of the retry schedule:
// min(30s, 1s·2ⁿ) for attempt n = 0,1,2… (jitter added separately). Kept as
// a thin wrapper around internal/backoff (the shared schedule — see that
// package's doc comment for why it exists) so every existing call site and
// test in this file needs no change.
func backoffDelay(attempt int) time.Duration {
	return backoff.Delay(attempt)
}

// jitter decorrelates retries across sessions: U(0, 500ms). See
// backoffDelay's comment.
func jitter() time.Duration {
	return backoff.Jitter()
}

// isRetryable classifies send errors. Timeouts retry (at-least-once — the
// dedup risk is documented, E10); unknown errors fail fast so the cause lands
// in wa_messages.error.
func isRetryable(err error) bool {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return true
	case errors.Is(err, ErrNotConnected), errors.Is(err, whatsmeow.ErrNotConnected):
		return true // transient drop; autoreconnect may restore between attempts
	case errors.Is(err, whatsmeow.ErrNotLoggedIn):
		return false // permanent: needs re-pair
	default:
		return false
	}
}
