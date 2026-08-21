// Package wasession is the persistence model for WhatsApp gateway sessions
// (the wa_sessions table). The live state machine lives in internal/wa; this
// package only stores what must survive a restart.
package wasession

import "time"

// Session is one row of wa_sessions.
type Session struct {
	ID             string // 16-byte crypto/rand hex
	Name           string
	Phone          string // intended own number, normalized digits
	JID            string // own WhatsApp JID, set on pairing success ("" until paired)
	Active         bool   // DESIRED state (user intent), not live connectivity
	LastStatus     string // informational: new|pairing|connected|disconnected|logged_out
	CreatedAt      time.Time
	UpdatedAt      time.Time
	LastSelectedAt *time.Time
}
