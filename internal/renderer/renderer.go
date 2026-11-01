// Package renderer renders the embedded html/template pages and HTMX partials.
package renderer

import (
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"time"
)

// pageDef declares which files make up a page set and which template in the
// set is the document root to execute.
type pageDef struct {
	files []string
	root  string
}

var pageDefs = map[string]pageDef{
	"login": {
		files: []string{
			"templates/login.html",
			"templates/partials/login_error.html",
			"templates/partials/theme_toggle.html",
		},
		root: "login",
	},
	"dashboard": {
		files: []string{
			"templates/base.html",
			"templates/dashboard.html",
			"templates/partials/sidebar.html",
			"templates/partials/header.html",
			"templates/partials/widgets.html",
			"templates/partials/theme_toggle.html",
		},
		root: "base",
	},
	"sessions": {
		files: append(chrome(), "templates/sessions/list.html"),
		root:  "base",
	},
	"session-new": {
		files: append(chrome(), "templates/sessions/new.html"),
		root:  "base",
	},
	"pair": {
		files: append(chrome(), "templates/sessions/pair.html"),
		root:  "base",
	},
	"send": {
		files: append(chrome(), "templates/send.html"),
		root:  "base",
	},
	// Note for "calls"/"session-ops"/"session-webhooks": none of these
	// filesets include their own row/panel partials — same as "sessions"
	// above never including session_rows.html. Every table/panel fills
	// itself via hx-get (load, every 5s) against a partial that lives only
	// in partialFiles below, so the poll and the initial paint can never
	// render two different shapes.
	"calls": {
		files: append(chrome(), "templates/calls.html"),
		root:  "base",
	},
	"verify": {
		files: append(chrome(), "templates/verify.html"),
		root:  "base",
	},
	"status": {
		files: append(chrome(), "templates/status.html"),
		root:  "base",
	},
	// self_convo/self_convo_templates: no rows partial in either fileset —
	// same rule as "calls" above. self_convo_template_view has no rows
	// partial to worry about at all; it renders one template's JSON, not a
	// table.
	"self_convo": {
		files: append(chrome(), "templates/self_convo/trigger.html"),
		root:  "base",
	},
	"self_convo_templates": {
		files: append(chrome(), "templates/self_convo/templates.html"),
		root:  "base",
	},
	"self_convo_template_view": {
		files: append(chrome(), "templates/self_convo/template_view.html"),
		root:  "base",
	},
	// self_convo_schedule_form is shared by GET .../schedules/new AND GET
	// .../schedules/{id}/edit (and both handlers' own validation-failure
	// re-render) — one template, IsEdit in the page data picks the
	// heading/submit label/action, api_clients.go's create/edit split
	// without needing two near-identical files.
	"self_convo_schedules": {
		files: append(chrome(), "templates/self_convo/schedules.html"),
		root:  "base",
	},
	"self_convo_schedule_form": {
		files: append(chrome(), "templates/self_convo/schedule_form.html"),
		root:  "base",
	},
	"self_convo_schedule_view": {
		files: append(chrome(), "templates/self_convo/schedule_view.html"),
		root:  "base",
	},
	"known_groups": {
		files: append(chrome(), "templates/known_groups.html"),
		root:  "base",
	},
	"contacts": {
		files: append(chrome(), "templates/contacts.html"),
		root:  "base",
	},
	// group_broadcast_schedule_form is shared by GET .../schedules/new AND
	// GET .../schedules/{id}/edit, same IsEdit-picks-the-difference
	// convention as self_convo_schedule_form above.
	"group_broadcast_schedules": {
		files: append(chrome(), "templates/group_broadcast/schedules.html"),
		root:  "base",
	},
	"group_broadcast_schedule_form": {
		files: append(chrome(), "templates/group_broadcast/schedule_form.html"),
		root:  "base",
	},
	"group_broadcast_schedule_view": {
		files: append(chrome(), "templates/group_broadcast/schedule_view.html"),
		root:  "base",
	},
	"session-ops": {
		files: append(chrome(), "templates/sessions/ops.html"),
		root:  "base",
	},
	"session-webhooks": {
		files: append(chrome(), "templates/sessions/webhooks.html"),
		root:  "base",
	},
	"api_clients/list": {
		files: append(chrome(), "templates/api_clients/list.html", "templates/api_clients/expiry.html",
			"templates/api_clients/secret.html", "templates/partials/pager.html"),
		root: "base",
	},
	"api_clients/edit": {
		files: append(chrome(), "templates/api_clients/edit.html", "templates/api_clients/expiry.html",
			"templates/api_clients/secret.html"),
		root: "base",
	},
	"logs": {
		files: append(chrome(), "templates/logs/list.html", "templates/partials/log_rows.html"),
		root:  "base",
	},
	// outgoing_chat embeds its first page of detail rows inline (like "logs"
	// above), so its rows partial is dual-included here AND in partialFiles
	// below — RenderPartial needs it independently for the keyset
	// Prev/Next-paged response. pager.html is the same numbered footer
	// api_clients/list uses, reused here for the (in-memory, non-keyset)
	// "Ringkasan per Sesi" table — see outgoingSummaryPageSize's doc comment.
	"outgoing_chat": {
		files: append(chrome(), "templates/outgoing_chat/list.html", "templates/partials/outgoing_chat_rows.html",
			"templates/partials/pager.html"),
		root: "base",
	},
	"system_log": {
		// No rows partial in this fileset — like "sessions" above (and
		// unlike "logs", which embeds its first page of rows inline), the
		// tbody is empty on first render and fills itself via hx-get
		// (system_log/list.html's hx-trigger="load, every 5s"); the rows
		// partial lives only in partialFiles below, same as
		// partials/session_rows.html.
		files: append(chrome(), "templates/system_log/list.html"),
		root:  "base",
	},
}

// chrome is the shared base-layout file set every full page needs.
func chrome() []string {
	return []string{
		"templates/base.html",
		"templates/partials/sidebar.html",
		"templates/partials/header.html",
		"templates/partials/theme_toggle.html",
	}
}

var partialFiles = []string{
	"templates/partials/login_error.html",
	"templates/partials/widgets.html",
	"templates/partials/form_error.html",
	"templates/partials/session_rows.html",
	"templates/partials/reconcile_banner.html",
	"templates/partials/pairing_status.html",
	"templates/partials/send_result.html",
	"templates/partials/status_result.html",
	"templates/partials/log_rows.html",
	"templates/partials/outgoing_chat_rows.html",
	"templates/partials/system_log_rows.html",
	"templates/partials/call_rows.html",
	"templates/partials/verify_rows.html",
	"templates/partials/session_ops_panel.html",
	"templates/partials/session_group_code_result.html",
	"templates/partials/session_group_editor.html",
	"templates/partials/session_name_editor.html",
	"templates/partials/webhook_rows.html",
	"templates/partials/webhook_dlq_rows.html",
	"templates/partials/convo_run_rows.html",
	"templates/partials/convo_template_rows.html",
	"templates/partials/convo_schedule_rows.html",
	"templates/partials/known_group_rows.html",
	"templates/partials/known_group_picker.html",
	"templates/partials/contact_rows.html",
	"templates/partials/group_broadcast_schedule_rows.html",
	"templates/partials/group_broadcast_occurrence_rows.html",
}

// Renderer holds the parsed template sets.
type Renderer struct {
	pages    map[string]*template.Template
	roots    map[string]string
	partials *template.Template
}

// New parses every page and partial from the embedded filesystem up front so
// template errors surface at startup, not per request.
//
// basePath is the mount prefix (config.BasePath): "/wsm" in production, "" when
// this serves at a host root. Every in-app URL in a template goes through the
// `u` function below.
//
// # WHY A TEMPLATE FUNCTION AND NOT A FIELD ON THE PAGE DATA
//
// The obvious move is a Base field on PageBase and {{.Base}}/sessions in the
// markup. It does not work here. RenderPartial executes a named partial with
// whatever the handler passes — a []Session, a pager struct — and those do not
// embed PageBase and never will. partials/session_rows.html alone holds six
// URLs. With a field, those six would silently keep rendering root-relative
// hrefs: correct-looking markup that 404s only once the app is mounted under a
// prefix, and only on the HTMX-swapped parts of the page.
//
// A FuncMap is bound at parse time and is therefore in scope for every template
// in the set, page and partial alike, regardless of the data handed to it.
//
// loc is the UI display timezone (config.Config.Location, WSM_TIMEZONE) that
// backs the `localtime`/`fmtTime` funcs below. nil falls back to UTC rather
// than panicking or silently using the host's time.Local — every caller that
// predates this parameter (every test server built before WSM_TIMEZONE
// existed) gets a deterministic, explicit zone instead of inheriting
// whatever /etc/localtime happens to be on the machine running the test.
func New(fsys fs.FS, basePath string, loc *time.Location) (*Renderer, error) {
	if loc == nil {
		loc = time.UTC
	}
	r := &Renderer{
		pages: make(map[string]*template.Template),
		roots: make(map[string]string),
	}
	// u joins the mount prefix to an app-absolute path. Paths passed to it must
	// start with "/" — that is what makes "" (unmounted) a no-op rather than a
	// source of "wsmsessions".
	//
	// localtime/fmtTime are the ONLY time-aware template funcs: every other
	// displayed timestamp in this codebase is pre-formatted in Go (see
	// logs_view.go's toRowViews doc comment) specifically because, until
	// this parameter existed, html/template had no formatter registered at
	// all. These two cover the templates that still call time.Time.Format
	// directly on a struct field (widgets.html, reconcile_banner.html,
	// convo_run_rows.html, call_rows.html, convo_template_rows.html,
	// template_view.html, contact_rows.html) — localtime converts, fmtTime
	// converts AND formats in one call for the common case.
	fm := template.FuncMap{
		"u":         func(p string) string { return basePath + p },
		"localtime": func(t time.Time) time.Time { return t.In(loc) },
		"fmtTime":   func(t time.Time, layout string) string { return t.In(loc).Format(layout) },
	}
	for name, def := range pageDefs {
		// template.New(...).Funcs(...) before ParseFS: a FuncMap added after
		// parsing is not visible to templates already parsed, and the failure is
		// a runtime "function not defined" on the page nobody opened yet.
		t, err := template.New(name).Funcs(fm).ParseFS(fsys, def.files...)
		if err != nil {
			return nil, fmt.Errorf("parse page %s: %w", name, err)
		}
		r.pages[name] = t
		r.roots[name] = def.root
	}
	partials, err := template.New("partials").Funcs(fm).ParseFS(fsys, partialFiles...)
	if err != nil {
		return nil, fmt.Errorf("parse partials: %w", err)
	}
	r.partials = partials
	return r, nil
}

// RenderPage writes a full HTML document.
func (r *Renderer) RenderPage(w http.ResponseWriter, name string, data any) {
	t, ok := r.pages[name]
	if !ok {
		http.Error(w, "unknown page "+name, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, r.roots[name], data); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

// RenderPartial writes an HTMX fragment (a named partial template).
func (r *Renderer) RenderPartial(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := r.partials.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}
