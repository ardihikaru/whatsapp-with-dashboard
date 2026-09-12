package handler

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

type sessionsPageData struct {
	PageBase
	// GroupCodes feeds session-new's <datalist> — see GetSessionNew. Not
	// needed on sessionRowsData: the rows table's own group-code cell stays
	// read-only (session_rows.html) — editing happens in the
	// session_group_editor partial, fetched on demand per row via
	// GetSessionGroupCodeEditor, which pulls its own GroupCodes list.
	GroupCodes []string
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
	data := sessionsPageData{PageBase: h.newPageBase(r, "New Session", "/sessions"), GroupCodes: h.Manager.GroupCodes()}
	h.Renderer.RenderPage(w, "session-new", data)
}

// PostSessionCreate validates and inserts the session, then bounces the
// operator straight to the pairing page. Validation errors render inline.
// Manager.Create's signature stays untouched (every other caller, including
// cmd/e2eseed and the API's PostAPISessions, is unaffected): a checked
// "is_master" form field is applied as a second step via SetMaster, the
// same shape PostAPISessions already uses for the REST equivalent.
//
// group_code is normalised FIRST, before Manager.Create ever runs — unlike
// is_master's checkbox, this is free text an operator can mistype, and
// SetMaster's shape (validate-after-create, swallow the failure into
// slog.Error) is wrong for that: an invalid code would otherwise create the
// session anyway, silently ungrouped, with the only trace of the typo in the
// server log the operator never sees. Normalising first means a bad code
// creates NOTHING and renders the same form_error partial Manager.Create's
// own validation errors use.
func (h *handlers) PostSessionCreate(w http.ResponseWriter, r *http.Request) {
	groupCode, gcErr := wa.NormalizeGroupCode(r.FormValue("group_code"))
	if gcErr != nil {
		h.Renderer.RenderPartial(w, "form_error", struct{ Error string }{gcErr.Error()})
		return
	}
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
	if parseFormBool(r.FormValue("is_master")) {
		if err := s.SetMaster(r.Context(), true); err != nil {
			slog.Error("set master on create", "session", s.ID, "err", err)
		}
	}
	if groupCode != "" {
		// Already validated above; SetGroupCode re-normalizes the same
		// (already-canonical) value, so this cannot fail on the code
		// itself — only a repository error, which SetMaster's own
		// call above treats the same way.
		if err := s.SetGroupCode(r.Context(), groupCode); err != nil {
			slog.Error("set group code on create", "session", s.ID, "err", err)
		}
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

// PostSessionSetMaster flips the routing-intent flag from the row toggle.
// One route serves both directions: the button posts master=true|false via
// hx-vals, mirroring PostSessionActivate/PostSessionDeactivate's shape but
// without a state/socket change — SetMaster touches routing intent only.
func (h *handlers) PostSessionSetMaster(w http.ResponseWriter, r *http.Request) {
	if s, ok := h.Manager.Get(r.PathValue("id")); ok {
		if err := s.SetMaster(r.Context(), parseFormBool(r.FormValue("master"))); err != nil {
			slog.Warn("set master", "session", s.ID, "err", err)
		}
	}
	h.GetSessionRows(w, r)
}

// sessionGroupEditorData feeds the session_group_editor partial — the
// /sessions list's on-demand editor loaded into #session-group-editor
// (list.html), OUTSIDE the polled #session-rows tbody for the same reason
// the ops page's own card sits outside #ops-panel: a live text input would
// get wiped by the next poll's hx-swap="innerHTML" mid-edit.
//
// Value carries the operator's just-submitted text on a validation error
// (so a typo stays visible to fix) and the stored code otherwise — see
// GetSessionGroupCodeEditor and the "view=list" branch of
// PostSessionSetGroupCode below.
type sessionGroupEditorData struct {
	Session    wa.Snapshot
	GroupCodes []string
	Value      string
	Error      string
	Saved      bool
}

// GetSessionGroupCodeEditor renders the list page's on-demand routing-group
// editor for one session (see sessionGroupEditorData's doc comment). 404s
// like every other single-session handler on a stale/unknown id.
func (h *handlers) GetSessionGroupCodeEditor(w http.ResponseWriter, r *http.Request) {
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	snap := s.Snapshot()
	h.Renderer.RenderPartial(w, "session_group_editor", sessionGroupEditorData{
		Session:    snap,
		GroupCodes: h.Manager.GroupCodes(),
		Value:      snap.GroupCode,
	})
}

// sessionGroupCodeResultData feeds the session_group_code_result partial —
// the ops page's OWN small result div, not #session-rows: this route lives
// on /sessions/{id}/ops, which has no such element, and unlike the master
// toggle above (a row-table checkbox this page never carries) there is
// nothing on that page to blindly re-render rows into.
type sessionGroupCodeResultData struct {
	GroupCode string
	Error     string
}

// PostSessionSetGroupCode is the submit target for BOTH routing-group
// editors: the ops page's own static card (ops.html) and the /sessions
// list's on-demand editor (session_group_editor.html), distinguished by a
// hidden "view=list" field the list editor's form carries and the ops
// card's does not. The save logic is identical either way (SetGroupCode,
// *wa.ValidationError → message shown inline rather than swallowed into
// slog — the same reasoning PostSessionCreate's doc comment gives: this is
// free text, not a checkbox, and an operator needs to see why their save
// did nothing); only the partial rendered back — and, on the list view,
// whether the polled tbody is told to refresh — differs.
func (h *handlers) PostSessionSetGroupCode(w http.ResponseWriter, r *http.Request) {
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	code := r.FormValue("group_code")
	fromList := r.FormValue("view") == "list"
	if err := s.SetGroupCode(r.Context(), code); err != nil {
		var verr *wa.ValidationError
		msg := "could not save routing group"
		if errors.As(err, &verr) {
			msg = verr.Msg
		} else {
			slog.Warn("set group code", "session", s.ID, "err", err)
		}
		if fromList {
			h.Renderer.RenderPartial(w, "session_group_editor", sessionGroupEditorData{
				Session: s.Snapshot(), GroupCodes: h.Manager.GroupCodes(), Value: code, Error: msg,
			})
			return
		}
		h.Renderer.RenderPartial(w, "session_group_code_result", sessionGroupCodeResultData{Error: msg})
		return
	}
	snap := s.Snapshot()
	if fromList {
		// The polled #session-rows tbody (list.html) listens for this via
		// hx-trigger="…, sessions-changed from:body" so the badge in the
		// row updates without waiting out the 5s poll.
		w.Header().Set("HX-Trigger", "sessions-changed")
		h.Renderer.RenderPartial(w, "session_group_editor", sessionGroupEditorData{
			Session: snap, GroupCodes: h.Manager.GroupCodes(), Value: snap.GroupCode, Saved: true,
		})
		return
	}
	h.Renderer.RenderPartial(w, "session_group_code_result", sessionGroupCodeResultData{GroupCode: snap.GroupCode})
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
