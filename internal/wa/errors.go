package wa

import "errors"

// Sentinel errors HTTP handlers map to statuses (docs/08):
// ErrNotFound→404, ErrWrongState/ErrNotPaired/ErrDeviceGone→409,
// ErrNotConnected→409, ErrQueueFull→429, ErrBackendBusy→503,
// ErrNotAGroupJID→400, ErrRejectedByBackend→422, ErrAlreadyExists→409,
// *ValidationError→400 (matched via errors.As, not errors.Is — it is a
// struct type carrying a per-field message, not a sentinel).
var (
	ErrNotFound     = errors.New("session not found")
	ErrNotPaired    = errors.New("session not paired yet")
	ErrDeviceGone   = errors.New("whatsmeow device missing for paired session (re-pair required)")
	ErrWrongState   = errors.New("operation not allowed in the current session state")
	ErrNotConnected = errors.New("session not connected")
	// ErrQueueFull means the per-session SEND queue (sender.go's bounded
	// channel, capacity sendQueueCap) is full — this session's own backlog
	// is too deep, independent of any upstream backend.
	ErrQueueFull = errors.New("send queue full")
	// ErrBackendBusy means the BACKEND itself (waxum's tower_governor,
	// keyed per peer IP — see backend_waxum.go's mapErr) is rate-limiting
	// this process, a wholly different condition from ErrQueueFull:
	// nothing about this session's own queue is full, and the backend is
	// expected to accept the request again shortly. Introduced after a
	// live incident where 429s from waxum were mapped onto ErrQueueFull,
	// whose "send queue full" message sent a production debugging session
	// looking at the wrong subsystem — see CLAUDE.md's waxum rate-limit
	// hazard note.
	ErrBackendBusy  = errors.New("whatsapp backend is rate limiting this request; retry shortly")
	ErrNotAGroupJID = errors.New("not a group jid — expected an id ending in @g.us")
	// ErrRejectedByBackend means the BACKEND validated the payload itself
	// and refused it — a bad mimetype, a malformed media descriptor, a
	// webhook callback URL waxum's SSRF guard refused — as opposed to
	// ErrWrongState (WSM's own session-state guard) or ErrNotConnected (no
	// live socket). Added for media/ops/webhook validation errors, which
	// waxum answers with 400, 415 or 422 (see backend_waxum.go's mapErr) —
	// until this, those three statuses had no case in mapErr at all and
	// surfaced as a bare *waxum.APIError, which read to the operator as
	// "internal server error" with no indication the request itself was
	// the problem. mapErr wraps this with waxum's own error message
	// (*waxum.APIError.Message()) whenever the body carries one, so
	// errors.Is(err, ErrRejectedByBackend) still matches but Error()
	// names the actual cause — see mapErr's doc comment on that case.
	ErrRejectedByBackend = errors.New("whatsapp backend rejected the request")
	// ErrAlreadyExists means the backend refused to create/register
	// something because an equivalent one already exists — e.g.
	// RegisterWebhook's duplicate-URL 409 (WebhookAdmin, Phase 6b). Both
	// this and ErrNotConnected are 409s that can originate from the SAME
	// generic mapErr case (waxum.ErrConflict), but read completely
	// differently to an operator: "session not connected" is not what
	// happened here. Any endpoint where a 409 genuinely means duplication,
	// not a connection-state conflict, must catch waxum.ErrConflict itself
	// BEFORE calling mapErr and return this instead of letting it fall
	// through to ErrNotConnected — see RegisterWebhook
	// (backend_waxum_webhooks.go) for the one caller that does.
	ErrAlreadyExists = errors.New("already exists (duplicate)")
)
