package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/auth"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// obsBackend is a gateway backend that also reports poll observations — the
// optional hook that switches the wa.Manager reconciler on.
type obsBackend struct {
	*fakeGatewayBackend
	sid   atomic.Value // string
	calls atomic.Int32
}

func (b *obsBackend) Observations() []wa.BackendObservation {
	b.calls.Add(1)
	id, _ := b.sid.Load().(string)
	if id == "" {
		return nil
	}
	return []wa.BackendObservation{{
		SessionID: id, FailingSince: time.Now().Add(-time.Hour), RateLimited: true,
	}}
}

// TestHealthReportsReconcileCountsOnlyAndTheSessionsPageShowsTheBanner:
// /api/v1/health is unauthenticated and edge-published, so it carries only
// booleans and counts; the authenticated /sessions/reconcile partial is where
// the operator reads the detail. Existing keys must be untouched.
func TestHealthReportsReconcileCountsOnlyAndTheSessionsPageShowsTheBanner(t *testing.T) {
	b := &obsBackend{fakeGatewayBackend: &fakeGatewayBackend{name: "waxum"}}
	srv, sessions, s, _ := newSessionAdminTestServer(t, b, false)
	b.sid.Store(s.ID)

	var body string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
		body = rec.Body.String()
		if strings.Contains(body, `"ok":false`) {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	var out struct {
		Status    string `json:"status"`
		Sessions  *int   `json:"sessions"`
		Connected *int   `json:"connected"`
		Reconcile struct {
			OK             bool   `json:"ok"`
			Mismatches     int    `json:"mismatches"`
			BackendProblem bool   `json:"backend_problem"`
			LastChecked    string `json:"last_checked"`
		} `json:"reconcile"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if out.Status != "ok" || out.Sessions == nil || out.Connected == nil {
		t.Errorf("existing health keys broken: %s", body)
	}
	if out.Reconcile.OK || !out.Reconcile.BackendProblem || out.Reconcile.LastChecked == "" {
		t.Errorf("reconcile = %+v, want ok=false backend_problem=true with last_checked; body=%s", out.Reconcile, body)
	}
	if strings.Contains(body, s.ID) || strings.Contains(body, s.Name()) {
		t.Errorf("unauthenticated health leaked a session id/name: %s", body)
	}

	banner := authedGet(t, srv, sessions, "/sessions/reconcile")
	if banner.Code != http.StatusOK || !strings.Contains(banner.Body.String(), "429") {
		t.Fatalf("banner: %d %s", banner.Code, banner.Body.String())
	}
	page := authedGet(t, srv, sessions, "/sessions")
	if !strings.Contains(page.Body.String(), `hx-get="/sessions/reconcile"`) {
		t.Errorf("sessions page does not poll the reconcile banner")
	}
}

func TestHealthOmitsReconcileWhenNoReconcilerRuns(t *testing.T) {
	srv, _, _, _, _ := newMediaTestServer(t, mediaTestConfig{backend: &fakeGatewayBackend{name: "waxum"}})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if strings.Contains(rec.Body.String(), "reconcile") {
		t.Fatalf("health claims a reconcile verdict with nobody checking: %s", rec.Body.String())
	}
}

func TestTheReconcileBarIsPresentButQuietWhenAllIsWell(t *testing.T) {
	srv, sessions, _, _ := newSessionAdminTestServer(t, &obsBackend{fakeGatewayBackend: &fakeGatewayBackend{name: "waxum"}}, false)
	rec := authedGet(t, srv, sessions, "/sessions/reconcile")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || strings.Contains(body, "alert-error") || !strings.Contains(body, `hx-post="/sessions/reconcile"`) {
		t.Fatalf("banner = %d %q, want 200, no alert, and the Check now button", rec.Code, body)
	}
}

func TestNoReconcilerMeansNoBarAtAll(t *testing.T) {
	srv, sessions, _, _ := newSessionAdminTestServer(t, &fakeGatewayBackend{name: "waxum"}, false)
	rec := authedGet(t, srv, sessions, "/sessions/reconcile")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf("banner = %d %q, want empty", rec.Code, rec.Body.String())
	}
}

// obsBackend counts how often the reconciler consulted it.
func (b *obsBackend) consulted() int32 { return b.calls.Load() }

func postReconcile(srv http.Handler, cookie *http.Cookie, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/sessions/reconcile", nil)
	req.Header.Set("HX-Request", "true")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// TestManualReconcileRequiresACookieSessionAndNeverRunsWithout: no cookie is
// bounced to login (and runs nothing), a bearer token is not a page credential,
// a cookie works, and the response carries the fresh verdict.
func TestManualReconcileRequiresACookieSessionAndNeverRunsWithout(t *testing.T) {
	b := &obsBackend{fakeGatewayBackend: &fakeGatewayBackend{name: "waxum"}}
	srv, sessions, s, _ := newSessionAdminTestServerReconcile(t, b, false, 0)
	b.sid.Store(s.ID)

	if rec := postReconcile(srv, nil, ""); rec.Code != http.StatusUnauthorized || rec.Header().Get("HX-Redirect") == "" {
		t.Fatalf("no cookie: %d %q, want 401 + HX-Redirect", rec.Code, rec.Body.String())
	}
	if rec := postReconcile(srv, nil, "some-api-token"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bearer on a page route: %d, want 401", rec.Code)
	}
	if n := b.consulted(); n != 0 {
		t.Fatalf("unauthenticated requests ran the reconciler %d time(s)", n)
	}

	sess, err := sessions.Create("admin")
	if err != nil {
		t.Fatal(err)
	}
	rec := postReconcile(srv, &http.Cookie{Name: auth.CookieName, Value: sess.Token}, "")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Last checked") || !strings.Contains(body, "429") {
		t.Fatalf("cookie: %d %q, want 200 with the fresh verdict (last checked + the 429 backend problem)", rec.Code, body)
	}
	if strings.Contains(body, "moments ago") {
		t.Errorf("first click reported as cooldown: %q", body)
	}
	if n := b.consulted(); n != 1 {
		t.Fatalf("backend consulted %d times after one click, want 1", n)
	}

	// A second click inside the cooldown is answered from the stored verdict:
	// no second consultation, and the page says so.
	again := postReconcile(srv, &http.Cookie{Name: auth.CookieName, Value: sess.Token}, "")
	if !strings.Contains(again.Body.String(), "Checked moments ago") {
		t.Errorf("second click: %q, want the cooldown notice", again.Body.String())
	}
	if n := b.consulted(); n != 1 {
		t.Fatalf("backend consulted %d times after two rapid clicks, want still 1", n)
	}
}

// The API surface must not expose the trigger: it is a page route only.
func TestManualReconcileIsNotOnTheAPISurface(t *testing.T) {
	srv, _, _, _ := newSessionAdminTestServer(t, &fakeGatewayBackend{name: "waxum"}, false)
	for _, path := range []string{"/api/v1/sessions/reconcile", "/api/v1/reconcile"} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusUnauthorized && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d", path, rec.Code)
		}
	}
}
