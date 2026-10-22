package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/convo"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/selfconvo"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// selfConvoDisabledMessage is answered by every /self-convo/runs/* endpoint
// (trigger and cancel — NOT template management, see
// self_convo_templates_ui.go's doc comment on why that half is exempt) when
// Deps.SelfConvoEnabled is off. Mirrors callsDisabledMessage's (api_calls.go)
// wording and reasoning: two of this deployment's own paired sessions
// exchanging a scripted conversation automatically is exactly the traffic
// shape that gets a WhatsApp account flagged, and neither identity has a
// second copy to fall back on — see CLAUDE.md's self-convo config entry.
const selfConvoDisabledMessage = "Self Convo is disabled on this deployment. Set WSM_SELF_CONVO_ENABLED=true and restart to enable it."

// selfConvoGuardUI mirrors calls_ui.go's callsGuardUI: every POST
// /self-convo/runs/* handler calls this first. Unlike the calls API's 501,
// the UI has nothing structured to return — it re-renders convo_run_rows
// with the same disabled message at 2xx (htmx only swaps 2xx — CLAUDE.md).
func (h *handlers) selfConvoGuardUI(w http.ResponseWriter, r *http.Request) bool {
	if h.SelfConvoEnabled {
		return true
	}
	h.convoRunRowsWithError(w, r, selfConvoDisabledMessage)
	return false
}

// selfConvoPageData drives GET /self-convo.
type selfConvoPageData struct {
	PageBase
	Enabled   bool
	Connected []wa.Snapshot
	Templates []convo.TemplateSummary
}

// convoRunView wraps a convo.Run with the presentation fields
// convo_run_rows.html needs but the domain model has no reason to carry.
type convoRunView struct {
	convo.Run
	Progress  string // "turn 7/20"
	Pair      string // "Alpha → Beta"
	NextFire  string // formatted in the UI display timezone, or "—" when there is nothing scheduled
	CanCancel bool
}

// convoRunRowsData feeds the convo_run_rows partial — the SAME partial the
// 5s poll (GetSelfConvoRunRows) and every trigger/cancel outcome render,
// per CLAUDE.md's "the polled partial and the action-response partial are
// the same partial" rule.
type convoRunRowsData struct {
	Enabled bool
	Error   string
	Runs    []convoRunView
}

// toConvoRunView builds the view fields for one run. loc is the UI display
// timezone (handlers.loc()) — NextFire used to be a fixed RFC3339 UTC
// string; nothing in web/static/js parses it, so reformatting it into the
// operator's configured zone is safe.
func toConvoRunView(r convo.Run, loc *time.Location) convoRunView {
	nextFire := "—"
	if r.NextDueAt != nil {
		nextFire = r.NextDueAt.In(loc).Format("2006-01-02 15:04:05")
	}
	return convoRunView{
		Run:       r,
		Progress:  fmt.Sprintf("turn %d/%d", r.TurnsDone, r.TurnCount),
		Pair:      r.SessionAName + " → " + r.SessionBName,
		NextFire:  nextFire,
		CanCancel: !convo.IsTerminal(r.State),
	}
}

// selfConvoRuns lists the most recent runs. h.Convo == nil (older test
// servers that predate this feature) renders an empty table rather than
// panicking — the same nil-guard recentCalls (calls_ui.go) uses for
// h.Calls.
func (h *handlers) selfConvoRuns(r *http.Request) []convoRunView {
	if h.Convo == nil {
		return nil
	}
	runs, err := h.Convo.ListRuns(r.Context(), 50)
	if err != nil {
		return nil
	}
	out := make([]convoRunView, len(runs))
	for i, run := range runs {
		out[i] = toConvoRunView(run, h.loc())
	}
	return out
}

// selfConvoTemplatesLimit is the cap selfConvoTemplates passes to
// ListTemplateSummaries — the same 200-row ceiling ListTemplateSummaries
// itself clamps to (convo.Repository, mirroring ListRuns), since neither
// the trigger page's <select> nor the templates management table paginates.
const selfConvoTemplatesLimit = 200

// selfConvoTemplates lists every stored template's summary (name, turn
// count, timestamps — never the body), newest first. h.Convo == nil renders
// an empty list, same nil-guard as selfConvoRuns above.
func (h *handlers) selfConvoTemplates(r *http.Request) []convo.TemplateSummary {
	if h.Convo == nil {
		return nil
	}
	templates, err := h.Convo.ListTemplateSummaries(r.Context(), selfConvoTemplatesLimit)
	if err != nil {
		return nil
	}
	return templates
}

// convoRunRowsWithError renders convo_run_rows with an optional inline
// error banner — callRowsWithError's (calls_ui.go) shape.
func (h *handlers) convoRunRowsWithError(w http.ResponseWriter, r *http.Request, errMsg string) {
	h.Renderer.RenderPartial(w, "convo_run_rows", convoRunRowsData{
		Enabled: h.SelfConvoEnabled, Error: errMsg, Runs: h.selfConvoRuns(r),
	})
}

// GetSelfConvo renders the /self-convo trigger page. It never 500s
// regardless of SelfConvoEnabled or Connected being empty — same
// "the page renders and explains" rule calls.html follows.
func (h *handlers) GetSelfConvo(w http.ResponseWriter, r *http.Request) {
	data := selfConvoPageData{
		Enabled:   h.SelfConvoEnabled,
		Connected: connectedSnapshots(h.Manager.Snapshots()),
		Templates: h.selfConvoTemplates(r),
	}
	data.PageBase = h.newPageBase(r, "Self Convo", "/self-convo")
	h.Renderer.RenderPage(w, "self_convo", data)
}

// GetSelfConvoRunRows is the 5s poll target (trigger.html's tbody
// hx-trigger) — never carries an error.
func (h *handlers) GetSelfConvoRunRows(w http.ResponseWriter, r *http.Request) {
	h.convoRunRowsWithError(w, r, "")
}

// parseStartAt turns the trigger form's start_at (a datetime-local value,
// "2006-01-02T15:04", optionally carrying seconds) and tz_offset (minutes,
// the browser's own Date.prototype.getTimezoneOffset() — UTC = local +
// offset) into a concrete UTC instant. An empty value means "start now" and
// returns now unchanged. An empty or unparseable tz_offset is treated as 0
// (UTC) — the documented no-JS fallback (self-convo.js's doc comment,
// trigger.html's help text). Factored out as its own pure function so the
// timezone arithmetic is unit-testable without a request or a clock mock.
func parseStartAt(value, tzOffset string, now time.Time) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return now, nil
	}

	var t time.Time
	var err error
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02T15:04:05"} {
		if t, err = time.Parse(layout, value); err == nil {
			break
		}
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid start_at %q", value)
	}

	offsetMin := 0
	if tzOffset = strings.TrimSpace(tzOffset); tzOffset != "" {
		if v, perr := strconv.Atoi(tzOffset); perr == nil {
			offsetMin = v
		}
	}
	return t.Add(time.Duration(offsetMin) * time.Minute).UTC(), nil
}

// triggerErrorMessage maps a Runner.Trigger error to operator-facing copy —
// callsGuardUI's/verifyErrorMessage's (calls_ui.go, verify_ui.go) sibling.
func triggerErrorMessage(err error) string {
	var verr *selfconvo.ValidationError
	switch {
	case errors.Is(err, selfconvo.ErrNotEnoughParticipants):
		return "fewer than two of the ticked sessions are connected"
	case errors.Is(err, convo.ErrNotFound):
		return "template not found"
	case errors.As(err, &verr):
		return verr.Error()
	default:
		return err.Error()
	}
}

// PostSelfConvoTrigger is POST /self-convo/runs: tick ≥2 connected
// sessions, pick or upload a template, and start a run. Every outcome —
// validation failure, Trigger error, or success — re-renders
// convo_run_rows at 2xx (htmx only swaps 2xx, CLAUDE.md).
func (h *handlers) PostSelfConvoTrigger(w http.ResponseWriter, r *http.Request) {
	if !h.selfConvoGuardUI(w, r) {
		return
	}
	if h.Convo == nil || h.SelfConvo == nil {
		h.convoRunRowsWithError(w, r, "self convo is not available on this deployment")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, selfconvo.MaxTemplateBytes+64*1024)
	if err := r.ParseMultipartForm(mediaFormMaxMemory); err != nil {
		h.convoRunRowsWithError(w, r, "invalid form submission")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}

	// Deduped here too, on top of selfconvo.PickPair's own dedupe
	// (expand.go) — a browser resubmitting the same checkbox twice (a
	// double-click, a stale form) must not count as two ticked sessions
	// when this length check decides whether "at least two" was met.
	sessionIDs := dedupeStrings(r.Form["session_id"])
	if len(sessionIDs) < 2 {
		h.convoRunRowsWithError(w, r, "tick at least two connected sessions")
		return
	}

	templateID, tmpl, script, errMsg := h.resolveTriggerTemplate(r)
	if errMsg != "" {
		h.convoRunRowsWithError(w, r, errMsg)
		return
	}

	startAt, err := parseStartAt(r.FormValue("start_at"), r.FormValue("tz_offset"), time.Now().UTC())
	if err != nil {
		h.convoRunRowsWithError(w, r, err.Error())
		return
	}

	_, err = h.SelfConvo.Trigger(r.Context(), selfconvo.TriggerRequest{
		TemplateID:       templateID,
		Template:         tmpl,
		Script:           script,
		TickedSessionIDs: sessionIDs,
		StartAt:          startAt,
	})
	if err != nil {
		h.convoRunRowsWithError(w, r, triggerErrorMessage(err))
		return
	}
	h.GetSelfConvoRunRows(w, r)
}

// resolveTriggerTemplate resolves the trigger form's template: an uploaded
// "file" part takes priority over "template_id" (an operator who attaches a
// new script clearly means to use it, not whatever the select happened to
// carry). A freshly uploaded template is validated ONCE (readTemplateUpload,
// self_convo_templates_ui.go) and saved through the same saveTemplate path
// PostSelfConvoTemplateUpload uses; tmpl/script are then both returned so
// PostSelfConvoTrigger can hand them to Runner.Trigger directly
// (TriggerRequest.Template/Script), letting it skip GetTemplate+Validate
// entirely rather than re-fetching and re-parsing the very row this
// function just inserted — see TriggerRequest's own doc comment and
// docs/brief-self-convo.md's review follow-ups ("upload-and-trigger
// validates the same bytes three times"). The "pick a saved template" case
// (no file attached) returns only a template id — tmpl/script stay nil, and
// Trigger falls back to its own GetTemplate+Validate path, since there is
// nothing pre-parsed to hand it.
func (h *handlers) resolveTriggerTemplate(r *http.Request) (id string, tmpl *convo.Template, script *selfconvo.Script, errMsg string) {
	if _, header, ferr := r.FormFile("file"); ferr == nil && header != nil && header.Size > 0 {
		name, body, sc, err := readTemplateUpload(r)
		if err != nil {
			return "", nil, nil, err.Error()
		}
		t, err := h.saveTemplate(r, name, body, sc)
		if err != nil {
			return "", nil, nil, err.Error()
		}
		return t.ID, &t, &sc, ""
	}
	templateID := strings.TrimSpace(r.FormValue("template_id"))
	if templateID == "" {
		return "", nil, nil, "choose a template or attach a script"
	}
	return templateID, nil, nil, ""
}

// PostSelfConvoCancel is POST /self-convo/runs/{id}/cancel.
func (h *handlers) PostSelfConvoCancel(w http.ResponseWriter, r *http.Request) {
	if !h.selfConvoGuardUI(w, r) {
		return
	}
	if h.SelfConvo == nil {
		h.convoRunRowsWithError(w, r, "self convo is not available on this deployment")
		return
	}
	id := r.PathValue("id")
	if err := h.SelfConvo.Cancel(r.Context(), id); err != nil {
		msg := err.Error()
		switch {
		case errors.Is(err, convo.ErrRunFinished):
			msg = "run already finished"
		case errors.Is(err, convo.ErrNotFound):
			msg = "run not found"
		}
		h.convoRunRowsWithError(w, r, msg)
		return
	}
	h.GetSelfConvoRunRows(w, r)
}

// dedupeStrings drops repeats, first occurrence wins, order preserved —
// used by PostSelfConvoTrigger on the ticked session_id list (see its call
// site's comment); selfconvo.PickPair (expand.go) dedupes independently on
// the same field, since Trigger is also reachable directly (tests, any
// future non-HTTP caller) without going through this handler.
func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
