// Package inbound is the persistence model for customer replies (the
// wa_inbound_messages table) — GET /api/v1/inbound's data source. This
// package mirrors internal/domain/call's shape on purpose: same
// Repository/SQLRepository split, same nullIfEmpty convention, same
// RFC3339-string time layout. See migrations/0006_inbound_messages.sql's
// header for why this is a separate table from wa_messages rather than more
// columns bolted onto it.
package inbound

import "time"

// Message is one row of wa_inbound_messages: a single customer reply,
// possibly stamped with the client_ref/tenant_ref of the outbound message it
// most likely answers.
type Message struct {
	ID        string // the WhatsApp message id itself — the natural idempotency key
	SessionID string
	FromPhone string // normalized digits; EMPTY when FromLID could not be resolved
	FromJID   string
	// FromLID is the raw @lid sender identity waxum reports. Kept even when
	// FromPhone resolves successfully so a resolution bug is diagnosable
	// from the row itself, not just from logs at the time of arrival — see
	// migrations/0006_inbound_messages.sql's header for the production fact
	// (every observed waxum inbound sender_jid is a LID, not a phone JID)
	// that makes this column exist at all.
	FromLID string
	Body    string

	// ReceivedAt comes from the WhatsApp envelope, not time.Now() — the
	// consumer's since= window (GET /api/v1/inbound?since=...) is only
	// meaningful if it measures when the customer actually sent the
	// message, not when this gateway happened to persist it.
	ReceivedAt time.Time

	// ClientRef/TenantRef are the STAMP: copied from the most recent
	// outbound wa_messages row to the same phone within a bounded window
	// (see Repository.LatestOutboundRefs), never supplied by whatever
	// produced the arrival. Empty means "no product may claim this message"
	// — the safe default per the brief's "tier stamp is the security
	// property" section.
	ClientRef string
	TenantRef string
	// RepliesToMessageID records which wa_messages.id the stamp came from,
	// so a stamped row is auditable back to the specific send it was
	// attributed to.
	RepliesToMessageID string

	CreatedAt time.Time
}

// Refs is the attribution triple read off the most recent matching outbound
// wa_messages row, returned by LatestOutboundRefs. A zero Refs (all empty
// strings) paired with found=false means no candidate existed in the window
// — the caller must leave the arrival un-stamped, not guess.
type Refs struct {
	ClientRef string
	TenantRef string
	// MessageID is the wa_messages.id the refs were read from, destined for
	// Message.RepliesToMessageID.
	MessageID string
}
