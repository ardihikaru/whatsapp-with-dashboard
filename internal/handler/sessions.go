package handler

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

type sessionsPageData struct {
	PageBase
}

type sessionRowsData struct {
	Sessions []wa.Snapshot
}

// GetSessions renders the sessions list page. The tbody loads and re-polls
// itself via hx-get /sessions/rows (load, every 5s), so this page never
// embeds row data.
func (h *handlers) GetSessions(w http.ResponseWriter, r *http.Request) {
	data := sessionsPageData{PageBase: h.newPageBase(r, "WhatsApp Sessions", "/sessions")}
	h.Renderer.RenderPage(w, "sessions", data)
}

// GetSessionRows renders the polled tbody partial: every action button
// targets #session-rows and gets this same partial back, so poll and action
// can never disagree.
func (h *handlers) GetSessionRows(w http.ResponseWriter, r *http.Request) {
	h.Renderer.RenderPartial(w, "session_rows", sessionRowsData{Sessions: h.Manager.Snapshots()})
}

// GetSessionNew renders the create form.
func (h *handlers) GetSessionNew(w http.ResponseWriter, r *http.Request) {
	data := sessionsPageData{PageBase: h.newPageBase(r, "New Session", "/sessions")}
	h.Renderer.RenderPage(w, "session-new", data)
}

// PostSessionCreate validates and inserts the session, then bounces the
// operator straight to the pairing page. Validation errors render inline.
func (h *handlers) PostSessionCreate(w http.ResponseWriter, r *http.Request) {
	s, err := h.Manager.Create(r.Context(), r.FormValue("name"), r.FormValue("phone"))
	if err != nil {
		var verr *wa.ValidationError
		msg := "could not create session"
		if errors.As(err, &verr) {
			msg = verr.Msg
		} else {
			slog.Error("create session", "err", err)
		}
		h.Renderer.RenderPartial(w, "form_error", struct{ Error string }{msg})
		return
	}
	target := h.BasePath + "/sessions/" + s.ID + "/pair"
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// PostSessionActivate reconnects a paired session; the refreshed rows partial
// carries the outcome (badge flips on success, logged_out on a gone device).
func (h *handlers) PostSessionActivate(w http.ResponseWriter, r *http.Request) {
	if s, ok := h.Manager.Get(r.PathValue("id")); ok {
		if err := s.Activate(r.Context()); err != nil {
			slog.Warn("activate", "session", s.ID, "err", err)
		}
	}
	h.GetSessionRows(w, r)
}

// PostSessionDeactivate drops the socket, keeps credentials.
func (h *handlers) PostSessionDeactivate(w http.ResponseWriter, r *http.Request) {
	if s, ok := h.Manager.Get(r.PathValue("id")); ok {
		if err := s.Deactivate(r.Context()); err != nil {
			slog.Warn("deactivate", "session", s.ID, "err", err)
		}
	}
	h.GetSessionRows(w, r)
}

// PostSessionDelete unlinks the device (phone-side too) and purges the row;
// the button carries hx-confirm so this is always operator-confirmed.
func (h *handlers) PostSessionDelete(w http.ResponseWriter, r *http.Request) {
	if err := h.Manager.Delete(r.Context(), r.PathValue("id")); err != nil && !errors.Is(err, wa.ErrNotFound) {
		slog.Warn("delete session", "err", err)
	}
	h.GetSessionRows(w, r)
}
