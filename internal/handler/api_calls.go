package handler

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/auth"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/call"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// callsDisabledMessage is answered, unconditionally, by every /calls/*
// endpoint when Deps.CallsEnabled (default false, WSM_CALLS_ENABLED) is
// off. See CLAUDE.md's calls hazard note and config.DefaultCallsEnabled's
// doc comment for why: WhatsApp's terms are hostile to automated outbound
// calling, and since 2026-08-23 the paired device identity has no second
// copy anywhere — a ban is unrecoverable. Shipping the capability while
// leaving it disarmed is the honest default. 501, not 403: this reads as
// "not implemented on this deployment" — the same meaning wa.ErrUnsupported
// carries everywhere else in this API — not "you are not allowed to do
// this."
const callsDisabledMessage = "voice calling is disabled on this deployment (set WSM_CALLS_ENABLED=true to enable it)"

// callsGuard answers 501 and returns false when calling is disabled;
// every /calls/* handler calls this FIRST, before touching the session
// manager or wa_calls, so a disabled deployment places nothing at all.
func (h *handlers) callsGuard(w http.ResponseWriter) bool {
	if h.CallsEnabled {
		return true
	}
	writeError(w, http.StatusNotImplemented, callsDisabledMessage)
	return false
}

// apiCall is the JSON shape for a wa_calls row — call.Call itself carries
// no json tags (its Recent/Insert path never needed them before this
// handler), so this mirrors api_messages.go's apiMessage: an explicit,
// snake_case view type rather than marshaling the domain struct directly.
type apiCall struct {
	ID           string     `json:"id"`
	ToPhone      string     `json:"to_phone"`
	Kind         string     `json:"kind"`
	Text         string     `json:"text,omitempty"`
	Voice        string     `json:"voice,omitempty"`
	AudioURL     string     `json:"audio_url,omitempty"`
	CallID       string     `json:"call_id,omitempty"`
	RecordingURL string     `json:"recording_url,omitempty"`
	Status       string     `json:"status"`
	Error        string     `json:"error,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	EndedAt      *time.Time `json:"ended_at,omitempty"`
}

func apiFromCall(c call.Call) apiCall {
	return apiCall{
		ID: c.ID, ToPhone: c.ToPhone, Kind: c.Kind, Text: c.Text, Voice: c.Voice,
		AudioURL: c.AudioURL, CallID: c.CallID, RecordingURL: c.RecordingURL,
		Status: c.Status, Error: c.Error, CreatedAt: c.CreatedAt, EndedAt: c.EndedAt,
	}
}

// placeCall is the shared tail of PostAPICallTTS/PostAPICallPlay: insert a
// wa_calls row (status=placing), place the call, then MarkPlaced/MarkFailed
// on the outcome — mirroring how dispatchSend (send.go) inserts a
// wa_messages row before ever enqueueing. place is either
// s.PlaceCallTTS or s.PlaceCallPlay, already bound to its call-specific
// arguments by the caller.
func (h *handlers) placeCall(w http.ResponseWriter, r *http.Request, c *call.Call, place func() (wa.CallOutcome, error)) {
	if p, ok := auth.APIPrincipalFrom(r.Context()); ok {
		c.APIClientID = p.ClientID
	}
	if err := h.Calls.Insert(r.Context(), c); err != nil {
		writeError(w, http.StatusInternalServerError, "could not record the call")
		return
	}
	outcome, err := place()
	if err != nil {
		_ = h.Calls.MarkFailed(r.Context(), c.ID, err.Error())
		writeWAError(w, err)
		return
	}
	_ = h.Calls.MarkPlaced(r.Context(), c.ID, outcome.CallID, outcome.RecordingURL)
	writeJSON(w, http.StatusOK, map[string]any{
		"id": c.ID, "status": call.StatusPlaced,
		"call_id": outcome.CallID, "recording_url": outcome.RecordingURL,
	})
}

// PostAPICallTTS is POST /api/v1/sessions/{id}/calls/tts — {to, text, voice}.
// voice defaults to waxum's own DefaultTTSVoice when empty; that default is
// applied inside internal/waxum.Client.CallTTS, not here.
func (h *handlers) PostAPICallTTS(w http.ResponseWriter, r *http.Request) {
	if !h.callsGuard(w) {
		return
	}
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	var req struct {
		To    string `json:"to"`
		Text  string `json:"text"`
		Voice string `json:"voice"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	digits, err := wa.NormalizePhone(req.To)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}
	c := &call.Call{ID: wa.NewID(), SessionID: s.ID, ToPhone: digits, Kind: call.KindTTS,
		Text: req.Text, Voice: req.Voice, Status: call.StatusPlacing, CreatedAt: time.Now().UTC()}
	h.placeCall(w, r, c, func() (wa.CallOutcome, error) {
		return s.PlaceCallTTS(r.Context(), wa.ToJID(digits), req.Text, req.Voice)
	})
}

// PostAPICallPlay is POST /api/v1/sessions/{id}/calls/play —
// {to, audio_url}. NOTE for an operator: waxum's SSRF guard rejects any
// private audio_url (loopback/RFC1918/link-local/… — see
// internal/waxum's own doc comments), so this can never point at
// anything inside the compose network.
func (h *handlers) PostAPICallPlay(w http.ResponseWriter, r *http.Request) {
	if !h.callsGuard(w) {
		return
	}
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	var req struct {
		To       string `json:"to"`
		AudioURL string `json:"audio_url"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	digits, err := wa.NormalizePhone(req.To)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.AudioURL = strings.TrimSpace(req.AudioURL)
	if req.AudioURL == "" {
		writeError(w, http.StatusBadRequest, "audio_url is required")
		return
	}
	c := &call.Call{ID: wa.NewID(), SessionID: s.ID, ToPhone: digits, Kind: call.KindPlay,
		AudioURL: req.AudioURL, Status: call.StatusPlacing, CreatedAt: time.Now().UTC()}
	h.placeCall(w, r, c, func() (wa.CallOutcome, error) {
		return s.PlaceCallPlay(r.Context(), wa.ToJID(digits), req.AudioURL)
	})
}

// PostAPICallTerminate is POST /api/v1/sessions/{id}/calls/terminate —
// {peer, call_id, reason}.
//
// After a successful backend terminate, this flips the wa_calls row to
// terminated via MarkTerminatedByCallID — keyed by waxum's call_id, since
// that (not our own row id) is all this handler is ever given. Best-effort,
// like MarkFailed/MarkPlaced elsewhere in this file: the backend call
// already succeeded by the time this runs, so a failure to update our own
// bookkeeping must not turn into a failed response for a call that WhatsApp
// has, in fact, already ended.
func (h *handlers) PostAPICallTerminate(w http.ResponseWriter, r *http.Request) {
	if !h.callsGuard(w) {
		return
	}
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	var req struct {
		Peer   string `json:"peer"`
		CallID string `json:"call_id"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	digits, err := wa.NormalizePhone(req.Peer)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.CallID = strings.TrimSpace(req.CallID)
	if req.CallID == "" {
		writeError(w, http.StatusBadRequest, "call_id is required")
		return
	}
	if err := s.TerminateCall(r.Context(), wa.ToJID(digits), req.CallID, req.Reason); err != nil {
		writeWAError(w, err)
		return
	}
	if err := h.Calls.MarkTerminatedByCallID(r.Context(), s.ID, req.CallID); err != nil {
		log.Printf("call terminate: could not mark %s/%s terminated in wa_calls (best-effort, continuing): %v", s.ID, req.CallID, err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetAPISessionCalls is GET /api/v1/sessions/{id}/calls — recent rows from
// wa_calls, mirroring GetAPISessionMessages (api_messages.go).
func (h *handlers) GetAPISessionCalls(w http.ResponseWriter, r *http.Request) {
	if !h.callsGuard(w) {
		return
	}
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	calls, err := h.Calls.Recent(r.Context(), s.ID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read calls")
		return
	}
	out := make([]apiCall, 0, len(calls))
	for _, c := range calls {
		out = append(out, apiFromCall(c))
	}
	writeJSON(w, http.StatusOK, out)
}
