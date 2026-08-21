package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// URLs that are built in GO rather than in a template, and therefore could not
// be reached by the sweep that moved all 67 template URLs onto the `u` function.
// Split from basepath_test.go — which asserts WHERE routes live — because these
// assert what a response BODY says; the shared harness lives there.

// THE SWAGGER PAGE IS NOT A TEMPLATE, and that is how it escaped the sweep that
// moved all 67 template URLs onto the `u` function. It was a Go string constant
// with /static/... baked in, and it shipped rendering root-absolute paths while
// the app was mounted at /wsm.
//
// On a shared origin that is worse than a 404: /static/swagger/swagger-ui.css
// is a request to the PLATFORM BACKOFFICE, which answers it from its own static
// handler. The page half-renders with a neighbour's assets, and neither
// service logs anything unusual.
func TestSwaggerPageAssetsCarryTheMount(t *testing.T) {
	rec := httptest.NewRecorder()
	mountedMux(t, "/wsm").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/wsm/docs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /wsm/docs = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`href="/wsm/static/swagger/swagger-ui.css"`,
		`href="/wsm/static/img/logo.svg"`,
		`src="/wsm/static/swagger/swagger-ui-bundle.js"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("swagger page missing %s", want)
		}
	}
	// The SPEC is the exception and must stay unprefixed: /openapi.yaml lives at
	// the container root with the API it documents.
	if !strings.Contains(body, `url: '/openapi.yaml'`) {
		t.Error("swagger page must fetch /openapi.yaml unprefixed — the spec does not move with the UI")
	}
	if strings.Contains(body, "/wsm/openapi.yaml") {
		t.Error("swagger page prefixed the spec URL; the API and its spec stay at the root")
	}
}

// The sidebar hrefs are built in Go, not in the template, so they are a second
// place a root-absolute path can survive. Active state is resolved by comparing
// against an UNPREFIXED currentPath — prefix only one side and every nav item
// goes permanently dark, which no href assertion would catch.
func TestNavPathsCarryTheMountAndActiveStillResolves(t *testing.T) {
	secs := buildNav("/wsm", "/sessions")
	var seen, active int
	for _, s := range secs {
		for _, it := range s.Items {
			seen++
			if !strings.HasPrefix(it.Path, "/wsm/") {
				t.Errorf("nav item %q has Path %q, want a /wsm/ prefix", it.Title, it.Path)
			}
			if it.Active {
				active++
				if it.Title != "Sessions" {
					t.Errorf("Active resolved to %q, want Sessions", it.Title)
				}
			}
		}
	}
	if seen == 0 {
		t.Fatal("buildNav returned no items")
	}
	if active != 1 {
		t.Errorf("got %d active nav items, want exactly 1 — Active is compared against an unprefixed currentPath", active)
	}
}
