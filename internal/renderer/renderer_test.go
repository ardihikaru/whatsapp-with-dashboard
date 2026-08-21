package renderer_test

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"

	wsm "github.com/ardihikaru/whatsapp-with-dashboard"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/renderer"
)

// webFS mirrors what cmd/app/main.go hands the renderer: the embedded FS
// re-rooted at web/, so template paths resolve as "templates/...".
func webFS(t *testing.T) fs.FS {
	t.Helper()
	sub, err := fs.Sub(wsm.FS, "web")
	if err != nil {
		t.Fatalf("sub web: %v", err)
	}
	return sub
}

// A template that references an undefined sub-template only fails when New runs,
// which in production is process start-up. Without this test a broken page set
// ships a green `go test` and a dead binary.
func TestNewParsesEveryPageAndPartial(t *testing.T) {
	if _, err := renderer.New(webFS(t), ""); err != nil {
		t.Fatalf("parse embedded templates: %v", err)
	}
}

// RenderPartial must be able to execute the partials the HTMX routes name. A
// typo'd name is otherwise only visible as a 500 in the browser.
func TestRenderPartialNamesResolve(t *testing.T) {
	r, err := renderer.New(webFS(t), "")
	if err != nil {
		t.Fatalf("parse embedded templates: %v", err)
	}
	for _, name := range []string{"log_rows", "log_more", "log_rows_page", "session_rows"} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r.RenderPartial(rec, name, struct {
				Rows     []struct{}
				Sessions []struct{}
				MoreURL  string
				OOB      bool
			}{})
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: render failed with status %d: %s", name, rec.Code, rec.Body.String())
			}
		})
	}
}
