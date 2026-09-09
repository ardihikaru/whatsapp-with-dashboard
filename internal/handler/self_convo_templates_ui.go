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
}

// selfConvoTemplateViewPageData drives GET /self-convo/templates/{id}.
type selfConvoTemplateViewPageData struct {
	PageBase
	Template   convo.Template
	PrettyJSON string
}

// convoTemplateRowsData feeds the convo_template_rows partial — the SAME
// partial GetSelfConvoTemplateRows (the "load"-only poll) and every
// upload/rename/delete outcome render, mirroring convoRunRowsData's own
// rule (self_convo_ui.go).
type convoTemplateRowsData struct {
	Error     string
	Templates []convo.TemplateSummary
}

// Template management (upload, rename, delete, view, download) is
// deliberately NOT gated by SelfConvoEnabled — unlike trigger/cancel
// (self_convo_ui.go's selfConvoGuardUI), saving or removing a script never
// sends anything, so there is no automated-traffic hazard to guard
// against. An operator can prepare and curate templates on a deployment
// that has not opted into running them yet. See
// TestSelfConvoTemplateManagementWorksWithTheFeatureDisabled.

// convoTemplateRowsWithError renders convo_template_rows with an optional
// inline error banner — convoRunRowsWithError's (self_convo_ui.go) sibling.
func (h *handlers) convoTemplateRowsWithError(w http.ResponseWriter, r *http.Request, errMsg string) {
	h.Renderer.RenderPartial(w, "convo_template_rows", convoTemplateRowsData{
		Error: errMsg, Templates: h.selfConvoTemplates(r),
	})
}

// GetSelfConvoTemplates renders the /self-convo/templates management page.
func (h *handlers) GetSelfConvoTemplates(w http.ResponseWriter, r *http.Request) {
	data := selfConvoTemplatesPageData{Enabled: h.SelfConvoEnabled}
	data.PageBase = h.newPageBase(r, "Self Convo Templates", "/self-convo")
	h.Renderer.RenderPage(w, "self_convo_templates", data)
}

// GetSelfConvoTemplateRows is the "load"-only poll target for the
// templates table — unlike convo_run_rows this list changes only on an
// operator's own action, so there is no 5s auto-refresh.
func (h *handlers) GetSelfConvoTemplateRows(w http.ResponseWriter, r *http.Request) {
	h.convoTemplateRowsWithError(w, r, "")
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

// GetSelfConvoTemplateView renders GET /self-convo/templates/{id}: name,
// turn count, created, and the JSON pretty-printed.
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
	data := selfConvoTemplateViewPageData{Template: t, PrettyJSON: prettyJSON(t.Body)}
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
