package knowngroup

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

// Repository is the known-groups persistence seam.
type Repository struct {
	pool *sql.DB
	// now is injected so tests get a deterministic clock, the same shape as
	// internal/domain/convo.Repository.now.
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

const groupCols = `id, session_id, group_jid, name, created_at, updated_at`

func scanGroup(s scanner) (Group, error) {
	var g Group
	var created, updated string
	if err := s.Scan(&g.ID, &g.SessionID, &g.GroupJID, &g.Name, &created, &updated); err != nil {
		return Group{}, err
	}
	g.CreatedAt, _ = time.Parse(timeLayout, created)
	g.UpdatedAt, _ = time.Parse(timeLayout, updated)
	return g, nil
}

// isDuplicateErr reports whether err is a unique-constraint violation on
// either backend this app runs on — SQLite ("UNIQUE constraint failed") or
// MySQL ("Error 1062: Duplicate entry"). Matching on the message text,
// rather than importing the MySQL driver's typed error here, keeps this
// package backend-agnostic the same way the rest of internal/domain already
// is (raw database/sql, no driver-specific import).
func isDuplicateErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") || strings.Contains(msg, "Duplicate entry")
}

// Insert fills CreatedAt/UpdatedAt from the injected clock when zero.
// Returns ErrDuplicate when (session_id, group_jid) already has a row
// (migrations/0010's UNIQUE constraint).
func (r *Repository) Insert(ctx context.Context, g *Group) error {
	if g.CreatedAt.IsZero() {
		g.CreatedAt = r.now()
	}
	if g.UpdatedAt.IsZero() {
		g.UpdatedAt = g.CreatedAt
	}
	_, err := r.pool.ExecContext(ctx,
		`INSERT INTO wa_known_groups (`+groupCols+`) VALUES (?, ?, ?, ?, ?, ?)`,
		g.ID, g.SessionID, g.GroupJID, g.Name, g.CreatedAt.Format(timeLayout), g.UpdatedAt.Format(timeLayout))
	if isDuplicateErr(err) {
		return ErrDuplicate
	}
	return err
}

// ListBySession returns every known group of sessionID, newest first.
func (r *Repository) ListBySession(ctx context.Context, sessionID string) ([]Group, error) {
	rows, err := r.pool.QueryContext(ctx,
		`SELECT `+groupCols+` FROM wa_known_groups WHERE session_id = ? ORDER BY created_at DESC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Group
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// List returns every known group across every session, newest first — the
// group-broadcast schedule form's picker source, which needs to resolve a
// known_group_id without already knowing which session it belongs to.
func (r *Repository) List(ctx context.Context) ([]Group, error) {
	rows, err := r.pool.QueryContext(ctx, `SELECT `+groupCols+` FROM wa_known_groups ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Group
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (r *Repository) Get(ctx context.Context, id string) (Group, error) {
	row := r.pool.QueryRowContext(ctx, `SELECT `+groupCols+` FROM wa_known_groups WHERE id = ?`, id)
	g, err := scanGroup(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Group{}, ErrNotFound
	}
	return g, err
}

func (r *Repository) Delete(ctx context.Context, id string) error {
	res, err := r.pool.ExecContext(ctx, `DELETE FROM wa_known_groups WHERE id = ?`, id)
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
