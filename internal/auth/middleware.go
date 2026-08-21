package auth

import (
	"context"
	"net/http"
	"strings"
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
}

func NewMiddleware(store *Store, apiAuth APIAuthenticator, basePath string) *Middleware {
	return &Middleware{store: store, apiAuth: apiAuth, basePath: basePath}
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
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"unauthorized"}`))
	})
}

func withAPIPrincipal(ctx context.Context, principal APIPrincipal) context.Context {
	return context.WithValue(ctx, apiPrincipalKey{}, principal)
}

func withSession(ctx context.Context, sess Session) context.Context {
	return context.WithValue(ctx, contextKey{}, sess)
}
