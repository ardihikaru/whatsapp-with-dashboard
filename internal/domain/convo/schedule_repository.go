package convo

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const scheduleCols = `id, name, sender_session_id, sender_name, sender_phone, topic,
	root_template_id, chain_enabled, recurrence, weekdays, interval_days, sets_per_day,
	window_start, window_end, timezone, start_date, end_date, state, paused_reason,
	paused_at, last_planned_date, created_at, updated_at`

func scanSchedule(s scanner) (Schedule, error) {
	var sc Schedule
	var rootTemplateID, endDate, pausedAt, lastPlannedDate sql.NullString
	var chainEnabled int
	var created, updated string
	if err := s.Scan(&sc.ID, &sc.Name, &sc.SenderSessionID, &sc.SenderName, &sc.SenderPhone, &sc.Topic,
		&rootTemplateID, &chainEnabled, &sc.Recurrence, &sc.Weekdays, &sc.IntervalDays, &sc.SetsPerDay,
		&sc.WindowStart, &sc.WindowEnd, &sc.Timezone, &sc.StartDate, &endDate, &sc.State, &sc.PausedReason,
		&pausedAt, &lastPlannedDate, &created, &updated); err != nil {
		return Schedule{}, err
	}
	sc.RootTemplateID = rootTemplateID.String
	sc.ChainEnabled = chainEnabled != 0
	sc.EndDate = endDate.String
	sc.PausedAt = parseNullableTime(pausedAt)
	sc.LastPlannedDate = lastPlannedDate.String
	sc.CreatedAt, _ = time.Parse(timeLayout, created)
	sc.UpdatedAt, _ = time.Parse(timeLayout, updated)
	return sc, nil
}

// InsertSchedule inserts sc and every one of receivers in one transaction —
// same shape as CreateRun: a receiver insert that fails rolls the schedule
// row back too, so a schedule is never persisted with a partial receiver
// set.
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

	chainEnabled := 0
	if sc.ChainEnabled {
		chainEnabled = 1
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO wa_convo_schedules (`+scheduleCols+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sc.ID, sc.Name, sc.SenderSessionID, sc.SenderName, sc.SenderPhone, sc.Topic,
		nullIfEmpty(sc.RootTemplateID), chainEnabled, sc.Recurrence, sc.Weekdays, sc.IntervalDays, sc.SetsPerDay,
		sc.WindowStart, sc.WindowEnd, sc.Timezone, sc.StartDate, nullIfEmpty(sc.EndDate), sc.State, sc.PausedReason,
		nullableTime(sc.PausedAt), nullIfEmpty(sc.LastPlannedDate), sc.CreatedAt.Format(timeLayout),
		sc.UpdatedAt.Format(timeLayout)); err != nil {
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
			`INSERT INTO wa_convo_schedule_receivers (schedule_id, session_id, session_name, phone, position)
			 VALUES (?, ?, ?, ?, ?)`,
			rec.ScheduleID, rec.SessionID, rec.SessionName, rec.Phone, rec.Position); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) GetSchedule(ctx context.Context, id string) (Schedule, error) {
	row := r.pool.QueryRowContext(ctx, `SELECT `+scheduleCols+` FROM wa_convo_schedules WHERE id = ?`, id)
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
// Position (the fan-out order a set starts runs in).
func (r *Repository) ScheduleReceivers(ctx context.Context, scheduleID string) ([]ScheduleReceiver, error) {
	rows, err := r.pool.QueryContext(ctx,
		`SELECT `+scheduleReceiverCols+` FROM wa_convo_schedule_receivers
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
// [1, 200], the same convention as ListRuns/ListTemplateSummaries.
func (r *Repository) ListSchedules(ctx context.Context, limit int) ([]Schedule, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := r.pool.QueryContext(ctx,
		`SELECT `+scheduleCols+` FROM wa_convo_schedules ORDER BY created_at DESC LIMIT ?`, limit)
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
// (delete-then-insert, in the same transaction as the row update) rather
// than diffing it — the caller always hands the full desired set, the same
// contract as InsertSchedule's initial set.
func (r *Repository) UpdateSchedule(ctx context.Context, sc *Schedule, receivers []ScheduleReceiver) error {
	sc.UpdatedAt = r.now()
	tx, err := r.pool.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	chainEnabled := 0
	if sc.ChainEnabled {
		chainEnabled = 1
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE wa_convo_schedules SET
		   name = ?, sender_session_id = ?, sender_name = ?, sender_phone = ?, topic = ?,
		   root_template_id = ?, chain_enabled = ?, recurrence = ?, weekdays = ?, interval_days = ?,
		   sets_per_day = ?, window_start = ?, window_end = ?, timezone = ?, start_date = ?, end_date = ?,
		   state = ?, paused_reason = ?, paused_at = ?, last_planned_date = ?, updated_at = ?
		 WHERE id = ?`,
		sc.Name, sc.SenderSessionID, sc.SenderName, sc.SenderPhone, sc.Topic,
		nullIfEmpty(sc.RootTemplateID), chainEnabled, sc.Recurrence, sc.Weekdays, sc.IntervalDays,
		sc.SetsPerDay, sc.WindowStart, sc.WindowEnd, sc.Timezone, sc.StartDate, nullIfEmpty(sc.EndDate),
		sc.State, sc.PausedReason, nullableTime(sc.PausedAt), nullIfEmpty(sc.LastPlannedDate),
		sc.UpdatedAt.Format(timeLayout), sc.ID)
	if err != nil {
		return err
	}
	if err := requireAffected(res); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM wa_convo_schedule_receivers WHERE schedule_id = ?`, sc.ID); err != nil {
		return err
	}
	if err := insertReceivers(ctx, tx, sc.ID, receivers); err != nil {
		return err
	}

	return tx.Commit()
}

// PlannableSchedules returns every schedule the planner should materialise
// an occurrence for on `today` (YYYY-MM-DD): active, already started, not
// yet ended, and not already planned for today. Comparing start_date/
// end_date/last_planned_date against today with plain string operators
// relies on YYYY-MM-DD sorting lexically the same as chronologically —
// exactly why these columns are VARCHAR rather than DATE (see
// migrations/0008_convo_scheduling.sql's header).
func (r *Repository) PlannableSchedules(ctx context.Context, today string, limit int) ([]Schedule, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.pool.QueryContext(ctx,
		`SELECT `+scheduleCols+` FROM wa_convo_schedules
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

// MarkPlanned stamps id's last_planned_date so the planner never double-plans
// date on a restart. ErrNotFound if id has no row.
func (r *Repository) MarkPlanned(ctx context.Context, id, date string, at time.Time) error {
	res, err := r.pool.ExecContext(ctx,
		`UPDATE wa_convo_schedules SET last_planned_date = ?, updated_at = ? WHERE id = ?`,
		date, at.UTC().Format(timeLayout), id)
	if err != nil {
		return err
	}
	return requireAffected(res)
}

// SetScheduleState refuses a transition out of a terminal state
// (ErrScheduleFinished), mirroring CancelRun's own terminal-state guard for
// runs. paused_at is stamped when moving into SchedulePaused or
// ScheduleStalled, and left as given (via at, converted to nil when the
// caller passes a zero reason and a non-pausing state) otherwise — callers
// resuming a schedule pass SchedulePaused/Stalled's own reason as "" and
// this clears both paused_reason and paused_at.
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
		`UPDATE wa_convo_schedules SET state = ?, paused_reason = ?, paused_at = ?, updated_at = ? WHERE id = ?`,
		state, truncate(reason, 255), pausedAt, at.UTC().Format(timeLayout), id)
	if err != nil {
		return err
	}
	return requireAffected(res)
}

func (r *Repository) DeleteSchedule(ctx context.Context, id string) error {
	res, err := r.pool.ExecContext(ctx, `DELETE FROM wa_convo_schedules WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return requireAffected(res)
}

// CountRunsForOccurrence is the planner's idempotency check: how many runs
// already exist for scheduleID on date, so it never starts a second set for
// a day it already planned.
func (r *Repository) CountRunsForOccurrence(ctx context.Context, scheduleID, date string) (int, error) {
	var n int
	err := r.pool.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM wa_convo_runs WHERE schedule_id = ? AND occurrence_date = ?`,
		scheduleID, date).Scan(&n)
	return n, err
}

// CountSenderTurnsForDate counts turns actually sent FROM senderSessionID
// (i.e. t.session_id = senderSessionID) across every run whose
// occurrence_date is date — the planner's per-sender daily cap
// (WSM_SELF_CONVO_MAX_MSGS_PER_DAY, see internal/selfconvo.PlannerOptions.
// MaxMsgsPerDay): how many messages this sender session is ALREADY
// committed to sending on date, scheduled or already gone out, so the cap
// is checked BEFORE the planner starts a further set, not only after
// something has already been sent. Scoped to occurrence_date (VARCHAR
// YYYY-MM-DD, same lexical-comparison convention as every other date column
// in this schema) rather than every run ever, so a manually triggered run
// (occurrence_date == "") never counts against a schedule's own daily
// budget.
func (r *Repository) CountSenderTurnsForDate(ctx context.Context, senderSessionID, date string) (int, error) {
	var n int
	err := r.pool.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM wa_convo_turns t
		 JOIN wa_convo_runs run ON run.id = t.run_id
		 WHERE t.session_id = ? AND run.occurrence_date = ?`,
		senderSessionID, date).Scan(&n)
	return n, err
}

// ---- template correlation graph ----

const templateLinkCols = `from_template_id, to_template_id, weight, min_gap_seconds, max_gap_seconds, created_at`

func scanTemplateLink(s scanner) (TemplateLink, error) {
	var l TemplateLink
	var created string
	if err := s.Scan(&l.FromTemplateID, &l.ToTemplateID, &l.Weight, &l.MinGapSeconds, &l.MaxGapSeconds,
		&created); err != nil {
		return TemplateLink{}, err
	}
	l.CreatedAt, _ = time.Parse(timeLayout, created)
	return l, nil
}

// ReplaceTemplateLinks atomically swaps every outgoing link of fromID for
// links — the same delete-then-insert shape UpdateSchedule uses for its
// receiver set.
func (r *Repository) ReplaceTemplateLinks(ctx context.Context, fromID string, links []TemplateLink) error {
	tx, err := r.pool.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM wa_convo_template_links WHERE from_template_id = ?`, fromID); err != nil {
		return err
	}
	for _, l := range links {
		l.FromTemplateID = fromID
		if l.CreatedAt.IsZero() {
			l.CreatedAt = r.now()
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO wa_convo_template_links (`+templateLinkCols+`) VALUES (?, ?, ?, ?, ?, ?)`,
			l.FromTemplateID, l.ToTemplateID, l.Weight, l.MinGapSeconds, l.MaxGapSeconds,
			l.CreatedAt.Format(timeLayout)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// TemplateLinks returns every outgoing link of fromID.
func (r *Repository) TemplateLinks(ctx context.Context, fromID string) ([]TemplateLink, error) {
	rows, err := r.pool.QueryContext(ctx,
		`SELECT `+templateLinkCols+` FROM wa_convo_template_links
		 WHERE from_template_id = ? ORDER BY to_template_id`, fromID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TemplateLink
	for rows.Next() {
		l, err := scanTemplateLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// TopicSummaries returns every distinct non-empty topic present in
// wa_convo_templates, alphabetical, with a COUNT of every template carrying
// it (any stage) — the schedule create/edit form's topic <select> uses this
// so an operator sees how much corpus backs each option before picking one.
// A template with topic="" (every row that predates
// migrations/0008_convo_scheduling.sql) is excluded: it is not a
// selectable topic, just an old row with none set.
func (r *Repository) TopicSummaries(ctx context.Context) ([]TopicSummary, error) {
	rows, err := r.pool.QueryContext(ctx,
		`SELECT topic, COUNT(*) FROM wa_convo_templates WHERE topic != '' GROUP BY topic ORDER BY topic`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TopicSummary
	for rows.Next() {
		var ts TopicSummary
		if err := rows.Scan(&ts.Topic, &ts.Count); err != nil {
			return nil, err
		}
		out = append(out, ts)
	}
	return out, rows.Err()
}

// TemplateIDsByTopicStage returns up to limit template ids matching topic
// and stage, newest first — the planner's draw pool for picking a candidate
// opener/middle/closer.
func (r *Repository) TemplateIDsByTopicStage(ctx context.Context, topic, stage string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.pool.QueryContext(ctx,
		`SELECT id FROM wa_convo_templates WHERE topic = ? AND stage = ? ORDER BY created_at DESC LIMIT ?`,
		topic, stage, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
