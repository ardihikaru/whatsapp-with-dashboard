package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/convo"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/selfconvo"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// selfConvoTemplatesPageData drives GET /self-convo/templates. Enabled is
// carried for the page's own "trigger is disabled" cross-link note, NOT to
// gate anything on this page — see the doc comment on the handlers below
// for why template management works with the feature flag off.
type selfConvoTemplatesPageData struct {
	PageBase
	Enabled bool
	// Topics feeds the topic filter <select> — see TestTheTemplatesPage...
	// tests and the phase brief's "2400 seeded templates" note on why an
	// unfiltered list needs one at all.
	Topics []convo.TopicSummary
}

// templateLinkView wraps a convo.TemplateLink with the presentation fields
// template_view.html's outgoing-links table needs: the target's own NAME
// (TemplateLink only stores an id) and a human-readable gap window.
type templateLinkView struct {
	ToTemplateID   string
	ToTemplateName string
	Weight         int
	GapLabel       string
}

// gapLabel renders a [min,max] second window as a scannable range in the
// largest sensible unit — "2–6 hours" / "1–3 days", the phase brief's own
// examples — rather than a raw second count nobody can read at a glance.
// This is a display nicety only; the stored seconds are exact and
// untouched (drawGap, chain.go, still draws from them directly).
func gapLabel(minSec, maxSec int) string {
	if minSec <= 0 && maxSec <= 0 {
		return "immediately"
	}
	switch {
	case maxSec < 7200:
		return fmt.Sprintf("%s–%s minutes", gapUnit(minSec, 60), gapUnit(maxSec, 60))
	case maxSec < 172800:
		return fmt.Sprintf("%s–%s hours", gapUnit(minSec, 3600), gapUnit(maxSec, 3600))
	default:
		return fmt.Sprintf("%s–%s days", gapUnit(minSec, 86400), gapUnit(maxSec, 86400))
	}
}

// gapUnit divides seconds by unitSeconds and trims a trailing ".0" — e.g.
// 5400/3600 reads as "1.5", 7200/3600 reads as "2", never "2.0".
func gapUnit(seconds, unitSeconds int) string {
	v := float64(seconds) / float64(unitSeconds)
	s := strconv.FormatFloat(v, 'f', 1, 64)
	return strings.TrimSuffix(strings.TrimSuffix(s, "0"), ".")
}

// selfConvoTemplateViewPageData drives GET /self-convo/templates/{id}.
type selfConvoTemplateViewPageData struct {
	PageBase
	Template   convo.Template
	PrettyJSON string
	// Links is the template's outgoing edges in the correlation graph
	// (TemplateLinks, schedule_repository.go) — the "korelasi antar
	// template" the phase brief asks an operator be able to see.
	Links []templateLinkView
}

// convoTemplateRowsData feeds the convo_template_rows partial — the SAME
// partial GetSelfConvoTemplateRows (the "load"-only poll) and every
// upload/rename/delete outcome render, mirroring convoRunRowsData's own
// rule (self_convo_ui.go). Topic is empty for the unfiltered view.
type convoTemplateRowsData struct {
	Error     string
	Topic     string
	Templates []convo.TemplateSummary
}

// Template management (upload, rename, delete, view, download) is
// deliberately NOT gated by SelfConvoEnabled — unlike trigger/cancel
// (self_convo_ui.go's selfConvoGuardUI), saving or removing a script never
// sends anything, so there is no automated-traffic hazard to guard
// against. An operator can prepare and curate templates on a deployment
// that has not opted into running them yet. See
// TestSelfConvoTemplateManagementWorksWithTheFeatureDisabled.

// selfConvoTemplatesByTopic is selfConvoTemplates scoped to one topic —
// empty topic falls back to the unfiltered list (ListTemplateSummaries)
// rather than calling ListTemplateSummariesByTopic with "", which is a
// different, deliberately unsupported query shape (that method's own doc
// comment).
func (h *handlers) selfConvoTemplatesByTopic(r *http.Request, topic string) []convo.TemplateSummary {
	if topic == "" {
		return h.selfConvoTemplates(r)
	}
	if h.Convo == nil {
		return nil
	}
	templates, err := h.Convo.ListTemplateSummariesByTopic(r.Context(), topic, selfConvoTemplatesLimit)
	if err != nil {
		return nil
	}
	return templates
}

// convoTemplateRowsWithError renders convo_template_rows with an optional
// inline error banner, unfiltered — convoRunRowsWithError's
// (self_convo_ui.go) sibling. Every mutation (upload/rename/delete) renders
// through this, resetting any active topic filter: the row that just
// changed might not even match the filter that was active before the
// action, so falling back to "show everything" is the shape least likely
// to look like the mutation silently did nothing.
func (h *handlers) convoTemplateRowsWithError(w http.ResponseWriter, r *http.Request, errMsg string) {
	h.convoTemplateRowsFiltered(w, r, errMsg, "")
}

// convoTemplateRowsFiltered is convoTemplateRowsWithError's topic-aware
// sibling — GetSelfConvoTemplateRows' own render point.
func (h *handlers) convoTemplateRowsFiltered(w http.ResponseWriter, r *http.Request, errMsg, topic string) {
	h.Renderer.RenderPartial(w, "convo_template_rows", convoTemplateRowsData{
		Error: errMsg, Topic: topic, Templates: h.selfConvoTemplatesByTopic(r, topic),
	})
}

// GetSelfConvoTemplates renders the /self-convo/templates management page.
func (h *handlers) GetSelfConvoTemplates(w http.ResponseWriter, r *http.Request) {
	var topics []convo.TopicSummary
	if h.Convo != nil {
		if ts, err := h.Convo.TopicSummaries(r.Context()); err == nil {
			topics = ts
		}
	}
	data := selfConvoTemplatesPageData{Enabled: h.SelfConvoEnabled, Topics: topics}
	data.PageBase = h.newPageBase(r, "Self Convo Templates", "/self-convo")
	h.Renderer.RenderPage(w, "self_convo_templates", data)
}

// GetSelfConvoTemplateRows is the "load"-only poll target for the
// templates table — unlike convo_run_rows this list changes only on an
// operator's own action, so there is no 5s auto-refresh. ?topic= narrows it
// to one topic — templates.html's filter <select> re-requests this over
// HTMX on change.
func (h *handlers) GetSelfConvoTemplateRows(w http.ResponseWriter, r *http.Request) {
	topic := strings.TrimSpace(r.URL.Query().Get("topic"))
	h.convoTemplateRowsFiltered(w, r, "", topic)
}

// templateNameFromFilename strips the extension off an uploaded file's
// name — the fallback stem when neither the form's "name" field nor the
// script's own "name" is set (script.go's Name doc comment).
func templateNameFromFilename(filename string) string {
	base := filepath.Base(filename)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// readTemplateUpload extracts and validates the "file" multipart part
// shared by PostSelfConvoTemplateUpload and PostSelfConvoTrigger's
// resolveTriggerTemplate (self_convo_ui.go) — ONE function so the two entry
// points can never validate an uploaded script differently, and the ONLY
// Validate call this upload's bytes ever go through: saveTemplate, below,
// and Runner.Trigger both now reuse the returned Script rather than
// re-parsing the same bytes (docs/brief-self-convo.md's review follow-ups —
// "upload-and-trigger validates the same bytes three times"). The caller
// must already have called r.ParseMultipartForm (both call sites do, with
// their own http.MaxBytesReader ceiling first). Returns the raw bytes
// UNCHANGED — convo.Template.Body is opaque storage (model.go's doc
// comment), never the re-marshaled struct — plus a name resolved in
// priority order: the form's own "name" field, then the script's own Name,
// then the uploaded filename's stem, then "template".
func readTemplateUpload(r *http.Request) (name string, body []byte, script selfconvo.Script, err error) {
	file, header, ferr := r.FormFile("file")
	if ferr != nil {
		return "", nil, selfconvo.Script{}, errors.New("a script file is required")
	}
	defer file.Close()

	data, rerr := io.ReadAll(io.LimitReader(file, selfconvo.MaxTemplateBytes+1))
	if rerr != nil {
		return "", nil, selfconvo.Script{}, errors.New("could not read the uploaded file")
	}
	if len(data) > selfconvo.MaxTemplateBytes {
		return "", nil, selfconvo.Script{}, fmt.Errorf("script body is over the %d byte limit", selfconvo.MaxTemplateBytes)
	}

	sc, verr := selfconvo.Validate(data)
	if verr != nil {
		return "", nil, selfconvo.Script{}, verr
	}

	name = strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = sc.Name
	}
	if name == "" {
		name = templateNameFromFilename(header.Filename)
	}
	if name == "" {
		name = "template"
	}
	if rc := len([]rune(name)); rc > selfconvo.MaxNameChars {
		return "", nil, selfconvo.Script{}, fmt.Errorf("name is over %d characters", selfconvo.MaxNameChars)
	}
	return name, data, sc, nil
}

// saveTemplate inserts a new row for a script the caller has ALREADY
// validated (readTemplateUpload's return, or its own re-parse when this is
// called from anywhere else) — it deliberately does not call
// selfconvo.Validate itself; see readTemplateUpload's doc comment for why a
// second copy of that check on the very same bytes was removed.
func (h *handlers) saveTemplate(r *http.Request, name string, body []byte, script selfconvo.Script) (convo.Template, error) {
	t := convo.Template{ID: wa.NewID(), Name: name, TurnCount: len(script.Turns), Body: string(body)}
	if err := h.Convo.InsertTemplate(r.Context(), &t); err != nil {
		return convo.Template{}, err
	}
	return t, nil
}

// PostSelfConvoTemplateUpload is POST /self-convo/templates.
func (h *handlers) PostSelfConvoTemplateUpload(w http.ResponseWriter, r *http.Request) {
	if h.Convo == nil {
		h.convoTemplateRowsWithError(w, r, "self convo is not available on this deployment")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, selfconvo.MaxTemplateBytes+64*1024)
	if err := r.ParseMultipartForm(mediaFormMaxMemory); err != nil {
		h.convoTemplateRowsWithError(w, r, "invalid form submission")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}

	name, body, script, err := readTemplateUpload(r)
	if err != nil {
		h.convoTemplateRowsWithError(w, r, err.Error())
		return
	}
	if _, err := h.saveTemplate(r, name, body, script); err != nil {
		h.convoTemplateRowsWithError(w, r, err.Error())
		return
	}
	h.GetSelfConvoTemplateRows(w, r)
}

// PostSelfConvoTemplateRename is POST /self-convo/templates/{id}/rename.
func (h *handlers) PostSelfConvoTemplateRename(w http.ResponseWriter, r *http.Request) {
	if h.Convo == nil {
		h.convoTemplateRowsWithError(w, r, "self convo is not available on this deployment")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		h.convoTemplateRowsWithError(w, r, "name is required")
		return
	}
	if rc := len([]rune(name)); rc > selfconvo.MaxNameChars {
		h.convoTemplateRowsWithError(w, r, fmt.Sprintf("name is over %d characters", selfconvo.MaxNameChars))
		return
	}
	if err := h.Convo.RenameTemplate(r.Context(), r.PathValue("id"), name); err != nil {
		msg := err.Error()
		if errors.Is(err, convo.ErrNotFound) {
			msg = "template not found"
		}
		h.convoTemplateRowsWithError(w, r, msg)
		return
	}
	h.GetSelfConvoTemplateRows(w, r)
}

// PostSelfConvoTemplateDelete is POST /self-convo/templates/{id}/delete.
func (h *handlers) PostSelfConvoTemplateDelete(w http.ResponseWriter, r *http.Request) {
	if h.Convo == nil {
		h.convoTemplateRowsWithError(w, r, "self convo is not available on this deployment")
		return
	}
	if err := h.Convo.DeleteTemplate(r.Context(), r.PathValue("id")); err != nil {
		msg := err.Error()
		if errors.Is(err, convo.ErrNotFound) {
			msg = "template not found"
		}
		h.convoTemplateRowsWithError(w, r, msg)
		return
	}
	h.GetSelfConvoTemplateRows(w, r)
}

// prettyJSON re-indents a stored template body for template_view.html's
// <pre>. Falls back to the raw string (rather than erroring the page) if
// it somehow does not parse — every stored row passed Validate on the way
// in, but a page render must never 500 over a display nicety.
func prettyJSON(raw string) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(raw), "", "  "); err != nil {
		return raw
	}
	return buf.String()
}

// templateLinkViews resolves fromID's outgoing edges (TemplateLinks) into
// display rows, looking up each target's current name — a link only stores
// an id (schedule_model.go's TemplateLink), and the page must read as
// "korelasi antar template" (which script leads to which), not a list of
// opaque ids. A target that no longer exists (deleted since the link was
// generated) falls back to showing its bare id rather than dropping the
// row or erroring the page.
func (h *handlers) templateLinkViews(r *http.Request, fromID string) []templateLinkView {
	links, err := h.Convo.TemplateLinks(r.Context(), fromID)
	if err != nil || len(links) == 0 {
		return nil
	}
	out := make([]templateLinkView, len(links))
	for i, l := range links {
		name := l.ToTemplateID
		if target, err := h.Convo.GetTemplate(r.Context(), l.ToTemplateID); err == nil {
			name = target.Name
		}
		out[i] = templateLinkView{
			ToTemplateID: l.ToTemplateID, ToTemplateName: name,
			Weight: l.Weight, GapLabel: gapLabel(l.MinGapSeconds, l.MaxGapSeconds),
		}
	}
	return out
}

// GetSelfConvoTemplateView renders GET /self-convo/templates/{id}: name,
// turn count, created, topic/stage/origin, the JSON pretty-printed, and its
// outgoing correlation-graph links.
func (h *handlers) GetSelfConvoTemplateView(w http.ResponseWriter, r *http.Request) {
	if h.Convo == nil {
		http.NotFound(w, r)
		return
	}
	t, err := h.Convo.GetTemplate(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data := selfConvoTemplateViewPageData{
		Template: t, PrettyJSON: prettyJSON(t.Body), Links: h.templateLinkViews(r, t.ID),
	}
	data.PageBase = h.newPageBase(r, "Template: "+t.Name, "/self-convo")
	h.Renderer.RenderPage(w, "self_convo_template_view", data)
}

// templateSlugRE matches one run of lowercase alnum characters — the units
// slugifyTemplateName joins with "-".
var templateSlugRE = regexp.MustCompile(`[a-z0-9]+`)

// slugifyTemplateName turns a template's Name into a safe download
// filename stem: lowercase, non-alnum runs collapsed to a single "-",
// falling back to "template" when nothing alphanumeric survives (an
// all-emoji or all-punctuation name, or an empty one).
func slugifyTemplateName(name string) string {
	parts := templateSlugRE.FindAllString(strings.ToLower(name), -1)
	if len(parts) == 0 {
		return "template"
	}
	return strings.Join(parts, "-")
}

// GetSelfConvoTemplateDownload is GET /self-convo/templates/{id}/download.
func (h *handlers) GetSelfConvoTemplateDownload(w http.ResponseWriter, r *http.Request) {
	if h.Convo == nil {
		http.NotFound(w, r)
		return
	}
	t, err := h.Convo.GetTemplate(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	filename := slugifyTemplateName(t.Name) + ".json"
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	_, _ = w.Write([]byte(t.Body))
}

// GetSelfConvoExample is GET /self-convo/templates/example — MUST be
// registered before GET /self-convo/templates/{id} in routes_ui.go (Go
// 1.22's mux prefers the more specific literal pattern automatically, but
// registration order is still what
// TestTheExampleRouteIsNotShadowedByTheTemplateIdRoute pins). Serves the
// shipped example script (internal/selfconvo.ExampleJSON) with the same
// attachment headers GetSelfConvoTemplateDownload uses.
func (h *handlers) GetSelfConvoExample(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+selfconvo.ExampleFilename+`"`)
	_, _ = w.Write(selfconvo.ExampleJSON)
}
