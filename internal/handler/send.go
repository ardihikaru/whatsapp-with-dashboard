package handler

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/auth"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

const (
	maxTextChars  = 4096
	sendWaitLimit = 15 * time.Second
)

type sendPageData struct {
	PageBase
	Connected []wa.Snapshot
}

type sendResultData struct {
	Status      string // sent | queued | failed | "" (validation error)
	WAMessageID string
	Error       string
	SessionName string
}

// GetSend renders the compose page. Automatic routing is the default; the
// select allows explicit routing when needed.
func (h *handlers) GetSend(w http.ResponseWriter, r *http.Request) {
	var connected []wa.Snapshot
	for _, s := range h.Manager.Snapshots() {
		if s.Connected {
			connected = append(connected, s)
		}
	}
	data := sendPageData{Connected: connected}
	data.PageBase = h.newPageBase(r, "Send Message", "/send")
	h.Renderer.RenderPage(w, "send", data)
}

// PostSend validates, logs the message row, enqueues, and waits up to 15s for
// the worker. Every outcome — including validation errors — renders the
// send_result partial inline so the form contents are never lost.
func (h *handlers) PostSend(w http.ResponseWriter, r *http.Request) {
	text := strings.TrimSpace(r.FormValue("text"))
	if text == "" || len(text) > maxTextChars {
		h.sendResult(w, sendResultData{Error: "message text is required (max 4096 characters)"})
		return
	}
	digits, err := wa.NormalizePhone(r.FormValue("to"))
	if err != nil {
		h.sendResult(w, sendResultData{Error: err.Error()})
		return
	}
	s, ok := h.Manager.Get(r.FormValue("session_id"))
	if r.FormValue("routing") == "auto" || r.FormValue("session_id") == "" {
		var snap wa.Snapshot
		s, snap, ok = h.Manager.SelectConnected(r.Context())
		if !ok {
			h.sendResult(w, sendResultData{Error: "no connected sessions available"})
			return
		}
		_ = snap
	} else if !ok {
		h.sendResult(w, sendResultData{Error: "select a connected session"})
		return
	}

	// The human /send page is a manual, un-attributed send.
	attr := message.Attribution{Category: message.CategoryManual}
	outcome, statusCode := h.sendThroughSession(r, s, digits, text, attr)
	if statusCode >= 400 {
		h.sendResult(w, sendResultData{Status: outcome.Status, Error: outcome.Error, SessionName: s.Name})
		return
	}
	h.sendResult(w, sendResultData{
		Status: outcome.Status, WAMessageID: outcome.WAMessageID, Error: outcome.Error,
		SessionName: s.Name,
	})
}

func (h *handlers) sendResult(w http.ResponseWriter, data sendResultData) {
	h.Renderer.RenderPartial(w, "send_result", data)
}

type sendOutcome struct {
	ID          string
	Status      string
	WAMessageID string
	Error       string
}

// sendThroughSession is the phone-number path: the caller has bare digits.
func (h *handlers) sendThroughSession(r *http.Request, s *wa.ManagedSession, digits, text string, attr message.Attribution) (sendOutcome, int) {
	return h.sendThroughJID(r, s, wa.ToJID(digits), digits, text, attr)
}

// sendThroughJID is the whole send path — audit row, enqueue, 15s wait — for ANY
// destination. It took bare phone digits until Fase 9 and built the JID itself,
// which is what kept group sends out.
//
// Nothing below the queue needed changing: sendJob.to has always been a wa.JID
// and trySend hands it straight to the backend, so the token bucket, the retry
// loop and MarkSent are destination-agnostic already. That is why kb-30 is a
// validator plus a handler and touches no crypto.
//
// toDisplay is stored in wa_messages.to_phone. For a group that is the @g.us JID
// rather than a phone number — the column name is now slightly wrong, and that is
// a better trade than a second audit table: GET /sessions/{id}/messages shows
// group sends for free, with no new code.
func (h *handlers) sendThroughJID(
	r *http.Request, s *wa.ManagedSession, to wa.JID, toDisplay, text string, attr message.Attribution,
) (sendOutcome, int) {
	msg := &message.Message{ID: wa.NewID(), SessionID: s.ID, ToPhone: toDisplay,
		Body: text, Status: message.StatusQueued, CreatedAt: time.Now().UTC(),
		ClientRef: attr.ClientRef, TenantRef: attr.TenantRef, Category: attr.Category}
	// api_client_id comes from the authenticated principal, never the request body,
	// so a client cannot attribute its sends to another. Empty for the /send UI.
	if p, ok := auth.APIPrincipalFrom(r.Context()); ok {
		msg.APIClientID = p.ClientID
	}
	if err := h.Messages.Insert(r.Context(), msg); err != nil {
		return sendOutcome{Error: "could not store the message"}, http.StatusInternalServerError
	}
	done, err := s.Enqueue(msg.ID, to, text)
	if err != nil {
		reason := "could not queue the message"
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, wa.ErrQueueFull):
			reason = "send queue full"
			status = http.StatusTooManyRequests
		case errors.Is(err, wa.ErrNotConnected):
			reason = "session not connected"
			status = http.StatusConflict
		}
		_ = h.Messages.MarkFailed(r.Context(), msg.ID, reason, 0)
		return sendOutcome{ID: msg.ID, Status: message.StatusFailed, Error: reason}, status
	}

	select {
	case res := <-done:
		if res.Err != nil {
			return sendOutcome{ID: msg.ID, Status: message.StatusFailed, Error: res.Err.Error()}, http.StatusOK
		}
		return sendOutcome{ID: msg.ID, Status: message.StatusSent, WAMessageID: res.WAMessageID}, http.StatusOK
	case <-time.After(sendWaitLimit):
		return sendOutcome{ID: msg.ID, Status: message.StatusQueued, Error: "still queued - it will be sent in order"}, http.StatusAccepted
	case <-r.Context().Done():
		return sendOutcome{ID: msg.ID, Status: message.StatusQueued}, http.StatusAccepted
	}
}
