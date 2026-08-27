package waxum

import (
	"context"
	"net/http"
)

// AutoReconnect is the shape shared by AutoReconnect and SetAutoReconnect:
// GET/PUT /sessions/{id}/reconnect.
type AutoReconnect struct {
	Enabled    bool   `json:"enabled"`
	ErrorCount uint32 `json:"error_count"`
}

// AppStateResync is POST /sessions/{id}/appstate/resync's response.
type AppStateResync struct {
	Synced    []string `json:"synced"`
	Fatal     []string `json:"fatal"`
	Retryable []string `json:"retryable"`
	Skipped   []string `json:"skipped"`
	AllSynced bool     `json:"all_synced"`
}

// SpamReport is the body of POST /sessions/{id}/spam/report, mirroring
// waxum's SpamReportRequest (src/models/messages.rs) field for field.
//
// MessageID and MessageTimestamp are REQUIRED server-side — MessageTimestamp
// has no serde default, so omitting it (as an earlier version of this type
// did) 422s on every call. It is int64, epoch SECONDS, matching this
// package's existing convention for a WhatsApp message timestamp
// (MessageResult.Timestamp) rather than a time.Time — waxum's own field is
// a bare u64 with no timezone or sub-second component to preserve, and
// every other timestamp already flowing through this package (models.go)
// is a plain integer for the same reason.
//
// FromJID, ParticipantJID, GroupJID, GroupSubject and MediaType are all
// Option<String> server-side and are omitted from the wire body when empty.
// SpamFlow is also optional but, unlike the others, waxum defaults it
// server-side to "message_menu" (default_spam_flow) when omitted — leaving
// it empty here gets that same default rather than an error, so it is
// omitted on the same "empty means let the server decide" basis.
type SpamReport struct {
	MessageID        string
	MessageTimestamp int64
	FromJID          string
	ParticipantJID   string
	GroupJID         string
	GroupSubject     string
	SpamFlow         string
	MediaType        string
}

// SpamReportResponse is ReportSpam's decoded response. ReportID is a
// pointer because waxum's own field is Option<String> — a report that
// waxum accepted but did not assign an id to (server-side, not this
// client's concern) is a real, distinct value from "" and must not be
// silently coerced into it.
type SpamReportResponse struct {
	Success  bool    `json:"success"`
	ReportID *string `json:"report_id"`
}

// TCToken is one entry of TCTokenIssue's response.
type TCToken struct {
	JID       string `json:"jid"`
	Timestamp int64  `json:"timestamp"`
}

// TCTokenInfo is TCTokenGet's response.
//
// SenderTimestamp is a pointer because waxum's own field
// (TcTokenGetResponse.sender_timestamp, src/handlers/operations.rs) is
// Option<i64> even when Found is true — the underlying tc_token entry can
// carry a token_timestamp with no sender_timestamp recorded at all, and a
// plain int64 would make that indistinguishable from "recorded as exactly
// epoch zero". TokenTimestamp does NOT need the same treatment: it is
// Some(...) on every entry that exists at all (operations.rs's
// tctoken_get: Some(e) => token_timestamp: Some(e.token_timestamp) always),
// so whenever Found is true TokenTimestamp is always genuinely present:
// the ambiguity that matters is `Found==false`, which is already its own
// bool and callers must already check before trusting either timestamp.
type TCTokenInfo struct {
	JID             string `json:"jid"`
	TokenTimestamp  int64  `json:"token_timestamp"`
	SenderTimestamp *int64 `json:"sender_timestamp"`
	Found           bool   `json:"found"`
}

// Pause suspends message processing for the session without dropping its
// connection. Unlike every other method in this file, waxum resolves this
// one WITHOUT requiring a live socket — see backend_waxum.go's mapErr for
// why that makes ErrClientNotConnected the wrong mapping for Pause/Resume
// specifically.
func (c *Client) Pause(ctx context.Context, id string) (bool, error) {
	var out struct {
		Paused bool `json:"paused"`
	}
	if err := c.do(ctx, http.MethodPost, sessionPath(id, "/pause"), nil, &out, true); err != nil {
		return false, err
	}
	return out.Paused, nil
}

// Resume reverses Pause.
func (c *Client) Resume(ctx context.Context, id string) (bool, error) {
	var out struct {
		Paused bool `json:"paused"`
	}
	if err := c.do(ctx, http.MethodPost, sessionPath(id, "/resume"), nil, &out, true); err != nil {
		return false, err
	}
	return out.Paused, nil
}

// AutoReconnect fetches the session's current auto-reconnect setting.
func (c *Client) AutoReconnect(ctx context.Context, id string) (*AutoReconnect, error) {
	var out AutoReconnect
	if err := c.do(ctx, http.MethodGet, sessionPath(id, "/reconnect"), nil, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetAutoReconnect changes the session's auto-reconnect setting.
func (c *Client) SetAutoReconnect(ctx context.Context, id string, enabled bool) (*AutoReconnect, error) {
	body := map[string]any{"enabled": enabled}
	var out AutoReconnect
	if err := c.do(ctx, http.MethodPut, sessionPath(id, "/reconnect"), body, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}

// HistorySync fetches whether the session skips WhatsApp's history-sync on
// (re)connect. The response field is "skip_history_sync", not "skip" — see
// SetHistorySync's doc comment for why the REQUEST field differs.
func (c *Client) HistorySync(ctx context.Context, id string) (bool, error) {
	var out struct {
		SkipHistorySync bool `json:"skip_history_sync"`
	}
	if err := c.do(ctx, http.MethodGet, sessionPath(id, "/history-sync"), nil, &out, true); err != nil {
		return false, err
	}
	return out.SkipHistorySync, nil
}

// SetHistorySync changes the skip-history-sync setting. The REQUEST body's
// field is "skip" (not "skip_history_sync" — that's the response field
// name only), easy to get backwards since both mean the same thing.
func (c *Client) SetHistorySync(ctx context.Context, id string, skip bool) (bool, error) {
	body := map[string]any{"skip": skip}
	var out struct {
		SkipHistorySync bool `json:"skip_history_sync"`
	}
	if err := c.do(ctx, http.MethodPut, sessionPath(id, "/history-sync"), body, &out, true); err != nil {
		return false, err
	}
	return out.SkipHistorySync, nil
}

// AppStateResync asks waxum to resync the named app-state collections (or
// all of them, mode-dependent — waxum's own enum for mode is passed through
// verbatim).
func (c *Client) AppStateResync(ctx context.Context, id string, collections []string, mode string) (*AppStateResync, error) {
	body := map[string]any{"collections": collections, "mode": mode}
	var out AppStateResync
	if err := c.do(ctx, http.MethodPost, sessionPath(id, "/appstate/resync"), body, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}

// ReportSpam reports a message as spam. MessageID and MessageTimestamp are
// REQUIRED and always sent — see SpamReport's doc comment for why omitting
// MessageTimestamp 422s. FromJID/ParticipantJID/GroupJID/GroupSubject/
// SpamFlow/MediaType are omitted from the wire body when empty, matching
// Terminate's reason-omission pattern in calls.go — all are Option<String>
// (or, for SpamFlow, server-defaulted) server-side. Returns waxum's decoded
// {success, report_id} rather than discarding it, so a caller can actually
// tell whether the report was accepted.
func (c *Client) ReportSpam(ctx context.Context, id string, req SpamReport) (*SpamReportResponse, error) {
	body := map[string]any{
		"message_id":        req.MessageID,
		"message_timestamp": req.MessageTimestamp,
	}
	if req.FromJID != "" {
		body["from_jid"] = req.FromJID
	}
	if req.ParticipantJID != "" {
		body["participant_jid"] = req.ParticipantJID
	}
	if req.GroupJID != "" {
		body["group_jid"] = req.GroupJID
	}
	if req.GroupSubject != "" {
		body["group_subject"] = req.GroupSubject
	}
	if req.SpamFlow != "" {
		body["spam_flow"] = req.SpamFlow
	}
	if req.MediaType != "" {
		body["media_type"] = req.MediaType
	}
	var out SpamReportResponse
	if err := c.do(ctx, http.MethodPost, sessionPath(id, "/spam/report"), body, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}

// TCTokenIssue issues trust-conversation tokens for the given JIDs.
func (c *Client) TCTokenIssue(ctx context.Context, id string, jids []string) ([]TCToken, error) {
	body := map[string]any{"jids": jids}
	var out struct {
		Tokens []TCToken `json:"tokens"`
	}
	if err := c.do(ctx, http.MethodPost, sessionPath(id, "/tctoken/issue"), body, &out, true); err != nil {
		return nil, err
	}
	return out.Tokens, nil
}

// TCTokenGet fetches the trust-conversation token state for one JID.
func (c *Client) TCTokenGet(ctx context.Context, id, jid string) (*TCTokenInfo, error) {
	var out TCTokenInfo
	if err := c.do(ctx, http.MethodGet, sessionPath(id, "/tctoken/"+jid), nil, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}

// TCTokenList lists every JID this session currently holds a trust token for.
func (c *Client) TCTokenList(ctx context.Context, id string) ([]string, error) {
	var out struct {
		JIDs []string `json:"jids"`
	}
	if err := c.do(ctx, http.MethodGet, sessionPath(id, "/tctoken/list"), nil, &out, true); err != nil {
		return nil, err
	}
	return out.JIDs, nil
}

// TCTokenPrune deletes expired trust-conversation tokens. This is a DELETE,
// not a POST, unlike TCTokenIssue — easy to get backwards since both
// mutate server state; TestTCTokenPruneUsesDeleteOnTheExpiredPath is the
// regression test.
func (c *Client) TCTokenPrune(ctx context.Context, id string) (int, error) {
	var out struct {
		PrunedCount int `json:"pruned_count"`
	}
	if err := c.do(ctx, http.MethodDelete, sessionPath(id, "/tctoken/expired"), nil, &out, true); err != nil {
		return 0, err
	}
	return out.PrunedCount, nil
}
