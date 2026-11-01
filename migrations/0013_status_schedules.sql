-- 0013_status_schedules.sql — scheduled WhatsApp status/story posts. One
-- row is one fixed post (text, image or video) that WSM publishes through
-- one session, once on each selected weekday, at a uniform-random time
-- inside a daily window. See internal/statussched for the planner/runner
-- and web/templates/status_schedule_form.html for the operator form.
--
-- ONE TABLE, deliberately. Unlike 0008/0011 there is no occurrence or turn
-- table: a status post has exactly one message per day and no multi-party
-- choreography, so "what is planned for today" is just next_fire_at on the
-- schedule row, and "has today already been planned" is last_planned_date.
-- The audit trail of what actually went out is wa_messages (to_phone
-- 'status@broadcast', written by Manager.LogStatusPost), the same trail a
-- hand-posted status leaves.
--
-- Timestamps are RFC3339 UTC VARCHARs and dates are 'YYYY-MM-DD' VARCHARs,
-- for the reasons 0007/0008 give (one string layout on MySQL and the SQLite
-- mirror in internal/db.EnsureSchema; the two string compares below —
-- next_fire_at <= now, and date ordering — are lexicographic and correct).
-- next_fire_at is '' (not NULL) when nothing is pending. end_date is NULL
-- for "no end date": the schedule then runs, and its audit rows grow,
-- indefinitely until an operator pauses or deletes it.
--
-- session_id is a reference with no FK, same as every other session
-- reference in this schema (a deleted session must not cascade-delete the
-- operator's schedule; the schedule then records "session not connected").
--
-- media_object_key points at the stored copy of an image/video post's file
-- in object storage. It is a HARD requirement for those kinds (unlike the
-- best-effort media archive): the file is re-fetched at fire time, because
-- waxum's media descriptor lifetime is undocumented and is never persisted.
--
-- recipients_mode 'all' means "every contact in wa_contacts AT FIRE TIME"
-- (recipients_json is then ''); 'custom' means exactly the JIDs listed in
-- recipients_json.
--
-- laundry_wa only — outside the platform conformance inventory (see 0001's
-- header).

-- +goose Up
CREATE TABLE wa_status_schedules (
  id                 VARCHAR(64)  NOT NULL,
  name               VARCHAR(120) NOT NULL,
  session_id         VARCHAR(64)  NOT NULL,
  kind               VARCHAR(8)   NOT NULL,
  text               TEXT         NOT NULL,
  bg_argb            BIGINT       NOT NULL DEFAULT 0,
  caption            TEXT         NOT NULL,
  media_object_key   VARCHAR(255) NOT NULL DEFAULT '',
  media_content_type VARCHAR(64)  NOT NULL DEFAULT '',
  media_filename     VARCHAR(255) NOT NULL DEFAULT '',
  weekdays           VARCHAR(32)  NOT NULL DEFAULT '',
  window_start       VARCHAR(5)   NOT NULL DEFAULT '07:00',
  window_end         VARCHAR(5)   NOT NULL DEFAULT '10:00',
  timezone           VARCHAR(64)  NOT NULL DEFAULT 'Asia/Jakarta',
  start_date         VARCHAR(10)  NOT NULL,
  end_date           VARCHAR(10)  NULL,
  recipients_mode    VARCHAR(8)   NOT NULL DEFAULT 'all',
  recipients_json    MEDIUMTEXT   NOT NULL,
  state              VARCHAR(16)  NOT NULL,
  paused_reason      VARCHAR(255) NOT NULL DEFAULT '',
  last_planned_date  VARCHAR(10)  NULL,
  next_fire_at       VARCHAR(40)  NOT NULL DEFAULT '',
  last_fired_at      VARCHAR(40)  NOT NULL DEFAULT '',
  last_result        VARCHAR(16)  NOT NULL DEFAULT '',
  last_error         VARCHAR(500) NOT NULL DEFAULT '',
  created_at         VARCHAR(40)  NOT NULL,
  updated_at         VARCHAR(40)  NOT NULL,
  PRIMARY KEY (id),
  KEY idx_wa_status_schedules_state_fire (state, next_fire_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- +goose Down
DROP TABLE IF EXISTS wa_status_schedules;
