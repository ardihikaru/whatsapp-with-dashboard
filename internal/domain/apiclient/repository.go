package apiclient

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

const timeLayout = time.RFC3339

var (
	ErrInvalidCredentials = errors.New("invalid login id or secret")
	ErrInactiveClient     = errors.New("api client is inactive")
	ErrNotFound           = errors.New("api client not found")
	// ErrExpiredClient means the client's absolute token cutoff
	// (token_expires_at) is in the past, so no new token can be issued.
	ErrExpiredClient = errors.New("api client access period has ended")
)

type Repository struct {
	pool *sql.DB
	now  func() time.Time
	// basic memoises verified HTTP Basic credentials so the bcrypt compare does
	// not run on every request. See basic.go.
	basic *basicCache
}

func NewRepository(pool *sql.DB) *Repository {
	return &Repository{
		pool:  pool,
		now:   func() time.Time { return time.Now().UTC() },
		basic: newBasicCache(basicCacheTTL),
	}
}

func (r *Repository) Create(ctx context.Context, name, loginID, description string, settings TokenSettings) (CreatedClient, error) {
	name = strings.TrimSpace(name)
	loginID = strings.TrimSpace(loginID)
	description = strings.TrimSpace(description)
	if name == "" {
		return CreatedClient{}, fmt.Errorf("name is required")
	}
	if loginID == "" {
		return CreatedClient{}, fmt.Errorf("login id is required")
	}
	if err := validateSettings(settings); err != nil {
		return CreatedClient{}, err
	}
	secret, err := randomToken(32)
	if err != nil {
		return CreatedClient{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.DefaultCost)
	if err != nil {
		return CreatedClient{}, err
	}
	ts := r.now()
	c := Client{
		ID: wa.NewID(), Name: name, LoginID: loginID, Description: description,
		SecretHash: string(hash), Active: true, TokenTTLSeconds: settings.TTLSeconds,
		NoExpiry: settings.NoExpiry, TokenExpiresAt: settings.ExpiresAt,
		CreatedAt: ts, UpdatedAt: ts,
	}
	_, err = r.pool.ExecContext(ctx,
		`INSERT INTO api_clients
		 (id, name, login_id, description, secret_hash, active, token_ttl_seconds,
		  no_expiry, token_expires_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?)`,
		c.ID, c.Name, c.LoginID, c.Description, c.SecretHash, nullableInt(c.TokenTTLSeconds),
		boolToInt(c.NoExpiry), nullableTime(c.TokenExpiresAt),
		ts.Format(timeLayout), ts.Format(timeLayout))
	if err != nil {
		return CreatedClient{}, err
	}
	return CreatedClient{Client: c, Secret: secret}, nil
}

func (r *Repository) List(ctx context.Context) ([]Client, error) {
	rows, err := r.pool.QueryContext(ctx,
		`SELECT `+clientCols+`
		 FROM api_clients ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Client
	for rows.Next() {
		c, err := scanClient(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *Repository) Get(ctx context.Context, id string) (Client, error) {
	row := r.pool.QueryRowContext(ctx,
		`SELECT `+clientCols+`
		 FROM api_clients WHERE id = ?`, id)
	c, err := scanClient(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Client{}, ErrNotFound
	}
	return c, err
}

func (r *Repository) Update(ctx context.Context, id, name, loginID, description string, settings TokenSettings) error {
	name = strings.TrimSpace(name)
	loginID = strings.TrimSpace(loginID)
	description = strings.TrimSpace(description)
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if loginID == "" {
		return fmt.Errorf("login id is required")
	}
	if err := validateSettings(settings); err != nil {
		return err
	}
	res, err := r.pool.ExecContext(ctx,
		`UPDATE api_clients
		 SET name = ?, login_id = ?, description = ?, token_ttl_seconds = ?,
		     no_expiry = ?, token_expires_at = ?, updated_at = ?
		 WHERE id = ?`,
		name, loginID, description, nullableInt(settings.TTLSeconds), boolToInt(settings.NoExpiry),
		nullableTime(settings.ExpiresAt), r.now().Format(timeLayout), id)
	if err != nil {
		return err
	}
	// The login id may have just changed, so a cached (login_id, secret) pair
	// could now name the wrong client.
	r.basic.invalidateClient(id)
	return requireAffected(res)
}

func (r *Repository) SetActive(ctx context.Context, id string, active bool) error {
	res, err := r.pool.ExecContext(ctx,
		`UPDATE api_clients SET active = ?, updated_at = ? WHERE id = ?`,
		boolToInt(active), r.now().Format(timeLayout), id)
	if err != nil {
		return err
	}
	// Deactivation must bite NOW, not when the cache entry happens to expire.
	r.basic.invalidateClient(id)
	return requireAffected(res)
}

func (r *Repository) Delete(ctx context.Context, id string) error {
	res, err := r.pool.ExecContext(ctx, `DELETE FROM api_clients WHERE id = ?`, id)
	if err != nil {
		return err
	}
	r.basic.invalidateClient(id)
	return requireAffected(res)
}
