// Package renderer renders the embedded html/template pages and HTMX partials.
package renderer

import (
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
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
	"templates/partials/pairing_status.html",
	"templates/partials/send_result.html",
	"templates/partials/log_rows.html",
	"templates/partials/system_log_rows.html",
	"templates/partials/call_rows.html",
	"templates/partials/session_ops_panel.html",
	"templates/partials/webhook_rows.html",
	"templates/partials/webhook_dlq_rows.html",
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
func New(fsys fs.FS, basePath string) (*Renderer, error) {
	r := &Renderer{
		pages: make(map[string]*template.Template),
		roots: make(map[string]string),
	}
	// u joins the mount prefix to an app-absolute path. Paths passed to it must
	// start with "/" — that is what makes "" (unmounted) a no-op rather than a
	// source of "wsmsessions".
	fm := template.FuncMap{
		"u": func(p string) string { return basePath + p },
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
