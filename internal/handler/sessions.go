package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

type sessionsPageData struct {
	PageBase
	// GroupCodes feeds session-new's <datalist> — see GetSessionNew. Not
	// needed on sessionRowsData: the rows table's own group badges stay
	// read-only (session_rows.html) — editing happens in the single Edit
	// session dialog (GetSessionEdit/PostSessionEdit below), fetched on
	// demand per row, which pulls its own GroupCodes list.
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

// reconcileBannerData feeds the reconcile_banner partial. Cooldown is true
// when an operator-triggered check was answered from the stored verdict
// because another ran moments ago.
type reconcileBannerData struct {
	Report   wa.ReconcileReport
	Cooldown bool
}

// GetSessionReconcile renders the polled banner above the sessions table: a
// warning when the reconciler (wa.Manager.Reconcile) has found WSM and the
// backend disagreeing past the grace period, plus the "Check now" bar. Renders
// nothing at all when no reconciler runs. Authenticated page route, so it may
// name sessions; /api/v1/health, which is public, may not.
func (h *handlers) GetSessionReconcile(w http.ResponseWriter, r *http.Request) {
	h.Renderer.RenderPartial(w, "reconcile_banner", reconcileBannerData{Report: h.Manager.Reconcile()})
}

// PostSessionReconcile is the "Check now" button: it runs the reconciler
// immediately (coalesced by wa.Manager.ReconcileNow's cooldown, so a burst of
// clicks cannot become a burst of work) and swaps the banner with the result.
// A page route (cookie session via RequirePage) on purpose — never /api/v1.
func (h *handlers) PostSessionReconcile(w http.ResponseWriter, r *http.Request) {
	rep, ran := h.Manager.ReconcileNow(time.Now())
	h.Renderer.RenderPartial(w, "reconcile_banner", reconcileBannerData{Report: rep, Cooldown: !ran})
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

// sessionGroupCodeResultData feeds the session_group_code_result partial —
// the ops page's OWN small result div (sessions/ops.html), the only
// remaining caller of PostSessionSetGroupCode below now that the
// list page's editing goes entirely through the Edit session dialog.
type sessionGroupCodeResultData struct {
	GroupCode string
	Error     string
}

// PostSessionSetGroupCode is the submit target for the ops page's own
// static routing-group card (sessions/ops.html) ONLY — the /sessions list's
// editing now goes through PostSessionEdit below, which calls
// SetGroupCodes directly rather than this HTTP route.
func (h *handlers) PostSessionSetGroupCode(w http.ResponseWriter, r *http.Request) {
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := s.SetGroupCode(r.Context(), r.FormValue("group_code")); err != nil {
		var verr *wa.ValidationError
		msg := "could not save routing group"
		if errors.As(err, &verr) {
			msg = verr.Msg
		} else {
			slog.Warn("set group code", "session", s.ID, "err", err)
		}
		h.Renderer.RenderPartial(w, "session_group_code_result", sessionGroupCodeResultData{Error: msg})
		return
	}
	h.Renderer.RenderPartial(w, "session_group_code_result", sessionGroupCodeResultData{GroupCode: s.Snapshot().GroupCode})
}

// sessionEditDialogData feeds the session_edit_dialog partial — the single
// <dialog> loaded into #session-edit-slot (sessions/list.html), OUTSIDE the
// polled #session-rows tbody for the same reason the old two inline cards
// sat outside it: the 5s poll's hx-swap="innerHTML" would wipe a half-typed
// value mid-edit. Replaces the old separate name/group-code editors with
// one form covering name, routing groups, the master toggle and the
// self-reminder toggle, saved by ONE POST (PostSessionEdit) that validates
// everything before changing anything.
type sessionEditDialogData struct {
	Session wa.Snapshot
	// Name/GroupCodesCSV/Master/SelfReminder carry the operator's
	// just-submitted values on a validation error (so a typo stays visible
	// to fix) and the stored values otherwise.
	Name          string
	GroupCodesCSV string
	Master        bool
	SelfReminder  bool
	GroupCodes    []string // every known group, for the chip input's datalist
	Error         string
	// CurrentHolderName names whichever OTHER session currently carries
	// the self-reminder flag, so the toggle's hint can read "will move
	// from <name>" — "" when no other session holds it (or this session
	// already does).
	CurrentHolderName string
}

// groupCodesToCSV/csvToGroupCodes round-trip the chip input's hidden field
// (session-edit.js keeps it comma-separated) — kept as named conversions
// rather than inline strings.Join/Split calls so both directions stay next
// to each other and a future delimiter change touches one place.
func groupCodesToCSV(codes []string) string { return strings.Join(codes, ",") }

func csvToGroupCodes(csv string) []string {
	var out []string
	for _, part := range strings.Split(csv, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// selfReminderHolderName returns the display name of whichever OTHER
// session currently carries the self-reminder flag, or "" when none does
// (or only exceptID does) — see sessionEditDialogData.CurrentHolderName's
// doc comment.
func (h *handlers) selfReminderHolderName(exceptID string) string {
	for _, snap := range h.Manager.Snapshots() {
		if snap.SelfReminder && snap.ID != exceptID {
			return snap.Name
		}
	}
	return ""
}

// GetSessionEdit renders the Edit session dialog, prefilled with this
// session's current values. 404s like every other single-session GET on a
// stale/unknown id.
func (h *handlers) GetSessionEdit(w http.ResponseWriter, r *http.Request) {
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	snap := s.Snapshot()
	h.Renderer.RenderPartial(w, "session_edit_dialog", sessionEditDialogData{
		Session: snap, Name: snap.Name, GroupCodesCSV: groupCodesToCSV(snap.GroupCodes),
		Master: snap.IsMaster, SelfReminder: snap.SelfReminder,
		GroupCodes:        h.Manager.GroupCodes(),
		CurrentHolderName: h.selfReminderHolderName(snap.ID),
	})
}

// PostSessionEdit validates name, routing groups, the master flag and the
// self-reminder flag TOGETHER before applying ANY of them — a bad group
// code or an empty name must leave every field (including master/
// self-reminder) exactly as they were, not half-saved. On success it fires
// HX-Trigger: sessions-changed (picked up by both the polled tbody and
// session-edit.js, which closes the dialog) and re-renders the dialog's
// body with a "Saved" state; a validation error re-renders in place with
// the operator's own typed values kept.
func (h *handlers) PostSessionEdit(w http.ResponseWriter, r *http.Request) {
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	name := r.FormValue("name")
	groupCodes := csvToGroupCodes(r.FormValue("group_codes"))
	master := parseFormBool(r.FormValue("master"))
	selfReminder := parseFormBool(r.FormValue("self_reminder"))

	fail := func(msg string) {
		h.Renderer.RenderPartial(w, "session_edit_dialog", sessionEditDialogData{
			Session: s.Snapshot(), Name: name, GroupCodesCSV: groupCodesToCSV(groupCodes),
			Master: master, SelfReminder: selfReminder, GroupCodes: h.Manager.GroupCodes(),
			CurrentHolderName: h.selfReminderHolderName(s.ID), Error: msg,
		})
	}

	// Validate everything BEFORE writing anything — wa.ValidateSessionName
	// and wa.NormalizeGroupCodes are the same pure checks
	// SetName/SetGroupCodes apply internally, called here first so a bad
	// name does not leave the groups half-saved and vice versa.
	if _, err := wa.ValidateSessionName(name); err != nil {
		fail(err.Error())
		return
	}
	if _, err := wa.NormalizeGroupCodes(groupCodes); err != nil {
		fail(err.Error())
		return
	}

	if err := s.SetName(r.Context(), name); err != nil {
		slog.Error("edit session: set name", "session", s.ID, "err", err)
		fail("could not save the session")
		return
	}
	if err := s.SetGroupCodes(r.Context(), groupCodes); err != nil {
		slog.Error("edit session: set group codes", "session", s.ID, "err", err)
		fail("could not save the session")
		return
	}
	if err := s.SetMaster(r.Context(), master); err != nil {
		slog.Error("edit session: set master", "session", s.ID, "err", err)
	}
	if err := h.Manager.SetSelfReminder(r.Context(), s.ID, selfReminder); err != nil {
		slog.Error("edit session: set self-reminder", "session", s.ID, "err", err)
	}

	snap := s.Snapshot()
	w.Header().Set("HX-Trigger", "sessions-changed")
	h.Renderer.RenderPartial(w, "session_edit_dialog", sessionEditDialogData{
		Session: snap, Name: snap.Name, GroupCodesCSV: groupCodesToCSV(snap.GroupCodes),
		Master: snap.IsMaster, SelfReminder: snap.SelfReminder, GroupCodes: h.Manager.GroupCodes(),
		CurrentHolderName: h.selfReminderHolderName(snap.ID),
	})
}

// sessionDeleteDialogData feeds the session_delete_dialog partial — the
// confirmation dialog that replaces the browser's native hx-confirm. Keeps
// the "does NOT unlink the device on the waxum backend / Linked devices"
// warning (see TestTheDeleteConfirmationDoesNotPromiseAnUnlinkTheWaxumBackendCannotDo).
type sessionDeleteDialogData struct {
	Session wa.Snapshot
}

// GetSessionDeleteConfirm renders the delete confirmation dialog for one
// session. 404s like every other single-session GET on a stale/unknown id.
func (h *handlers) GetSessionDeleteConfirm(w http.ResponseWriter, r *http.Request) {
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	h.Renderer.RenderPartial(w, "session_delete_dialog", sessionDeleteDialogData{Session: s.Snapshot()})
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

// PostSessionDelete purges the WSM row and calls the backend's own Logout —
// on the default waxum backend this does NOT unlink the device from the
// handset (waxum has no unlink API; see session_delete_dialog.html's own
// warning and CLAUDE.md's "Deleting a session does NOT unlink the device"
// hazard). Reached only through the delete confirmation dialog
// (GetSessionDeleteConfirm), which types-to-confirm before this route is
// ever called — unchanged by the Part 3 dialog redesign.
func (h *handlers) PostSessionDelete(w http.ResponseWriter, r *http.Request) {
	if err := h.Manager.Delete(r.Context(), r.PathValue("id")); err != nil && !errors.Is(err, wa.ErrNotFound) {
		slog.Warn("delete session", "err", err)
	}
	h.GetSessionRows(w, r)
}
