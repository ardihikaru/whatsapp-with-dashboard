package handler

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	wsm "github.com/ardihikaru/whatsapp-with-dashboard"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/auth"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/renderer"
	"golang.org/x/crypto/bcrypt"
)

const (
	testAdminUser = "admin"
	testAdminPass = "correct horse"
)

// loginMux builds the router with a real admin credential and no database. The
// local login touches neither MySQL nor the whatsmeow store, which is what makes
// these tests cheap enough to be worth having.
func loginMux(t *testing.T, cookieSecure bool) (http.Handler, *auth.Store) {
	t.Helper()
	webFS, err := fs.Sub(wsm.FS, "web")
	if err != nil {
		t.Fatal(err)
	}
	rend, err := renderer.New(webFS, "")
	if err != nil {
		t.Fatal(err)
	}
	// MinCost: these tests exercise the wiring, not bcrypt's work factor.
	hash, err := bcrypt.GenerateFromPassword([]byte(testAdminPass), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewStore(time.Hour)
	t.Cleanup(sessions.Close)
	return New(Deps{
		Renderer:      rend,
		Sessions:      sessions,
		Middleware:    auth.NewMiddleware(sessions, nil, ""),
		AssetsFS:      wsm.FS,
		SessionTTL:    time.Hour,
		DarkMode:      true,
		CookieSecure:  cookieSecure,
		Admin:         auth.AdminCredential{Username: testAdminUser, PasswordHash: hash},
		LoginThrottle: auth.NewThrottle(0.1, 5),
	}), sessions
}

func postLogin(t *testing.T, h http.Handler, user, pass string, htmx bool) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"username": {user}, "password": {pass}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "10.0.0.1:54321"
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func sessionCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName {
			return c
		}
	}
	return nil
}

// The login template went unrendered for months while SSO owned /login, so the
// form itself needs a guard: nothing else in the suite would notice it rotting.
func TestLoginPageRendersCredentialForm(t *testing.T) {
	h, _ := loginMux(t, false)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /login = %d, want 200", rec.Code)
	}
	for _, want := range []string{`name="username"`, `name="password"`, `method="post"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("login page is missing %s", want)
		}
	}
}

func TestLoginSetsSessionCookieAndAdmitsThePage(t *testing.T) {
	h, sessions := loginMux(t, false)
	rec := postLogin(t, h, testAdminUser, testAdminPass, false)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /login = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/dashboard" {
		t.Errorf("Location = %q, want /dashboard", loc)
	}
	c := sessionCookie(rec)
	if c == nil {
		t.Fatal("no session cookie was set")
	}
	if !c.HttpOnly {
		t.Error("session cookie is not HttpOnly")
	}
	if c.Path != "/" {
		t.Errorf("cookie Path = %q, want /", c.Path)
	}
	if _, ok := sessions.Get(c.Value); !ok {
		t.Error("the cookie does not name a live session")
	}

	// And the cookie actually opens a guarded route. This goes through the real
	// RequirePage middleware rather than one of the app's pages: every page
	// handler wants repositories this DB-free fixture does not have, and what is
	// under test here is admission, not what the page renders once admitted.
	guarded := auth.NewMiddleware(sessions, nil, "").RequirePage(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
	req := httptest.NewRequest(http.MethodGet, "/guarded", nil)
	req.AddCookie(c)
	rec2 := httptest.NewRecorder()
	guarded.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Errorf("a guarded route answered %d despite a valid session cookie", rec2.Code)
	}

	// The same route without the cookie must bounce — otherwise the assertion
	// above would pass on a middleware that admits everyone.
	rec3 := httptest.NewRecorder()
	guarded.ServeHTTP(rec3, httptest.NewRequest(http.MethodGet, "/guarded", nil))
	if rec3.Code != http.StatusSeeOther {
		t.Errorf("a guarded route answered %d WITHOUT a session cookie, want 303", rec3.Code)
	}
}

func TestLoginCookieIsSecureWhenConfigured(t *testing.T) {
	h, _ := loginMux(t, true)
	c := sessionCookie(postLogin(t, h, testAdminUser, testAdminPass, false))
	if c == nil {
		t.Fatal("no session cookie was set")
	}
	if !c.Secure {
		t.Error("cookie is not Secure although CookieSecure was set")
	}
}

func TestLoginRejectsBadCredentials(t *testing.T) {
	tests := []struct {
		name, user, pass string
	}{
		{"wrong password", testAdminUser, "nope"},
		{"wrong username", "root", testAdminPass},
		{"empty", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := loginMux(t, false)
			rec := postLogin(t, h, tc.user, tc.pass, false)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("POST /login = %d, want 401", rec.Code)
			}
			if sessionCookie(rec) != nil {
				t.Error("a session cookie was set for a rejected login")
			}
			if !strings.Contains(rec.Body.String(), "salah") {
				t.Error("the error message was not rendered")
			}
		})
	}
}

// This pins the htmx-swaps-only-2xx decision so nobody "corrects" it to 401
// later and leaves the form silently doing nothing on a wrong password.
func TestLoginFailureIsSwappableByHTMX(t *testing.T) {
	h, _ := loginMux(t, false)
	rec := postLogin(t, h, testAdminUser, "nope", true)

	if rec.Code != http.StatusOK {
		t.Fatalf("HTMX POST /login = %d, want 200 — htmx only swaps 2xx, so any "+
			"other status renders nothing and the form looks dead", rec.Code)
	}
	if sessionCookie(rec) != nil {
		t.Error("a session cookie was set for a rejected login")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "salah") {
		t.Error("the error fragment was not rendered")
	}
	// A fragment, not the whole page — it is swapped into #login-error.
	if strings.Contains(body, "<html") {
		t.Error("the HTMX branch returned a full page instead of the error fragment")
	}
}

func TestLoginSuccessRedirectsHTMXViaHeader(t *testing.T) {
	h, _ := loginMux(t, false)
	rec := postLogin(t, h, testAdminUser, testAdminPass, true)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("HTMX POST /login = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("HX-Redirect"); got != "/dashboard" {
		t.Errorf("HX-Redirect = %q, want /dashboard", got)
	}
	if sessionCookie(rec) == nil {
		t.Error("no session cookie was set")
	}
}

func TestLoginThrottleRejectsAfterBurst(t *testing.T) {
	h, _ := loginMux(t, false) // burst 5
	for i := range 5 {
		if code := postLogin(t, h, testAdminUser, "nope", false).Code; code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401 (still inside the burst)", i+1, code)
		}
	}
	if code := postLogin(t, h, testAdminUser, "nope", false).Code; code != http.StatusTooManyRequests {
		t.Errorf("attempt 6 = %d, want 429", code)
	}
}

func TestLogoutClearsTheSession(t *testing.T) {
	h, sessions := loginMux(t, false)
	c := sessionCookie(postLogin(t, h, testAdminUser, testAdminPass, false))
	if c == nil {
		t.Fatal("no session cookie to log out with")
	}

	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /logout = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
	if _, ok := sessions.Get(c.Value); ok {
		t.Error("the session survived logout")
	}
	cleared := sessionCookie(rec)
	if cleared == nil || cleared.MaxAge >= 0 {
		t.Error("the session cookie was not expired at the same path")
	}
}
