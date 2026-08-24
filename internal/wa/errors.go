package wa

import "errors"

// Sentinel errors HTTP handlers map to statuses (docs/08):
// ErrNotFound→404, ErrWrongState/ErrNotPaired/ErrDeviceGone→409,
// ErrNotConnected→409, ErrQueueFull→429, ErrBackendBusy→503,
// ErrNotAGroupJID→400.
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
)
