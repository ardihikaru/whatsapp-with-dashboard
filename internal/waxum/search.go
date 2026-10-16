package waxum

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// MessageHit is one message returned by waxum's search / per-chat history
// routes. ReceivedAt is a real UTC time (waxum sends a zone-less
// "2006-01-02 15:04:05" string; see messageTimestampLayout).
type MessageHit struct {
	ID         int64     `json:"id"`
	MessageID  string    `json:"message_id"`
	SessionID  string    `json:"session_id"`
	ChatJID    string    `json:"chat_jid"`
	SenderJID  string    `json:"sender_jid"`
	Direction  string    `json:"direction"` // "in" or "out"
	MsgType    string    `json:"msg_type"`
	Body       *string   `json:"body"`
	Snippet    *string   `json:"snippet"`
	PushName   *string   `json:"push_name"`
	ReceivedAt time.Time `json:"received_at"`
}

// MessageHitsPage is one page of hits, newest first. Count is the hits in
// THIS page, not a global total.
type MessageHitsPage struct {
	Messages []MessageHit `json:"messages"`
	Count    int          `json:"count"`
}

type rawHitsPage struct {
	Messages []struct {
		MessageHit
		MsgTimestamp string `json:"msg_timestamp"`
	} `json:"messages"`
}

func (c *Client) hitsPage(ctx context.Context, path string, q url.Values) (MessageHitsPage, error) {
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var raw rawHitsPage
	if err := c.do(ctx, http.MethodGet, path, nil, &raw, true); err != nil {
		return MessageHitsPage{}, err
	}
	out := MessageHitsPage{Messages: make([]MessageHit, 0, len(raw.Messages))}
	for _, m := range raw.Messages {
		t, err := parseMessageTimestamp(m.MsgTimestamp)
		if err != nil {
			continue // one malformed row must not fail the page
		}
		h := m.MessageHit
		h.ReceivedAt = t
		out.Messages = append(out.Messages, h)
	}
	out.Count = len(out.Messages)
	return out, nil
}

func pageQuery(limit, offset int) url.Values {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	return q
}

// SearchMessages runs waxum's full-text search over one session's stored
// message history (GET /sessions/{id}/messages/search). q must not be
// empty; waxum clamps limit to 1..200 (default 20).
func (c *Client) SearchMessages(ctx context.Context, sessionID, q string, limit, offset int) (MessageHitsPage, error) {
	v := pageQuery(limit, offset)
	v.Set("q", q)
	return c.hitsPage(ctx, sessionPath(sessionID, "/messages/search"), v)
}

// ChatMessages reads one chat's history, newest first
// (GET /sessions/{id}/messages/chat/{chat_jid}); chatJID is a DM partner
// or a group JID. PushName and media pointers are only filled by this route.
func (c *Client) ChatMessages(ctx context.Context, sessionID, chatJID string, limit, offset int) (MessageHitsPage, error) {
	return c.hitsPage(ctx, sessionPath(sessionID, "/messages/chat/"+url.PathEscape(chatJID)), pageQuery(limit, offset))
}

// StoredContact is one row of the session's synced address book.
type StoredContact struct {
	JID          string  `json:"jid"`
	Phone        *string `json:"phone"`
	LIDJID       *string `json:"lid_jid"`
	FullName     *string `json:"full_name"`
	FirstName    *string `json:"first_name"`
	PushName     *string `json:"push_name"`
	BusinessName *string `json:"business_name"`
}

// ContactsPage is one page of GET /sessions/{id}/contacts.
type ContactsPage struct {
	Contacts []StoredContact `json:"contacts"`
	Total    int64           `json:"total"`
	Limit    int64           `json:"limit"`
	Offset   int64           `json:"offset"`
}

// Contacts lists (and optionally filters by q) the session's synced contacts.
func (c *Client) Contacts(ctx context.Context, sessionID, q string, limit, offset int) (ContactsPage, error) {
	v := pageQuery(limit, offset)
	if q != "" {
		v.Set("q", q)
	}
	path := sessionPath(sessionID, "/contacts")
	if len(v) > 0 {
		path += "?" + v.Encode()
	}
	var out ContactsPage
	if err := c.do(ctx, http.MethodGet, path, nil, &out, true); err != nil {
		return ContactsPage{}, err
	}
	if out.Contacts == nil {
		out.Contacts = []StoredContact{}
	}
	return out, nil
}
