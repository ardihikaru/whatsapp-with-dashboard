-- 0011_group_broadcast.sql — Group Broadcast Schedule: one WSM session
-- ("sender") posts an opening message into a real WhatsApp group at a
-- random morning time, and a configurable list of other sessions
-- ("receivers") each reply into that same group at their own random later
-- time, some in Indonesian, some in Javanese, with a locally-seeded excuse
-- when the reply lands after a configured cutoff. See
-- docs/brief-group-broadcast.md (if present) / the phase plan for the full
-- design; this header covers only the schema decisions.
--
-- This is DELIBERATELY NOT an extension of wa_convo_schedules (0008): that
-- schema is hard-wired to exactly two parties per run and has no concept of
-- a shared WhatsApp group destination. It reuses that package's PATTERNS
-- (day-level planning, per-day idempotency via an occurrence row, a
-- receivers join table, active|paused|stalled|finished|stopped state) but
-- keeps its own schema, the same "reuse patterns, not code" decision
-- 0009/0010 already made about group_code vs. wa_known_groups.
--
-- FOUR NEW TABLES:
--
--   wa_group_broadcast_schedules — the recurring plan. sender_name/
--     sender_phone/group_name are SNAPSHOTS taken at save time, no FK, same
--     reasoning as every other session/group reference in this schema.
--     known_group_id is a reference ONLY (no FK either) — it exists so the
--     edit form can re-select the same wa_known_groups row, but group_jid is
--     the value every send actually uses, so a deleted known-group row (or
--     one whose JID later changes) can never retroactively alter an
--     already-saved schedule's destination. sender_window_*/
--     receiver_window_*/late_cutoff are separate columns, not a single
--     shared window like wa_convo_schedules' window_start/window_end,
--     because the sender's opener and every receiver's reply are drawn from
--     TWO DIFFERENT windows of the morning (see the planner's own
--     doc comment). lang_id_weight is a percent (0-100); the Javanese
--     weight is always 100 minus this, so there is exactly one number to
--     configure per schedule rather than two that could sum to something
--     other than 100. state/paused_reason/paused_at/last_planned_date/
--     start_date/end_date follow wa_convo_schedules' own conventions
--     exactly (see migrations/0008_convo_scheduling.sql's header for why
--     the date columns are VARCHAR, not DATE).
--
--   wa_group_broadcast_receivers — deliberately the SAME shape as
--     wa_convo_schedule_receivers (0008) so that table's existing
--     "delete+reinsert the whole set on update" edit pattern transfers to
--     this feature unchanged — see groupbroadcast.Repository.UpdateSchedule.
--
--   wa_group_broadcast_occurrences — one row per (schedule, date), the
--     planner's idempotency unit, mirroring wa_convo_runs'
--     (schedule_id, occurrence_date) role but as its OWN row rather than
--     inferred from existing run rows, since one occurrence here fans out
--     into N+1 turns (opener + every healthy receiver's reply), not one row
--     per participant the way a convo run is.
--
--   wa_group_broadcast_turns — one row per actual message: the sender's
--     opener OR one receiver's reply. body/language/is_late/late_reason/
--     source are ALL PRE-GENERATED AT PLANNING TIME (hours before due_at)
--     and never touched again before the runner dispatches them as-is — see
--     the planner's own doc comment for why (the AI wording service is an
--     async job runner, not a synchronous call this app can make at send
--     time). idx_wa_gb_turns_session_due backs the planner's own
--     minimum-same-session-gap check
--     (groupbroadcast.Repository.SessionTurnTimesForDate) — the anti-burst
--     rule that no two sends from the same session, sender or receiver, may
--     be scheduled closer together than WSM_GROUP_BROADCAST_MIN_SESSION_GAP.
--
-- No FK from any of these to wa_sessions (session snapshots) or from
-- wa_group_broadcast_schedules to wa_known_groups (see above) — only the
-- receivers-to-schedule and turns-to-occurrence edges cascade, the same
-- "cascade off the row that owns you, snapshot everything else" split
-- 0007/0008 already establish.
--
-- laundry_wa only — outside the platform conformance inventory (see 0001's
-- header).

-- +goose Up
CREATE TABLE wa_group_broadcast_schedules (
  id                    VARCHAR(64)  NOT NULL,
  name                  VARCHAR(120) NOT NULL,
  sender_session_id     VARCHAR(64)  NOT NULL,
  sender_name           VARCHAR(190) NOT NULL DEFAULT '',
  sender_phone          VARCHAR(32)  NOT NULL DEFAULT '',
  known_group_id        VARCHAR(64)  NULL,
  group_jid             VARCHAR(64)  NOT NULL,
  group_name            VARCHAR(120) NOT NULL DEFAULT '',
  recurrence            VARCHAR(16)  NOT NULL,
  weekdays              VARCHAR(32)  NOT NULL DEFAULT '',
  interval_days         INT          NOT NULL DEFAULT 0,
  sender_window_start   VARCHAR(5)   NOT NULL DEFAULT '07:55',
  sender_window_end     VARCHAR(5)   NOT NULL DEFAULT '08:16',
  receiver_window_start VARCHAR(5)   NOT NULL DEFAULT '08:17',
  receiver_window_end   VARCHAR(5)   NOT NULL DEFAULT '09:30',
  late_cutoff           VARCHAR(5)   NOT NULL DEFAULT '08:30',
  lang_id_weight        INT          NOT NULL DEFAULT 70,
  timezone              VARCHAR(64)  NOT NULL DEFAULT 'Asia/Jakarta',
  start_date            VARCHAR(10)  NOT NULL,
  end_date              VARCHAR(10)  NULL,
  state                 VARCHAR(16)  NOT NULL,
  paused_reason         VARCHAR(255) NOT NULL DEFAULT '',
  paused_at             VARCHAR(40)  NULL,
  last_planned_date     VARCHAR(10)  NULL,
  created_at            VARCHAR(40)  NOT NULL,
  updated_at            VARCHAR(40)  NOT NULL,
  PRIMARY KEY (id),
  KEY idx_wa_gb_schedules_state_planned (state, last_planned_date)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE wa_group_broadcast_receivers (
  schedule_id  VARCHAR(64)  NOT NULL,
  session_id   VARCHAR(64)  NOT NULL,
  session_name VARCHAR(190) NOT NULL DEFAULT '',
  phone        VARCHAR(32)  NOT NULL DEFAULT '',
  position     INT          NOT NULL DEFAULT 0,
  PRIMARY KEY (schedule_id, session_id),
  CONSTRAINT fk_wa_gb_receivers_schedule FOREIGN KEY (schedule_id)
    REFERENCES wa_group_broadcast_schedules (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE wa_group_broadcast_occurrences (
  id              VARCHAR(64) NOT NULL,
  schedule_id     VARCHAR(64) NOT NULL,
  occurrence_date VARCHAR(10) NOT NULL,
  group_jid       VARCHAR(64) NOT NULL,
  state           VARCHAR(16) NOT NULL DEFAULT 'scheduled',
  created_at      VARCHAR(40) NOT NULL,
  finished_at     VARCHAR(40) NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_wa_gb_occ_schedule_date (schedule_id, occurrence_date),
  KEY idx_wa_gb_occ_schedule_date (schedule_id, occurrence_date)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE wa_group_broadcast_turns (
  id            VARCHAR(64)  NOT NULL,
  occurrence_id VARCHAR(64)  NOT NULL,
  role          VARCHAR(8)   NOT NULL,
  session_id    VARCHAR(64)  NOT NULL,
  session_name  VARCHAR(190) NOT NULL DEFAULT '',
  language      VARCHAR(8)   NOT NULL,
  is_late       TINYINT      NOT NULL DEFAULT 0,
  late_reason   VARCHAR(64)  NOT NULL DEFAULT '',
  body          TEXT         NOT NULL,
  source        VARCHAR(16)  NOT NULL,
  due_at        VARCHAR(40)  NOT NULL,
  state         VARCHAR(16)  NOT NULL DEFAULT 'pending',
  message_id    VARCHAR(64)  NOT NULL DEFAULT '',
  error         VARCHAR(255) NOT NULL DEFAULT '',
  sent_at       VARCHAR(40)  NULL,
  created_at    VARCHAR(40)  NOT NULL,
  PRIMARY KEY (id),
  KEY idx_wa_gb_turns_due (state, due_at),
  KEY idx_wa_gb_turns_occurrence (occurrence_id),
  KEY idx_wa_gb_turns_session_due (session_id, due_at),
  CONSTRAINT fk_wa_gb_turns_occurrence FOREIGN KEY (occurrence_id)
    REFERENCES wa_group_broadcast_occurrences (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- +goose Down
DROP TABLE IF EXISTS wa_group_broadcast_turns;
DROP TABLE IF EXISTS wa_group_broadcast_occurrences;
DROP TABLE IF EXISTS wa_group_broadcast_receivers;
DROP TABLE IF EXISTS wa_group_broadcast_schedules;
