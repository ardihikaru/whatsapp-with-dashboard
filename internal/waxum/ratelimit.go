package waxum

import (
	"context"
	"math"
	"sync"
	"time"
)

// defaultClientRate/defaultClientBurst bound how many requests THIS
// process's Client will send to waxum per second, across every session and
// pump that shares it — cmd/app/main.go constructs exactly one *Client and
// hands it to NewWaxumBackend, so every statusPump/pairPump in the process
// funnels through one clientBucket. This is defense-in-depth, not the
// primary fix: docker-compose.yml raising waxum's own RATE_LIMIT_PER_SECOND
// /RATE_LIMIT_BURST well above what this app needs is what actually stops
// the 429s (see CLAUDE.md's waxum rate-limit hazard note), and pairPump's
// point 7 (backend_waxum.go) already cut this app's own worst-case request
// rate by no longer polling GET /qr every tick. This bucket exists for what
// neither of those guarantees: a future extra pump, an operator who has not
// applied the compose change yet, or several sessions pairing/reconnecting
// at once, must not be able to burst this process past a rate ANY
// reasonably-configured waxum would throttle — regardless of how many
// pumps exist. 20 req/s sustained with a burst of 30 is comfortably below
// docker-compose.yml's documented RATE_LIMIT_BURST=200 (RATE_LIMIT_PER_SECOND
// there is a replenish INTERVAL, not a rate, so it isn't the number to
// compare against req/s — see that file's comment on the waxum service)
// (leaving headroom for waxum's own webhook or health traffic) and
// comfortably above what this app's pollers legitimately need even with
// several sessions active at once (statusPump every 15s, pairPump's status
// every WSM_WAXUM_QR_POLL_INTERVAL — 3s by default — and QR only when
// actually needed, point 7).
const (
	defaultClientRate  = 20.0
	defaultClientBurst = 30
)

// clientBucket is a plain token bucket, the same refill formula as
// internal/wa/sender_limits.go's bucket (tokens = min(burst, tokens +
// Δt·rate); one token per request). It is NOT shared with that type
// directly — sender.go's bucket paces OUTBOUND WhatsApp sends per SESSION,
// a different rate and a different purpose from this client-wide HTTP
// throttle, and internal/waxum cannot import internal/wa's copy anyway
// (see this package's doc comment on the import direction). The formula
// itself is simple enough (a handful of lines) that duplicating it here is
// cheaper than a third shared package for two structurally different call
// sites — internal/backoff was worth sharing because backoffDelay/jitter
// are TIME SCHEDULES both callers must agree on to avoid two conflicting
// retry curves; a steady-state rate limit has no such "must match" concern.
type clientBucket struct {
	mu     sync.Mutex
	rate   float64 // tokens per second
	burst  float64
	tokens float64
	last   time.Time
}

func newClientBucket(rate float64, burst int) *clientBucket {
	if rate <= 0 {
		rate = defaultClientRate
	}
	if burst < 1 {
		burst = defaultClientBurst
	}
	return &clientBucket{rate: rate, burst: float64(burst), tokens: float64(burst), last: time.Now()}
}

// wait blocks until a token is available (returning nil) or ctx ends
// (returning ctx.Err()) — do() checks this before every attempt, including
// retries, since each is a real outgoing request.
func (b *clientBucket) wait(ctx context.Context) error {
	for {
		b.mu.Lock()
		now := time.Now()
		b.tokens = math.Min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rate)
		b.last = now
		if b.tokens >= 1 {
			b.tokens--
			b.mu.Unlock()
			return nil
		}
		wait := time.Duration((1 - b.tokens) / b.rate * float64(time.Second))
		b.mu.Unlock()
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
