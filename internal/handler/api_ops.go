package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// This file is Phase 6b §1: the 13 waxum session-administration endpoints
// (capabilities.go's SessionOps) that have no analogue on whatsmeow at all.
// Every route answers 501 via writeWAError(wa.ErrUnsupported) on a
// whatsmeow-backed deployment, since Manager.SessionOps() returns that when
// the backend does not implement the interface — see
// manager_capabilities.go.
//
// Route names are WSM-native, not waxum's own: "auto-reconnect" rather than
// waxum's "/reconnect" (which would read as WSM's OWN reconnect supervisor,
// internal/wa/reconnect.go — a completely different mechanism) and
// "trust-tokens" rather than waxum's opaque "tctoken".

// opsOr501 fetches Manager.SessionOps(), answering writeWAError (501 for
// ErrUnsupported) and returning ok=false if the active backend does not
// implement it — every handler in this file calls this first.
func (h *handlers) opsOr501(w http.ResponseWriter) (wa.SessionOps, bool) {
	ops, err := h.Manager.SessionOps()
	if err != nil {
		writeWAError(w, err)
		return nil, false
	}
	return ops, true
}

func decodeOpsBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

type autoReconnectResponse struct {
	Enabled    bool   `json:"enabled"`
	ErrorCount uint32 `json:"error_count"`
}

func apiFromAutoReconnect(s wa.AutoReconnectState) autoReconnectResponse {
	return autoReconnectResponse{Enabled: s.Enabled, ErrorCount: s.ErrorCount}
}

// operationsSnapshotResponse is GET .../operations' aggregate shape: one
// request carrying paused + auto-reconnect + history-sync, so the Phase 7
// UI panel needs one round trip instead of three (the plan brief's own
// justification for this endpoint).
type operationsSnapshotResponse struct {
	Paused        bool                  `json:"paused"`
	AutoReconnect autoReconnectResponse `json:"auto_reconnect"`
	HistorySync   struct {
		Skip bool `json:"skip"`
	} `json:"history_sync"`
}

// GetAPISessionOperations is GET /api/v1/sessions/{id}/operations.
func (h *handlers) GetAPISessionOperations(w http.ResponseWriter, r *http.Request) {
	ops, ok := h.opsOr501(w)
	if !ok {
		return
	}
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	paused, err := ops.IsPaused(r.Context(), s.ID)
	if err != nil {
		writeWAError(w, err)
		return
	}
	ar, err := ops.AutoReconnect(r.Context(), s.ID)
	if err != nil {
		writeWAError(w, err)
		return
	}
	skip, err := ops.HistorySync(r.Context(), s.ID)
	if err != nil {
		writeWAError(w, err)
		return
	}
	resp := operationsSnapshotResponse{Paused: paused, AutoReconnect: apiFromAutoReconnect(ar)}
	resp.HistorySync.Skip = skip
	writeJSON(w, http.StatusOK, resp)
}

// PostAPISessionPause is POST /api/v1/sessions/{id}/pause. Resolves WITHOUT
// a live socket — waxum's get_installed_client succeeds for pause/resume
// even while disconnected — so this must never be gated behind a
// connected-session check the other endpoints in this file need.
func (h *handlers) PostAPISessionPause(w http.ResponseWriter, r *http.Request) {
	ops, ok := h.opsOr501(w)
	if !ok {
		return
	}
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	paused, err := ops.Pause(r.Context(), s.ID)
	if err != nil {
		writeWAError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"paused": paused})
}

// PostAPISessionResume is POST /api/v1/sessions/{id}/resume — see
// PostAPISessionPause's doc comment on why this too resolves without a
// live socket.
func (h *handlers) PostAPISessionResume(w http.ResponseWriter, r *http.Request) {
	ops, ok := h.opsOr501(w)
	if !ok {
		return
	}
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	paused, err := ops.Resume(r.Context(), s.ID)
	if err != nil {
		writeWAError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"paused": paused})
}

// GetAPISessionAutoReconnect is GET /api/v1/sessions/{id}/auto-reconnect.
func (h *handlers) GetAPISessionAutoReconnect(w http.ResponseWriter, r *http.Request) {
	ops, ok := h.opsOr501(w)
	if !ok {
		return
	}
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	ar, err := ops.AutoReconnect(r.Context(), s.ID)
	if err != nil {
		writeWAError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiFromAutoReconnect(ar))
}

// PutAPISessionAutoReconnect is PUT /api/v1/sessions/{id}/auto-reconnect —
// {enabled}.
func (h *handlers) PutAPISessionAutoReconnect(w http.ResponseWriter, r *http.Request) {
	ops, ok := h.opsOr501(w)
	if !ok {
		return
	}
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeOpsBody(w, r, &req) {
		return
	}
	ar, err := ops.SetAutoReconnect(r.Context(), s.ID, req.Enabled)
	if err != nil {
		writeWAError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiFromAutoReconnect(ar))
}

// GetAPISessionHistorySync is GET /api/v1/sessions/{id}/history-sync.
func (h *handlers) GetAPISessionHistorySync(w http.ResponseWriter, r *http.Request) {
	ops, ok := h.opsOr501(w)
	if !ok {
		return
	}
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	skip, err := ops.HistorySync(r.Context(), s.ID)
	if err != nil {
		writeWAError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"skip": skip})
}

// PutAPISessionHistorySync is PUT /api/v1/sessions/{id}/history-sync —
// {skip}.
//
// NOTE for an operator: setting skip=false on a session that has been
// dormant for a long time makes WhatsApp replay a potentially very large
// backlog of history on its next (re)connect. That cost lands on this
// session's own message pipeline, not on the caller of this endpoint —
// think before flipping it on a long-idle session.
func (h *handlers) PutAPISessionHistorySync(w http.ResponseWriter, r *http.Request) {
	ops, ok := h.opsOr501(w)
	if !ok {
		return
	}
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	var req struct {
		Skip bool `json:"skip"`
	}
	if !decodeOpsBody(w, r, &req) {
		return
	}
	skip, err := ops.SetHistorySync(r.Context(), s.ID, req.Skip)
	if err != nil {
		writeWAError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"skip": skip})
}

// PostAPISessionAppStateResync is POST /api/v1/sessions/{id}/appstate/resync
// — {collections, mode}.
//
// Destructive-ish and requires a live socket: waxum answers 503 with no
// connection, which mapErr/writeWAError already turn into 409
// wa.ErrNotConnected ("session not connected") rather than a generic
// failure — see writeWAError's doc comment for the full status map.
func (h *handlers) PostAPISessionAppStateResync(w http.ResponseWriter, r *http.Request) {
	ops, ok := h.opsOr501(w)
	if !ok {
		return
	}
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	var req struct {
		Collections []string `json:"collections"`
		Mode        string   `json:"mode"`
	}
	if !decodeOpsBody(w, r, &req) {
		return
	}
	res, err := ops.AppStateResync(r.Context(), s.ID, req.Collections, req.Mode)
	if err != nil {
		writeWAError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"synced": res.Synced, "fatal": res.Fatal,
		"retryable": res.Retryable, "skipped": res.Skipped, "all_synced": res.AllSynced,
	})
}

// PostAPISessionSpamReport is POST /api/v1/sessions/{id}/spam-report.
//
// DANGER: this reports a THIRD PARTY to WhatsApp and is IRREVERSIBLE — there
// is no unreport. API-only by design; the plan deliberately gives this no
// UI button (Phase 7 does not add one). message_id and message_timestamp
// are both required — waxum 422s outright without message_timestamp (see
// internal/waxum.SpamReport's doc comment).
func (h *handlers) PostAPISessionSpamReport(w http.ResponseWriter, r *http.Request) {
	ops, ok := h.opsOr501(w)
	if !ok {
		return
	}
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	var req struct {
		MessageID        string `json:"message_id"`
		MessageTimestamp int64  `json:"message_timestamp"`
		FromJID          string `json:"from_jid"`
		ParticipantJID   string `json:"participant_jid"`
		GroupJID         string `json:"group_jid"`
		GroupSubject     string `json:"group_subject"`
		SpamFlow         string `json:"spam_flow"`
		MediaType        string `json:"media_type"`
	}
	if !decodeOpsBody(w, r, &req) {
		return
	}
	req.MessageID = strings.TrimSpace(req.MessageID)
	if req.MessageID == "" {
		writeError(w, http.StatusBadRequest, "message_id is required")
		return
	}
	if req.MessageTimestamp == 0 {
		writeError(w, http.StatusBadRequest, "message_timestamp is required")
		return
	}
	res, err := ops.ReportSpam(r.Context(), s.ID, wa.SpamReport{
		MessageID: req.MessageID, MessageTimestamp: req.MessageTimestamp,
		FromJID: req.FromJID, ParticipantJID: req.ParticipantJID,
		GroupJID: req.GroupJID, GroupSubject: req.GroupSubject,
		SpamFlow: req.SpamFlow, MediaType: req.MediaType,
	})
	if err != nil {
		writeWAError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": res.Success, "report_id": res.ReportID})
}

// PostAPISessionTrustTokens is POST /api/v1/sessions/{id}/trust-tokens —
// {jids} — issues trust-conversation tokens. Named "trust-tokens" rather
// than waxum's "tctoken" per this file's header comment.
func (h *handlers) PostAPISessionTrustTokens(w http.ResponseWriter, r *http.Request) {
	ops, ok := h.opsOr501(w)
	if !ok {
		return
	}
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	var req struct {
		JIDs []string `json:"jids"`
	}
	if !decodeOpsBody(w, r, &req) {
		return
	}
	if len(req.JIDs) == 0 {
		writeError(w, http.StatusBadRequest, "jids is required")
		return
	}
	tokens, err := ops.TCTokenIssue(r.Context(), s.ID, req.JIDs)
	if err != nil {
		writeWAError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, map[string]any{"jid": t.JID, "timestamp": t.Timestamp})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out})
}

// GetAPISessionTrustTokens is GET /api/v1/sessions/{id}/trust-tokens — lists
// every JID this session currently holds a trust token for.
func (h *handlers) GetAPISessionTrustTokens(w http.ResponseWriter, r *http.Request) {
	ops, ok := h.opsOr501(w)
	if !ok {
		return
	}
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	jids, err := ops.TCTokenList(r.Context(), s.ID)
	if err != nil {
		writeWAError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jids": jids})
}

// GetAPISessionTrustToken is GET /api/v1/sessions/{id}/trust-tokens/{jid} —
// registered as its own route, distinct by METHOD (GET) from the DELETE
// .../trust-tokens/expired prune route below, so the two do not conflict
// under Go 1.22's mux even though "expired" could otherwise collide with
// the {jid} wildcard.
func (h *handlers) GetAPISessionTrustToken(w http.ResponseWriter, r *http.Request) {
	ops, ok := h.opsOr501(w)
	if !ok {
		return
	}
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	info, err := ops.TCTokenGet(r.Context(), s.ID, r.PathValue("jid"))
	if err != nil {
		writeWAError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"jid": info.JID, "token_timestamp": info.TokenTimestamp,
		"sender_timestamp": info.SenderTimestamp, "found": info.Found,
	})
}

// DeleteAPISessionTrustTokensExpired is
// DELETE /api/v1/sessions/{id}/trust-tokens/expired — pruning. Destructive
// (deletes tokens) and requires a live socket; see
// PostAPISessionAppStateResync's doc comment for how a 503-without-a-socket
// surfaces here (409, "session not connected", not a generic failure).
func (h *handlers) DeleteAPISessionTrustTokensExpired(w http.ResponseWriter, r *http.Request) {
	ops, ok := h.opsOr501(w)
	if !ok {
		return
	}
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	n, err := ops.TCTokenPrune(r.Context(), s.ID)
	if err != nil {
		writeWAError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pruned_count": n})
}
