package convo

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// timeLayout matches every other table in this schema — RFC3339 UTC
// strings, chosen because they compare lexically in both MySQL and SQLite
// (see migrations/0007_self_convo.sql's header).
const timeLayout = time.RFC3339

// Repository is the Self Convo persistence seam: the trigger handler and the
// runner goroutine both go through it, never through raw SQL of their own.
type Repository struct {
	pool *sql.DB
	// now is injected so tests get a deterministic clock, the same shape as
	// internal/domain/apiclient/repository.go's Repository.now.
	now func() time.Time
}

// NewRepository wraps pool. now defaults to time.Now().UTC(); tests
// overwrite the field directly (same-package convention, no setter).
func NewRepository(pool *sql.DB) *Repository {
	return &Repository{pool: pool, now: func() time.Time { return time.Now().UTC() }}
}

// scanner is satisfied by both *sql.Row and *sql.Rows, letting scanRun and
// scanTurn serve QueryRowContext and QueryContext callers alike (same
// pattern as internal/domain/apiclient/scan.go's scanClient).
type scanner interface {
	Scan(dest ...any) error
}

// nullIfEmpty stores "" as SQL NULL, matching
// internal/domain/message/repository.go's helper of the same name and for
// the same reason: an absent attribution/reference column should read back
// as genuinely absent, not as an empty string masquerading as a value.
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

// truncate keeps a stored error reason inside error VARCHAR(255) — same
// width the column carries in both migrations/0007_self_convo.sql and its
// SQLite twin.
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

// ---- templates ----

const templateCols = `id, name, topic, stage, origin, turn_count, body, created_at, updated_at`

func scanTemplate(s scanner) (Template, error) {
	var t Template
	var created, updated string
	if err := s.Scan(&t.ID, &t.Name, &t.Topic, &t.Stage, &t.Origin, &t.TurnCount, &t.Body,
		&created, &updated); err != nil {
		return Template{}, err
	}
	t.CreatedAt, _ = time.Parse(timeLayout, created)
	t.UpdatedAt, _ = time.Parse(timeLayout, updated)
	return t, nil
}

// InsertTemplate fills CreatedAt/UpdatedAt from the injected clock when
// zero, mirroring message.Repository.Insert. Origin defaults to
// OriginUpload when unset, matching the column's own DB default — every
// caller from before the Convo scheduling phase never sets it and still
// gets an "upload" row.
func (r *Repository) InsertTemplate(ctx context.Context, t *Template) error {
	if t.CreatedAt.IsZero() {
		t.CreatedAt = r.now()
	}
	if t.UpdatedAt.IsZero() {
		t.UpdatedAt = r.now()
	}
	if t.Origin == "" {
		t.Origin = OriginUpload
	}
	_, err := r.pool.ExecContext(ctx,
		`INSERT INTO wa_convo_templates (`+templateCols+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Name, t.Topic, t.Stage, t.Origin, t.TurnCount, t.Body,
		t.CreatedAt.Format(timeLayout), t.UpdatedAt.Format(timeLayout))
	return err
}

// templateSummaryCols deliberately excludes body — see ListTemplateSummaries's
// doc comment for why every list view must not select it.
const templateSummaryCols = `id, name, topic, stage, origin, turn_count, created_at, updated_at`

func scanTemplateSummary(s scanner) (TemplateSummary, error) {
	var t TemplateSummary
	var created, updated string
	if err := s.Scan(&t.ID, &t.Name, &t.Topic, &t.Stage, &t.Origin, &t.TurnCount,
		&created, &updated); err != nil {
		return TemplateSummary{}, err
	}
	t.CreatedAt, _ = time.Parse(timeLayout, created)
	t.UpdatedAt, _ = time.Parse(timeLayout, updated)
	return t, nil
}

// ListTemplateSummaries returns every template's summary (no body), newest
// first, limit clamped to [1, 200] the same way ListRuns clamps its own
// limit. Every caller that used to go through ListTemplates — the trigger
// page's <select>, the templates rows partial, and every upload/rename/
// delete response — renders only name/turn_count/created_at, yet the old
// ListTemplates selected the full body TEXT column (up to
// selfconvo.MaxTemplateBytes, 256 KiB) for every row with no LIMIT at all.
// GetTemplate remains the only body-bearing read.
func (r *Repository) ListTemplateSummaries(ctx context.Context, limit int) ([]TemplateSummary, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := r.pool.QueryContext(ctx,
		`SELECT `+templateSummaryCols+` FROM wa_convo_templates ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TemplateSummary
	for rows.Next() {
		t, err := scanTemplateSummary(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListTemplateSummariesByTopic is ListTemplateSummaries scoped to one
// topic — the templates management page's topic filter (topic == ""
// callers should use ListTemplateSummaries instead; this is not that
// case's fallback, to keep the two query shapes each doing exactly one
// thing). Same [1, 200] limit clamp, same newest-first order, same
// excluded body column.
func (r *Repository) ListTemplateSummariesByTopic(ctx context.Context, topic string, limit int) ([]TemplateSummary, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := r.pool.QueryContext(ctx,
		`SELECT `+templateSummaryCols+` FROM wa_convo_templates WHERE topic = ? ORDER BY created_at DESC LIMIT ?`,
		topic, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TemplateSummary
	for rows.Next() {
		t, err := scanTemplateSummary(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *Repository) GetTemplate(ctx context.Context, id string) (Template, error) {
	row := r.pool.QueryRowContext(ctx,
		`SELECT `+templateCols+` FROM wa_convo_templates WHERE id = ?`, id)
	t, err := scanTemplate(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Template{}, ErrNotFound
	}
	return t, err
}

// RenameTemplate updates name and touches updated_at from the injected
// clock. ErrNotFound if id has no row.
func (r *Repository) RenameTemplate(ctx context.Context, id, name string) error {
	res, err := r.pool.ExecContext(ctx,
		`UPDATE wa_convo_templates SET name = ?, updated_at = ? WHERE id = ?`,
		name, r.now().Format(timeLayout), id)
	if err != nil {
		return err
	}
	return requireAffected(res)
}

func (r *Repository) DeleteTemplate(ctx context.Context, id string) error {
	res, err := r.pool.ExecContext(ctx, `DELETE FROM wa_convo_templates WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return requireAffected(res)
}

// ---- runs ----

const runCols = `id, template_id, template_name, schedule_id, occurrence_date,
	chain_prev_run_id, chain_depth, topic, paraphrase_seed,
	session_a_id, session_b_id, session_a_name, session_b_name, phone_a, phone_b,
	state, turn_count, turns_done, start_at, next_due_at, started_at, finished_at,
	error, created_at`

func scanRun(s scanner) (Run, error) {
	var run Run
	var templateID, scheduleID, chainPrevRunID sql.NullString
	var startAt, createdAt string
	var nextDue, startedAt, finishedAt sql.NullString
	if err := s.Scan(&run.ID, &templateID, &run.TemplateName, &scheduleID, &run.OccurrenceDate,
		&chainPrevRunID, &run.ChainDepth, &run.Topic, &run.ParaphraseSeed,
		&run.SessionAID, &run.SessionBID,
		&run.SessionAName, &run.SessionBName, &run.PhoneA, &run.PhoneB, &run.State,
		&run.TurnCount, &run.TurnsDone, &startAt, &nextDue, &startedAt, &finishedAt,
		&run.Error, &createdAt); err != nil {
		return Run{}, err
	}
	run.TemplateID = templateID.String
	run.ScheduleID = scheduleID.String
	run.ChainPrevRunID = chainPrevRunID.String
	run.StartAt, _ = time.Parse(timeLayout, startAt)
	run.CreatedAt, _ = time.Parse(timeLayout, createdAt)
	run.NextDueAt = parseNullableTime(nextDue)
	run.StartedAt = parseNullableTime(startedAt)
	run.FinishedAt = parseNullableTime(finishedAt)
	return run, nil
}

// CreateRun inserts run and every one of turns in one transaction: a turn
// insert that fails (e.g. a duplicate seq colliding with the (run_id, seq)
// primary key) rolls the run row back too, so a run is never persisted with
// a partial turn set.
func (r *Repository) CreateRun(ctx context.Context, run *Run, turns []Turn) error {
	if run.CreatedAt.IsZero() {
		run.CreatedAt = r.now()
	}
	tx, err := r.pool.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO wa_convo_runs (`+runCols+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, nullIfEmpty(run.TemplateID), run.TemplateName, nullIfEmpty(run.ScheduleID), run.OccurrenceDate,
		nullIfEmpty(run.ChainPrevRunID), run.ChainDepth, run.Topic, run.ParaphraseSeed,
		run.SessionAID, run.SessionBID,
		run.SessionAName, run.SessionBName, run.PhoneA, run.PhoneB, run.State,
		run.TurnCount, run.TurnsDone, run.StartAt.Format(timeLayout), nullableTime(run.NextDueAt),
		nullableTime(run.StartedAt), nullableTime(run.FinishedAt), run.Error,
		run.CreatedAt.Format(timeLayout)); err != nil {
		return err
	}

	for _, t := range turns {
		t.RunID = run.ID
		if t.State == "" {
			t.State = TurnPending
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO wa_convo_turns
			   (run_id, seq, speaker, session_id, to_phone, body, delay_seconds, state, message_id, error, sent_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.RunID, t.Seq, t.Speaker, t.SessionID, t.ToPhone, t.Body, t.DelaySeconds, t.State,
			nullIfEmpty(t.MessageID), t.Error, nullableTime(t.SentAt)); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// ListRuns returns every run, newest first. limit is clamped to [1, 200].
func (r *Repository) ListRuns(ctx context.Context, limit int) ([]Run, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := r.pool.QueryContext(ctx,
		`SELECT `+runCols+` FROM wa_convo_runs ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// RunsForSchedule returns every run scheduleID has produced, newest first,
// limit clamped to [1, 200] the same as ListRuns — the schedule detail
// page's "recent runs" table.
func (r *Repository) RunsForSchedule(ctx context.Context, scheduleID string, limit int) ([]Run, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := r.pool.QueryContext(ctx,
		`SELECT `+runCols+` FROM wa_convo_runs WHERE schedule_id = ? ORDER BY created_at DESC LIMIT ?`,
		scheduleID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

func (r *Repository) GetRun(ctx context.Context, id string) (Run, error) {
	row := r.pool.QueryRowContext(ctx, `SELECT `+runCols+` FROM wa_convo_runs WHERE id = ?`, id)
	run, err := scanRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	return run, err
}

// DueRuns is the runner's poll query: every non-terminal run whose
// next_due_at has arrived, earliest first. limit defaults to 20 (the
// runner's own Tick batch size) when not positive.
func (r *Repository) DueRuns(ctx context.Context, now time.Time, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.pool.QueryContext(ctx,
		`SELECT `+runCols+` FROM wa_convo_runs
		 WHERE state IN (?, ?) AND next_due_at IS NOT NULL AND next_due_at <= ?
		 ORDER BY next_due_at, id LIMIT ?`,
		RunScheduled, RunRunning, now.UTC().Format(timeLayout), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// EarliestDue returns the smallest next_due_at across every non-terminal
// run, so the runner can sleep until exactly then instead of polling blind.
func (r *Repository) EarliestDue(ctx context.Context) (time.Time, bool, error) {
	var nextDue sql.NullString
	err := r.pool.QueryRowContext(ctx,
		`SELECT MIN(next_due_at) FROM wa_convo_runs
		 WHERE state IN (?, ?) AND next_due_at IS NOT NULL`,
		RunScheduled, RunRunning).Scan(&nextDue)
	if err != nil {
		return time.Time{}, false, err
	}
	if !nextDue.Valid || nextDue.String == "" {
		return time.Time{}, false, nil
	}
	t, err := time.Parse(timeLayout, nextDue.String)
	if err != nil {
		return time.Time{}, false, err
	}
	return t, true, nil
}

// ---- turns ----

const turnCols = `run_id, seq, speaker, session_id, to_phone, body, delay_seconds, state, message_id, error, sent_at`

func scanTurn(s scanner) (Turn, error) {
	var t Turn
	var messageID sql.NullString
	var sentAt sql.NullString
	if err := s.Scan(&t.RunID, &t.Seq, &t.Speaker, &t.SessionID, &t.ToPhone, &t.Body,
		&t.DelaySeconds, &t.State, &messageID, &t.Error, &sentAt); err != nil {
		return Turn{}, err
	}
	t.MessageID = messageID.String
	t.SentAt = parseNullableTime(sentAt)
	return t, nil
}

// Turns returns every turn of runID, ordered by seq.
func (r *Repository) Turns(ctx context.Context, runID string) ([]Turn, error) {
	rows, err := r.pool.QueryContext(ctx,
		`SELECT `+turnCols+` FROM wa_convo_turns WHERE run_id = ? ORDER BY seq`, runID)
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

// NextPendingTurn returns the lowest-seq turn still pending, if any.
func (r *Repository) NextPendingTurn(ctx context.Context, runID string) (Turn, bool, error) {
	row := r.pool.QueryRowContext(ctx,
		`SELECT `+turnCols+` FROM wa_convo_turns
		 WHERE run_id = ? AND state = ? ORDER BY seq LIMIT 1`, runID, TurnPending)
	t, err := scanTurn(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Turn{}, false, nil
	}
	if err != nil {
		return Turn{}, false, err
	}
	return t, true, nil
}

// NextPendingTurns returns up to n turns of runID still pending, lowest seq
// first. The runner calls this ONCE per turn with n=2 (runner.go's
// processRun): index 0 is the turn about to be claimed and sent; index 1,
// when present, is consulted only for ITS DelaySeconds, to compute the
// "lazy next-due" next_due_at after the send resolves without a second
// round trip back to this query. A short slice (len < n, possibly empty) is
// not an error — it just means fewer pending turns remain than asked for.
func (r *Repository) NextPendingTurns(ctx context.Context, runID string, n int) ([]Turn, error) {
	if n <= 0 {
		n = 1
	}
	rows, err := r.pool.QueryContext(ctx,
		`SELECT `+turnCols+` FROM wa_convo_turns
		 WHERE run_id = ? AND state = ? ORDER BY seq LIMIT ?`, runID, TurnPending, n)
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

// SendingTurns returns every turn across every run still in "sending" —
// the runner's boot-time reconciliation query (Recover, runner.go).
// Deliberately NOT scoped to one run, unlike MarkInterrupted below: a
// process can die with more than one run's turn mid-send at once.
func (r *Repository) SendingTurns(ctx context.Context) ([]Turn, error) {
	rows, err := r.pool.QueryContext(ctx,
		`SELECT `+turnCols+` FROM wa_convo_turns WHERE state = ? ORDER BY run_id, seq`, TurnSending)
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

// MarkInterrupted moves every turn of runID still in "sending" to
// "interrupted" — the boot-time cleanup for a process that died mid-send —
// and reports how many rows it touched.
func (r *Repository) MarkInterrupted(ctx context.Context, runID string) (int64, error) {
	res, err := r.pool.ExecContext(ctx,
		`UPDATE wa_convo_turns SET state = ? WHERE run_id = ? AND state = ?`,
		TurnInterrupted, runID, TurnSending)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ClaimTurn atomically moves runID/seq from pending to sending AND stamps
// the caller's messageID in the same UPDATE — the ONLY path by which a turn
// may go on to be sent. Storing messageID here, before Send is ever called
// (rather than in a separate post-send write, the old SetTurnMessageID),
// means a turn cut off mid-send (crash or graceful stop) always carries the
// wa_messages id Recover needs to reconcile it on the next boot (see
// runner.go's Recover and docs/brief-self-convo.md's restart-safety
// section) — the audit row and the turn are linked before either can be
// left in an ambiguous state.
//
// The WHERE clause's own state = 'pending' check is the guard: a CancelRun
// that lands between the runner's NextPendingTurns (which merely READS the
// row) and this call moves the row straight to skipped, so by the time this
// UPDATE runs it no longer matches and 0 rows are affected —
// ErrTurnNotPending tells the caller exactly that ("someone finished this
// run out from under you"), rather than silently re-claiming a turn a
// concurrent Cancel already decided should never be sent. See runner.go's
// processRun for the caller side.
func (r *Repository) ClaimTurn(ctx context.Context, runID string, seq int, messageID string) error {
	res, err := r.pool.ExecContext(ctx,
		`UPDATE wa_convo_turns SET state = ?, message_id = ? WHERE run_id = ? AND seq = ? AND state = ?`,
		TurnSending, nullIfEmpty(messageID), runID, seq, TurnPending)
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

func (r *Repository) MarkTurnSent(ctx context.Context, runID string, seq int, at time.Time) error {
	res, err := r.pool.ExecContext(ctx,
		`UPDATE wa_convo_turns SET state = ?, sent_at = ? WHERE run_id = ? AND seq = ?`,
		TurnSent, at.UTC().Format(timeLayout), runID, seq)
	if err != nil {
		return err
	}
	return requireAffected(res)
}

func (r *Repository) MarkTurnFailed(ctx context.Context, runID string, seq int, reason string) error {
	res, err := r.pool.ExecContext(ctx,
		`UPDATE wa_convo_turns SET state = ?, error = ? WHERE run_id = ? AND seq = ?`,
		TurnFailed, truncate(reason, 255), runID, seq)
	if err != nil {
		return err
	}
	return requireAffected(res)
}

// SetRunning flips a scheduled run to running and stamps started_at, but
// only if the run is still scheduled — a no-op (no error) otherwise, so a
// duplicate poke or a stale poll cannot re-stamp started_at or resurrect a
// run that has already moved on.
func (r *Repository) SetRunning(ctx context.Context, runID string, at time.Time) error {
	_, err := r.pool.ExecContext(ctx,
		`UPDATE wa_convo_runs SET state = ?, started_at = ? WHERE id = ? AND state = ?`,
		RunRunning, at.UTC().Format(timeLayout), runID, RunScheduled)
	return err
}

// AdvanceRun records progress after a turn resolves and computes the next
// lazy due time — see the plan's "Lazy next-due" rule: next_due_at is always
// now + the next turn's delay, never previous_due + delay.
func (r *Repository) AdvanceRun(ctx context.Context, runID string, turnsDone int, nextDue time.Time) error {
	res, err := r.pool.ExecContext(ctx,
		`UPDATE wa_convo_runs SET turns_done = ?, next_due_at = ? WHERE id = ?`,
		turnsDone, nextDue.UTC().Format(timeLayout), runID)
	if err != nil {
		return err
	}
	return requireAffected(res)
}

// FinishRun sets a terminal state, clears next_due_at (so DueRuns never
// selects this run again) and skips every turn still pending, in one
// transaction.
func (r *Repository) FinishRun(ctx context.Context, runID, state, reason string, at time.Time) error {
	tx, err := r.pool.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`UPDATE wa_convo_runs SET state = ?, error = ?, finished_at = ?, next_due_at = NULL WHERE id = ?`,
		state, truncate(reason, 255), at.UTC().Format(timeLayout), runID)
	if err != nil {
		return err
	}
	if err := requireAffected(res); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE wa_convo_turns SET state = ? WHERE run_id = ? AND state = ?`,
		TurnSkipped, runID, TurnPending); err != nil {
		return err
	}

	return tx.Commit()
}

// CancelRun refuses a run that has already finished (ErrRunFinished) or does
// not exist (ErrNotFound), and otherwise finishes it as cancelled with no
// reason recorded.
func (r *Repository) CancelRun(ctx context.Context, runID string, at time.Time) error {
	run, err := r.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if IsTerminal(run.State) {
		return ErrRunFinished
	}
	return r.FinishRun(ctx, runID, RunCancelled, "", at)
}
