// Package groupbroadcast is the planner/runner for Group Broadcast
// Schedule — one WSM session ("sender") posting an opening message into a
// real WhatsApp group at a random morning time, and a configurable list of
// other sessions ("receivers") replying into that same group at their own
// random later time. It is a NEW, PARALLEL package to internal/selfconvo,
// reusing that package's PATTERNS (day-level planning, per-day idempotency,
// health gating, disjoint-bucket time jitter) but not its code — the two
// stay decoupled the same way internal/domain/convo and
// internal/domain/groupbroadcast stay decoupled (see that package's own
// doc comment). See the phase plan's Confirmed decisions section for every
// place this package's behaviour deliberately diverges from selfconvo's own
// precedent — most importantly the per-receiver (not all-or-nothing) health
// gate and the same-session minimum-gap rule.
package groupbroadcast

import (
	"context"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// SendRequest is one turn's send, already resolved to a concrete session
// and a group destination. Unlike internal/selfconvo's own SendRequest
// (ToDigits, a phone number), the destination here is a group JID —
// ToGroupJID's type is wa.JID rather than a plain string so gateway_wa.go's
// Dispatch call is exactly the same destination-agnostic path
// POST /api/v1/messages/group already uses, with no string-to-JID
// conversion of its own to get wrong.
type SendRequest struct {
	SessionID  string
	ToGroupJID wa.JID
	Text       string
	// OccurrenceID stamps the audit row's client_ref, so the message log
	// can be filtered to one occurrence's sends the same way self-convo
	// runs filter by run id.
	OccurrenceID string
	// MessageID is minted by the runner and already stored on the turn via
	// ClaimTurn BEFORE this call — see internal/selfconvo's own
	// SendRequest.MessageID doc comment for why that ordering matters on a
	// crash mid-send.
	MessageID string
}

// SendReceipt is what a successful Send returns.
type SendReceipt struct {
	MessageID   string
	WAMessageID string
}

// Participant is the slice of a session this package's health checks and
// sends need — mirrors internal/selfconvo.Participant.
type Participant struct {
	ID        string
	Name      string
	Phone     string
	Connected bool
}

// Gateway is the runner's only way to reach a live WhatsApp session — an
// interface, not a direct *wa.Manager dependency, for the exact reason
// internal/selfconvo/gateway.go's own doc comment gives: wa.Snapshot.Connected
// cannot be fabricated outside package wa, and the runner's positive send
// path must still be unit-testable without a real WhatsApp transport.
// NewManagerGateway (gateway_wa.go) is the only production implementation.
type Gateway interface {
	// Participant reports a session's identity and whether it can send
	// right now. ok is false when id names no known session.
	Participant(id string) (Participant, bool)

	// Send delivers one text message through the named session into the
	// group and blocks until the terminal result is known — same contract
	// as internal/selfconvo.Gateway.Send.
	Send(ctx context.Context, req SendRequest) (SendReceipt, error)

	// Healthy reports whether every named session can send right now. The
	// PLANNER (planner.go) deliberately calls this ONE ID AT A TIME for
	// every receiver — never batched the way internal/selfconvo's planner
	// batches sender+every receiver into one call — because an unhealthy
	// receiver must be skipped alone, not stall the whole occurrence (see
	// planner.go's own doc comment for the full health-gating rule this
	// implements). It is still batchable for the sender-only check
	// (Healthy([]string{senderID})).
	Healthy(ids []string) (sessionID, reason string, ok bool)
}
