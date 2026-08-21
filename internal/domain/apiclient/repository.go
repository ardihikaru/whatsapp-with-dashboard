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
)

type Repository struct {
	pool *sql.DB
	now  func() time.Time
}

func NewRepository(pool *sql.DB) *Repository {
	return &Repository{pool: pool, now: func() time.Time { return time.Now().UTC() }}
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
		NoExpiry: settings.NoExpiry, CreatedAt: ts, UpdatedAt: ts,
	}
	_, err = r.pool.ExecContext(ctx,
		`INSERT INTO api_clients
		 (id, name, login_id, description, secret_hash, active, token_ttl_seconds,
		  no_expiry, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?, ?)`,
		c.ID, c.Name, c.LoginID, c.Description, c.SecretHash, nullableInt(c.TokenTTLSeconds),
		boolToInt(c.NoExpiry), ts.Format(timeLayout), ts.Format(timeLayout))
	if err != nil {
		return CreatedClient{}, err
	}
	return CreatedClient{Client: c, Secret: secret}, nil
}

func (r *Repository) List(ctx context.Context) ([]Client, error) {
	rows, err := r.pool.QueryContext(ctx,
		`SELECT id, name, login_id, description, secret_hash, active,
		        token_ttl_seconds, no_expiry, created_at, updated_at
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
		`SELECT id, name, login_id, description, secret_hash, active,
		        token_ttl_seconds, no_expiry, created_at, updated_at
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
		     no_expiry = ?, updated_at = ?
		 WHERE id = ?`,
		name, loginID, description, nullableInt(settings.TTLSeconds), boolToInt(settings.NoExpiry),
		r.now().Format(timeLayout), id)
	if err != nil {
		return err
	}
	return requireAffected(res)
}

func (r *Repository) SetActive(ctx context.Context, id string, active bool) error {
	res, err := r.pool.ExecContext(ctx,
		`UPDATE api_clients SET active = ?, updated_at = ? WHERE id = ?`,
		boolToInt(active), r.now().Format(timeLayout), id)
	if err != nil {
		return err
	}
	return requireAffected(res)
}

func (r *Repository) Delete(ctx context.Context, id string) error {
	res, err := r.pool.ExecContext(ctx, `DELETE FROM api_clients WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return requireAffected(res)
}
