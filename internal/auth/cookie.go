package auth

import (
	"net/http"
	"time"
)

// CookieName identifies the session cookie (also referenced by openapi.yaml).
const CookieName = "wsm_session"

// CookieOpts carries the two properties that depend on WHERE this service is
// mounted and HOW it is reached, rather than on the session itself.
//
// PATH SCOPES THE COOKIE TO THE MOUNT. Mounted under a prefix on an origin
// shared with other apps, a Path=/ cookie would be sent to every one of those
// surfaces, none of which have any use for it. It would not grant them anything
// (they do not read this name), but a credential travelling further than it is
// needed is one more place it can leak from, and the mount already tells us
// exactly how far that is.
//
// The cookie is NOT __Host- prefixed, which is what makes a Path other than "/"
// legal here at all. __Host- forbids Path — it pins to an exact origin instead —
// and adopting it would mean giving up this scoping.
//
// SECURE WAS UNCONDITIONALLY OFF UNTIL AGUSTUS 2026, with the comment "the app
// serves plain HTTP on localhost". That was true while WSM was localhost-only.
// It is now served over TLS at https://app.clino.id/wsm, and a session cookie
// without Secure is one downgraded request away from being read in the clear.
// The flag was already configured (WA_COOKIE_SECURE, config.CookieSecure) and
// already applied to the OIDC state cookie in handler/auth.go — this cookie,
// the one that actually carries the session, was simply never given it.
type CookieOpts struct {
	Path   string // "" means "/" — see BasePath in internal/config
	Secure bool
}

func (o CookieOpts) path() string {
	if o.Path == "" {
		return "/"
	}
	return o.Path
}

// SetSessionCookie attaches the session token to the response.
func SetSessionCookie(w http.ResponseWriter, token string, ttl time.Duration, o CookieOpts) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     o.path(),
		MaxAge:   int(ttl.Seconds()),
		Secure:   o.Secure,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearSessionCookie expires the session cookie immediately.
//
// Path and Secure MUST match what SetSessionCookie wrote. A browser keys a
// cookie by (name, domain, path); clearing with a different Path leaves the
// original in place and the user stays logged in after pressing Logout — a
// failure that looks like nothing happened and produces no server-side error.
func ClearSessionCookie(w http.ResponseWriter, o CookieOpts) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     o.path(),
		MaxAge:   -1,
		Secure:   o.Secure,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}
