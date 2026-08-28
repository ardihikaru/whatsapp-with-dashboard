package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// contactsCheckRequest is POST .../contacts/check's body. phones accepts
// human formatting ("+62 812-0000-0000" or "6289900000000") — checkContacts
// runs every entry through wa.NormalizePhone individually.
type contactsCheckRequest struct {
	Phones []string `json:"phones"`
}

// decodeContactsCheckRequest reads the body through the same
// io.LimitReader(maxBodyBytes) idiom every other API handler uses, and
// rejects a malformed or empty request BEFORE either route picks a session
// — mirroring decodeSendRequest/decodeMediaSendRequest's shape.
func decodeContactsCheckRequest(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	var req contactsCheckRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return nil, false
	}
	if len(req.Phones) == 0 {
		writeError(w, http.StatusBadRequest, "phones is required")
		return nil, false
	}
	return req.Phones, true
}

// respondContactsCheck is the shared tail of both routes below: run
// checkContacts and write its result, or map its error. errTooManyPhones
// is the one checkContacts error that is a caller mistake (400), not a
// backend condition — everything else goes through writeWAError exactly
// like every other Phase 6 handler (see api.go's doc comment on why that
// switch is not hand-rolled per handler).
func (h *handlers) respondContactsCheck(w http.ResponseWriter, r *http.Request, s *wa.ManagedSession, phones []string) {
	verdicts, err := h.checkContacts(r.Context(), s.ID, phones)
	if err != nil {
		if errors.Is(err, errTooManyPhones) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeWAError(w, err)
		return
	}
	snap := s.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"session": map[string]any{"id": snap.ID, "name": snap.Name},
		"results": verdicts,
	})
}

// PostAPIContactsCheck is POST /api/v1/sessions/{id}/contacts/check — the
// explicit-session route, mirroring PostAPISendMessage's split from its
// auto-routed counterpart below.
func (h *handlers) PostAPIContactsCheck(w http.ResponseWriter, r *http.Request) {
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	phones, ok := decodeContactsCheckRequest(w, r)
	if !ok {
		return
	}
	h.respondContactsCheck(w, r, s, phones)
}

// PostAPIContactsCheckAuto is POST /api/v1/contacts/check — auto-routed,
// mirroring PostAPISendMessageAuto's shape: decode first, THEN pick a
// connected session.
func (h *handlers) PostAPIContactsCheckAuto(w http.ResponseWriter, r *http.Request) {
	phones, ok := decodeContactsCheckRequest(w, r)
	if !ok {
		return
	}
	s, _, ok := h.Manager.SelectConnected(r.Context())
	if !ok {
		writeError(w, http.StatusConflict, "no connected session available")
		return
	}
	h.respondContactsCheck(w, r, s, phones)
}
