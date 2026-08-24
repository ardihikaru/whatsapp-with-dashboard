package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// pagesWithChrome are the base-layout routes. Every one of them must carry the
// drawer markup, because the sidebar is shared chrome: a page that renders the
// old markup would have no reachable navigation on a phone.
var pagesWithChrome = []string{
	"/dashboard",
	"/sessions",
	"/sessions/new",
	"/send",
	"/logs",
	"/admin/api-clients",
	"/system-log",
}

// The drawer is three cooperating pieces: an <aside> the button can name, the
// button itself, and a backdrop. Missing any one of them leaves the phone
// layout half-built — the CSS is keyed on all three.
func TestEveryChromePageRendersTheDrawerMarkup(t *testing.T) {
	srv, sessions, _, _ := newLogsTestServer(t)

	for _, path := range pagesWithChrome {
		t.Run(path, func(t *testing.T) {
			res := authedGet(t, srv, sessions, path)
			if res.Code != http.StatusOK {
				t.Fatalf("GET %s = %d: %s", path, res.Code, res.Body.String())
			}
			body := res.Body.String()
			for _, want := range []string{
				`id="app-sidebar"`,
				`class="nav-toggle btn-icon"`,
				`aria-controls="app-sidebar"`,
				`aria-expanded="false"`,
				`class="nav-backdrop"`,
				`/static/css/responsive.css`,
				`/static/js/nav-drawer.js`,
			} {
				if !strings.Contains(body, want) {
					t.Errorf("GET %s is missing %s", path, want)
				}
			}
		})
	}
}

// aria-controls is only meaningful if it points at an element that exists, and
// the CSS selector [data-nav-js] .sidebar only matches if the aside still
// carries its class. Both are easy to break by renaming one and not the other.
func TestDrawerToggleTargetsTheSidebarItControls(t *testing.T) {
	srv, sessions, _, _ := newLogsTestServer(t)
	body := authedGet(t, srv, sessions, "/dashboard").Body.String()

	if !strings.Contains(body, `<aside class="sidebar" id="app-sidebar"`) {
		t.Error(`the aside must keep class="sidebar" AND carry id="app-sidebar"; ` +
			"the stylesheet selects on the class, the button on the id")
	}
}

// The whole point of the change: the drawer must still contain the complete nav
// tree. A phone user loses nothing that a desktop user has.
func TestDrawerCarriesEverySidebarDestination(t *testing.T) {
	srv, sessions, _, _ := newLogsTestServer(t)
	body := authedGet(t, srv, sessions, "/dashboard").Body.String()

	sidebar := sidebarHTML(t, body)

	for _, want := range []string{
		"Dashboard", "Sessions", "Send Message", "Message Log", "API Clients", "System Log", "API Docs",
	} {
		if !strings.Contains(sidebar, want) {
			t.Errorf("sidebar is missing the %q destination", want)
		}
	}
	for _, want := range []string{"Overview", "WhatsApp", "Admin", "Docs"} {
		if !strings.Contains(sidebar, ">"+want+"<") {
			t.Errorf("sidebar is missing the %q section subheader", want)
		}
	}
}

// .header-user is hidden below 480px, so the signed-in identity has to survive
// somewhere the phone user can still reach: inside the drawer.
func TestDrawerNamesTheSignedInUser(t *testing.T) {
	srv, sessions, _, _ := newLogsTestServer(t)
	sidebar := sidebarHTML(t, authedGet(t, srv, sessions, "/dashboard").Body.String())

	if !strings.Contains(sidebar, "nav-user") || !strings.Contains(sidebar, "admin") {
		t.Errorf("the drawer does not name the signed-in user; got:\n%s", sidebar)
	}
}

// The hamburger must never ship pre-expanded: the drawer starts closed, and a
// stale aria-expanded="true" tells a screen reader the opposite.
func TestDrawerStartsClosed(t *testing.T) {
	srv, sessions, _, _ := newLogsTestServer(t)
	body := authedGet(t, srv, sessions, "/dashboard").Body.String()

	if strings.Contains(body, `aria-expanded="true"`) {
		t.Error(`a control ships with aria-expanded="true"; the drawer must start closed`)
	}
	if !strings.Contains(body, `<div class="nav-backdrop" hidden>`) {
		t.Error("the backdrop must ship hidden; nav-drawer.js unhides it only when JS runs")
	}
}

// The login page has no sidebar and must not grow one — it renders its own
// document, not base.html.
func TestLoginPageHasNoDrawer(t *testing.T) {
	srv, _, _, _ := newLogsTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, req)
	body := res.Body.String()

	if strings.Contains(body, "nav-toggle") || strings.Contains(body, "app-sidebar") {
		t.Error("the login page must not render drawer chrome")
	}
	if !strings.Contains(body, "/static/css/responsive.css") {
		t.Error("the login page still needs responsive.css for its own phone layout")
	}
}

// sidebarHTML slices out the <aside> so an assertion about "the sidebar" cannot
// be satisfied by a match somewhere else on the page.
func sidebarHTML(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, `<aside class="sidebar"`)
	if start < 0 {
		t.Fatalf("no sidebar in the rendered page:\n%s", body)
	}
	end := strings.Index(body[start:], "</aside>")
	if end < 0 {
		t.Fatalf("sidebar is not closed:\n%s", body[start:])
	}
	return body[start : start+end]
}
