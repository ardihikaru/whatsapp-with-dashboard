package selfconvo

import "context"

// SendRequest is one turn's send, already resolved to a concrete session
// and recipient by Expand — the runner never builds a message.Message
// itself (that stays a wa-package concern, behind Gateway.Send).
type SendRequest struct {
	SessionID string
	ToDigits  string
	Text      string
	// RunID stamps the audit row's client_ref, so /logs?client_ref=<run id>
	// and the paired session's inbound row (LatestOutboundRefs) both carry
	// it — see docs/brief-self-convo.md's Message Log decision.
	RunID string
	// MessageID is the wa_messages id the runner already minted and stored
	// via ClaimTurn BEFORE calling Send (runner.go's processRun) — Send
	// must use THIS id for the outbound message.Message rather than minting
	// its own, so the turn and its audit row are the same id from before
	// the send even starts. See runner.go's Recover for why that ordering
	// matters: a turn cut off mid-send otherwise has no id to reconcile
	// against on the next boot.
	MessageID string
}

// SendReceipt is what a successful Send returns: WSM's own audit-row id and
// the backend's message id, in case a caller (none today) needs to
// correlate the two.
type SendReceipt struct {
	MessageID   string
	WAMessageID string
}

// Gateway is the runner's only way to reach a live WhatsApp session — an
// interface, not a direct *wa.Manager dependency, because
// wa.Snapshot.Connected cannot be fabricated outside package wa (its state
// is unexported; see internal/handler/verify_ui.go's own doc comment on the
// same limitation) and the runner's POSITIVE path (a send that actually
// succeeds) must still be unit-testable without a real WhatsApp transport.
// NewManagerGateway (gateway_wa.go) is the only production implementation.
type Gateway interface {
	// Participant reports a session's identity and whether it can send
	// right now. ok is false when id names no known session.
	Participant(id string) (Participant, bool)

	// Send delivers one text message through the named session and blocks
	// until the terminal result (sent or failed) is known. A non-nil error
	// means the message did NOT reach WhatsApp — if an audit row was
	// created for the attempt, it is already marked failed before Send
	// returns.
	Send(ctx context.Context, req SendRequest) (SendReceipt, error)

	// MessageStatus reports the terminal status wa_messages recorded for
	// messageID — one of the message.Status* string values, DUPLICATED as
	// plain strings rather than importing internal/domain/message here (see
	// this file's own doc comment on why gateway_wa.go is the only file
	// that talks to package wa; the same reasoning keeps this file free of
	// domain-package imports too) — plus the row's stored reason when
	// status is "failed", and whether a row exists at all. found is false
	// only when messageID names no row at all: the genuinely unknown case
	// Recover (runner.go) falls back to "interrupted" for, as opposed to a
	// row that exists and says "queued" (also folded into that same
	// fallback — see Recover's doc comment).
	MessageStatus(ctx context.Context, messageID string) (status, reason string, found bool, err error)

	// Healthy reports whether every named session can send right now, for
	// the planner's pre-flight gate (PlanDay) and processRun's mid-run gate
	// for a scheduled run. sessionID/reason name the FIRST unhealthy session
	// found, so a stall reason can point at it directly; ok is true only
	// when every id passed.
	//
	// Read CLAUDE.md's "'Connected' means three different things here, and
	// they can disagree" hazard before touching this method. Healthy reads
	// exactly the same signal Participant does above — ManagedSession.
	// Snapshot's Connected field (wa/session.go), itself
	// `state == StateConnected && backend.Live(s)` — and nothing else. It
	// deliberately does NOT reach past Snapshot to check whether the
	// session's sender loop exists (Snapshot has no field for that; the
	// underlying s.sendQ != nil check is Manager-internal and unexported,
	// and this package's Gateway boundary intentionally never reaches past
	// Snapshot — see this file's own doc comment on why gateway_wa.go is
	// the only file that imports internal/wa at all). A "healthy" answer
	// here therefore does NOT guarantee a subsequent Send succeeds: a nil
	// send queue on an otherwise Connected session (the exact defect
	// TestAWaxumSessionCanSendAfterConnectingWithoutPairing, in package wa,
	// exists to catch) presents identically at this layer, and only Send's
	// own error return can tell the difference. Healthy exists to catch a
	// disconnected or wedged session before scheduling work for it, not to
	// make Send infallible — processRun's own Send error handling remains
	// the backstop for everything Healthy cannot see.
	Healthy(ids []string) (sessionID, reason string, ok bool)
}

// Message status strings MessageStatus returns — the same values
// internal/domain/message.StatusSent/StatusFailed/StatusQueued hold,
// duplicated here so this file never imports that package (see
// MessageStatus's own doc comment).
const (
	MessageStatusSent   = "sent"
	MessageStatusFailed = "failed"
	MessageStatusQueued = "queued"
)
