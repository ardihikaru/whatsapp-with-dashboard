-- 0008_convo_scheduling.sql — persistence for Convo scheduling: turning a
-- Self Convo run (0007) from something an operator triggers by hand into
-- something that runs on a recurring cadence, to more than one receiver,
-- and chains into a follow-up conversation on its own.
--
-- ADDITIVE COLUMNS, not new tables, for the two things a schedule and a
-- corpus-driven run need from rows that already exist:
--
--   wa_convo_templates gains topic/stage/origin so a schedule can draw a
--     candidate ("give me an opener for topic=futsal") instead of an
--     operator picking one template id by hand every time. `origin`
--     ('upload' vs 'corpus') exists so a later phase can bulk-replace every
--     generated corpus row in one DELETE WHERE origin='corpus' without
--     touching anything an operator uploaded themselves.
--
--   wa_convo_runs gains schedule_id/occurrence_date/chain_prev_run_id/
--     chain_depth/topic/paraphrase_seed so a run created by the planner
--     carries where it came from. schedule_id is deliberately NOT a foreign
--     key, same reasoning as session_a_name/phone_a in 0007's header:
--     deleting a schedule must not make its already-finished runs
--     un-renderable. occurrence_date is the planner's own idempotency unit
--     ("has a set already been started for this schedule today?") and is a
--     VARCHAR, not a DATE, for the same lexical-comparison reason as every
--     timestamp in this schema — see PlannableSchedules'/
--     CountRunsForOccurrence's callers in schedule_repository.go.
--     paraphrase_seed is stored, not just used in memory, so a run's exact
--     wording can be reproduced later for debugging without re-rolling it.
--
-- TWO NEW TABLES:
--
--   wa_convo_template_links — the template correlation graph. A weighted
--     directed edge "after FromTemplateID, ToTemplateID is a plausible next
--     conversation", with the random pre-start delay window the next run
--     should be scheduled inside. UNLIKE wa_convo_runs, this table DOES
--     cascade off both its endpoints (ON DELETE CASCADE on from and to): a
--     link has no independent audit value once either template it connects
--     is gone, the same reasoning wa_convo_turns' cascade off its run
--     carries in 0007. InnoDB needs an index with the FK column as its
--     leftmost member for EACH foreign key, hence the explicit
--     idx_wa_convo_template_links_to alongside the (from_template_id,
--     to_template_id) primary key, which already serves that role for
--     from_template_id.
--
--   wa_convo_schedules / wa_convo_schedule_receivers — a recurring plan
--     plus its fan-out targets. SEPARATE tables, not a comma-joined column
--     on wa_convo_schedules, because a schedule fans out to N receivers and
--     a set produces one run per receiver — exactly the same
--     one-row-per-target shape wa_convo_schedule_receivers borrows from
--     wa_convo_runs' own session_a/session_b pair, generalised past two.
--     sender_name/sender_phone (on the schedule) and session_name/phone (on
--     each receiver) are SNAPSHOTS, no FK, same reasoning as every other
--     session snapshot in this schema. `state` follows wa_convo_runs'
--     enum-not-boolean convention: active | paused | stalled | finished |
--     stopped — paused is operator-initiated, stalled is the automatic
--     health stop when a participating session goes down (paused_reason
--     records why), and only those two are resumable; finished/stopped are
--     terminal (see convo.IsScheduleTerminal). start_date/end_date/
--     last_planned_date are VARCHAR(10) YYYY-MM-DD for the same lexical-
--     comparison reason as occurrence_date above — end_date NULL means
--     unlimited/non-stop, and last_planned_date NULL means the planner has
--     never materialised an occurrence for this schedule yet.
--     idx_wa_convo_schedules_state_planned (state, last_planned_date) is
--     the planner's only poll query, the same one-index-per-query
--     discipline as wa_convo_runs' idx_wa_convo_runs_due in 0007.
--
-- Timestamps are RFC3339 VARCHAR(40) strings, matching every other table in
-- this schema. Booleans are TINYINT (chain_enabled), same as
-- 0005_master_session.sql's is_master.
--
-- laundry_wa only — outside the platform conformance inventory (see 0001's
-- header).

-- +goose Up
ALTER TABLE wa_convo_templates
  ADD COLUMN topic  VARCHAR(48) NOT NULL DEFAULT ''       AFTER name,
  ADD COLUMN stage  VARCHAR(16) NOT NULL DEFAULT ''       AFTER topic,
  ADD COLUMN origin VARCHAR(16) NOT NULL DEFAULT 'upload' AFTER stage;

CREATE INDEX idx_wa_convo_templates_topic_stage ON wa_convo_templates(topic, stage);

ALTER TABLE wa_convo_runs
  ADD COLUMN schedule_id       VARCHAR(64) NULL              AFTER template_name,
  ADD COLUMN occurrence_date   VARCHAR(10) NOT NULL DEFAULT '' AFTER schedule_id,
  ADD COLUMN chain_prev_run_id VARCHAR(64) NULL              AFTER occurrence_date,
  ADD COLUMN chain_depth       INT         NOT NULL DEFAULT 0  AFTER chain_prev_run_id,
  ADD COLUMN topic             VARCHAR(48) NOT NULL DEFAULT '' AFTER chain_depth,
  ADD COLUMN paraphrase_seed   BIGINT      NOT NULL DEFAULT 0  AFTER topic;

CREATE INDEX idx_wa_convo_runs_schedule_date ON wa_convo_runs(schedule_id, occurrence_date);

CREATE TABLE wa_convo_template_links (
  from_template_id VARCHAR(64) NOT NULL,
  to_template_id   VARCHAR(64) NOT NULL,
  weight           INT         NOT NULL DEFAULT 1,
  min_gap_seconds  INT         NOT NULL DEFAULT 0,
  max_gap_seconds  INT         NOT NULL DEFAULT 0,
  created_at       VARCHAR(40) NOT NULL,
  PRIMARY KEY (from_template_id, to_template_id),
  KEY idx_wa_convo_template_links_to (to_template_id),
  CONSTRAINT fk_wa_convo_template_links_from FOREIGN KEY (from_template_id)
    REFERENCES wa_convo_templates (id) ON DELETE CASCADE,
  CONSTRAINT fk_wa_convo_template_links_to FOREIGN KEY (to_template_id)
    REFERENCES wa_convo_templates (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE wa_convo_schedules (
  id                VARCHAR(64)  NOT NULL,
  name              VARCHAR(120) NOT NULL,
  sender_session_id VARCHAR(64)  NOT NULL,
  sender_name       VARCHAR(190) NOT NULL DEFAULT '',
  sender_phone      VARCHAR(32)  NOT NULL DEFAULT '',
  topic             VARCHAR(48)  NOT NULL DEFAULT '',
  root_template_id  VARCHAR(64)  NULL,
  chain_enabled     TINYINT      NOT NULL DEFAULT 1,
  recurrence        VARCHAR(16)  NOT NULL,
  weekdays          VARCHAR(20)  NOT NULL DEFAULT '',
  interval_days     INT          NOT NULL DEFAULT 0,
  sets_per_day      INT          NOT NULL DEFAULT 1,
  window_start      VARCHAR(5)   NOT NULL,
  window_end        VARCHAR(5)   NOT NULL,
  timezone          VARCHAR(64)  NOT NULL DEFAULT 'UTC',
  start_date        VARCHAR(10)  NOT NULL,
  end_date          VARCHAR(10)  NULL,
  state             VARCHAR(16)  NOT NULL,
  paused_reason     VARCHAR(255) NOT NULL DEFAULT '',
  paused_at         VARCHAR(40)  NULL,
  last_planned_date VARCHAR(10)  NULL,
  created_at        VARCHAR(40)  NOT NULL,
  updated_at        VARCHAR(40)  NOT NULL,
  PRIMARY KEY (id),
  KEY idx_wa_convo_schedules_state_planned (state, last_planned_date)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE wa_convo_schedule_receivers (
  schedule_id  VARCHAR(64)  NOT NULL,
  session_id   VARCHAR(64)  NOT NULL,
  session_name VARCHAR(190) NOT NULL DEFAULT '',
  phone        VARCHAR(32)  NOT NULL DEFAULT '',
  position     INT          NOT NULL DEFAULT 0,
  PRIMARY KEY (schedule_id, session_id),
  CONSTRAINT fk_wa_convo_schedule_receivers_schedule FOREIGN KEY (schedule_id)
    REFERENCES wa_convo_schedules (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- +goose Down
DROP TABLE IF EXISTS wa_convo_schedule_receivers;
DROP TABLE IF EXISTS wa_convo_schedules;
DROP TABLE IF EXISTS wa_convo_template_links;

DROP INDEX idx_wa_convo_runs_schedule_date ON wa_convo_runs;
ALTER TABLE wa_convo_runs
  DROP COLUMN paraphrase_seed,
  DROP COLUMN topic,
  DROP COLUMN chain_depth,
  DROP COLUMN chain_prev_run_id,
  DROP COLUMN occurrence_date,
  DROP COLUMN schedule_id;

DROP INDEX idx_wa_convo_templates_topic_stage ON wa_convo_templates;
ALTER TABLE wa_convo_templates
  DROP COLUMN origin,
  DROP COLUMN stage,
  DROP COLUMN topic;
