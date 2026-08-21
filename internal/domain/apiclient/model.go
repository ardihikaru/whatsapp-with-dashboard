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
	CreatedAt       time.Time
	UpdatedAt       time.Time
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

type TokenSettings struct {
	TTLSeconds *int
	NoExpiry   bool
}

type CreatedClient struct {
	Client Client
	Secret string
}

type IssuedToken struct {
	Token     string
	ExpiresAt *time.Time
}
