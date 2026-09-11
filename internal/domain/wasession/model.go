// Package wasession is the persistence model for WhatsApp gateway sessions
// (the wa_sessions table). The live state machine lives in internal/wa; this
// package only stores what must survive a restart.
package wasession

import "time"

// Session is one row of wa_sessions.
type Session struct {
	ID    string // 16-byte crypto/rand hex
	Name  string
	Phone string // intended own number, normalized digits
	JID   string // own WhatsApp JID, set on pairing success ("" until paired)
	// Active is DESIRED state (user intent), not live connectivity.
	Active bool
	// IsMaster is ROUTING intent: never picked by automatic session
	// selection (internal/wa/load_balance.go) unless a caller opts in. This
	// is deliberately separate from Active's CONNECTION intent — a master
	// session still stays connected and remains directly sendable; it is
	// only excluded from the automatic candidate pool.
	IsMaster bool
	// GroupCode is a ROUTING SCOPE parallel to IsMaster, not a second
	// exclusion flag: "" is the ungrouped default pool every existing row
	// keeps; a non-empty code removes the session from default automatic
	// routing entirely (including a global master_only pick) and makes it
	// reachable only by a request naming the same code — see
	// internal/wa.EligibleForRoute. Normalised ([a-z0-9_-], lowercase, max
	// 32) by wa.NormalizeGroupCode, the only writer (via
	// ManagedSession.SetGroupCode); this field carries whatever is stored,
	// raw.
	GroupCode      string
	LastStatus     string // informational: new|pairing|connected|disconnected|logged_out
	CreatedAt      time.Time
	UpdatedAt      time.Time
	LastSelectedAt *time.Time
}
