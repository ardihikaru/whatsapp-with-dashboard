package apiclient

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/auth"
)

// Bearer-token half of the apiclient repository. Split from repository.go, which
// keeps the client-entity CRUD (scripts/loc-check.sh, 200-line rule) — the same
// concern-named convention list_query.go already follows.
//
// RotateSecret lives HERE rather than with the other client mutations because its
// transaction also revokes every live token for that client: rotating the secret
// without killing the tokens it minted would leave the old credential usable.

func (r *Repository) RotateSecret(ctx context.Context, id string) (string, error) {
	secret, err := randomToken(32)
	if err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	tx, err := r.pool.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`UPDATE api_clients SET secret_hash = ?, updated_at = ? WHERE id = ?`,
		string(hash), r.now().Format(timeLayout), id)
	if err != nil {
		return "", err
	}
	if err := requireAffected(res); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE api_access_tokens SET revoked_at = ?
		 WHERE client_id = ? AND revoked_at IS NULL`,
		r.now().Format(timeLayout), id); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return secret, nil
}

func (r *Repository) Login(ctx context.Context, loginID, secret string) (IssuedToken, error) {
	loginID = strings.TrimSpace(loginID)
	if loginID == "" || secret == "" {
		return IssuedToken{}, ErrInvalidCredentials
	}
	row := r.pool.QueryRowContext(ctx,
		`SELECT id, name, login_id, description, secret_hash, active,
		        token_ttl_seconds, no_expiry, created_at, updated_at
		 FROM api_clients WHERE login_id = ?`, loginID)
	c, err := scanClient(row)
	if errors.Is(err, sql.ErrNoRows) {
		return IssuedToken{}, ErrInvalidCredentials
	}
	if err != nil {
		return IssuedToken{}, err
	}
	if !c.Active {
		return IssuedToken{}, ErrInactiveClient
	}
	if bcrypt.CompareHashAndPassword([]byte(c.SecretHash), []byte(secret)) != nil {
		return IssuedToken{}, ErrInvalidCredentials
	}
	token, err := randomToken(32)
	if err != nil {
		return IssuedToken{}, err
	}
	now := r.now()
	var expires *time.Time
	if !c.NoExpiry {
		ttl := 24 * time.Hour
		if c.TokenTTLSeconds != nil {
			ttl = time.Duration(*c.TokenTTLSeconds) * time.Second
		}
		t := now.Add(ttl)
		expires = &t
	}
	_, err = r.pool.ExecContext(ctx,
		`INSERT INTO api_access_tokens
		 (token_hash, client_id, issued_at, expires_at)
		 VALUES (?, ?, ?, ?)`,
		HashToken(token), c.ID, now.Format(timeLayout), nullableTime(expires))
	if err != nil {
		return IssuedToken{}, err
	}
	return IssuedToken{Token: token, ExpiresAt: expires}, nil
}

func (r *Repository) AuthenticateBearer(ctx context.Context, token string) (auth.APIPrincipal, bool) {
	hash := HashToken(strings.TrimSpace(token))
	row := r.pool.QueryRowContext(ctx,
		`SELECT t.token_hash, t.client_id, c.login_id, t.expires_at, t.revoked_at, c.active
		 FROM api_access_tokens t
		 JOIN api_clients c ON c.id = t.client_id
		 WHERE t.token_hash = ?`, hash)
	var principal auth.APIPrincipal
	var expires, revoked sql.NullString
	var active int
	if err := row.Scan(&principal.TokenHash, &principal.ClientID, &principal.LoginID,
		&expires, &revoked, &active); err != nil {
		return auth.APIPrincipal{}, false
	}
	now := r.now()
	if active == 0 || revoked.Valid {
		return auth.APIPrincipal{}, false
	}
	if expires.Valid {
		t, err := time.Parse(timeLayout, expires.String)
		if err != nil || !now.Before(t) {
			return auth.APIPrincipal{}, false
		}
	}
	_, _ = r.pool.ExecContext(ctx,
		`UPDATE api_access_tokens SET last_used_at = ? WHERE token_hash = ?`,
		now.Format(timeLayout), hash)
	return principal, true
}

func (r *Repository) RevokeToken(ctx context.Context, tokenHash string) error {
	_, err := r.pool.ExecContext(ctx,
		`UPDATE api_access_tokens SET revoked_at = ?
		 WHERE token_hash = ? AND revoked_at IS NULL`,
		r.now().Format(timeLayout), tokenHash)
	return err
}

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
