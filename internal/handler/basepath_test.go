package handler

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	wsm "github.com/ardihikaru/whatsapp-with-dashboard"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/auth"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/renderer"
)

// The mounted route table. Every other test in this package builds Deps with
// BasePath "" — the shape that shipped — so without this file /wsm could be
// entirely broken and the suite would stay green.

// mountedMux builds just enough of the router to assert WHERE patterns live.
// It deliberately does not stand up a database: these tests ask "does this path
// route", and a 401/303 from the auth middleware answers that as well as a 200
// would. What matters is 404 vs not-404.
func mountedMux(t *testing.T, basePath string) http.Handler {
	t.Helper()
	webFS, err := fs.Sub(wsm.FS, "web")
	if err != nil {
		t.Fatal(err)
	}
	rend, err := renderer.New(webFS, basePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewStore(time.Hour)
	t.Cleanup(sessions.Close)
	return New(Deps{
		Renderer:   rend,
		Sessions:   sessions,
		Middleware: auth.NewMiddleware(sessions, nil, basePath),
		AssetsFS:   wsm.FS,
		BasePath:   basePath,
		SessionTTL: time.Hour,
		// No Admin credential on purpose: the zero value authenticates nobody,
		// and these tests assert WHERE a route lives, not what it does once it
		// gets there. GetLogin renders from the renderer above, so it answers
		// 200 without any of this being configured.
	})
}

func status(t *testing.T, h http.Handler, method, path string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec.Code
}

// The browser surface moves; the JSON API does not. Both halves matter — a
// version of this change that moved everything would break wa-alerter,
// wa-notify.sh, the backoffice sender and the container healthcheck, all of
// which dial /api/v1 over container DNS and never touch the ingress.
func TestMountMovesTheUIButNotTheAPI(t *testing.T) {
	h := mountedMux(t, "/wsm")

	for _, p := range []string{"/wsm/login", "/wsm/dashboard", "/wsm/sessions", "/wsm/static/css/base.css", "/wsm/docs"} {
		if code := status(t, h, http.MethodGet, p); code == http.StatusNotFound {
			t.Errorf("GET %s = 404; the UI did not move with the mount", p)
		}
	}
	// The pre-mount paths must be GONE. If they still answer, the service is
	// serving two surfaces and a stripping ingress would appear to work — the
	// exact defect this design exists to make impossible.
	for _, p := range []string{"/login", "/dashboard", "/sessions", "/docs"} {
		if code := status(t, h, http.MethodGet, p); code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404; the old root route is still mounted", p, code)
		}
	}
	// The login POST is a route in its own right and must move with the mount
	// too — the login form renders its own action, so a POST left at the root
	// would 404 the moment the page is served under a prefix.
	if code := status(t, h, http.MethodPost, "/wsm/login"); code == http.StatusNotFound {
		t.Error("POST /wsm/login = 404; the login POST did not move with the mount")
	}
	if code := status(t, h, http.MethodPost, "/login"); code != http.StatusNotFound {
		t.Errorf("POST /login = %d, want 404; the old root route is still mounted", code)
	}
	// The OIDC callback is GONE. This service is no longer a relying party of
	// anything; a surviving /auth/callback would be a route nothing implements.
	if code := status(t, h, http.MethodGet, "/wsm/auth/callback"); code != http.StatusNotFound {
		t.Errorf("GET /wsm/auth/callback = %d, want 404; the OIDC callback outlived SSO", code)
	}
	// The API stays at the root, unprefixed. /stats rather than /health because
	// it is BEHIND RequireAPI: the 401 proves the pattern is registered without
	// running a handler that would need a live wa.Manager. /health is the same
	// mux entry and is exercised for real by the container healthcheck.
	if code := status(t, h, http.MethodGet, "/api/v1/stats"); code == http.StatusNotFound {
		t.Error("GET /api/v1/stats = 404; the API must NOT move with the mount")
	}
	if code := status(t, h, http.MethodGet, "/wsm/api/v1/stats"); code != http.StatusNotFound {
		t.Errorf("GET /wsm/api/v1/stats = %d, want 404; the API was prefixed by mistake", code)
	}
	// openapi.yaml is an artefact of the API and stays with it.
	if code := status(t, h, http.MethodGet, "/openapi.yaml"); code == http.StatusNotFound {
		t.Error("GET /openapi.yaml = 404; the spec must stay at the root with the API")
	}
}

// A browser following the backoffice sidebar link asks for "/wsm", with no
// trailing slash. ServeMux only auto-redirects to a subtree pattern ending in
// "/", and "GET /wsm/{$}" is not one — so without an explicit rule that link is
// the single 404 in an otherwise working product.
func TestBareMountRedirectsToTheDashboard(t *testing.T) {
	h := mountedMux(t, "/wsm")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/wsm", nil))
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("GET /wsm = %d, want 301", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/wsm/" {
		t.Errorf("Location = %q, want /wsm/", loc)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/wsm/", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("GET /wsm/ = %d, want 302", rec.Code)
	}
	// Must point INTO the mount. "/dashboard" here would leave WSM entirely and
	// land on the platform backoffice — a different product on the same origin.
	if loc := rec.Header().Get("Location"); loc != "/wsm/dashboard" {
		t.Errorf("Location = %q, want /wsm/dashboard", loc)
	}
}

// Unmounted, the table must be exactly what shipped before this change.
func TestUnmountedRouteTableIsUnchanged(t *testing.T) {
	h := mountedMux(t, "")
	for _, p := range []string{"/login", "/dashboard", "/sessions", "/docs", "/api/v1/stats", "/openapi.yaml"} {
		if code := status(t, h, http.MethodGet, p); code == http.StatusNotFound {
			t.Errorf("GET %s = 404 with no mount; the original route table regressed", p)
		}
	}
	if code := status(t, h, http.MethodGet, "/wsm/dashboard"); code != http.StatusNotFound {
		t.Errorf("GET /wsm/dashboard = %d with no mount, want 404", code)
	}
}

// An unauthenticated page request must bounce INSIDE the mount. A bare
// "/login" would hand the operator the platform backoffice's front door instead
// — a session quietly expiring on the WhatsApp dashboard would look like being
// signed out of a different product.
func TestUnauthenticatedBounceStaysInsideTheMount(t *testing.T) {
	h := mountedMux(t, "/wsm")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/wsm/dashboard", nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("GET /wsm/dashboard unauthenticated = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/wsm/login" {
		t.Errorf("Location = %q, want /wsm/login", loc)
	}

	// The HTMX branch is a separate code path and bounces via a header.
	req := httptest.NewRequest(http.MethodGet, "/wsm/sessions/rows", nil)
	req.Header.Set("HX-Request", "true")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("HX-Redirect"); got != "/wsm/login" {
		t.Errorf("HX-Redirect = %q, want /wsm/login", got)
	}
}
