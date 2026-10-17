package contact

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// timeLayout matches every other table in this schema — RFC3339 UTC
// strings (see migrations/0007_self_convo.sql's header for why).
const timeLayout = time.RFC3339

// Repository is the contact-book persistence seam.
type Repository struct {
	pool *sql.DB
	// now is injected so tests get a deterministic clock, the same shape as
	// internal/domain/knowngroup.Repository.now.
	now func() time.Time
}

// NewRepository wraps pool. now defaults to time.Now().UTC(); tests
// overwrite the field directly (same-package convention, no setter).
func NewRepository(pool *sql.DB) *Repository {
	return &Repository{pool: pool, now: func() time.Time { return time.Now().UTC() }}
}

type scanner interface {
	Scan(dest ...any) error
}

const contactCols = `id, name, phone, aliases, jid, wa_status, verified_at, created_at, updated_at`

func scanContact(s scanner) (Contact, error) {
	var c Contact
	var status, verifiedAt, created, updated string
	if err := s.Scan(&c.ID, &c.Name, &c.Phone, &c.Aliases, &c.JID, &status, &verifiedAt, &created, &updated); err != nil {
		return Contact{}, err
	}
	c.WAStatus = Status(status)
	if verifiedAt != "" {
		c.VerifiedAt, _ = time.Parse(timeLayout, verifiedAt)
	}
	c.CreatedAt, _ = time.Parse(timeLayout, created)
	c.UpdatedAt, _ = time.Parse(timeLayout, updated)
	return c, nil
}

// isDuplicateErr reports whether err is a unique-constraint violation on
// either backend this app runs on — SQLite ("UNIQUE constraint failed") or
// MySQL ("Error 1062: Duplicate entry") — the same idea
// internal/domain/knowngroup's own isDuplicateErr uses.
func isDuplicateErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") || strings.Contains(msg, "Duplicate entry")
}

// Insert fills CreatedAt/UpdatedAt from the injected clock when zero, and
// defaults WAStatus to StatusUnknown when empty. Returns ErrDuplicate when
// phone already has a row (migrations/0012's UNIQUE constraint).
func (r *Repository) Insert(ctx context.Context, c *Contact) error {
	if c.WAStatus == "" {
		c.WAStatus = StatusUnknown
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = r.now()
	}
	if c.UpdatedAt.IsZero() {
		c.UpdatedAt = c.CreatedAt
	}
	var verifiedAt string
	if !c.VerifiedAt.IsZero() {
		verifiedAt = c.VerifiedAt.Format(timeLayout)
	}
	_, err := r.pool.ExecContext(ctx,
		`INSERT INTO wa_contacts (`+contactCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Name, c.Phone, c.Aliases, c.JID, string(c.WAStatus), verifiedAt,
		c.CreatedAt.Format(timeLayout), c.UpdatedAt.Format(timeLayout))
	if isDuplicateErr(err) {
		return ErrDuplicate
	}
	return err
}

// List returns every contact, newest first.
func (r *Repository) List(ctx context.Context) ([]Contact, error) {
	rows, err := r.pool.QueryContext(ctx, `SELECT `+contactCols+` FROM wa_contacts ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Contact
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Get returns one contact by id, or ErrNotFound.
func (r *Repository) Get(ctx context.Context, id string) (Contact, error) {
	row := r.pool.QueryRowContext(ctx, `SELECT `+contactCols+` FROM wa_contacts WHERE id = ?`, id)
	c, err := scanContact(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Contact{}, ErrNotFound
	}
	return c, err
}

// Delete removes a contact by id. Returns ErrNotFound when no row matched.
func (r *Repository) Delete(ctx context.Context, id string) error {
	res, err := r.pool.ExecContext(ctx, `DELETE FROM wa_contacts WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Update overwrites name/aliases for an existing contact, stamping
// UpdatedAt from the injected clock. Returns ErrNotFound when no row
// matched.
func (r *Repository) Update(ctx context.Context, id, name, aliases string) error {
	res, err := r.pool.ExecContext(ctx,
		`UPDATE wa_contacts SET name = ?, aliases = ?, updated_at = ? WHERE id = ?`,
		name, aliases, r.now().Format(timeLayout), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetVerification records a verification outcome: jid (empty unless
// registered), status, and the verification timestamp (defaults to the
// injected clock when at is zero). Returns ErrNotFound when no row
// matched.
func (r *Repository) SetVerification(ctx context.Context, id, jid string, status Status, at time.Time) error {
	if at.IsZero() {
		at = r.now()
	}
	res, err := r.pool.ExecContext(ctx,
		`UPDATE wa_contacts SET jid = ?, wa_status = ?, verified_at = ?, updated_at = ? WHERE id = ?`,
		jid, string(status), at.Format(timeLayout), r.now().Format(timeLayout), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
