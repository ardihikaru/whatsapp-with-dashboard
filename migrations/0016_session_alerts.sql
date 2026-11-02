-- 0016_session_alerts.sql — operator-configurable alerting for the gateway
-- itself: a WhatsApp-group message whenever a session goes logged_out, and
-- an optional once-a-day roundup of every session that is not connected.
-- See internal/sessionalert's package doc comment for the sending logic;
-- this migration only adds the settings this single-row table stores.
--
-- SINGLE ROW, id ALWAYS 1 — mirrors no existing table in this codebase
-- exactly, but follows the same shape every settings-ish table elsewhere
-- settles into: one editable row an operator reaches through one form,
-- never a list. id is a plain INTEGER PRIMARY KEY rather than this
-- codebase's usual crypto/rand hex id (wa_sessions.id etc.) precisely
-- because there must only ever be one row to find, and "the row whose id
-- is 1" is simpler for every writer (internal/sessionalert, the settings
-- handler) to agree on than "the only row, whatever its id turns out to
-- be". The settings repository INSERTs row id=1 with every column at its
-- default the first time it is read and finds nothing, so neither this
-- migration nor EnsureSchema (internal/db) seeds it directly — same
-- reasoning as 0015's self-reminder flag: there is nothing host-independent
-- to seed, every field starts "off" until an operator opts in through
-- /settings/alerts.
--
--   logout_alert_enabled  1 = send the batched logout alert (internal/
--                         sessionalert's 30s batching window + per-session
--                         cooldown) when a session transitions to
--                         logged_out.
--   daily_report_enabled  1 = send the once-daily roundup of every
--                         non-connected session (including ones
--                         deliberately deactivated).
--   daily_time            "HH:MM", local to `timezone` — parsed by
--                         schedclock.ParseClock, same shape as
--                         wa_status_schedules.window_start/_end (0013).
--   timezone               IANA zone name, default Asia/Jakarta — same
--                         convention as wa_status_schedules.timezone.
--   target_group_jid/      the WhatsApp group both alert kinds post to.
--   target_group_name      _name is display-only, never sent to WhatsApp.
--   sender_session_id      which session POSTS the alert. '' means "use
--                         whichever session currently carries
--                         is_self_reminder" — see internal/sessionalert's
--                         sender-fallback order (configured ->
--                         self-reminder -> any connected, never a session
--                         being reported).
--   last_daily_date        the local date (schedclock.LocalDate) the daily
--                         report was last actually sent for — claimed
--                         BEFORE sending, the same anti-double-send
--                         discipline wa_status_schedules.last_planned_date
--                         uses, so a slow send or a crash mid-post cannot
--                         produce two reports for one day.
--   updated_at              last time any of the above was saved, RFC3339
--                         UTC like every other timestamp in this schema.

-- +goose Up
CREATE TABLE wa_session_alert_settings (
  id                    INTEGER PRIMARY KEY,
  logout_alert_enabled  TINYINT      NOT NULL DEFAULT 0,
  daily_report_enabled  TINYINT      NOT NULL DEFAULT 0,
  daily_time            VARCHAR(5)   NOT NULL DEFAULT '08:00',
  timezone              VARCHAR(64)  NOT NULL DEFAULT 'Asia/Jakarta',
  target_group_jid      VARCHAR(190) NOT NULL DEFAULT '',
  target_group_name     VARCHAR(190) NOT NULL DEFAULT '',
  sender_session_id     VARCHAR(64)  NOT NULL DEFAULT '',
  last_daily_date       VARCHAR(10)  NOT NULL DEFAULT '',
  updated_at            VARCHAR(40)  NOT NULL
);

-- +goose Down
DROP TABLE wa_session_alert_settings;
