package wa

import "context"

// This file holds waxumBackend's MessageHistory implementation — the
// customer-reply-reading half of the waxum surface (capabilities.go's
// MessageHistory doc comment), consumed by inbound.go's pump. Every error
// crosses mapErr (backend_waxum.go), matching every other waxumBackend
// method in this package, so a raw *waxum.APIError never reaches the pump.

// RecentMessages satisfies MessageHistory: GET /sessions/{id}/messages via
// internal/waxum.Client.SessionMessages, mapped field-for-field onto this
// package's own MessagePage/InboundMessage (capabilities.go) so no waxum
// type crosses the seam.
func (b *waxumBackend) RecentMessages(ctx context.Context, sessionID string, after *int64, limit int) (MessagePage, error) {
	page, err := b.client.SessionMessages(ctx, sessionID, after, limit)
	if err != nil {
		return MessagePage{}, mapErr(err)
	}
	out := MessagePage{
		Messages:   make([]InboundMessage, 0, len(page.Messages)),
		NextCursor: page.NextCursor,
	}
	for _, m := range page.Messages {
		out.Messages = append(out.Messages, InboundMessage{
			ID:        m.ID,
			MessageID: m.MessageID,
			ChatJID:   m.ChatJID,
			SenderJID: m.SenderJID,
			Direction: m.Direction,
			Body:      m.Body,
			Timestamp: m.ReceivedAt,
		})
	}
	return out, nil
}

// ResolvePhone satisfies MessageHistory: GET
// /sessions/{id}/contacts/{jid}/lid via internal/waxum.Client.ResolveLID,
// returning just the bare-digit phone number the inbound pump needs. A
// mapErr'd ErrNotFound (no known mapping for jid yet) is returned as-is —
// the pump's own caller decides what an unresolvable LID means (store the
// row with FromLID set and FromPhone empty — see inbound.go), this method
// does not swallow or reinterpret it.
func (b *waxumBackend) ResolvePhone(ctx context.Context, sessionID, jid string) (string, error) {
	m, err := b.client.ResolveLID(ctx, sessionID, jid)
	if err != nil {
		return "", mapErr(err)
	}
	return m.PhoneNumber, nil
}

// var _ MessageHistory guards against a typo'd signature compiling and only
// failing at runtime as a bogus ErrUnsupported — matching MediaStore's own
// guard (backend_waxum_media.go).
var _ MessageHistory = (*waxumBackend)(nil)
