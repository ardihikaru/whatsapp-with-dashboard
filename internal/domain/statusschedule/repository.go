package statusschedule

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// timeLayout matches every other table in this schema: RFC3339 UTC strings.
const timeLayout = time.RFC3339

// Repository is the status-schedule persistence seam.
type Repository struct {
	pool *sql.DB
	now  func() time.Time // injected so tests get a deterministic clock
}

// NewRepository wraps pool.
func NewRepository(pool *sql.DB) *Repository {
	return &Repository{pool: pool, now: func() time.Time { return time.Now().UTC() }}
}

const cols = `id, name, session_id, kind, text, bg_argb, caption,
  media_object_key, media_content_type, media_filename,
  weekdays, window_start, window_end, timezone, start_date, end_date,
  recipients_mode, recipients_json,
  state, paused_reason, last_planned_date, next_fire_at, last_fired_at,
  last_result, last_error, created_at, updated_at`

type scanner interface{ Scan(dest ...any) error }

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(timeLayout)
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, _ := time.Parse(timeLayout, s)
	return t
}

func scan(s scanner) (Schedule, error) {
	var sc Schedule
	var bg int64
	var end, planned sql.NullString
	var next, fired, created, updated string
	err := s.Scan(&sc.ID, &sc.Name, &sc.SessionID, &sc.Kind, &sc.Text, &bg, &sc.Caption,
		&sc.MediaObjectKey, &sc.MediaContentType, &sc.MediaFilename,
		&sc.Weekdays, &sc.WindowStart, &sc.WindowEnd, &sc.Timezone, &sc.StartDate, &end,
		&sc.RecipientsMode, &sc.RecipientsJSON,
		&sc.State, &sc.PausedReason, &planned, &next, &fired,
		&sc.LastResult, &sc.LastError, &created, &updated)
	if err != nil {
		return Schedule{}, err
	}
	sc.BgARGB = uint32(bg)
	sc.EndDate, sc.LastPlannedDate = end.String, planned.String
	sc.NextFireAt, sc.LastFiredAt = parseTime(next), parseTime(fired)
	sc.CreatedAt, sc.UpdatedAt = parseTime(created), parseTime(updated)
	return sc, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Insert stores sc, stamping CreatedAt/UpdatedAt when zero.
func (r *Repository) Insert(ctx context.Context, sc *Schedule) error {
	if sc.CreatedAt.IsZero() {
		sc.CreatedAt = r.now()
	}
	if sc.UpdatedAt.IsZero() {
		sc.UpdatedAt = sc.CreatedAt
	}
	_, err := r.pool.ExecContext(ctx,
		`INSERT INTO wa_status_schedules (`+cols+`) VALUES (?,?,?,?,?,?,?, ?,?,?, ?,?,?,?,?,?, ?,?, ?,?,?,?,?, ?,?,?,?)`,
		sc.ID, sc.Name, sc.SessionID, sc.Kind, sc.Text, int64(sc.BgARGB), sc.Caption,
		sc.MediaObjectKey, sc.MediaContentType, sc.MediaFilename,
		sc.Weekdays, sc.WindowStart, sc.WindowEnd, sc.Timezone, sc.StartDate, nullable(sc.EndDate),
		sc.RecipientsMode, sc.RecipientsJSON,
		sc.State, sc.PausedReason, nullable(sc.LastPlannedDate), fmtTime(sc.NextFireAt), fmtTime(sc.LastFiredAt),
		sc.LastResult, sc.LastError, fmtTime(sc.CreatedAt), fmtTime(sc.UpdatedAt))
	return err
}

// Get returns one schedule, or ErrNotFound.
func (r *Repository) Get(ctx context.Context, id string) (Schedule, error) {
	sc, err := scan(r.pool.QueryRowContext(ctx, `SELECT `+cols+` FROM wa_status_schedules WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Schedule{}, ErrNotFound
	}
	return sc, err
}

func (r *Repository) query(ctx context.Context, where string, args ...any) ([]Schedule, error) {
	rows, err := r.pool.QueryContext(ctx, `SELECT `+cols+` FROM wa_status_schedules `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		sc, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// List returns up to limit schedules, newest first.
func (r *Repository) List(ctx context.Context, limit int) ([]Schedule, error) {
	return r.query(ctx, `ORDER BY created_at DESC, id LIMIT ?`, limit)
}

// ListActive returns every active schedule (the planner's working set).
func (r *Repository) ListActive(ctx context.Context) ([]Schedule, error) {
	return r.query(ctx, `WHERE state = ? ORDER BY created_at, id`, StateActive)
}

// Due returns active schedules whose next_fire_at is set and not after now.
func (r *Repository) Due(ctx context.Context, now time.Time) ([]Schedule, error) {
	return r.query(ctx, `WHERE state = ? AND next_fire_at <> '' AND next_fire_at <= ? ORDER BY next_fire_at, id`,
		StateActive, fmtTime(now))
}

// Update overwrites the settings the edit form owns and RESETS planning
// (last_planned_date, next_fire_at), so an edited window is re-drawn rather
// than leaving a stale instant behind. State, last_fired_at and the last_*
// result are untouched: the planner's already-fired-today guard keys on
// last_fired_at, so an edit cannot cause a second post the same day.
func (r *Repository) Update(ctx context.Context, sc *Schedule) error {
	sc.UpdatedAt = r.now()
	res, err := r.pool.ExecContext(ctx,
		`UPDATE wa_status_schedules SET name=?, session_id=?, kind=?, text=?, bg_argb=?, caption=?,
		   media_object_key=?, media_content_type=?, media_filename=?,
		   weekdays=?, window_start=?, window_end=?, timezone=?, start_date=?, end_date=?,
		   recipients_mode=?, recipients_json=?,
		   last_planned_date=NULL, next_fire_at='', updated_at=? WHERE id=?`,
		sc.Name, sc.SessionID, sc.Kind, sc.Text, int64(sc.BgARGB), sc.Caption,
		sc.MediaObjectKey, sc.MediaContentType, sc.MediaFilename,
		sc.Weekdays, sc.WindowStart, sc.WindowEnd, sc.Timezone, sc.StartDate, nullable(sc.EndDate),
		sc.RecipientsMode, sc.RecipientsJSON, fmtTime(sc.UpdatedAt), sc.ID)
	return affected(res, err)
}

func affected(res sql.Result, err error) error {
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

// Delete removes a schedule.
func (r *Repository) Delete(ctx context.Context, id string) error {
	return affected(r.pool.ExecContext(ctx, `DELETE FROM wa_status_schedules WHERE id = ?`, id))
}

// SetState moves a schedule to state with reason, always clearing any
// pending next_fire_at and last_planned_date: a resumed schedule is
// re-planned from scratch and never fires a stale instant from before the
// pause.
func (r *Repository) SetState(ctx context.Context, id, state, reason string) error {
	return affected(r.pool.ExecContext(ctx,
		`UPDATE wa_status_schedules SET state=?, paused_reason=?, last_planned_date=NULL, next_fire_at='', updated_at=? WHERE id=?`,
		state, reason, fmtTime(r.now()), id))
}

// MarkPlanned records that date has been planned. fireAt zero means "nothing
// to fire today" (the draw was skipped); non-zero is the instant to fire.
func (r *Repository) MarkPlanned(ctx context.Context, id, date string, fireAt time.Time) error {
	return affected(r.pool.ExecContext(ctx,
		`UPDATE wa_status_schedules SET last_planned_date=?, next_fire_at=?, updated_at=? WHERE id=?`,
		date, fmtTime(fireAt), fmtTime(r.now()), id))
}

// ClaimFire atomically clears next_fire_at iff it still equals fireAt. The
// caller posts only when this returns nil, so a status is attempted AT MOST
// once per plan: a crash between claim and post loses the post rather than
// double-posting it (a status that did go out cannot be unsent).
func (r *Repository) ClaimFire(ctx context.Context, id string, fireAt time.Time) error {
	res, err := r.pool.ExecContext(ctx,
		`UPDATE wa_status_schedules SET next_fire_at='', updated_at=? WHERE id=? AND next_fire_at=?`,
		fmtTime(r.now()), id, fmtTime(fireAt))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotClaimable
	}
	return nil
}

// RecordResult stores the outcome of a fire attempt.
func (r *Repository) RecordResult(ctx context.Context, id string, firedAt time.Time, result, errText string) error {
	if len(errText) > 500 {
		errText = errText[:500]
	}
	return affected(r.pool.ExecContext(ctx,
		`UPDATE wa_status_schedules SET last_fired_at=?, last_result=?, last_error=?, updated_at=? WHERE id=?`,
		fmtTime(firedAt), result, errText, fmtTime(r.now()), id))
}
