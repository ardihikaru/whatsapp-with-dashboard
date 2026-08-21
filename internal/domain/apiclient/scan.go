package apiclient

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	"time"
)

// Row scanning and the small shared helpers for the apiclient repository.
// scanClient is used by repository.go, token.go AND list_query.go, which is why
// it sits in its own file rather than with either caller.

// clientCols is THE column list every client SELECT must use, in the order
// scanClient reads them. It lives next to the scanner deliberately: the two were
// duplicated across four files, so adding token_expires_at meant editing five
// places and getting a runtime scan mismatch if one was missed.
const clientCols = `id, name, login_id, description, secret_hash, active,
	        token_ttl_seconds, no_expiry, token_expires_at, created_at, updated_at`

func scanClient(scanner interface {
	Scan(dest ...any) error
}) (Client, error) {
	var c Client
	var active, noExpiry int
	var ttl sql.NullInt64
	var expiresAt sql.NullString
	var created, updated string
	if err := scanner.Scan(&c.ID, &c.Name, &c.LoginID, &c.Description, &c.SecretHash,
		&active, &ttl, &noExpiry, &expiresAt, &created, &updated); err != nil {
		return Client{}, err
	}
	c.Active = active != 0
	if ttl.Valid {
		v := int(ttl.Int64)
		c.TokenTTLSeconds = &v
	}
	c.NoExpiry = noExpiry != 0
	if expiresAt.Valid && expiresAt.String != "" {
		if t, err := time.Parse(timeLayout, expiresAt.String); err == nil {
			c.TokenExpiresAt = &t
		}
	}
	c.CreatedAt, _ = time.Parse(timeLayout, created)
	c.UpdatedAt, _ = time.Parse(timeLayout, updated)
	return c, nil
}

func validateSettings(settings TokenSettings) error {
	if settings.NoExpiry {
		return nil
	}
	if settings.ExpiresAt != nil {
		return nil
	}
	if settings.TTLSeconds == nil {
		return fmt.Errorf("token expiry is required")
	}
	if *settings.TTLSeconds <= 0 {
		return fmt.Errorf("token expiry must be positive")
	}
	return nil
}

func nullableInt(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Format(timeLayout)
}

func requireAffected(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
