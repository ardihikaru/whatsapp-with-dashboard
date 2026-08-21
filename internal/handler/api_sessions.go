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
}

type apiEvent struct {
	At   time.Time `json:"at"`
	Note string    `json:"note"`
}

func apiFromSnapshot(s wa.Snapshot) apiSession {
	return apiSession{ID: s.ID, Name: s.Name, Phone: s.Phone, JID: s.JID,
		State: string(s.State), Connected: s.Connected, QueueLen: s.QueueLen}
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
func (h *handlers) PostAPISessions(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name, Phone string }
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
		status := http.StatusInternalServerError
		if errors.Is(err, wa.ErrNotPaired) || errors.Is(err, wa.ErrDeviceGone) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
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
