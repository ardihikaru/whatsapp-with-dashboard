package handler

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// sessionOpsPageData drives GET /sessions/{id}/ops. Session is the plain
// snapshot (name/phone/state) the page heading and back-link need. The
// panel itself is NOT computed here — it fills via hx-get (load, every 5s),
// same as every table on sessions/list.html, so the poll and the initial
// paint can never render two different shapes (renderer.go's pageDefs
// comment on why "session-ops" carries no panel partial in its own
// fileset).
type sessionOpsPageData struct {
	PageBase
	Session wa.Snapshot
	// GroupCodes feeds the routing-group editor's <datalist> — the second,
	// static card GetSessionOps renders below #ops-panel (see ops.html);
	// unlike Session above this is NOT re-fetched by the panel's own poll,
	// since that card sits outside #ops-panel entirely.
	GroupCodes []string
}

// sessionOpsPanelData feeds the session_ops_panel partial — rendered by the
// initial GET AND by every action below, so a toggle can never show state
// the page itself disagrees with (the same rule session_rows/sessions.go
// follows for the sessions table).
//
// Unsupported is set when the active backend has no SessionOps at all
// (whatsmeow) — the phase brief's §3 requirement: this renders a plain
// "not supported by this backend" panel, never a 500 or a blank one. Error
// is set on any OTHER failure (a real backend error once SessionOps exists)
// and is kept distinct from Unsupported so the panel can word the two
// differently.
type sessionOpsPanelData struct {
	SessionID   string
	Unsupported bool
	Error       string

	Paused                  bool
	AutoReconnectEnabled    bool
	AutoReconnectErrorCount uint32
	// HistorySyncSkip mirrors wa.SessionOps.HistorySync's own "skip" naming
	// (capabilities.go) rather than inverting it into a friendlier
	// "SyncEnabled" — the template negates it once, at the one place an
	// operator reads it, rather than this struct and the template each
	// carrying their own sense of the boolean.
	HistorySyncSkip bool
}

// loadOpsPanel resolves the current SessionOps snapshot for sessionID,
// mirroring GetAPISessionOperations' three-call shape (api_ops.go) but
// producing a template-shaped struct instead of a JSON one, and NEVER
// writing an HTTP error status — every failure here is a field on the
// struct the template renders around.
func (h *handlers) loadOpsPanel(r *http.Request, sessionID string) sessionOpsPanelData {
	data := sessionOpsPanelData{SessionID: sessionID}
	ops, err := h.Manager.SessionOps()
	if err != nil {
		if errors.Is(err, wa.ErrUnsupported) {
			data.Unsupported = true
		} else {
			data.Error = err.Error()
		}
		return data
	}
	paused, err := ops.IsPaused(r.Context(), sessionID)
	if err != nil {
		data.Error = err.Error()
		return data
	}
	ar, err := ops.AutoReconnect(r.Context(), sessionID)
	if err != nil {
		data.Error = err.Error()
		return data
	}
	skip, err := ops.HistorySync(r.Context(), sessionID)
	if err != nil {
		data.Error = err.Error()
		return data
	}
	data.Paused = paused
	data.AutoReconnectEnabled = ar.Enabled
	data.AutoReconnectErrorCount = ar.ErrorCount
	data.HistorySyncSkip = skip
	return data
}

// renderOpsPanel is every action handler's shared tail: recompute the
// current snapshot and swap #ops-panel with it — the poll-and-action-agree
// rule from CLAUDE.md, applied to a panel rather than a table.
//
// actionErr, when non-empty, overrides whatever loadOpsPanel's own read
// calls come back with — the same precedence renderWebhookRows /
// renderWebhookDLQRows (webhooks_ui.go) give a failed register/replay over
// a subsequent (successful) list: the operator needs to see why THEIR
// click failed, not a read that happened to succeed right after it. It is
// ignored when loadOpsPanel reports Unsupported, since that message ("not
// supported by this backend") already explains why the action could not
// have worked and is more specific than a generic actionErr string.
func (h *handlers) renderOpsPanel(w http.ResponseWriter, r *http.Request, sessionID, actionErr string) {
	data := h.loadOpsPanel(r, sessionID)
	if actionErr != "" && !data.Unsupported {
		data.Error = actionErr
	}
	h.Renderer.RenderPartial(w, "session_ops_panel", data)
}

// GetSessionOps renders the per-session operations page. 404s only when the
// SESSION itself does not exist — an unsupported backend still renders the
// page, with the panel explaining why (loadOpsPanel's Unsupported field).
func (h *handlers) GetSessionOps(w http.ResponseWriter, r *http.Request) {
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	snap := s.Snapshot()
	data := sessionOpsPageData{Session: snap, GroupCodes: h.Manager.GroupCodes()}
	data.PageBase = h.newPageBase(r, "Session Operations — "+snap.Name, "/sessions")
	h.Renderer.RenderPage(w, "session-ops", data)
}

// GetSessionOpsPanel is the panel's own poll target (load, every 5s) — see
// sessionOpsPageData's doc comment.
func (h *handlers) GetSessionOpsPanel(w http.ResponseWriter, r *http.Request) {
	h.renderOpsPanel(w, r, r.PathValue("id"), "")
}

// opsAction is the shared shape of every button below: resolve the
// session, resolve SessionOps (rendering the Unsupported/Error panel and
// stopping if the backend has none), run fn, and re-render the panel with
// whatever fn failed with surfaced as the panel's own Error — the same
// rule loadOpsPanel's three read calls already follow. A failed action
// used to only slog.Warn and re-render an ordinary, error-free panel: the
// operator clicked a button, nothing happened, and the page said nothing.
// errors.Is(err, wa.ErrUnsupported) gets the same "not supported by this
// backend" wording loadOpsPanel gives an unsupported backend, rather than
// leaking a raw ErrUnsupported.Error() string that means nothing to an
// operator; every other error is shown verbatim, same as loadOpsPanel.
func (h *handlers) opsAction(w http.ResponseWriter, r *http.Request, fn func(ops wa.SessionOps, sessionID string) error) {
	id := r.PathValue("id")
	if _, ok := h.Manager.Get(id); !ok {
		http.NotFound(w, r)
		return
	}
	ops, err := h.Manager.SessionOps()
	if err != nil {
		h.renderOpsPanel(w, r, id, "")
		return
	}
	actionErr := ""
	if err := fn(ops, id); err != nil {
		slog.Warn("session ops action failed", "session", id, "err", err)
		if errors.Is(err, wa.ErrUnsupported) {
			actionErr = "not supported by this backend"
		} else {
			actionErr = err.Error()
		}
	}
	h.renderOpsPanel(w, r, id, actionErr)
}

func (h *handlers) PostSessionOpsPause(w http.ResponseWriter, r *http.Request) {
	h.opsAction(w, r, func(ops wa.SessionOps, id string) error {
		_, err := ops.Pause(r.Context(), id)
		return err
	})
}

func (h *handlers) PostSessionOpsResume(w http.ResponseWriter, r *http.Request) {
	h.opsAction(w, r, func(ops wa.SessionOps, id string) error {
		_, err := ops.Resume(r.Context(), id)
		return err
	})
}

func (h *handlers) PostSessionOpsAutoReconnectEnable(w http.ResponseWriter, r *http.Request) {
	h.opsAction(w, r, func(ops wa.SessionOps, id string) error {
		_, err := ops.SetAutoReconnect(r.Context(), id, true)
		return err
	})
}

func (h *handlers) PostSessionOpsAutoReconnectDisable(w http.ResponseWriter, r *http.Request) {
	h.opsAction(w, r, func(ops wa.SessionOps, id string) error {
		_, err := ops.SetAutoReconnect(r.Context(), id, false)
		return err
	})
}

// PostSessionOpsHistorySyncEnable turns history sync ON, i.e. skip=false —
// see api_ops.go's PutAPISessionHistorySync doc comment: this can pull a
// very large backlog on a long-dormant session. The button carries
// hx-confirm with that warning (session_ops_panel.html); this handler does
// not re-check it, htmx already refused to send the request otherwise.
func (h *handlers) PostSessionOpsHistorySyncEnable(w http.ResponseWriter, r *http.Request) {
	h.opsAction(w, r, func(ops wa.SessionOps, id string) error {
		_, err := ops.SetHistorySync(r.Context(), id, false)
		return err
	})
}

func (h *handlers) PostSessionOpsHistorySyncDisable(w http.ResponseWriter, r *http.Request) {
	h.opsAction(w, r, func(ops wa.SessionOps, id string) error {
		_, err := ops.SetHistorySync(r.Context(), id, true)
		return err
	})
}

// PostSessionOpsAppStateResync triggers a full resync (no collection
// filter, waxum's default mode) — destructive-ish and gated by hx-confirm
// in the template, same as PostSessionDelete (sessions.go).
func (h *handlers) PostSessionOpsAppStateResync(w http.ResponseWriter, r *http.Request) {
	h.opsAction(w, r, func(ops wa.SessionOps, id string) error {
		_, err := ops.AppStateResync(r.Context(), id, nil, "")
		return err
	})
}
