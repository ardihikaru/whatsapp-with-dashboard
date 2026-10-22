package renderer_test

import (
	"io/fs"
	"net/http/httptest"
	"strings"
	"testing"

	wsm "github.com/ardihikaru/whatsapp-with-dashboard"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/renderer"
)

// headTemplates returns every embedded template that owns a <head> — i.e. every
// place a stylesheet has to be registered. Discovered by scanning rather than
// hardcoded, so a third document template cannot quietly skip these checks.
func headTemplates(t *testing.T) map[string]string {
	t.Helper()
	found := map[string]string{}
	err := fs.WalkDir(wsm.FS, "web/templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".html") {
			return err
		}
		b, err := fs.ReadFile(wsm.FS, path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), "<head>") {
			found[path] = string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk templates: %v", err)
	}
	if len(found) < 2 {
		t.Fatalf("expected at least base.html and login.html to own a <head>, found %d: %v",
			len(found), found)
	}
	return found
}

// login.html duplicates base.html's entire <head> rather than sharing it, so a
// new stylesheet has to be added in two files. Forgetting the second is
// invisible until someone opens /login on a phone.
func TestEveryHeadTemplateLinksTheResponsiveStylesheet(t *testing.T) {
	for path, src := range headTemplates(t) {
		t.Run(path, func(t *testing.T) {
			if !strings.Contains(src, "/static/css/responsive.css") {
				t.Errorf("%s owns a <head> but does not link responsive.css — "+
					"the phone layout will not apply to the pages it renders", path)
			}
		})
	}
}

// responsive.css only works because it loads AFTER charts.css, whose trailing
// @media (max-width: 900px) { .sidebar { display: none } } it exists to
// override. Same rules, wrong order, and the sidebar stays deleted on phones.
func TestResponsiveStylesheetIsLinkedAfterChartsStylesheet(t *testing.T) {
	for path, src := range headTemplates(t) {
		t.Run(path, func(t *testing.T) {
			charts := strings.Index(src, "/static/css/charts.css")
			responsive := strings.Index(src, "/static/css/responsive.css")
			if charts < 0 || responsive < 0 {
				t.Fatalf("%s: charts.css at %d, responsive.css at %d — both must be linked",
					path, charts, responsive)
			}
			if responsive < charts {
				t.Errorf("%s links responsive.css before charts.css; charts.css would win "+
					"and keep .sidebar { display: none } on phones", path)
			}
		})
	}
}

// The drawer script has to reach the browser, or the sidebar never becomes a
// drawer and the hamburger stays hidden (it is gated on data-nav-js).
func TestBaseLayoutLoadsTheNavDrawerScript(t *testing.T) {
	b, err := fs.ReadFile(wsm.FS, "web/templates/base.html")
	if err != nil {
		t.Fatalf("read base.html: %v", err)
	}
	if !strings.Contains(string(b), "/static/js/nav-drawer.js") {
		t.Error("base.html does not load nav-drawer.js")
	}
}

// Assets must be reachable at the URL the templates ask for. A stylesheet named
// in a <link> but absent from the embedded FS is a 404 in the browser and a
// green test suite everywhere else.
func TestResponsiveAssetsExistInTheEmbeddedFS(t *testing.T) {
	for _, path := range []string{
		"web/static/css/responsive.css",
		"web/static/js/nav-drawer.js",
	} {
		if _, err := fs.ReadFile(wsm.FS, path); err != nil {
			t.Errorf("%s is linked by a template but missing from the embedded FS: %v", path, err)
		}
	}
}

type loginStub struct {
	Theme           string
	DarkModeEnabled bool
	Error           string
}

// A real render of the login document, proving the link survives execution and
// is prefixed like every other in-app URL when the app is mounted under a path.
func TestLoginPageRendersTheResponsiveStylesheetUnderBasePath(t *testing.T) {
	r, err := renderer.New(webFS(t), "/wsm", nil)
	if err != nil {
		t.Fatalf("parse embedded templates: %v", err)
	}
	rec := httptest.NewRecorder()
	r.RenderPage(rec, "login", loginStub{Theme: "light", DarkModeEnabled: true})
	body := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("render login: status %d: %s", rec.Code, body)
	}
	if !strings.Contains(body, `href="/wsm/static/css/responsive.css"`) {
		t.Errorf("login page is missing the base-path-prefixed responsive.css link; got:\n%s", body)
	}
}
