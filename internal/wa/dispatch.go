package wa

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
)

// ErrNoMessageRepository means this Manager was built with a nil message
// repository (m.msgs) — every production call site
// (cmd/app/main.go's wa.NewManager) always passes a real one; only a test
// helper could construct the nil case, and Dispatch would otherwise panic
// on the first m.msgs.Insert call rather than failing cleanly.
var ErrNoMessageRepository = errors.New("wa: manager has no message repository")

// DispatchResult is Dispatch's outcome. Result == nil means the job was
// queued but no terminal result arrived within wait (or the caller's ctx
// ended first) — MsgID is still set in that case, since the audit row and
// the enqueue both already happened; only the wait for a terminal result
// gave up early.
type DispatchResult struct {
	MsgID  string
	Result *SendResult
}

// Dispatch is dispatchSend's tail (internal/handler/send.go) moved down one
// layer so a non-HTTP caller — the self-convo runner (internal/selfconvo),
// which has no *http.Request to hang the old method off of — shares the
// SAME audit row, per-session token bucket (sender_limits.go) and retry
// loop (sender.go's senderLoop) the HTTP handlers use, instead of growing a
// second send path next to it. Manager already holds msgs and persistCtx
// (manager.go's NewManager/Manager doc comments) exactly because callers
// like this one need them without a request in scope.
//
// msg.CreatedAt defaults to now (UTC) and msg.Status defaults to
// StatusQueued when zero, mirroring message.SQLRepository.Insert's own
// defaulting — callers that already set both (the HTTP handler does) get no
// surprise. media == nil selects s.Enqueue (plain text, msg.Body as the
// text); non-nil selects s.EnqueueMedia with *media — Dispatch never
// uploads, exactly like EnqueueMedia's own doc comment requires.
//
// Error contract, in the order a caller should check it:
//   - m.msgs == nil: returns ErrNoMessageRepository immediately, MsgID unset,
//     nothing inserted or enqueued.
//   - m.msgs.Insert fails: returns the wrapped error, MsgID unset (result is
//     the zero DispatchResult) and Enqueue/EnqueueMedia is never called.
//   - Enqueue/EnqueueMedia fails: MsgID is set (the row exists), the row is
//     marked failed via m.msgs.MarkFailed with attempts 0 and the SAME
//     reason strings the handler used before this refactor ("send queue
//     full", "session not connected", "could not queue the message" for
//     anything else), and the SENTINEL error itself (ErrQueueFull /
//     ErrNotConnected / whatever Enqueue returned, unwrapped) is returned so
//     a caller can errors.Is on it exactly as the old inline code's switch
//     did.
//   - The wait elapses with no terminal result: (DispatchResult{MsgID},
//     nil) — no error, Result nil. A caller distinguishes this from the two
//     cases above by checking err == nil.
//   - The caller's ctx ends before a terminal result AND before wait
//     elapses: (DispatchResult{MsgID}, context.Cause(ctx)) — MsgID set,
//     Result nil, err set to the ctx's cause (ordinarily context.Canceled).
//     This is deliberately never equal to ErrQueueFull/ErrNotConnected, so
//     a caller that already checked those two first can treat "err != nil
//     but MsgID set and not one of the enqueue sentinels" as this case —
//     exactly the distinction internal/handler/send.go's dispatchSend makes
//     to tell its two different 202 branches apart (one carries "still
//     queued - it will be sent in order", the other carries no text).
//   - Otherwise: (DispatchResult{MsgID, Result: &res}, nil) — the terminal
//     result, sent or failed (res.Err may itself be non-nil for a failed
//     send; that is NOT this function's error return, it is the send's own
//     outcome, same as the old select's `res := <-done` branch).
//
// wait <= 0 blocks until the sender finishes or ctx ends — no timer branch
// at all, matching the plan's "wait == 0 blocks" contract for the
// self-convo runner (no 15s cap; senderLoop already bounds an attempt at 3
// tries × 30s, sender.go's maxAttempts/sendTimeout).
func (m *Manager) Dispatch(ctx context.Context, s *ManagedSession, msg *message.Message, to JID, media *OutboundMedia, wait time.Duration) (DispatchResult, error) {
	if m.msgs == nil {
		return DispatchResult{}, ErrNoMessageRepository
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	if msg.Status == "" {
		msg.Status = message.StatusQueued
	}
	if err := m.msgs.Insert(ctx, msg); err != nil {
		return DispatchResult{}, fmt.Errorf("insert message: %w", err)
	}

	var done <-chan SendResult
	var err error
	if media != nil {
		done, err = s.EnqueueMedia(msg.ID, to, *media)
	} else {
		done, err = s.Enqueue(msg.ID, to, msg.Body)
	}
	if err != nil {
		reason := "could not queue the message"
		switch {
		case errors.Is(err, ErrQueueFull):
			reason = "send queue full"
		case errors.Is(err, ErrNotConnected):
			reason = "session not connected"
		}
		_ = m.msgs.MarkFailed(ctx, msg.ID, reason, 0)
		return DispatchResult{MsgID: msg.ID}, err
	}

	if wait <= 0 {
		select {
		case res := <-done:
			return DispatchResult{MsgID: msg.ID, Result: &res}, nil
		case <-ctx.Done():
			return DispatchResult{MsgID: msg.ID}, context.Cause(ctx)
		}
	}
	select {
	case res := <-done:
		return DispatchResult{MsgID: msg.ID, Result: &res}, nil
	case <-time.After(wait):
		return DispatchResult{MsgID: msg.ID}, nil
	case <-ctx.Done():
		return DispatchResult{MsgID: msg.ID}, context.Cause(ctx)
	}
}
