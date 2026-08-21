package auth

import (
	"sync"
	"time"
)

// Throttle rate-limits failed logins per client IP.
//
// With SSO gone there is exactly ONE credential in front of the entire browser
// surface, and every attempt costs a bcrypt compare (~80ms of CPU at the
// default cost) inside a process that must also keep a WhatsApp websocket
// alive. Unthrottled, /login is simultaneously a brute-force target and a cheap
// way to starve the send pumps.
//
// The bucket math mirrors internal/wa/sender_limits.go, which cannot be
// imported here: its bucket type is unexported and its file pulls in whatsmeow.
type Throttle struct {
	rate  float64 // tokens per second
	burst float64

	mu    sync.Mutex
	byKey map[string]*bucketState
}

type bucketState struct {
	n    float64
	last time.Time
}

// NewThrottle allows burst attempts immediately, then one more every 1/rate
// seconds.
func NewThrottle(rate float64, burst int) *Throttle {
	return &Throttle{
		rate:  rate,
		burst: float64(burst),
		byKey: make(map[string]*bucketState),
	}
}

// Take reports how long the caller must wait; 0 means "go ahead".
//
// A nil Throttle never throttles, so a Deps built without one — every existing
// handler test — behaves exactly as it did before.
//
// now is a parameter rather than a time.Now() call so the tests can drive the
// refill deterministically, the same reason sender_limits.go takes one.
func (t *Throttle) Take(key string, now time.Time) time.Duration {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	b, ok := t.byKey[key]
	if !ok {
		t.byKey[key] = &bucketState{n: t.burst - 1, last: now}
		return 0
	}
	b.n += now.Sub(b.last).Seconds() * t.rate
	if b.n > t.burst {
		b.n = t.burst
	}
	b.last = now

	if b.n >= 1 {
		b.n--
		// A key that has refilled completely carries no state worth keeping.
		// Dropping it here is the whole eviction story for a map keyed by
		// attacker-controlled IPs — no janitor goroutine needed.
		if b.n >= t.burst-1 {
			delete(t.byKey, key)
		}
		return 0
	}
	return time.Duration((1 - b.n) / t.rate * float64(time.Second))
}

// Reset drops a key's counter after a successful login, so a fumbled password
// does not follow the operator around for the next few minutes.
func (t *Throttle) Reset(key string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	delete(t.byKey, key)
	t.mu.Unlock()
}
