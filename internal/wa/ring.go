package wa

import (
	"sync"
	"time"
)

// ringSize is the per-session event log capacity (docs/04 §5).
const ringSize = 256

// Event is one ring entry: a timestamped note about a whatsmeow event or a
// lifecycle transition.
//
// SessionID/SessionName/Level are additive (Manager.RecentEvents /
// internal/handler's system log page — see reconnect.go and manager.go):
// every per-session ring still holds exactly what it always held (a plain
// Note against a session it's already scoped to, so those two fields are
// "" there), and TestRingWraparound/TestRingConcurrent (ring_test.go)
// construct Event with named fields and keep passing unchanged. Level is a
// plain string ("info"/"warn" today, see ManagedSession.ring/ringLevel) —
// not its own type — because the ONLY consumer is the system log page
// rendering it as text; a closed enum would buy nothing over that and
// would need touching every call site that logs today.
type Event struct {
	At          time.Time
	Note        string
	SessionID   string // "" for a manager-level event with no single session in scope
	SessionName string // "" alongside SessionID; kept alongside it so the page never needs a live lookup for a session that may since have been deleted
	Level       string // "info" (default) or "warn" — see ManagedSession.ringLevel
}

// Ring is a fixed-capacity, drop-oldest event log. Append is O(1) and both
// operations are mutex-guarded, so whatsmeow's socket-loop callbacks never
// block on it.
type Ring struct {
	mu    sync.Mutex
	buf   []Event
	next  int // next write index
	count int // min(writes, len(buf))
}

// NewRing allocates a ring holding the last n events.
func NewRing(n int) *Ring {
	return &Ring{buf: make([]Event, n)}
}

// Append records an event, overwriting the oldest entry when full.
func (r *Ring) Append(e Event) {
	r.mu.Lock()
	r.buf[r.next] = e
	r.next = (r.next + 1) % len(r.buf)
	if r.count < len(r.buf) {
		r.count++
	}
	r.mu.Unlock()
}

// Snapshot returns a chronological copy for rendering.
func (r *Ring) Snapshot() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Event, 0, r.count)
	start := (r.next - r.count + len(r.buf)) % len(r.buf)
	for i := 0; i < r.count; i++ {
		out = append(out, r.buf[(start+i)%len(r.buf)])
	}
	return out
}
