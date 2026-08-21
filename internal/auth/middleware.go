package auth

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type contextKey struct{}
type apiPrincipalKey struct{}

type APIPrincipal struct {
	TokenHash string
	ClientID  string
	LoginID   string
}

type APIAuthenticator interface {
	AuthenticateBearer(ctx context.Context, token string) (APIPrincipal, bool)
}

// APIBasicAuthenticator is the OPTIONAL second credential path. An
// APIAuthenticator that also implements it lets callers skip the token exchange
// and send `Authorization: Basic base64(login_id:secret)` on every request.
//
// It is a separate interface, discovered by type assertion, so that an
// authenticator which only understands tokens stays valid — and so the Basic
// branch below simply does not exist for one.
type APIBasicAuthenticator interface {
	AuthenticateBasic(ctx context.Context, loginID, secret string) (APIPrincipal, bool)
}

// SessionFrom extracts the session placed in the request context by the
// middleware; ok is false on unauthenticated requests.
func SessionFrom(ctx context.Context) (Session, bool) {
	sess, ok := ctx.Value(contextKey{}).(Session)
	return sess, ok
}

func APIPrincipalFrom(ctx context.Context) (APIPrincipal, bool) {
	principal, ok := ctx.Value(apiPrincipalKey{}).(APIPrincipal)
	return principal, ok
}

// Middleware guards browser routes with the in-memory web session store and
// JSON API routes with persisted API-client bearer tokens.
//
// basePath is the mount prefix, needed here for one reason only: the two
// unauthenticated bounces below are absolute URLs. Under /wsm a bare "/login"
// leaves this service entirely and lands on the platform backoffice's front
// door, which then shows its own chooser — a session expiring on the WhatsApp
// dashboard would silently hand the operator a different product.
type Middleware struct {
	store    *Store
	apiAuth  APIAuthenticator
	basePath string
	// basicThrottle bounds FAILED Basic attempts per client IP. Verifying Basic
	// costs a bcrypt compare, and the success cache cannot absorb an attacker
	// who varies the secret every request — that misses by construction. nil
	// means no throttling.
	basicThrottle *Throttle
}

func NewMiddleware(store *Store, apiAuth APIAuthenticator, basePath string) *Middleware {
	return &Middleware{store: store, apiAuth: apiAuth, basePath: basePath}
}

// WithBasicThrottle attaches the failed-Basic-attempt limiter. Separate from
// NewMiddleware so every existing caller — and every test — keeps working
// unthrottled.
func (m *Middleware) WithBasicThrottle(t *Throttle) *Middleware {
	m.basicThrottle = t
	return m
}

// LoginPath is the mount-aware login URL these bounces target.
func (m *Middleware) LoginPath() string { return m.basePath + "/login" }

// SessionFromRequest resolves the web session from the cookie only.
func (m *Middleware) SessionFromRequest(r *http.Request) (Session, bool) {
	if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
		if sess, ok := m.store.Get(c.Value); ok {
			return sess, true
		}
	}
	return Session{}, false
}

// RequirePage protects HTML routes. Unauthenticated HTMX requests get
// 401 + HX-Redirect so a stale poll bounces cleanly to the login page;
// plain browser requests get a 303.
func (m *Middleware) RequirePage(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := m.SessionFromRequest(r)
		if !ok {
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", m.LoginPath())
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, m.LoginPath(), http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r.WithContext(withSession(r.Context(), sess)))
	})
}

// RequireAPI protects JSON routes with API-client bearer tokens only.
// RequireAPI protects JSON routes with API-client credentials, in either of the
// two accepted forms:
//
//	Authorization: Bearer <token>                     — from POST /api/v1/auth/login
//	Authorization: Basic base64(login_id:secret)      — the credential directly
//
// Both resolve to the same APIPrincipal, so no handler downstream needs to know
// which was used. The browser session cookie is NOT accepted here and never has
// been: the two credential systems stay non-interchangeable.
func (m *Middleware) RequireAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		if strings.HasPrefix(h, "Bearer ") && m.apiAuth != nil {
			token := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
			if principal, ok := m.apiAuth.AuthenticateBearer(r.Context(), token); ok {
				next.ServeHTTP(w, r.WithContext(withAPIPrincipal(r.Context(), principal)))
				return
			}
		}
		if loginID, secret, ok := r.BasicAuth(); ok && m.apiAuth != nil {
			if basic, supported := m.apiAuth.(APIBasicAuthenticator); supported {
				// Throttle BEFORE the bcrypt compare — checking afterwards would
				// mean the work we are trying to bound has already been done.
				if wait := m.basicThrottle.Take(ClientIP(r), time.Now()); wait > 0 {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
					w.WriteHeader(http.StatusTooManyRequests)
					w.Write([]byte(`{"error":"too many failed attempts"}`))
					return
				}
				if principal, ok := basic.AuthenticateBasic(r.Context(), loginID, secret); ok {
					// Only failures should consume the budget; a working caller
					// must never throttle itself.
					m.basicThrottle.Reset(ClientIP(r))
					next.ServeHTTP(w, r.WithContext(withAPIPrincipal(r.Context(), principal)))
					return
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		// WWW-Authenticate is what makes a 401 actionable for a Basic client;
		// without it a browser or an SDK cannot know which scheme to retry with.
		w.Header().Set("WWW-Authenticate", `Basic realm="api", charset="UTF-8"`)
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"unauthorized"}`))
	})
}

// ClientIP is the throttle key: the host half of RemoteAddr.
//
// Deliberately NOT X-Forwarded-For — that header is attacker-controlled unless a
// trusted proxy is known to overwrite it, and a spoofable key is a throttle that
// does not exist. Behind a reverse proxy every attempt collapses onto the
// proxy's address, which throttles the proxy; for a single-operator tool that is
// the safer way to be wrong.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func withAPIPrincipal(ctx context.Context, principal APIPrincipal) context.Context {
	return context.WithValue(ctx, apiPrincipalKey{}, principal)
}

func withSession(ctx context.Context, sess Session) context.Context {
	return context.WithValue(ctx, contextKey{}, sess)
}
