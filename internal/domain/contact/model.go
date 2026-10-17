// Package contact is the persistence model for WSM's owner-managed WhatsApp
// contact book — a global (not per-session) name/phone/aliases entry with a
// last-known WhatsApp verification status. See
// migrations/0012_contacts.sql's header for the schema this package sits on.
package contact

import (
	"errors"
	"time"
)

// Status is the last-known WhatsApp registration verdict for a Contact's
// phone. "unknown" is deliberately distinct from "not_registered" — a
// routing/backend failure during verification must leave a contact here,
// never claim "not_registered" for a number nobody actually got an answer
// about (same rule as contactVerdict's own doc comment,
// internal/handler/contacts_check.go).
type Status string

const (
	StatusRegistered    Status = "registered"
	StatusNotRegistered Status = "not_registered"
	StatusUnknown       Status = "unknown"
)

// Contact is one row of wa_contacts. Phone is CANONICAL digits
// (wa.NormalizePhone's output). Aliases is a small, free-text list of
// extra names a ranked search (internal/contactmatch) also matches
// against — callers are expected to pass it already lowercased; this
// package does not enforce that itself, the same way wa_known_groups does
// not re-validate GroupJID's canonical form on every read.
type Contact struct {
	ID         string
	Name       string
	Phone      string
	Aliases    string
	JID        string
	WAStatus   Status
	VerifiedAt time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

var (
	// ErrNotFound is returned by any lookup/mutation keyed on an id that has
	// no matching row.
	ErrNotFound = errors.New("contact: not found")
	// ErrDuplicate is returned by Insert when phone already has a row
	// (migrations/0012's UNIQUE constraint) — the book is global, so the
	// same number cannot be saved twice under two different names.
	ErrDuplicate = errors.New("contact: a contact with this phone number already exists")
)
