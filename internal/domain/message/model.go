// Package message is the persistence model for the outbound message audit
// log (the wa_messages table). Every send — UI or API — leaves a row here.
package message

import "time"

// Statuses a message row moves through. There is no "sending": the row is
// queued from insert until the worker records the terminal outcome.
const (
	StatusQueued = "queued"
	StatusSent   = "sent"
	StatusFailed = "failed"
)

// Message is one row of wa_messages.
type Message struct {
	ID          string
	SessionID   string
	ToPhone     string // normalized digits
	Body        string
	Status      string // queued | sent | failed
	WAMessageID string
	Error       string
	Attempts    int
	CreatedAt   time.Time
	SentAt      *time.Time

	// Attribution — who/what this send is for. All optional (0001+0002 columns
	// are nullable); an un-attributed send leaves them empty.
	APIClientID string // api_clients.id of the bearer principal ("" for the /send UI)
	ClientRef   string // laundry client uid (caller-supplied)
	TenantRef   string // laundry tenant/outlet uid (caller-supplied)
	Category    string // otp | notification | alert | manual
}

// Categories a send may declare. Unknown values are clamped to CategoryNotification
// so the log's category filter always sees a closed set.
const (
	CategoryOTP          = "otp"
	CategoryNotification = "notification"
	CategoryAlert        = "alert"
	CategoryManual       = "manual"
)

// Attribution is the caller-supplied slice of a send's provenance, threaded from the
// handlers down to the single Insert. APIClientID is NOT here: it is set from the
// authenticated principal, never from the request body, so a client cannot spoof it.
type Attribution struct {
	ClientRef string
	TenantRef string
	Category  string
}

// NormalizeCategory clamps an arbitrary category string to the closed set, applying
// fallback when empty or unknown.
func NormalizeCategory(c, fallback string) string {
	switch c {
	case CategoryOTP, CategoryNotification, CategoryAlert, CategoryManual:
		return c
	default:
		return fallback
	}
}
