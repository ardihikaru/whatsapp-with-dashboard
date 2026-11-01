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

	// Media (0004 columns). All optional: "" / 0 mean "no media", i.e. a plain
	// text send. The caption is NOT here — it reuses Body, the same column a
	// text message already has (see migrations/0004's header).
	MediaKind      string // image | video | audio | document ("" for text)
	MediaFilename  string
	MediaMime      string
	MediaSize      int64
	MediaObjectKey string // this app's own MinIO archive key, independent of waxum's copy
}

// Categories a send may declare. Unknown values are clamped to CategoryNotification
// so the log's category filter always sees a closed set.
const (
	CategoryOTP          = "otp"
	CategoryNotification = "notification"
	CategoryAlert        = "alert"
	CategoryManual       = "manual"
	// CategorySelfConvo marks a send that belongs to a self-convo run (two of
	// this deployment's own paired sessions exchanging a scripted
	// conversation; see internal/selfconvo). It is deliberately NOT part of
	// NormalizeCategory's accepted set below: that value is stamped only by
	// the self-convo gateway adapter (internal/selfconvo/gateway_wa.go),
	// never taken from a caller-supplied field, so an ordinary API/UI send
	// cannot claim this category for itself. NormalizeCategory keeps
	// clamping "self_convo" from an untrusted caller to the fallback.
	CategorySelfConvo = "self_convo"
	// CategoryGroupBroadcast marks a send that belongs to a group-broadcast
	// occurrence (internal/groupbroadcast) — the sender's opener or a
	// receiver's reply into a real WhatsApp group. Same reasoning as
	// CategorySelfConvo above: stamped only by that package's gateway
	// adapter (internal/groupbroadcast/gateway_wa.go), never taken from a
	// caller-supplied field, and NormalizeCategory clamps it from an
	// untrusted caller the same way.
	CategoryGroupBroadcast = "group_broadcast"
	// CategorySessionAlert marks a send that belongs to internal/
	// sessionalert — a logged-out alert or the daily non-connected report,
	// posted into an operator-configured WhatsApp group. Same reasoning as
	// CategorySelfConvo/CategoryGroupBroadcast above: stamped only by that
	// package's gateway adapter, never taken from a caller-supplied field,
	// and NormalizeCategory clamps "session_alert" from an untrusted
	// caller the same way.
	CategorySessionAlert = "session_alert"
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
