package waxum

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// messageTimestampLayout is the exact (zone-less) shape waxum's
// GET .../messages sends msg_timestamp in — confirmed live against
// production (ardihikaru/waxum:0.12.2-uploaded-media):
// "2026-08-31 07:46:44". There is no zone marker at all, and the value IS
// UTC — parsing with time.Parse against RFC3339 fails outright (wrong
// shape), and parsing with time.ParseInLocation against time.Local would
// silently succeed while attributing the wrong zone, shifting every
// ReceivedAt by the host's own offset. Only ParseInLocation(...,
// time.UTC) is both correct and fails loudly on a genuinely malformed
// value. See parseMessageTimestamp's own test,
// TestParseMessageTimestampInterpretsTheValueAsUTCRegardlessOfHostTZ, for
// the regression this guards.
const messageTimestampLayout = "2006-01-02 15:04:05"

func parseMessageTimestamp(v string) (time.Time, error) {
	t, err := time.ParseInLocation(messageTimestampLayout, v, time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("waxum: parsing msg_timestamp %q: %w", v, err)
	}
	return t, nil
}

// rawInboundMessage is the wire shape of one entry in
// GET /sessions/{id}/messages' "messages" array. Unexported: SessionMessages
// maps it to InboundMessage before returning, the same split Groups() uses
// for groupsResponse — so no caller can read msg_timestamp as a raw string
// and repeat the local-time parsing mistake this file's doc comment above
// exists to prevent.
type rawInboundMessage struct {
	ID           int64   `json:"id"`
	MessageID    string  `json:"message_id"`
	SessionID    string  `json:"session_id"`
	ChatJID      string  `json:"chat_jid"`
	SenderJID    string  `json:"sender_jid"`
	Direction    string  `json:"direction"` // "in" or "out" — waxum maps is_from_me to "out"
	MsgType      string  `json:"msg_type"`
	Body         *string `json:"body"` // nullable — stickers/media-without-caption send null
	Snippet      *string `json:"snippet"`
	MsgTimestamp string  `json:"msg_timestamp"`
	PushName     *string `json:"push_name"`
	Media        any     `json:"media"`
}

// InboundMessage is one row of GET /sessions/{id}/messages, mapped from
// rawInboundMessage. ReceivedAt is a real time.Time — see
// messageTimestampLayout's doc comment for why parsing lives here rather
// than being left to every caller.
type InboundMessage struct {
	ID         int64
	MessageID  string
	SessionID  string
	ChatJID    string
	SenderJID  string
	Direction  string // "in" or "out"
	MsgType    string
	Body       *string
	PushName   *string
	ReceivedAt time.Time
}

// SessionMessagesPage is one page of GET /sessions/{id}/messages, newest
// first. NextCursor pages BACKWARDS (older) — there is no "newer than X"
// parameter — and is nil on a short page (waxum's own "next_cursor": null).
type SessionMessagesPage struct {
	Messages   []InboundMessage
	Count      int
	NextCursor *int64
}

// rawSessionMessagesPage is the wire envelope; mapped to
// SessionMessagesPage the same way rawInboundMessage maps to InboundMessage.
type rawSessionMessagesPage struct {
	Messages   []rawInboundMessage `json:"messages"`
	Count      int                 `json:"count"`
	NextCursor *int64              `json:"next_cursor"`
}

// SessionMessages fetches one page of session_id's message history, newest
// first. after is waxum's keyset cursor (its own "id" column) — omitted
// from the request entirely when nil, which asks for the newest page.
// limit is passed through VERBATIM when > 0; waxum itself defaults it to
// 20 and clamps it to 1..200 server-side, so this method does not
// second-guess that — a caller wanting a specific window (internal/wa's
// inbound pump clamps to <=200) is expected to clamp before calling, not
// rely on this method to.
//
// Returns waxum's typed errors via c.do (errorFromStatus): ErrNotFound for
// an unknown session_id, ErrClientNotConnected (waxum's 503) when the
// session is not connected — the live chat store this endpoint reads needs
// a running connection — both expected, ordinary outcomes for a caller
// polling a fleet of sessions, not something this method itself treats as
// exceptional.
func (c *Client) SessionMessages(ctx context.Context, sessionID string, after *int64, limit int) (SessionMessagesPage, error) {
	q := url.Values{}
	if after != nil {
		q.Set("after", strconv.FormatInt(*after, 10))
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	path := sessionPath(sessionID, "/messages")
	if len(q) > 0 {
		path += "?" + q.Encode()
	}

	var raw rawSessionMessagesPage
	if err := c.do(ctx, http.MethodGet, path, nil, &raw, true); err != nil {
		return SessionMessagesPage{}, err
	}

	out := SessionMessagesPage{
		Messages:   make([]InboundMessage, 0, len(raw.Messages)),
		Count:      raw.Count,
		NextCursor: raw.NextCursor,
	}
	for _, m := range raw.Messages {
		receivedAt, err := parseMessageTimestamp(m.MsgTimestamp)
		if err != nil {
			// A single unparseable row must not fail the whole page — the
			// caller (internal/wa's inbound pump) is best-effort per
			// CLAUDE.md's SSE-event hazard note, and one malformed
			// timestamp from waxum is exactly the kind of thing that
			// should be skipped, not abort a fleet-wide poll tick.
			continue
		}
		out.Messages = append(out.Messages, InboundMessage{
			ID:         m.ID,
			MessageID:  m.MessageID,
			SessionID:  m.SessionID,
			ChatJID:    m.ChatJID,
			SenderJID:  m.SenderJID,
			Direction:  m.Direction,
			MsgType:    m.MsgType,
			Body:       m.Body,
			PushName:   m.PushName,
			ReceivedAt: receivedAt,
		})
	}
	return out, nil
}

// LIDMapping is GET /sessions/{id}/contacts/{jid}/lid — waxum's resolution
// of a @lid sender identity to the underlying MSISDN. PhoneNumber is bare
// digits, no "@" suffix, matching ContactCheck.Phone's own convention.
type LIDMapping struct {
	LID            string `json:"lid"`
	PhoneNumber    string `json:"phone_number"`
	LearningSource string `json:"learning_source"`
}

// ResolveLID asks waxum for the phone number behind a @lid sender/chat
// identity. jid is PathEscape'd — a LID's user part is plain digits, but
// the "@lid" suffix and any future non-digit form should not need to be
// pre-sanitized by every caller. Returns ErrNotFound when waxum has no
// known mapping for jid yet (an unresolvable contact — see
// internal/wa/inbound.go for how that case is handled: the row is still
// stored, with FromLID set and FromPhone left empty, rather than dropped).
func (c *Client) ResolveLID(ctx context.Context, sessionID, jid string) (LIDMapping, error) {
	var out LIDMapping
	path := sessionPath(sessionID, "/contacts/"+url.PathEscape(jid)+"/lid")
	if err := c.do(ctx, http.MethodGet, path, nil, &out, true); err != nil {
		return LIDMapping{}, err
	}
	return out, nil
}
