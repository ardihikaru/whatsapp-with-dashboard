package wasession

import (
	"context"
	"database/sql"
	"time"
)

// Repository is the persistence seam the wa.Manager writes through. It is an
// interface so lifecycle tests can stub persistence without SQLite.
type Repository interface {
	All(ctx context.Context) ([]Session, error)
	Insert(ctx context.Context, s *Session) error
	Delete(ctx context.Context, id string) error
	UpdateStatus(ctx context.Context, id, status string) error
	SetActive(ctx context.Context, id string, active bool) error
	MarkSelected(ctx context.Context, id string, at time.Time) error
	// SetPaired records a successful pairing: jid, active=1, last_status=connected.
	SetPaired(ctx context.Context, id, jid string) error
}

// SQLRepository implements Repository on the shared SQLite pool.
type SQLRepository struct{ pool *sql.DB }

func NewRepository(pool *sql.DB) *SQLRepository { return &SQLRepository{pool: pool} }

const timeLayout = time.RFC3339

func now() string { return time.Now().UTC().Format(timeLayout) }

func (r *SQLRepository) All(ctx context.Context) ([]Session, error) {
	rows, err := r.pool.QueryContext(ctx,
		`SELECT id, name, phone, jid, active, last_status, created_at, updated_at, last_selected_at
		 FROM wa_sessions ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		var active int
		var created, updated string
		var selected sql.NullString
		if err := rows.Scan(&s.ID, &s.Name, &s.Phone, &s.JID, &active,
			&s.LastStatus, &created, &updated, &selected); err != nil {
			return nil, err
		}
		s.Active = active != 0
		s.CreatedAt, _ = time.Parse(timeLayout, created)
		s.UpdatedAt, _ = time.Parse(timeLayout, updated)
		if selected.Valid {
			if t, err := time.Parse(timeLayout, selected.String); err == nil {
				s.LastSelectedAt = &t
			}
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *SQLRepository) Insert(ctx context.Context, s *Session) error {
	ts := now()
	_, err := r.pool.ExecContext(ctx,
		`INSERT INTO wa_sessions (id, name, phone, jid, active, last_status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.Name, s.Phone, s.JID, boolToInt(s.Active), s.LastStatus, ts, ts)
	return err
}

func (r *SQLRepository) Delete(ctx context.Context, id string) error {
	_, err := r.pool.ExecContext(ctx, `DELETE FROM wa_sessions WHERE id = ?`, id)
	return err
}

func (r *SQLRepository) UpdateStatus(ctx context.Context, id, status string) error {
	_, err := r.pool.ExecContext(ctx,
		`UPDATE wa_sessions SET last_status = ?, updated_at = ? WHERE id = ?`,
		status, now(), id)
	return err
}

func (r *SQLRepository) SetActive(ctx context.Context, id string, active bool) error {
	_, err := r.pool.ExecContext(ctx,
		`UPDATE wa_sessions SET active = ?, updated_at = ? WHERE id = ?`,
		boolToInt(active), now(), id)
	return err
}

func (r *SQLRepository) MarkSelected(ctx context.Context, id string, at time.Time) error {
	_, err := r.pool.ExecContext(ctx,
		`UPDATE wa_sessions SET last_selected_at = ?, updated_at = ? WHERE id = ?`,
		at.UTC().Format(timeLayout), now(), id)
	return err
}

func (r *SQLRepository) SetPaired(ctx context.Context, id, jid string) error {
	_, err := r.pool.ExecContext(ctx,
		`UPDATE wa_sessions SET jid = ?, active = 1, last_status = 'connected', updated_at = ?
		 WHERE id = ?`,
		jid, now(), id)
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
