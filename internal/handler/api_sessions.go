package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

type apiSession struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Phone     string `json:"phone"`
	JID       string `json:"jid"`
	State     string `json:"state"`
	Connected bool   `json:"connected"`
	QueueLen  int    `json:"queue_len"`
	// IsMaster mirrors wa.Snapshot.IsMaster — routing intent, not
	// connection intent. Carried here so PUT .../master can report back
	// the value it just set, and GET .../sessions can show it.
	IsMaster bool `json:"is_master"`
}

type apiEvent struct {
	At   time.Time `json:"at"`
	Note string    `json:"note"`
}

func apiFromSnapshot(s wa.Snapshot) apiSession {
	return apiSession{ID: s.ID, Name: s.Name, Phone: s.Phone, JID: s.JID,
		State: string(s.State), Connected: s.Connected, QueueLen: s.QueueLen,
		IsMaster: s.IsMaster}
}

// GetAPISessions lists all sessions as live snapshots.
func (h *handlers) GetAPISessions(w http.ResponseWriter, r *http.Request) {
	snaps := h.Manager.Snapshots()
	out := make([]apiSession, 0, len(snaps))
	for _, s := range snaps {
		out = append(out, apiFromSnapshot(s))
	}
	writeJSON(w, http.StatusOK, out)
}

// PostAPISessions creates a session (state=new); pairing is a separate call.
//
// The request struct carries explicit json tags on ALL THREE fields
// deliberately, not just IsMaster: Go's encoding/json matches an untagged
// field name case-INSENSITIVELY, so an untagged IsMaster would silently
// bind "ismaster" but NOT the documented "is_master" — the same trap this
// struct used to be one field away from walking into.
func (h *handlers) PostAPISessions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		Phone    string `json:"phone"`
		IsMaster bool   `json:"is_master"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	s, err := h.Manager.Create(r.Context(), req.Name, req.Phone)
	if err != nil {
		var verr *wa.ValidationError
		if errors.As(err, &verr) {
			writeError(w, http.StatusBadRequest, verr.Msg)
			return
		}
		writeError(w, http.StatusInternalServerError, "could not create session")
		return
	}
	// Manager.Create's signature stays untouched (9 test call sites + 2
	// production, per the plan) — flag it as master AFTER creation instead,
	// the same pattern PostSessionCreate would use for the UI form. A new
	// row's is_master column already defaults to 0, so the unflagged case
	// needs nothing further.
	if req.IsMaster {
		if err := s.SetMaster(r.Context(), true); err != nil {
			writeWAError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusCreated, apiFromSnapshot(s.Snapshot()))
}

// GetAPISession returns one snapshot plus the recent event ring.
func (h *handlers) GetAPISession(w http.ResponseWriter, r *http.Request) {
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	snap := s.Snapshot()
	events := make([]apiEvent, 0, len(snap.LastEvents))
	for _, e := range snap.LastEvents {
		events = append(events, apiEvent{At: e.At, Note: e.Note})
	}
	writeJSON(w, http.StatusOK, struct {
		apiSession
		LastEvents []apiEvent `json:"last_events"`
	}{apiFromSnapshot(snap), events})
}

// DeleteAPISession is the full teardown (unlink + purge).
func (h *handlers) DeleteAPISession(w http.ResponseWriter, r *http.Request) {
	if err := h.Manager.Delete(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, wa.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PostAPISessionActivate / PostAPISessionDeactivate toggle the connection.
func (h *handlers) PostAPISessionActivate(w http.ResponseWriter, r *http.Request) {
	h.apiToggle(w, r, func(s *wa.ManagedSession) error { return s.Activate(r.Context()) })
}

func (h *handlers) PostAPISessionDeactivate(w http.ResponseWriter, r *http.Request) {
	h.apiToggle(w, r, func(s *wa.ManagedSession) error { return s.Deactivate(r.Context()) })
}

func (h *handlers) apiToggle(w http.ResponseWriter, r *http.Request, op func(*wa.ManagedSession) error) {
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if err := op(s); err != nil {
		writeWAError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiFromSnapshot(s.Snapshot()))
}

// PutAPISessionMaster is PUT /api/v1/sessions/{id}/master — {is_master}.
// Follows PutAPISessionAutoReconnect's shape (api_ops.go) minus the
// opsOr501 guard: unlike auto-reconnect/history-sync/pause/resume, is_master
// is WSM-native routing state, not a backend capability, so it must work
// identically on whatsmeow and waxum.
func (h *handlers) PutAPISessionMaster(w http.ResponseWriter, r *http.Request) {
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	var req struct {
		IsMaster bool `json:"is_master"`
	}
	if !decodeOpsBody(w, r, &req) {
		return
	}
	if err := s.SetMaster(r.Context(), req.IsMaster); err != nil {
		writeWAError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiFromSnapshot(s.Snapshot()))
}

// GetAPISessionPairing exposes the pairing box — including the raw QR payload
// so a machine client can render its own QR (docs/08 §4).
func (h *handlers) GetAPISessionPairing(w http.ResponseWriter, r *http.Request) {
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	ps, ok := s.PairingSnapshot()
	if !ok {
		if s.Snapshot().State == wa.StateConnected {
			writeJSON(w, http.StatusOK, map[string]any{"done": true, "error": ""})
			return
		}
		writeError(w, http.StatusNotFound, "session is not pairing")
		return
	}
	code, _ := s.PairingCode()
	writeJSON(w, http.StatusOK, map[string]any{
		"mode": string(ps.Mode), "gen": ps.Gen, "has_code": ps.HasCode, "qr": code,
		"phone_code": ps.PhoneCode, "seconds_left": ps.SecondsLeft,
		"done": ps.Done, "error": ps.Err,
	})
}
