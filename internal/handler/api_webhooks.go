package handler

import (
	"net/http"
	"strings"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// This file is Phase 6b §2: webhook administration
// (capabilities.go's WebhookAdmin), scoped to a session except for the one
// fleet-level reenable-all route. Every route answers 501 via writeWAError
// on a backend that does not implement WebhookAdmin.
//
// THE SECRET IS AN HMAC SIGNING KEY. It must never appear in a response
// body, a log line, or an error message — every response type in this file
// reports has_secret bool instead of the secret's value. See
// TestWebhookResponsesNeverIncludeTheSecret.

// waxumWebhookEvents is the exact snake_case event-name set waxum accepts,
// taken from its own handler (the phase brief's list). Validated here so a
// bad value gets a clear 400 naming it, instead of an opaque 422 from
// waxum's own validation.
var waxumWebhookEvents = map[string]bool{
	"all": true, "message": true, "receipt": true, "presence": true,
	"chat_presence": true, "group_update": true, "joined_group": true,
	"qr_code": true, "pair_code": true, "connected": true, "disconnected": true,
	"logged_out": true, "account_locked": true, "picture_update": true,
	"user_about_update": true, "push_name_update": true, "contact_update": true,
	"device_list_update": true, "pin_update": true, "mute_update": true,
	"archive_update": true, "mark_chat_as_read": true, "undecryptable_message": true,
	"client_outdated": true, "offline_sync_preview": true, "offline_sync_completed": true,
	"scheduled_sent": true, "scheduled_failed": true, "blast_progress": true,
	"blast_completed": true, "call_log_sync": true, "stream_error": true,
	"enc_decrypt_failed": true,
}

// validateWebhookEvents reports the first event name waxum would not
// recognise, or "" if every one is known.
func validateWebhookEvents(events []string) string {
	for _, e := range events {
		if !waxumWebhookEvents[e] {
			return e
		}
	}
	return ""
}

// webhookResponse is the JSON shape ListWebhooks/RegisterWebhook answer —
// NEVER wa.WebhookInfo directly, whose Secret field would otherwise
// marshal the HMAC key straight into the response body.
type webhookResponse struct {
	ID        string   `json:"id"`
	URL       string   `json:"url"`
	Events    []string `json:"events"`
	Enabled   bool     `json:"enabled"`
	HasSecret bool     `json:"has_secret"`
}

func apiFromWebhook(wh wa.WebhookInfo) webhookResponse {
	return webhookResponse{
		ID: wh.ID, URL: wh.URL, Events: wh.Events, Enabled: wh.Enabled,
		HasSecret: wh.Secret != nil && *wh.Secret != "",
	}
}

// webhookAdminOr501 mirrors opsOr501 (api_ops.go) for WebhookAdmin.
func (h *handlers) webhookAdminOr501(w http.ResponseWriter) (wa.WebhookAdmin, bool) {
	wh, err := h.Manager.WebhookAdmin()
	if err != nil {
		writeWAError(w, err)
		return nil, false
	}
	return wh, true
}

// GetAPISessionWebhooks is GET /api/v1/sessions/{id}/webhooks.
func (h *handlers) GetAPISessionWebhooks(w http.ResponseWriter, r *http.Request) {
	wh, ok := h.webhookAdminOr501(w)
	if !ok {
		return
	}
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	hooks, err := wh.ListWebhooks(r.Context(), s.ID)
	if err != nil {
		writeWAError(w, err)
		return
	}
	out := make([]webhookResponse, 0, len(hooks))
	for _, hk := range hooks {
		out = append(out, apiFromWebhook(hk))
	}
	writeJSON(w, http.StatusOK, out)
}

// PostAPISessionWebhooks is POST /api/v1/sessions/{id}/webhooks —
// {url, events, secret}.
//
// waxum's net_guard (src/net_guard.rs) rejects ANY callback url resolving
// to loopback/RFC1918/ULA/CGNAT/link-local, with no env escape hatch — so
// http://app:8800/... or any 10.x address is refused with a 400, which
// mapErr turns into wa.ErrRejectedByBackend -> 422 (writeWAError). Most
// operator-run receivers ARE on a private network, so this is the single
// most likely mistake here; the 422's message names the actual cause
// rather than reading as a generic failure. A duplicate URL answers 409
// via wa.ErrAlreadyExists — see backend_waxum_webhooks.go's
// RegisterWebhook for why that is caught separately from the generic 409
// mapErr would otherwise produce ("session not connected", which is not
// what a duplicate URL means).
func (h *handlers) PostAPISessionWebhooks(w http.ResponseWriter, r *http.Request) {
	wh, ok := h.webhookAdminOr501(w)
	if !ok {
		return
	}
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	var req struct {
		URL    string   `json:"url"`
		Events []string `json:"events"`
		Secret string   `json:"secret"`
	}
	if !decodeOpsBody(w, r, &req) {
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	if req.URL == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}
	if len(req.Events) == 0 {
		writeError(w, http.StatusBadRequest, "events is required")
		return
	}
	if bad := validateWebhookEvents(req.Events); bad != "" {
		writeError(w, http.StatusBadRequest, "unknown event name: "+bad)
		return
	}
	created, err := wh.RegisterWebhook(r.Context(), s.ID, wa.WebhookSpec{
		URL: req.URL, Secret: req.Secret, Events: req.Events,
	})
	if err != nil {
		writeWAError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiFromWebhook(created))
}

// DeleteAPISessionWebhook is DELETE /api/v1/sessions/{id}/webhooks/{webhookID}.
func (h *handlers) DeleteAPISessionWebhook(w http.ResponseWriter, r *http.Request) {
	wh, ok := h.webhookAdminOr501(w)
	if !ok {
		return
	}
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if err := wh.UnregisterWebhook(r.Context(), s.ID, r.PathValue("webhookID")); err != nil {
		writeWAError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PostAPISessionWebhookEnable is
// POST /api/v1/sessions/{id}/webhooks/{webhookID}/enable — re-enables a
// webhook whose delivery circuit waxum opened after repeated failures.
func (h *handlers) PostAPISessionWebhookEnable(w http.ResponseWriter, r *http.Request) {
	wh, ok := h.webhookAdminOr501(w)
	if !ok {
		return
	}
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if err := wh.EnableWebhook(r.Context(), s.ID, r.PathValue("webhookID")); err != nil {
		writeWAError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// dlqEntryResponse is the JSON shape for one dead-lettered delivery —
// snake_case, mirroring wa.DLQEntry field-for-field (no secret involved
// here, unlike webhookResponse).
type dlqEntryResponse struct {
	ID         string `json:"id"`
	SessionID  string `json:"session_id"`
	WebhookURL string `json:"webhook_url"`
	Event      string `json:"event"`
	Payload    string `json:"payload"`
	LastError  string `json:"last_error"`
	Attempts   int    `json:"attempts"`
	FailedAt   int64  `json:"failed_at"`
}

func apiFromDLQEntry(e wa.DLQEntry) dlqEntryResponse {
	return dlqEntryResponse{
		ID: e.ID, SessionID: e.SessionID, WebhookURL: e.WebhookURL, Event: e.Event,
		Payload: e.Payload, LastError: e.LastError, Attempts: e.Attempts, FailedAt: e.FailedAt,
	}
}

// GetAPISessionWebhookDLQ is GET /api/v1/sessions/{id}/webhooks/dlq.
func (h *handlers) GetAPISessionWebhookDLQ(w http.ResponseWriter, r *http.Request) {
	wh, ok := h.webhookAdminOr501(w)
	if !ok {
		return
	}
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	entries, err := wh.WebhookDLQ(r.Context(), s.ID)
	if err != nil {
		writeWAError(w, err)
		return
	}
	out := make([]dlqEntryResponse, 0, len(entries))
	for _, e := range entries {
		out = append(out, apiFromDLQEntry(e))
	}
	writeJSON(w, http.StatusOK, out)
}

// PostAPISessionWebhookDLQReplay is
// POST /api/v1/sessions/{id}/webhooks/dlq/{entryID}/replay.
func (h *handlers) PostAPISessionWebhookDLQReplay(w http.ResponseWriter, r *http.Request) {
	wh, ok := h.webhookAdminOr501(w)
	if !ok {
		return
	}
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if err := wh.ReplayWebhookDLQEntry(r.Context(), s.ID, r.PathValue("entryID")); err != nil {
		writeWAError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PostAPIWebhooksReenableAll is POST /api/v1/webhooks/reenable-all —
// FLEET-level, deliberately NOT under /sessions/{id}: it re-enables every
// open webhook circuit across every session on this waxum instance,
// mirroring waxum.ReenableAllWebhooks's own doc comment.
func (h *handlers) PostAPIWebhooksReenableAll(w http.ResponseWriter, r *http.Request) {
	wh, ok := h.webhookAdminOr501(w)
	if !ok {
		return
	}
	if err := wh.ReenableAllWebhooks(r.Context()); err != nil {
		writeWAError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
