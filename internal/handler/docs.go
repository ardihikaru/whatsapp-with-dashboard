package handler

import (
	"fmt"
	"io/fs"
	"net/http"
)

// swaggerPageFmt loads the vendored swagger-ui-dist assets and points them at
// the embedded spec — fully offline, no CDN.
//
// ⚠ THIS PAGE IS NOT A TEMPLATE, WHICH IS WHY IT WAS MISSED. Every other URL in
// this service goes through web/templates and the `u` function; these four were
// a Go string constant with root-absolute paths baked in, so they survived the
// sweep untouched and rendered "/static/..." while the app was mounted at /wsm.
//
// On a shared origin that is worse than a 404: /static/swagger/swagger-ui.css
// is a request to the PLATFORM BACKOFFICE, which answers it with its own
// static handler. The page half-renders with a neighbour's assets and nothing
// in either service's log says anything is wrong.
//
// The three %[1]s take the mount prefix. %[2]s is the spec URL, which does NOT
// take it: /openapi.yaml stays at the container root with the API it describes
// (see routes_api.go), so the browser must ask for it unprefixed.
const swaggerPageFmt = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>WhatsApp Session Management API · Swagger UI</title>
  <link rel="stylesheet" href="%[1]s/static/swagger/swagger-ui.css">
  <link rel="icon" href="%[1]s/static/img/logo.svg">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="%[1]s/static/swagger/swagger-ui-bundle.js"></script>
  <script>
    window.ui = SwaggerUIBundle({
      url: '%[2]s',
      dom_id: '#swagger-ui',
      presets: [SwaggerUIBundle.presets.apis],
      requestSnippetsEnabled: true,
    });
  </script>
</body>
</html>`

// GetDocs serves the Swagger UI page.
func (h *handlers) GetDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, swaggerPageFmt, h.BasePath, "/openapi.yaml")
}

// GetSpec serves the embedded OpenAPI spec.
func (h *handlers) GetSpec(w http.ResponseWriter, r *http.Request) {
	spec, err := fs.ReadFile(h.AssetsFS, "openapi.yaml")
	if err != nil {
		http.Error(w, "spec not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	w.Write(spec)
}
