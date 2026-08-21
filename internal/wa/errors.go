package wa

import "errors"

// Sentinel errors HTTP handlers map to statuses (docs/08):
// ErrNotFound→404, ErrWrongState/ErrNotPaired/ErrDeviceGone→409,
// ErrNotConnected→409, ErrQueueFull→429, ErrNotAGroupJID→400.
var (
	ErrNotFound     = errors.New("session not found")
	ErrNotPaired    = errors.New("session not paired yet")
	ErrDeviceGone   = errors.New("whatsmeow device missing for paired session (re-pair required)")
	ErrWrongState   = errors.New("operation not allowed in the current session state")
	ErrNotConnected = errors.New("session not connected")
	ErrQueueFull    = errors.New("send queue full")
	ErrNotAGroupJID = errors.New("not a group jid — expected an id ending in @g.us")
)
