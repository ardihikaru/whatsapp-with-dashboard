package handler

import "net/http"

const (
	themeCookieName   = "wsm_theme"
	themeDark         = "dark"
	themeLight        = "light"
	themeCookieMaxAge = 365 * 24 * 60 * 60 // 1 year, in seconds
)

// themeFrom returns the effective theme for the request. Anything other than
// a "dark" cookie (missing, garbage value, feature disabled) means light.
func (h *handlers) themeFrom(r *http.Request) string {
	if !h.DarkMode {
		return themeLight
	}
	if c, err := r.Cookie(themeCookieName); err == nil && c.Value == themeDark {
		return themeDark
	}
	return themeLight
}

// setThemeCookie persists the theme preference.
//
// Scoped to the mount like every other cookie this service writes (see
// handlers.cookiePath). Not a security boundary — it holds "dark" or "light" —
// but a Path=/ cookie on a shared origin is sent to every other app on that
// origin on every single request, and "wsm_theme" means nothing to any of them.
// Bytes on the wire for no one.
//
// Secure rides on the same flag as the session cookie: this is set on the
// login page too, which is reachable before any session exists.
func setThemeCookie(w http.ResponseWriter, theme, path string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     themeCookieName,
		Value:    theme,
		Path:     path,
		MaxAge:   themeCookieMaxAge,
		Secure:   secure,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// PostThemeToggle flips the theme cookie. Public by design: it only writes a
// display preference for the requesting browser, and the login page hosts the
// same toggle. Registered only when dark mode is enabled.
func (h *handlers) PostThemeToggle(w http.ResponseWriter, r *http.Request) {
	next := themeDark
	if h.themeFrom(r) == themeDark {
		next = themeLight
	}
	setThemeCookie(w, next, h.cookiePath(), h.CookieSecure)
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, h.BasePath+"/dashboard", http.StatusSeeOther)
}
