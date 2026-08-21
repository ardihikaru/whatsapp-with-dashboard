// Package apiclient stores machine API credentials and issued access tokens.
package apiclient

import "time"

// Client is one API credential set. SecretHash is never displayed.
type Client struct {
	ID              string
	Name            string
	LoginID         string
	Description     string
	SecretHash      string
	Active          bool
	TokenTTLSeconds *int
	NoExpiry        bool
	// TokenExpiresAt is an ABSOLUTE cutoff. When set it wins over
	// TokenTTLSeconds: tokens expire exactly then rather than TTL after issue,
	// which is what "berlaku sampai tanggal X" actually means.
	TokenExpiresAt *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Token is one bearer token issued to an API client. TokenHash is persisted;
// the raw token is only returned once when login succeeds.
type Token struct {
	TokenHash  string
	ClientID   string
	IssuedAt   time.Time
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
	LastUsedAt *time.Time
}

// TokenSettings is the write-side view of a client's expiry policy. Exactly one
// of the three fields decides, in this order: NoExpiry, ExpiresAt, TTLSeconds.
type TokenSettings struct {
	TTLSeconds *int
	NoExpiry   bool
	ExpiresAt  *time.Time
}

type CreatedClient struct {
	Client Client
	Secret string
}

type IssuedToken struct {
	Token     string
	ExpiresAt *time.Time
}
