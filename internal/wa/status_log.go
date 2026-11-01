package wa

import (
	"context"
	"fmt"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
)

// LogStatusPost is the one call site that writes a wa_messages row for a
// WhatsApp status/story post (StatusPoster, capabilities.go). Unlike every
// other send in this app, a status post bypasses the ordinary send queue
// entirely — the HTTP handler calls the StatusPoster capability directly
// and already knows the outcome by the time this runs — so sender.go's
// worker (which is what normally calls Insert/MarkSent/MarkFailed, see
// Dispatch's doc comment) never executes for this path, and this is the
// only place that trail gets written.
//
// msg.ToPhone is expected to already be "status@broadcast" (the caller's
// job, matching waxum's own "to" field for a status reply — see
// /tmp/waxum-status-api.md). postErr nil marks the row sent with
// waMessageID; non-nil marks it failed with postErr's text, attempts 1
// either way (exactly one attempt is ever made — there is no retry loop for
// this path).
func (m *Manager) LogStatusPost(ctx context.Context, msg *message.Message, waMessageID string, postErr error) error {
	if m.msgs == nil {
		return ErrNoMessageRepository
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	msg.Status = message.StatusQueued
	if err := m.msgs.Insert(ctx, msg); err != nil {
		return fmt.Errorf("insert message: %w", err)
	}
	if postErr != nil {
		return m.msgs.MarkFailed(ctx, msg.ID, postErr.Error(), 1)
	}
	return m.msgs.MarkSent(ctx, msg.ID, waMessageID, 1)
}
