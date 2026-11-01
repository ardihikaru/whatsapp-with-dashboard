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
	GroupCode string
	// GroupCodes is the FULL set (0015's wa_session_groups), loaded by
	// All/GroupCodesByID alongside GroupCode above. GroupCode is kept as a
	// cached projection (the first element here, sorted) so an old binary
	// still running between `migrate` and `up` keeps reading/writing it
	// unchanged — see 0015_session_groups_self_reminder.sql's header. A
	// plain Insert (no SetGroupCodes call) leaves this nil/empty, matching
	// GroupCode's own "" default.
	GroupCodes []string
	// IsSelfReminder names the one session, per host, every alert this app
	// sends defaults to posting FROM (internal/sessionalert). Exclusivity
	// is enforced by the application (Manager.SetSelfReminder), not a DB
	// index — see 0015's header.
	IsSelfReminder bool
	LastStatus     string // informational: new|pairing|connected|disconnected|logged_out
	CreatedAt      time.Time
	UpdatedAt      time.Time
	LastSelectedAt *time.Time
}
