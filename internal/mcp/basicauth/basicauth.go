// Package basicauth checks the HTTP Basic credentials wsm-mcp's two front
// doors (MCP Streamable HTTP and gRPC) both require. Same shape as
// hikaru-mcp's own internal/basicauth (constant-time compare, refuses to
// start with a short password).
package basicauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"
)

// Checker holds the expected credentials as hashes (constant-time compare,
// no early exit on the username).
type Checker struct{ user, pass [32]byte }

// New refuses empty or short credentials so a misconfigured service never
// runs open.
func New(user, pass string) (*Checker, error) {
	if user == "" || len(pass) < 16 {
		return nil, errors.New("basic auth: user and a password of at least 16 chars are required")
	}
	return &Checker{sha256.Sum256([]byte(user)), sha256.Sum256([]byte(pass))}, nil
}

// Header renders an Authorization header value for clients.
func Header(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

// Check validates an Authorization header value.
func (c *Checker) Check(header string) bool {
	raw, ok := strings.CutPrefix(header, "Basic ")
	if !ok {
		return false
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	u, p, ok := strings.Cut(string(b), ":")
	if !ok {
		return false
	}
	hu, hp := sha256.Sum256([]byte(u)), sha256.Sum256([]byte(p))
	okU := subtle.ConstantTimeCompare(hu[:], c.user[:])
	okP := subtle.ConstantTimeCompare(hp[:], c.pass[:])
	return okU&okP == 1
}
