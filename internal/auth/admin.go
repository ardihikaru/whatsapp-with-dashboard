package auth

import (
	"crypto/subtle"
	"errors"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// ErrInvalidCredentials is the ONLY failure a login may surface. "No such user"
// and "wrong password" deliberately share one error: telling them apart turns
// the login form into a username oracle.
var ErrInvalidCredentials = errors.New("auth: invalid username or password")

// dummyHash is a real bcrypt digest of a value nobody holds. When the username
// does not match we still run a full bcrypt compare against it, so a wrong
// username and a wrong password cost the same wall clock. Without it the
// handler answers a bad username in microseconds and a bad password in ~80ms,
// which is a remotely measurable "this account exists".
var dummyHash = []byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy")

// AdminCredential is the ONE local operator account guarding the browser
// surface, resolved from the environment by config.Load.
//
// This service was an OIDC relying party of a separate auth-service upstream;
// standalone there is no such service, so a single env-supplied admin replaces
// it. One account and no user table is the honest shape here: this is an admin
// console for a service that owns exactly one WhatsApp identity.
//
// The zero value authenticates nobody (nil hash → always ErrInvalidCredentials),
// so a Deps assembled without it in a test is a closed door, not an open one.
//
// The MACHINE api — api_clients + bearer tokens at POST /api/v1/auth/login — is
// a different door entirely and shares nothing with this type.
type AdminCredential struct {
	Username     string
	PasswordHash []byte // bcrypt; never the plaintext
}

// Verify returns nil only when BOTH the username and the password match.
func (c AdminCredential) Verify(username, password string) error {
	// ConstantTimeCompare returns 0 on a length mismatch, so this leaks the
	// LENGTH of the configured username. That is not a secret — it defaults to
	// "admin" and is documented in the README — so resist "fixing" it into
	// something slower and no safer.
	userOK := subtle.ConstantTimeCompare(
		[]byte(strings.TrimSpace(username)), []byte(c.Username)) == 1

	hash := c.PasswordHash
	if !userOK || len(hash) == 0 {
		hash = dummyHash
	}
	pwOK := bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil

	if !userOK || !pwOK || len(c.PasswordHash) == 0 {
		return ErrInvalidCredentials
	}
	return nil
}
