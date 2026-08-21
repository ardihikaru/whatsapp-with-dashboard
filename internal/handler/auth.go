package handler

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/auth"
)

// Local admin login. This service was an OIDC relying party of a separate
// auth-service upstream; standalone there is no such service, so the browser
// surface is guarded by ONE credential resolved from the environment
// (config.AdminUser / AdminPasswordHash).
//
// The MACHINE api — api_clients + bearer tokens at POST /api/v1/auth/login — is
// a different door and shares nothing with this file.

// maxLoginBodyBytes caps the login form. Nothing legitimate here is large, and
// an uncapped ParseForm on an unauthenticated route is free memory for anyone.
const maxLoginBodyBytes = 4 << 10

// loginPageData is what the "login" page set needs: partials/login_error.html
// reads .Error, partials/theme_toggle.html reads .Theme and .DarkModeEnabled.
type loginPageData struct {
	Theme           string
	DarkModeEnabled bool
	Error           string
}

func (h *handlers) loginPageData(r *http.Request, msg string) loginPageData {
	return loginPageData{Theme: h.themeFrom(r), DarkModeEnabled: h.DarkMode, Error: msg}
}

// GetLogin renders the sign-in form. An existing session skips to the dashboard.
func (h *handlers) GetLogin(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.Middleware.SessionFromRequest(r); ok {
		http.Redirect(w, r, h.BasePath+"/dashboard", http.StatusSeeOther)
		return
	}
	h.Renderer.RenderPage(w, "login", h.loginPageData(r, ""))
}

// PostLogin verifies the credential and mints a local session. Sessions live in
// memory (internal/auth), so a restart signs the operator out — the same
// contract the SSO build had.
func (h *handlers) PostLogin(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBodyBytes)
	if err := r.ParseForm(); err != nil {
		h.loginFailed(w, r, "Permintaan tidak valid.", http.StatusBadRequest)
		return
	}
	ip := clientIP(r)
	if wait := h.LoginThrottle.Take(ip, time.Now()); wait > 0 {
		h.loginFailed(w, r,
			fmt.Sprintf("Terlalu banyak percobaan. Coba lagi dalam %d detik.", int(wait.Seconds())+1),
			http.StatusTooManyRequests)
		return
	}
	if err := h.Admin.Verify(r.PostFormValue("username"), r.PostFormValue("password")); err != nil {
		// One message for both halves — see auth.ErrInvalidCredentials.
		h.loginFailed(w, r, "Username atau password salah.", http.StatusUnauthorized)
		return
	}
	h.LoginThrottle.Reset(ip)
	sess, err := h.Sessions.Create(h.Admin.Username)
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	auth.SetSessionCookie(w, sess.Token, h.SessionTTL, h.cookieOpts())
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", h.BasePath+"/dashboard")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, h.BasePath+"/dashboard", http.StatusSeeOther)
}

// loginFailed answers both submission styles from one place.
//
// THE HTMX BRANCH MUST BE 200. htmx only swaps 2xx responses by default, so
// returning the honest 401 alongside the error partial paints NOTHING and the
// form looks like it silently did nothing. The status is not the security
// boundary here — the absent session cookie is. The full-page branch keeps the
// real status, for curl and for the access log.
func (h *handlers) loginFailed(w http.ResponseWriter, r *http.Request, msg string, code int) {
	if r.Header.Get("HX-Request") == "true" {
		h.Renderer.RenderPartial(w, "login_error", struct{ Error string }{msg})
		return
	}
	// Content-Type BEFORE WriteHeader: a header set afterwards is a silent no-op.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	h.Renderer.RenderPage(w, "login", h.loginPageData(r, msg))
}

// PostLogout drops the session and bounces to /login.
func (h *handlers) PostLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.CookieName); err == nil {
		h.Sessions.Delete(c.Value)
	}
	auth.ClearSessionCookie(w, h.cookieOpts())
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", h.BasePath+"/login")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, h.BasePath+"/login", http.StatusSeeOther)
}

// clientIP is the throttle key: the host half of RemoteAddr.
//
// Deliberately NOT X-Forwarded-For — that header is attacker-controlled unless a
// trusted proxy is known to overwrite it, and a spoofable key is a throttle that
// does not exist. Behind a reverse proxy every attempt collapses onto the
// proxy's address, which throttles the proxy; for a single-operator tool that is
// the safer way to be wrong.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
