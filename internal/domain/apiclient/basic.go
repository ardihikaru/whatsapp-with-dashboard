package apiclient

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/auth"
)

// HTTP Basic half of the apiclient repository: the ALTERNATIVE to the bearer
// flow in token.go, not a replacement for it. A caller may either exchange its
// credentials for a token once (POST /api/v1/auth/login) and send
// `Authorization: Bearer <token>`, or send `Authorization: Basic
// base64(login_id:secret)` on every request. Both resolve to the same
// auth.APIPrincipal, so nothing downstream can tell them apart.
//
// The credential is identical in both cases — the bcrypt hash in
// api_clients.secret_hash. Basic issues NO token: there is no row in
// api_access_tokens to revoke, so revoking Basic access means deactivating the
// client or rotating its secret.

// basicCacheTTL is how long a VERIFIED credential is trusted without touching
// bcrypt again.
//
// Bearer needs no such cache: a token is verified with one SHA-256 and an
// indexed lookup. Basic re-presents the raw secret on every request, and
// verifying it means bcrypt at cost 10 — ~80ms of CPU, in a process that must
// also keep a WhatsApp websocket alive. A caller polling once a second would
// spend 8% of a core on nothing but re-proving what it proved a second ago.
//
// 60s is short enough that a rotated secret or a deactivated client stops
// working promptly even if the explicit invalidation below were ever missed,
// and long enough that a busy caller pays bcrypt once a minute.
const basicCacheTTL = time.Minute

type basicCacheEntry struct {
	principal auth.APIPrincipal
	expiresAt time.Time
}

// basicCache memoises SUCCESSFUL Basic verifications.
//
// Only successes are stored, which is what bounds the memory: an entry requires
// the correct secret, so the map is sized by the number of real clients, not by
// what an attacker sends. Caching failures would not work anyway — a caller
// varying the secret every request misses the cache by construction, which is
// why the middleware throttles failed Basic attempts per IP instead.
type basicCache struct {
	ttl   time.Duration
	mu    sync.RWMutex
	byKey map[string]basicCacheEntry
}

func newBasicCache(ttl time.Duration) *basicCache {
	return &basicCache{ttl: ttl, byKey: make(map[string]basicCacheEntry)}
}

// basicCacheKey binds the login id to the secret that was verified.
//
// The secret is stored as SHA-256, never raw: this map would otherwise be a
// list of live API credentials sitting in memory, readable in any core dump.
// A fast hash is the right choice here (unlike for the at-rest secret_hash,
// which is bcrypt): these secrets are 32 random bytes, so there is no
// dictionary to run against them, and the lookup has to be cheap or the cache
// defeats its own purpose.
func basicCacheKey(loginID, secret string) string {
	sum := sha256.Sum256([]byte(loginID + "\x00" + secret))
	return hex.EncodeToString(sum[:])
}

func (c *basicCache) get(key string, now time.Time) (auth.APIPrincipal, bool) {
	c.mu.RLock()
	e, ok := c.byKey[key]
	c.mu.RUnlock()
	if !ok || !now.Before(e.expiresAt) {
		return auth.APIPrincipal{}, false
	}
	return e.principal, true
}

func (c *basicCache) put(key string, principal auth.APIPrincipal, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Opportunistic sweep of expired entries while the lock is held; this map is
	// small enough that a janitor goroutine would be more machinery than the
	// problem deserves.
	for k, e := range c.byKey {
		if !now.Before(e.expiresAt) {
			delete(c.byKey, k)
		}
	}
	c.byKey[key] = basicCacheEntry{principal: principal, expiresAt: now.Add(c.ttl)}
}

// invalidateClient drops every cached credential for one client.
//
// Called from the four mutations that can make a cached answer WRONG:
// RotateSecret, SetActive, Update (the login id may change) and Delete. Without
// this, deactivating a client would leave it working for up to basicCacheTTL —
// which is exactly the kind of "revoked but still admitted" window that makes
// caching auth decisions dangerous.
func (c *basicCache) invalidateClient(clientID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.byKey {
		if e.principal.ClientID == clientID {
			delete(c.byKey, k)
		}
	}
}

// AuthenticateBasic verifies a login id and secret presented on every request.
//
// It returns the same auth.APIPrincipal the bearer path returns, with an empty
// TokenHash: Basic mints no token, so there is nothing to revoke and nothing to
// record a last_used_at against.
func (r *Repository) AuthenticateBasic(ctx context.Context, loginID, secret string) (auth.APIPrincipal, bool) {
	loginID = strings.TrimSpace(loginID)
	if loginID == "" || secret == "" {
		return auth.APIPrincipal{}, false
	}
	key := basicCacheKey(loginID, secret)
	now := r.now()
	if principal, ok := r.basic.get(key, now); ok {
		return principal, true
	}

	row := r.pool.QueryRowContext(ctx,
		`SELECT id, name, login_id, description, secret_hash, active,
		        token_ttl_seconds, no_expiry, created_at, updated_at
		 FROM api_clients WHERE login_id = ?`, loginID)
	c, err := scanClient(row)
	if err != nil {
		// A missing row and a broken pool are both "not authenticated" here; the
		// caller has no business learning which.
		if !errors.Is(err, sql.ErrNoRows) {
			return auth.APIPrincipal{}, false
		}
		return auth.APIPrincipal{}, false
	}
	if !c.Active {
		return auth.APIPrincipal{}, false
	}
	if bcrypt.CompareHashAndPassword([]byte(c.SecretHash), []byte(secret)) != nil {
		return auth.APIPrincipal{}, false
	}
	principal := auth.APIPrincipal{ClientID: c.ID, LoginID: c.LoginID}
	r.basic.put(key, principal, now)
	return principal, true
}
