package sessionalert

import (
	"context"
	"database/sql"
	"time"
)

const timeLayout = time.RFC3339

// Repository is the persistence seam the /settings/alerts handler and
// internal/sessionalert's Service write through. An interface so a test
// can stub persistence without SQLite, mirroring every other domain
// repository in this codebase.
type Repository interface {
	Get(ctx context.Context) (Settings, error)
	Save(ctx context.Context, s Settings) error
	// ClaimDailyDate atomically moves LastDailyDate to date and reports
	// whether THIS call did so (false means some earlier call already
	// claimed it) — the single statement that keeps the daily report from
	// ever going out twice for one local day, even across a crash
	// mid-send (the claim commits before the send is attempted).
	ClaimDailyDate(ctx context.Context, date string) (bool, error)
}

// SQLRepository implements Repository on the shared app-store pool.
type SQLRepository struct{ pool *sql.DB }

func NewRepository(pool *sql.DB) *SQLRepository { return &SQLRepository{pool: pool} }

func now() string { return time.Now().UTC().Format(timeLayout) }

// Get returns the single settings row, inserting the DEFAULT row (id=1,
// every column at its migration default) the first time nothing exists —
// so every caller always gets a usable Settings value, never a "not
// found" error to special-case.
func (r *SQLRepository) Get(ctx context.Context) (Settings, error) {
	row := r.pool.QueryRowContext(ctx,
		`SELECT logout_alert_enabled, daily_report_enabled, daily_time, timezone,
		        target_group_jid, target_group_name, sender_session_id, last_daily_date, updated_at
		 FROM wa_session_alert_settings WHERE id = 1`)
	var s Settings
	var logoutEnabled, dailyEnabled int
	var updated string
	err := row.Scan(&logoutEnabled, &dailyEnabled, &s.DailyTime, &s.Timezone,
		&s.TargetGroupJID, &s.TargetGroupName, &s.SenderSessionID, &s.LastDailyDate, &updated)
	if err == sql.ErrNoRows {
		def := DefaultSettings()
		if _, err := r.pool.ExecContext(ctx,
			`INSERT INTO wa_session_alert_settings (id, logout_alert_enabled, daily_report_enabled,
			   daily_time, timezone, target_group_jid, target_group_name, sender_session_id,
			   last_daily_date, updated_at) VALUES (1, 0, 0, ?, ?, '', '', '', '', ?)`,
			def.DailyTime, def.Timezone, now()); err != nil {
			return Settings{}, err
		}
		return r.Get(ctx)
	}
	if err != nil {
		return Settings{}, err
	}
	s.LogoutAlertEnabled = logoutEnabled != 0
	s.DailyReportEnabled = dailyEnabled != 0
	s.UpdatedAt, _ = time.Parse(timeLayout, updated)
	return s, nil
}

// Save replaces every column of the single row — UPDATE first (the common
// case: the row already exists, created by an earlier Get), falling back
// to INSERT only if nothing was updated, rather than a dialect-specific
// upsert (MySQL's ON DUPLICATE KEY vs SQLite's ON CONFLICT have never been
// relied on elsewhere in this codebase — see 0009's header on why raw,
// portable SQL is preferred here over a feature only one dialect has).
func (r *SQLRepository) Save(ctx context.Context, s Settings) error {
	ts := now()
	res, err := r.pool.ExecContext(ctx,
		`UPDATE wa_session_alert_settings SET
		   logout_alert_enabled = ?, daily_report_enabled = ?, daily_time = ?, timezone = ?,
		   target_group_jid = ?, target_group_name = ?, sender_session_id = ?, updated_at = ?
		 WHERE id = 1`,
		boolToInt(s.LogoutAlertEnabled), boolToInt(s.DailyReportEnabled), s.DailyTime, s.Timezone,
		s.TargetGroupJID, s.TargetGroupName, s.SenderSessionID, ts)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	_, err = r.pool.ExecContext(ctx,
		`INSERT INTO wa_session_alert_settings (id, logout_alert_enabled, daily_report_enabled,
		   daily_time, timezone, target_group_jid, target_group_name, sender_session_id,
		   last_daily_date, updated_at) VALUES (1, ?, ?, ?, ?, ?, ?, ?, '', ?)`,
		boolToInt(s.LogoutAlertEnabled), boolToInt(s.DailyReportEnabled), s.DailyTime, s.Timezone,
		s.TargetGroupJID, s.TargetGroupName, s.SenderSessionID, ts)
	return err
}

func (r *SQLRepository) ClaimDailyDate(ctx context.Context, date string) (bool, error) {
	res, err := r.pool.ExecContext(ctx,
		`UPDATE wa_session_alert_settings SET last_daily_date = ?, updated_at = ?
		 WHERE id = 1 AND last_daily_date <> ?`,
		date, now(), date)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
