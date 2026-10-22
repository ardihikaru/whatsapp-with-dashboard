package handler

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// sessionWebhooksPageData drives GET /sessions/{id}/webhooks. Unsupported/
// Error mirror sessionOpsPanelData's split (ops_ui.go): Unsupported for a
// backend with no WebhookAdmin at all (whatsmeow — the phase brief's §3
// requirement), Error for anything else. The register form, the webhooks
// table and the DLQ table all live in the template gated on Unsupported,
// same as GetSessionOps's panel.
type sessionWebhooksPageData struct {
	PageBase
	Session     wa.Snapshot
	Unsupported bool
	Error       string
}

// webhookRowView mirrors api_webhooks.go's webhookResponse field-for-field,
// one layer up: THE SECRET NEVER APPEARS HERE EITHER, same rule, same
// reason (that file's header comment) — HasSecret is the only trace of it.
type webhookRowView struct {
	ID        string
	URL       string
	Events    []string
	Enabled   bool
	HasSecret bool
}

func webhookRowViewFrom(wh wa.WebhookInfo) webhookRowView {
	return webhookRowView{
		ID: wh.ID, URL: wh.URL, Events: wh.Events, Enabled: wh.Enabled,
		HasSecret: wh.Secret != nil && *wh.Secret != "",
	}
}

// webhookRowsData feeds the webhook_rows partial — the 5s poll target AND
// every register/delete/enable action's response, per the
// poll-and-action-agree rule.
type webhookRowsData struct {
	SessionID   string
	Unsupported bool
	Error       string
	Webhooks    []webhookRowView
}

// webhookDLQRowsData feeds the webhook_dlq_rows partial — the DLQ table's
// own poll/action pair, kept separate from webhookRowsData because the two
// tables poll and act independently (replaying a DLQ entry does not change
// the webhooks table, and vice versa).
type webhookDLQRowsData struct {
	SessionID   string
	Unsupported bool
	Error       string
	Entries     []dlqRowView
}

// dlqRowView wraps wa.DLQEntry with FailedAt pre-formatted. FailedAt itself
// is a unix int64 (wa.DLQEntry's doc comment), not a time.Time, so it
// cannot go through internal/renderer's fmtTime func the way every other
// displayed timestamp in this codebase now does — this view-model
// conversion is the Go-side equivalent, same convention as
// logs_view.go's toRowViews. The /api/v1 JSON field (api_webhooks.go's
// dlqEntryResponse) is untouched — this label is UI-only.
type dlqRowView struct {
	wa.DLQEntry
	FailedAtLabel string
}

func toDLQRowViews(entries []wa.DLQEntry, loc *time.Location) []dlqRowView {
	out := make([]dlqRowView, 0, len(entries))
	for _, e := range entries {
		out = append(out, dlqRowView{
			DLQEntry:      e,
			FailedAtLabel: time.Unix(e.FailedAt, 0).In(loc).Format("2006-01-02 15:04:05"),
		})
	}
	return out
}

// GetSessionWebhooks renders the per-session webhooks page. 404s only when
// the session itself does not exist.
func (h *handlers) GetSessionWebhooks(w http.ResponseWriter, r *http.Request) {
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	snap := s.Snapshot()
	data := sessionWebhooksPageData{Session: snap}
	if _, err := h.Manager.WebhookAdmin(); err != nil {
		if errors.Is(err, wa.ErrUnsupported) {
			data.Unsupported = true
		} else {
			data.Error = err.Error()
		}
	}
	data.PageBase = h.newPageBase(r, "Webhooks — "+snap.Name, "/sessions")
	h.Renderer.RenderPage(w, "session-webhooks", data)
}

// renderWebhookRows is every webhooks-table handler's shared tail —
// resolve WebhookAdmin (rendering the Unsupported/Error state and stopping
// if there is none), list, render.
func (h *handlers) renderWebhookRows(w http.ResponseWriter, r *http.Request, sessionID, actionErr string) {
	data := webhookRowsData{SessionID: sessionID, Error: actionErr}
	wh, err := h.Manager.WebhookAdmin()
	if err != nil {
		if errors.Is(err, wa.ErrUnsupported) {
			data.Unsupported = true
		} else if data.Error == "" {
			data.Error = err.Error()
		}
		h.Renderer.RenderPartial(w, "webhook_rows", data)
		return
	}
	hooks, err := wh.ListWebhooks(r.Context(), sessionID)
	if err != nil && data.Error == "" {
		data.Error = err.Error()
	}
	for _, hk := range hooks {
		data.Webhooks = append(data.Webhooks, webhookRowViewFrom(hk))
	}
	h.Renderer.RenderPartial(w, "webhook_rows", data)
}

// GetSessionWebhookRows is the 5s poll target.
func (h *handlers) GetSessionWebhookRows(w http.ResponseWriter, r *http.Request) {
	h.renderWebhookRows(w, r, r.PathValue("id"), "")
}

// waxumWebhookEventsHint is what the registration form's placeholder shows
// — a short, representative sample, not the full 32-value set
// (waxumWebhookEvents, api_webhooks.go) which would overwhelm the field.
const waxumWebhookEventsHint = "message, receipt, connected, disconnected"

// PostSessionWebhookRegister is POST /sessions/{id}/webhooks — the browser
// form's sibling of PostAPISessionWebhooks (api_webhooks.go). events is a
// comma-separated list (the browser form has no JSON array input); the
// SAME waxumWebhookEvents validation the API uses applies here too, so a
// typo names itself instead of reaching waxum as an opaque 422.
//
// The registration form must ALSO say, in the template, that waxum refuses
// any callback URL resolving to a private address — see
// PostAPISessionWebhooks' doc comment for why that is the single most
// likely operator mistake here.
func (h *handlers) PostSessionWebhookRegister(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.Manager.Get(id); !ok {
		http.NotFound(w, r)
		return
	}
	wh, err := h.Manager.WebhookAdmin()
	if err != nil {
		h.renderWebhookRows(w, r, id, "")
		return
	}
	url := strings.TrimSpace(r.FormValue("url"))
	if url == "" {
		h.renderWebhookRows(w, r, id, "url is required")
		return
	}
	var events []string
	for _, e := range strings.Split(r.FormValue("events"), ",") {
		e = strings.TrimSpace(e)
		if e != "" {
			events = append(events, e)
		}
	}
	if len(events) == 0 {
		h.renderWebhookRows(w, r, id, "events is required (comma-separated, e.g. "+waxumWebhookEventsHint+")")
		return
	}
	if bad := validateWebhookEvents(events); bad != "" {
		h.renderWebhookRows(w, r, id, "unknown event name: "+bad)
		return
	}
	if _, err := wh.RegisterWebhook(r.Context(), id, wa.WebhookSpec{
		URL: url, Secret: r.FormValue("secret"), Events: events,
	}); err != nil {
		h.renderWebhookRows(w, r, id, err.Error())
		return
	}
	h.renderWebhookRows(w, r, id, "")
}

// PostSessionWebhookDelete is POST /sessions/{id}/webhooks/{webhookID}/delete
// — a browser form has no DELETE method, so this is POST, mirroring
// PostSessionDelete's own reason (sessions.go) for being POST rather than
// DELETE.
func (h *handlers) PostSessionWebhookDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	wh, err := h.Manager.WebhookAdmin()
	if err != nil {
		h.renderWebhookRows(w, r, id, "")
		return
	}
	if err := wh.UnregisterWebhook(r.Context(), id, r.PathValue("webhookID")); err != nil {
		h.renderWebhookRows(w, r, id, err.Error())
		return
	}
	h.renderWebhookRows(w, r, id, "")
}

// PostSessionWebhookEnable is POST /sessions/{id}/webhooks/{webhookID}/enable
// — re-enables a webhook whose delivery circuit waxum opened after
// repeated failures.
func (h *handlers) PostSessionWebhookEnable(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	wh, err := h.Manager.WebhookAdmin()
	if err != nil {
		h.renderWebhookRows(w, r, id, "")
		return
	}
	if err := wh.EnableWebhook(r.Context(), id, r.PathValue("webhookID")); err != nil {
		h.renderWebhookRows(w, r, id, err.Error())
		return
	}
	h.renderWebhookRows(w, r, id, "")
}

// renderWebhookDLQRows is the DLQ table's own poll/action tail, kept
// separate from renderWebhookRows — see webhookDLQRowsData's doc comment.
func (h *handlers) renderWebhookDLQRows(w http.ResponseWriter, r *http.Request, sessionID, actionErr string) {
	data := webhookDLQRowsData{SessionID: sessionID, Error: actionErr}
	wh, err := h.Manager.WebhookAdmin()
	if err != nil {
		if errors.Is(err, wa.ErrUnsupported) {
			data.Unsupported = true
		} else if data.Error == "" {
			data.Error = err.Error()
		}
		h.Renderer.RenderPartial(w, "webhook_dlq_rows", data)
		return
	}
	entries, err := wh.WebhookDLQ(r.Context(), sessionID)
	if err != nil && data.Error == "" {
		data.Error = err.Error()
	}
	data.Entries = toDLQRowViews(entries, h.loc())
	h.Renderer.RenderPartial(w, "webhook_dlq_rows", data)
}

// GetSessionWebhookDLQRows is the DLQ table's 5s poll target.
func (h *handlers) GetSessionWebhookDLQRows(w http.ResponseWriter, r *http.Request) {
	h.renderWebhookDLQRows(w, r, r.PathValue("id"), "")
}

// PostSessionWebhookDLQReplay is
// POST /sessions/{id}/webhooks/dlq/{entryID}/replay.
func (h *handlers) PostSessionWebhookDLQReplay(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	wh, err := h.Manager.WebhookAdmin()
	if err != nil {
		h.renderWebhookDLQRows(w, r, id, "")
		return
	}
	if err := wh.ReplayWebhookDLQEntry(r.Context(), id, r.PathValue("entryID")); err != nil {
		h.renderWebhookDLQRows(w, r, id, err.Error())
		return
	}
	h.renderWebhookDLQRows(w, r, id, "")
}
