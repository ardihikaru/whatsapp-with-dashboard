package groupbroadcast

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// timeLayout matches every other table in this schema — RFC3339 UTC
// strings (see migrations/0007_self_convo.sql's header for why).
const timeLayout = time.RFC3339

// Repository is the Group Broadcast Schedule persistence seam.
type Repository struct {
	pool *sql.DB
	// now is injected so tests get a deterministic clock, same shape as
	// convo.Repository.now.
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

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(timeLayout)
}

func parseNullableTime(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t, err := time.Parse(timeLayout, s.String)
	if err != nil {
		return nil
	}
	return &t
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
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

// ---- schedules ----

const scheduleCols = `id, name, sender_session_id, sender_name, sender_phone, known_group_id, group_jid, group_name,
	recurrence, weekdays, interval_days,
	sender_window_start, sender_window_end, receiver_window_start, receiver_window_end, late_cutoff,
	lang_id_weight, timezone, start_date, end_date, state, paused_reason, paused_at, last_planned_date,
	created_at, updated_at`

func scanSchedule(s scanner) (Schedule, error) {
	var sc Schedule
	var knownGroupID, endDate, pausedAt, lastPlannedDate sql.NullString
	var created, updated string
	if err := s.Scan(&sc.ID, &sc.Name, &sc.SenderSessionID, &sc.SenderName, &sc.SenderPhone,
		&knownGroupID, &sc.GroupJID, &sc.GroupName,
		&sc.Recurrence, &sc.Weekdays, &sc.IntervalDays,
		&sc.SenderWindowStart, &sc.SenderWindowEnd, &sc.ReceiverWindowStart, &sc.ReceiverWindowEnd, &sc.LateCutoff,
		&sc.LangIDWeight, &sc.Timezone, &sc.StartDate, &endDate, &sc.State, &sc.PausedReason,
		&pausedAt, &lastPlannedDate, &created, &updated); err != nil {
		return Schedule{}, err
	}
	sc.KnownGroupID = knownGroupID.String
	sc.EndDate = endDate.String
	sc.PausedAt = parseNullableTime(pausedAt)
	sc.LastPlannedDate = lastPlannedDate.String
	sc.CreatedAt, _ = time.Parse(timeLayout, created)
	sc.UpdatedAt, _ = time.Parse(timeLayout, updated)
	return sc, nil
}

// InsertSchedule inserts sc and every one of receivers in one transaction —
// mirrors convo.Repository.InsertSchedule exactly.
func (r *Repository) InsertSchedule(ctx context.Context, sc *Schedule, receivers []ScheduleReceiver) error {
	if sc.CreatedAt.IsZero() {
		sc.CreatedAt = r.now()
	}
	if sc.UpdatedAt.IsZero() {
		sc.UpdatedAt = sc.CreatedAt
	}
	tx, err := r.pool.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO wa_group_broadcast_schedules (`+scheduleCols+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sc.ID, sc.Name, sc.SenderSessionID, sc.SenderName, sc.SenderPhone,
		nullIfEmpty(sc.KnownGroupID), sc.GroupJID, sc.GroupName,
		sc.Recurrence, sc.Weekdays, sc.IntervalDays,
		sc.SenderWindowStart, sc.SenderWindowEnd, sc.ReceiverWindowStart, sc.ReceiverWindowEnd, sc.LateCutoff,
		sc.LangIDWeight, sc.Timezone, sc.StartDate, nullIfEmpty(sc.EndDate), sc.State, sc.PausedReason,
		nullableTime(sc.PausedAt), nullIfEmpty(sc.LastPlannedDate),
		sc.CreatedAt.Format(timeLayout), sc.UpdatedAt.Format(timeLayout)); err != nil {
		return err
	}

	if err := insertReceivers(ctx, tx, sc.ID, receivers); err != nil {
		return err
	}

	return tx.Commit()
}

func insertReceivers(ctx context.Context, tx *sql.Tx, scheduleID string, receivers []ScheduleReceiver) error {
	for _, rec := range receivers {
		rec.ScheduleID = scheduleID
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO wa_group_broadcast_receivers (schedule_id, session_id, session_name, phone, position)
			 VALUES (?, ?, ?, ?, ?)`,
			rec.ScheduleID, rec.SessionID, rec.SessionName, rec.Phone, rec.Position); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) GetSchedule(ctx context.Context, id string) (Schedule, error) {
	row := r.pool.QueryRowContext(ctx, `SELECT `+scheduleCols+` FROM wa_group_broadcast_schedules WHERE id = ?`, id)
	sc, err := scanSchedule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Schedule{}, ErrNotFound
	}
	return sc, err
}

const scheduleReceiverCols = `schedule_id, session_id, session_name, phone, position`

func scanScheduleReceiver(s scanner) (ScheduleReceiver, error) {
	var rec ScheduleReceiver
	if err := s.Scan(&rec.ScheduleID, &rec.SessionID, &rec.SessionName, &rec.Phone, &rec.Position); err != nil {
		return ScheduleReceiver{}, err
	}
	return rec, nil
}

// ScheduleReceivers returns every receiver of scheduleID, ordered by
// Position.
func (r *Repository) ScheduleReceivers(ctx context.Context, scheduleID string) ([]ScheduleReceiver, error) {
	rows, err := r.pool.QueryContext(ctx,
		`SELECT `+scheduleReceiverCols+` FROM wa_group_broadcast_receivers
		 WHERE schedule_id = ? ORDER BY position`, scheduleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScheduleReceiver
	for rows.Next() {
		rec, err := scanScheduleReceiver(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// ListSchedules returns every schedule, newest first. limit is clamped to
// [1, 200].
func (r *Repository) ListSchedules(ctx context.Context, limit int) ([]Schedule, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := r.pool.QueryContext(ctx,
		`SELECT `+scheduleCols+` FROM wa_group_broadcast_schedules ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		sc, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// UpdateSchedule replaces sc's row and REPLACES its entire receiver set
// (delete-then-insert, in the same transaction as the row update) — mirrors
// convo.Repository.UpdateSchedule exactly. This is what makes the receiver
// list editable after creation: the caller always hands the full desired
// set.
func (r *Repository) UpdateSchedule(ctx context.Context, sc *Schedule, receivers []ScheduleReceiver) error {
	sc.UpdatedAt = r.now()
	tx, err := r.pool.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`UPDATE wa_group_broadcast_schedules SET
		   name = ?, sender_session_id = ?, sender_name = ?, sender_phone = ?,
		   known_group_id = ?, group_jid = ?, group_name = ?,
		   recurrence = ?, weekdays = ?, interval_days = ?,
		   sender_window_start = ?, sender_window_end = ?, receiver_window_start = ?, receiver_window_end = ?,
		   late_cutoff = ?, lang_id_weight = ?, timezone = ?, start_date = ?, end_date = ?,
		   state = ?, paused_reason = ?, paused_at = ?, last_planned_date = ?, updated_at = ?
		 WHERE id = ?`,
		sc.Name, sc.SenderSessionID, sc.SenderName, sc.SenderPhone,
		nullIfEmpty(sc.KnownGroupID), sc.GroupJID, sc.GroupName,
		sc.Recurrence, sc.Weekdays, sc.IntervalDays,
		sc.SenderWindowStart, sc.SenderWindowEnd, sc.ReceiverWindowStart, sc.ReceiverWindowEnd,
		sc.LateCutoff, sc.LangIDWeight, sc.Timezone, sc.StartDate, nullIfEmpty(sc.EndDate),
		sc.State, sc.PausedReason, nullableTime(sc.PausedAt), nullIfEmpty(sc.LastPlannedDate),
		sc.UpdatedAt.Format(timeLayout), sc.ID)
	if err != nil {
		return err
	}
	if err := requireAffected(res); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM wa_group_broadcast_receivers WHERE schedule_id = ?`, sc.ID); err != nil {
		return err
	}
	if err := insertReceivers(ctx, tx, sc.ID, receivers); err != nil {
		return err
	}

	return tx.Commit()
}

// PlannableSchedules returns every schedule the planner should materialise
// an occurrence for on `today` (YYYY-MM-DD) — mirrors
// convo.Repository.PlannableSchedules exactly, including the VARCHAR
// lexical-comparison reasoning.
func (r *Repository) PlannableSchedules(ctx context.Context, today string, limit int) ([]Schedule, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.pool.QueryContext(ctx,
		`SELECT `+scheduleCols+` FROM wa_group_broadcast_schedules
		 WHERE state = ? AND start_date <= ?
		   AND (end_date IS NULL OR end_date >= ?)
		   AND (last_planned_date IS NULL OR last_planned_date < ?)
		 ORDER BY id LIMIT ?`,
		ScheduleActive, today, today, today, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		sc, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// MarkPlanned stamps id's last_planned_date so the planner never
// double-plans date on a restart.
func (r *Repository) MarkPlanned(ctx context.Context, id, date string, at time.Time) error {
	res, err := r.pool.ExecContext(ctx,
		`UPDATE wa_group_broadcast_schedules SET last_planned_date = ?, updated_at = ? WHERE id = ?`,
		date, at.UTC().Format(timeLayout), id)
	if err != nil {
		return err
	}
	return requireAffected(res)
}

// SetScheduleState refuses a transition out of a terminal state — mirrors
// convo.Repository.SetScheduleState exactly.
func (r *Repository) SetScheduleState(ctx context.Context, id, state, reason string, at time.Time) error {
	sc, err := r.GetSchedule(ctx, id)
	if err != nil {
		return err
	}
	if IsScheduleTerminal(sc.State) {
		return ErrScheduleFinished
	}

	var pausedAt any
	if state == SchedulePaused || state == ScheduleStalled {
		pausedAt = at.UTC().Format(timeLayout)
	}

	res, err := r.pool.ExecContext(ctx,
		`UPDATE wa_group_broadcast_schedules SET state = ?, paused_reason = ?, paused_at = ?, updated_at = ? WHERE id = ?`,
		state, truncate(reason, 255), pausedAt, at.UTC().Format(timeLayout), id)
	if err != nil {
		return err
	}
	return requireAffected(res)
}

func (r *Repository) DeleteSchedule(ctx context.Context, id string) error {
	res, err := r.pool.ExecContext(ctx, `DELETE FROM wa_group_broadcast_schedules WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return requireAffected(res)
}

// ---- occurrences ----

const occurrenceCols = `id, schedule_id, occurrence_date, group_jid, state, created_at, finished_at`

func scanOccurrence(s scanner) (Occurrence, error) {
	var o Occurrence
	var created string
	var finishedAt sql.NullString
	if err := s.Scan(&o.ID, &o.ScheduleID, &o.OccurrenceDate, &o.GroupJID, &o.State, &created, &finishedAt); err != nil {
		return Occurrence{}, err
	}
	o.CreatedAt, _ = time.Parse(timeLayout, created)
	o.FinishedAt = parseNullableTime(finishedAt)
	return o, nil
}

// CountOccurrencesForDate is the planner's idempotency check: how many
// occurrences already exist for scheduleID on date, so it never starts a
// second set for a day it already planned — mirrors
// convo.Repository.CountRunsForOccurrence.
func (r *Repository) CountOccurrencesForDate(ctx context.Context, scheduleID, date string) (int, error) {
	var n int
	err := r.pool.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM wa_group_broadcast_occurrences WHERE schedule_id = ? AND occurrence_date = ?`,
		scheduleID, date).Scan(&n)
	return n, err
}

// CreateOccurrence inserts occ and every one of turns in one transaction — a
// turn insert failing rolls the occurrence row back too, mirroring
// convo.Repository.CreateRun.
func (r *Repository) CreateOccurrence(ctx context.Context, occ *Occurrence, turns []Turn) error {
	if occ.CreatedAt.IsZero() {
		occ.CreatedAt = r.now()
	}
	tx, err := r.pool.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO wa_group_broadcast_occurrences (`+occurrenceCols+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		occ.ID, occ.ScheduleID, occ.OccurrenceDate, occ.GroupJID, occ.State,
		occ.CreatedAt.Format(timeLayout), nullableTime(occ.FinishedAt)); err != nil {
		return err
	}

	for _, t := range turns {
		t.OccurrenceID = occ.ID
		if t.State == "" {
			t.State = TurnPending
		}
		if t.CreatedAt.IsZero() {
			t.CreatedAt = occ.CreatedAt
		}
		isLate := 0
		if t.IsLate {
			isLate = 1
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO wa_group_broadcast_turns
			   (id, occurrence_id, role, session_id, session_name, language, is_late, late_reason,
			    body, source, due_at, state, message_id, error, sent_at, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.ID, t.OccurrenceID, t.Role, t.SessionID, t.SessionName, t.Language, isLate, t.LateReason,
			t.Body, t.Source, t.DueAt.UTC().Format(timeLayout), t.State, t.MessageID, t.Error,
			nullableTime(t.SentAt), t.CreatedAt.Format(timeLayout)); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (r *Repository) GetOccurrence(ctx context.Context, id string) (Occurrence, error) {
	row := r.pool.QueryRowContext(ctx, `SELECT `+occurrenceCols+` FROM wa_group_broadcast_occurrences WHERE id = ?`, id)
	o, err := scanOccurrence(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Occurrence{}, ErrNotFound
	}
	return o, err
}

// OccurrencesForSchedule returns every occurrence scheduleID has produced,
// newest first, limit clamped to [1, 200].
func (r *Repository) OccurrencesForSchedule(ctx context.Context, scheduleID string, limit int) ([]Occurrence, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := r.pool.QueryContext(ctx,
		`SELECT `+occurrenceCols+` FROM wa_group_broadcast_occurrences
		 WHERE schedule_id = ? ORDER BY created_at DESC LIMIT ?`, scheduleID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Occurrence
	for rows.Next() {
		o, err := scanOccurrence(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// FinishOccurrenceIfDone stamps occ's terminal state and finished_at, but
// only when occ is not already terminal — the same "don't overwrite a
// concurrent conclusion" guard convo.Repository.FinishRun applies.
func (r *Repository) FinishOccurrenceIfDone(ctx context.Context, occID, state string, at time.Time) error {
	occ, err := r.GetOccurrence(ctx, occID)
	if err != nil {
		return err
	}
	if IsOccurrenceTerminal(occ.State) {
		return nil
	}
	_, err = r.pool.ExecContext(ctx,
		`UPDATE wa_group_broadcast_occurrences SET state = ?, finished_at = ? WHERE id = ?`,
		state, at.UTC().Format(timeLayout), occID)
	return err
}

// ---- turns ----

const turnCols = `id, occurrence_id, role, session_id, session_name, language, is_late, late_reason,
	body, source, due_at, state, message_id, error, sent_at, created_at`

func scanTurn(s scanner) (Turn, error) {
	var t Turn
	var isLate int
	var dueAt, created string
	var sentAt sql.NullString
	if err := s.Scan(&t.ID, &t.OccurrenceID, &t.Role, &t.SessionID, &t.SessionName, &t.Language, &isLate,
		&t.LateReason, &t.Body, &t.Source, &dueAt, &t.State, &t.MessageID, &t.Error, &sentAt, &created); err != nil {
		return Turn{}, err
	}
	t.IsLate = isLate != 0
	t.DueAt, _ = time.Parse(timeLayout, dueAt)
	t.SentAt = parseNullableTime(sentAt)
	t.CreatedAt, _ = time.Parse(timeLayout, created)
	return t, nil
}

// TurnsForOccurrence returns every turn of occurrenceID, sender first then
// receivers in Position/insertion order (created_at, id — turns have no
// seq column the way convo.Turn does, since role+session_id already
// identifies each row uniquely within an occurrence).
func (r *Repository) TurnsForOccurrence(ctx context.Context, occurrenceID string) ([]Turn, error) {
	rows, err := r.pool.QueryContext(ctx,
		`SELECT `+turnCols+` FROM wa_group_broadcast_turns WHERE occurrence_id = ? ORDER BY created_at, id`,
		occurrenceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Turn
	for rows.Next() {
		t, err := scanTurn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DueTurns is the runner's poll query: every pending turn whose due_at has
// arrived, earliest first. limit defaults to 20 when not positive — mirrors
// convo.Repository.DueRuns' own shape, scoped to turns directly (there is no
// separate run-level "due" concept here; a turn IS the unit of work).
func (r *Repository) DueTurns(ctx context.Context, now time.Time, limit int) ([]Turn, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.pool.QueryContext(ctx,
		`SELECT `+turnCols+` FROM wa_group_broadcast_turns
		 WHERE state = ? AND due_at <= ?
		 ORDER BY due_at, id LIMIT ?`,
		TurnPending, now.UTC().Format(timeLayout), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Turn
	for rows.Next() {
		t, err := scanTurn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// PendingTurnsForOccurrence returns every turn of occurrenceID still
// pending — used to skip the rest of an occurrence when its sender turn
// fails a re-check (mirrors selfconvo's "sender failure cancels the
// remaining pending receivers" rule).
func (r *Repository) PendingTurnsForOccurrence(ctx context.Context, occurrenceID string) ([]Turn, error) {
	rows, err := r.pool.QueryContext(ctx,
		`SELECT `+turnCols+` FROM wa_group_broadcast_turns
		 WHERE occurrence_id = ? AND state = ? ORDER BY due_at, id`, occurrenceID, TurnPending)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Turn
	for rows.Next() {
		t, err := scanTurn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SessionTurnTimesForDate returns every OTHER planned due_at for sessionID
// on date (YYYY-MM-DD, matched against due_at's own date prefix), ACROSS
// EVERY group-broadcast schedule — a session could be a receiver in more
// than one. This is the planner's min-same-session-gap check
// (WSM_GROUP_BROADCAST_MIN_SESSION_GAP): after drawing a candidate time for
// sessionID, the planner redraws/nudges away from anything this query
// returns that lands too close. excludeOccurrenceID, when non-empty, omits
// turns belonging to that occurrence — the occurrence currently being
// planned, whose own not-yet-committed draws must not compare against
// themselves via a stale read (the planner instead tracks those in memory
// during one PlanDay pass; this exclusion guards a caller replanning the
// same occurrence id, e.g. a retry).
func (r *Repository) SessionTurnTimesForDate(ctx context.Context, sessionID, date, excludeOccurrenceID string) ([]time.Time, error) {
	rows, err := r.pool.QueryContext(ctx,
		`SELECT due_at FROM wa_group_broadcast_turns
		 WHERE session_id = ? AND substr(due_at, 1, 10) = ? AND state != ?
		   AND occurrence_id != ?`,
		sessionID, date, TurnSkipped, nullIfEmptyExclude(excludeOccurrenceID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []time.Time
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		t, err := time.Parse(timeLayout, s)
		if err != nil {
			continue
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// nullIfEmptyExclude is SessionTurnTimesForDate's own helper: an empty
// excludeOccurrenceID must exclude NOTHING (occurrence_id is never NULL, so
// "!= ”" already excludes nothing that matters, but this makes the intent
// explicit rather than relying on that coincidence).
func nullIfEmptyExclude(id string) string {
	if id == "" {
		return "\x00no-such-occurrence\x00"
	}
	return id
}

// ClaimTurn atomically moves id from pending to sending and stamps the
// caller's messageID — mirrors convo.Repository.ClaimTurn's
// state='pending' guard and ErrTurnNotPending contract exactly.
func (r *Repository) ClaimTurn(ctx context.Context, id, messageID string) error {
	res, err := r.pool.ExecContext(ctx,
		`UPDATE wa_group_broadcast_turns SET state = ?, message_id = ? WHERE id = ? AND state = ?`,
		TurnSending, messageID, id, TurnPending)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrTurnNotPending
	}
	return nil
}

func (r *Repository) MarkTurnSent(ctx context.Context, id string, at time.Time) error {
	res, err := r.pool.ExecContext(ctx,
		`UPDATE wa_group_broadcast_turns SET state = ?, sent_at = ? WHERE id = ?`,
		TurnSent, at.UTC().Format(timeLayout), id)
	if err != nil {
		return err
	}
	return requireAffected(res)
}

func (r *Repository) MarkTurnFailed(ctx context.Context, id, reason string) error {
	res, err := r.pool.ExecContext(ctx,
		`UPDATE wa_group_broadcast_turns SET state = ?, error = ? WHERE id = ?`,
		TurnFailed, truncate(reason, 255), id)
	if err != nil {
		return err
	}
	return requireAffected(res)
}

// MarkTurnSkipped moves id to skipped — used when a receiver disconnects
// between planning and its send time (Tick re-check), or when a sender
// failure cancels the remaining pending receiver turns of an occurrence.
func (r *Repository) MarkTurnSkipped(ctx context.Context, id, reason string) error {
	res, err := r.pool.ExecContext(ctx,
		`UPDATE wa_group_broadcast_turns SET state = ?, error = ? WHERE id = ?`,
		TurnSkipped, truncate(reason, 255), id)
	if err != nil {
		return err
	}
	return requireAffected(res)
}
