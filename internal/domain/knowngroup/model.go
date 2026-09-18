// Package knowngroup is the persistence model for a session's known
// WhatsApp groups — a small friendly-name-to-JID CRUD, fed by the existing
// live GET /api/v1/sessions/{id}/groups endpoint. See
// migrations/0010_known_groups.sql for the schema this package sits on.
package knowngroup

import (
	"errors"
	"time"
)

// Group is one row of wa_known_groups. SessionID is a snapshot, no FK, same
// reasoning as every other session reference in this schema (see
// migrations/0007_self_convo.sql's header). GroupJID is stored in its
// CANONICAL form (wa.ParseGroupJID's output) so every reader compares apples
// to apples regardless of what an operator originally pasted into the form.
type Group struct {
	ID        string
	SessionID string
	GroupJID  string
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

var (
	// ErrNotFound is returned by any lookup/mutation keyed on an id that has
	// no matching row.
	ErrNotFound = errors.New("knowngroup: not found")
	// ErrDuplicate is returned by Insert when (session_id, group_jid)
	// already has a row — the same session cannot save the same group
	// twice under two different names (migrations/0010's UNIQUE
	// constraint).
	ErrDuplicate = errors.New("knowngroup: session already has this group saved")
)
