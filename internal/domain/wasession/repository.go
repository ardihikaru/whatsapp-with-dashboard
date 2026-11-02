package wasession

import (
	"context"
	"database/sql"
	"sort"
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
	SetMaster(ctx context.Context, id string, master bool) error
	SetGroupCode(ctx context.Context, id, code string) error
	// SetGroupCodes replaces a session's full routing-group set (0015) in
	// one transaction: delete every existing wa_session_groups row for id,
	// insert one per code, and write wa_sessions.group_code = the first
	// (sorted) code, or "" for an empty set — see SetGroupCodes's caller
	// (wa.ManagedSession.SetGroupCodes) for why the legacy column must
	// always agree with this table rather than drift from it.
	SetGroupCodes(ctx context.Context, id string, codes []string) error
	// GroupCodes loads every (session_id -> sorted codes) mapping in one
	// pass — the boot-time hydration path (wa.Manager.StartAll) needs the
	// WHOLE table, not one session at a time.
	GroupCodes(ctx context.Context) (map[string][]string, error)
	SetName(ctx context.Context, id, name string) error
	// SetSelfReminder moves the is_self_reminder flag to id, clearing it
	// from every other row, in ONE statement — see
	// wa.Manager.SetSelfReminder's doc comment for why exclusivity is
	// enforced this way rather than by a DB index. on=false just clears id
	// without touching any other row.
	SetSelfReminder(ctx context.Context, id string, on bool) error
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
		`SELECT id, name, phone, jid, active, is_master, group_code, is_self_reminder, last_status, created_at, updated_at, last_selected_at
		 FROM wa_sessions ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		var active, isMaster, selfReminder int
		var created, updated string
		var selected sql.NullString
		if err := rows.Scan(&s.ID, &s.Name, &s.Phone, &s.JID, &active, &isMaster, &s.GroupCode, &selfReminder,
			&s.LastStatus, &created, &updated, &selected); err != nil {
			return nil, err
		}
		s.Active = active != 0
		s.IsMaster = isMaster != 0
		s.IsSelfReminder = selfReminder != 0
		s.CreatedAt, _ = time.Parse(timeLayout, created)
		s.UpdatedAt, _ = time.Parse(timeLayout, updated)
		if selected.Valid {
			if t, err := time.Parse(timeLayout, selected.String); err == nil {
				s.LastSelectedAt = &t
			}
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	groups, err := r.GroupCodes(ctx)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].GroupCodes = groups[out[i].ID]
	}
	return out, nil
}

// GroupCodes loads every session's routing-group set in one query — see
// the Repository interface doc comment for why.
func (r *SQLRepository) GroupCodes(ctx context.Context) (map[string][]string, error) {
	rows, err := r.pool.QueryContext(ctx, `SELECT session_id, group_code FROM wa_session_groups`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var id, code string
		if err := rows.Scan(&id, &code); err != nil {
			return nil, err
		}
		out[id] = append(out[id], code)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for id := range out {
		sort.Strings(out[id])
	}
	return out, nil
}

func (r *SQLRepository) Insert(ctx context.Context, s *Session) error {
	ts := now()
	_, err := r.pool.ExecContext(ctx,
		`INSERT INTO wa_sessions (id, name, phone, jid, active, is_master, group_code, last_status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.Name, s.Phone, s.JID, boolToInt(s.Active), boolToInt(s.IsMaster), s.GroupCode, s.LastStatus, ts, ts)
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

func (r *SQLRepository) SetMaster(ctx context.Context, id string, master bool) error {
	_, err := r.pool.ExecContext(ctx,
		`UPDATE wa_sessions SET is_master = ?, updated_at = ? WHERE id = ?`,
		boolToInt(master), now(), id)
	return err
}

func (r *SQLRepository) SetGroupCode(ctx context.Context, id, code string) error {
	_, err := r.pool.ExecContext(ctx,
		`UPDATE wa_sessions SET group_code = ?, updated_at = ? WHERE id = ?`,
		code, now(), id)
	return err
}

// SetGroupCodes replaces a session's full routing-group set in one
// transaction (delete then insert then write the cached first-code column)
// — see the Repository interface doc comment for why both halves must
// agree. codes is assumed already de-duplicated/sorted by the caller
// (wa.ManagedSession.SetGroupCodes normalizes and sorts before this is ever
// called); this layer trusts that and does no re-validation of its own, the
// same division of labour SetGroupCode already has with
// wa.NormalizeGroupCode.
func (r *SQLRepository) SetGroupCodes(ctx context.Context, id string, codes []string) error {
	tx, err := r.pool.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM wa_session_groups WHERE session_id = ?`, id); err != nil {
		return err
	}
	ts := now()
	for _, code := range codes {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO wa_session_groups (session_id, group_code, created_at) VALUES (?, ?, ?)`,
			id, code, ts); err != nil {
			return err
		}
	}
	first := ""
	if len(codes) > 0 {
		first = codes[0]
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE wa_sessions SET group_code = ?, updated_at = ? WHERE id = ?`,
		first, ts, id); err != nil {
		return err
	}
	return tx.Commit()
}

// SetSelfReminder moves is_self_reminder to id (or clears it) in one
// statement — see the Repository interface doc comment and
// wa.Manager.SetSelfReminder for why exclusivity lives here as a single
// CASE expression rather than a read-then-write pair.
func (r *SQLRepository) SetSelfReminder(ctx context.Context, id string, on bool) error {
	ts := now()
	if !on {
		// Scoped to id ALONE — a prior version of this statement read
		// `WHERE id = ? OR is_self_reminder = 1`, which cleared whichever
		// OTHER row actually held the flag any time this was called with
		// on=false for an unrelated session (e.g. PostSessionEdit saves
		// self_reminder=false on every edit of every session that does
		// not itself hold it). See
		// TestSetSelfReminderFalseOnlyClearsTheNamedSessionNotWhicheverIsFlagged.
		_, err := r.pool.ExecContext(ctx,
			`UPDATE wa_sessions SET is_self_reminder = 0, updated_at = ? WHERE id = ?`,
			ts, id)
		return err
	}
	_, err := r.pool.ExecContext(ctx,
		`UPDATE wa_sessions SET is_self_reminder = CASE WHEN id = ? THEN 1 ELSE 0 END,
		 updated_at = CASE WHEN id = ? OR is_self_reminder = 1 THEN ? ELSE updated_at END`,
		id, id, ts)
	return err
}

func (r *SQLRepository) SetName(ctx context.Context, id, name string) error {
	_, err := r.pool.ExecContext(ctx,
		`UPDATE wa_sessions SET name = ?, updated_at = ? WHERE id = ?`,
		name, now(), id)
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
