package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// PostAPISendMessage is the machine-facing send endpoint (docs/06 §6):
// 200 sent / 200 failed / 202 still queued after 15s / 400 validation /
// 404 unknown / 409 not connected / 429 queue full.
func (h *handlers) PostAPISendMessage(w http.ResponseWriter, r *http.Request) {
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	req, ok := decodeSendRequest(w, r)
	if !ok {
		return
	}
	h.postAPISendThrough(w, r, s, wa.ToJID(req.Digits), req.Digits, req.Text, req.Attr)
}

func (h *handlers) PostAPISendMessageAuto(w http.ResponseWriter, r *http.Request) {
	req, valid := decodeSendRequest(w, r)
	if !valid {
		return
	}
	s, _, ok := h.Manager.SelectConnected(r.Context(), req.AllowMaster)
	if !ok {
		writeError(w, http.StatusConflict, noAutoRouteMessage(h.Manager.Snapshots(), req.AllowMaster))
		return
	}
	h.postAPISendThrough(w, r, s, wa.ToJID(req.Digits), req.Digits, req.Text, req.Attr)
}

// textSendRequest is decodeSendRequest's parsed, validated result. A named
// struct rather than a growing positional-return tuple — a fifth return
// value would put two adjacent bools (AllowMaster and the ok success flag)
// in the signature, which go vet cannot protect against a swap.
type textSendRequest struct {
	Digits      string
	Text        string
	Attr        message.Attribution
	AllowMaster bool
}

// decodeSendRequest parses the send body, including the optional attribution the
// log page breaks down by. category defaults to "notification"; api_client_id is
// NOT read here — it comes from the bearer principal in sendThroughJID.
// allow_master governs AUTOMATIC routing only (PostAPISendMessageAuto); the
// explicit-session route (PostAPISendMessage) ignores it — naming a session
// id already bypasses the exclusion.
func decodeSendRequest(w http.ResponseWriter, r *http.Request) (textSendRequest, bool) {
	var req struct {
		To          string `json:"to"`
		Text        string `json:"text"`
		ClientRef   string `json:"client_ref"`
		TenantRef   string `json:"tenant_ref"`
		Category    string `json:"category"`
		AllowMaster bool   `json:"allow_master"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return textSendRequest{}, false
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" || len(req.Text) > maxTextChars {
		writeError(w, http.StatusBadRequest, "text is required, max 4096 characters")
		return textSendRequest{}, false
	}
	digits, err := wa.NormalizePhone(req.To)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return textSendRequest{}, false
	}
	attr := message.Attribution{
		ClientRef: strings.TrimSpace(req.ClientRef),
		TenantRef: strings.TrimSpace(req.TenantRef),
		Category:  message.NormalizeCategory(req.Category, message.CategoryNotification),
	}
	return textSendRequest{Digits: digits, Text: req.Text, Attr: attr, AllowMaster: req.AllowMaster}, true
}

// postAPISendThrough is the shared JSON tail of every machine send path — phone
// or group. It takes a resolved wa.JID rather than digits so the group handler
// reuses it verbatim (see sendThroughJID).
func (h *handlers) postAPISendThrough(
	w http.ResponseWriter, r *http.Request, s *wa.ManagedSession, to wa.JID, toDisplay, text string, attr message.Attribution,
) {
	outcome, status := h.sendThroughJID(r, s, to, toDisplay, text, attr)
	if status >= 400 {
		writeError(w, status, outcome.Error)
		return
	}
	snap := s.Snapshot()
	resp := map[string]any{
		"id":           outcome.ID,
		"status":       outcome.Status,
		"session_id":   snap.ID,
		"session_name": snap.Name,
	}
	if outcome.WAMessageID != "" {
		resp["wa_message_id"] = outcome.WAMessageID
	}
	if outcome.Error != "" {
		resp["error"] = outcome.Error
	}
	writeJSON(w, status, resp)
}

// GetAPISessionMessages returns the recent audit log for one session.
func (h *handlers) GetAPISessionMessages(w http.ResponseWriter, r *http.Request) {
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	msgs, err := h.Messages.Recent(r.Context(), s.ID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read messages")
		return
	}
	type apiMessage struct {
		ID          string     `json:"id"`
		ToPhone     string     `json:"to_phone"`
		Status      string     `json:"status"`
		WAMessageID string     `json:"wa_message_id"`
		Error       string     `json:"error"`
		Attempts    int        `json:"attempts"`
		CreatedAt   time.Time  `json:"created_at"`
		SentAt      *time.Time `json:"sent_at"`
	}
	out := make([]apiMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, apiMessage{ID: m.ID, ToPhone: m.ToPhone, Status: m.Status,
			WAMessageID: m.WAMessageID, Error: m.Error, Attempts: m.Attempts,
			CreatedAt: m.CreatedAt, SentAt: m.SentAt})
	}
	writeJSON(w, http.StatusOK, out)
}
