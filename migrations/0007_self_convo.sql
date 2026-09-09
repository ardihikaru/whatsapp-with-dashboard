-- 0007_self_convo.sql — persistence for Self Convo: a simulated two-party
-- conversation between two of WSM's own paired sessions, driven by a
-- persisted state machine that a runner goroutine polls (see
-- docs/brief-self-convo.md and the plan's "Scheduler design" section).
--
-- THREE TABLES, not columns bolted onto wa_messages, because a run is a
-- SCHEDULE, not a send: wa_convo_runs and wa_convo_turns exist before any
-- message goes out, carry pacing/progress/failure state a send row has no
-- place for, and outlive any single send's lifecycle. The sends themselves
-- still land in wa_messages exactly like any other outbound message —
-- category='self_convo', client_ref=<run id> — so /logs, the per-session
-- history and every existing send-side test keep working unchanged; these
-- three tables are additive, not a parallel message store.
--
--   wa_convo_templates — one row per uploaded/saved JSON script. Scalar
--     columns only (name, turn_count) plus the raw script itself as an
--     OPAQUE TEXT blob in `body`, same shape as wa_inbound_messages.body:
--     the schema this table owns is "here is a template", not "here is
--     what a template contains" — internal/selfconvo.Validate is the only
--     thing that ever parses `body`, and it does so on read, not on a
--     column-by-column basis. Column widths mirror wa_convo_templates'
--     own use, not the JSON's internal shape.
--
--   wa_convo_runs — one row per triggered run. EXPANDED TURNS are rows in
--     wa_convo_turns, not counted in this table, because the runner needs
--     to `ORDER BY seq` and track per-turn state (pending/sending/sent/
--     failed/interrupted/skipped) independently of the run's own state —
--     see wa_convo_turns below. session_a_name/session_b_name and
--     phone_a/phone_b are SNAPSHOTS taken at trigger time, not FKs to
--     wa_sessions: same reasoning as 0006_inbound_messages.sql's header —
--     a deleted session must not make a finished run un-renderable, and a
--     run in progress must not fail because a session in the middle of it
--     was deleted. Hence NO FOREIGN KEY from wa_convo_runs to wa_sessions.
--     `next_due_at` is the ONLY column the runner's poll loop reads
--     (`state IN ('scheduled','running') AND next_due_at <= now`), so
--     idx_wa_convo_runs_due (state, next_due_at) exists purely to serve
--     that one query — see the plan's "Runner loop" section. Run states:
--     scheduled | running | done | failed | cancelled.
--
--   wa_convo_turns — one row per expanded turn, keyed (run_id, seq) so the
--     runner can `ORDER BY seq` and update a single turn's state without a
--     second identifier. UNLIKE wa_convo_runs, this table DOES carry a
--     foreign key back to its own run (ON DELETE CASCADE): a turn has no
--     independent existence or audit value once its run is gone — the
--     durable audit trail for a SENT turn is its wa_messages row (via
--     client_ref=run id), not this table. Turn states: pending | sending |
--     sent | failed | interrupted | skipped. message_id, when set, is the
--     wa_messages.id the send landed as, so a turn can be cross-referenced
--     to its audit row without a text search on client_ref.
--
-- Timestamps are RFC3339 VARCHAR(40) strings, matching every other table in
-- this schema — chosen deliberately (not DATETIME) because they compare
-- LEXICALLY in both MySQL and SQLite, which is what lets the runner's
-- `next_due_at <= ?` poll and idx_wa_convo_runs_due behave identically on
-- both dialects without a dialect-specific date function anywhere. Booleans
-- are avoided; state is a VARCHAR enum like every other status column here
-- (wa_messages.status, wa_calls.status).
--
-- laundry_wa only — outside the platform conformance inventory (see 0001's
-- header).

-- +goose Up
CREATE TABLE wa_convo_templates (
  id          VARCHAR(64)  NOT NULL PRIMARY KEY,
  name        VARCHAR(120) NOT NULL,
  turn_count  INT          NOT NULL,
  body        TEXT         NOT NULL,
  created_at  VARCHAR(40)  NOT NULL,
  updated_at  VARCHAR(40)  NOT NULL,
  KEY idx_wa_convo_templates_created (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE wa_convo_runs (
  id              VARCHAR(64)  NOT NULL PRIMARY KEY,
  template_id     VARCHAR(64)  NULL,
  template_name   VARCHAR(120) NOT NULL,
  session_a_id    VARCHAR(64)  NOT NULL,
  session_b_id    VARCHAR(64)  NOT NULL,
  session_a_name  VARCHAR(190) NOT NULL DEFAULT '',
  session_b_name  VARCHAR(190) NOT NULL DEFAULT '',
  phone_a         VARCHAR(32)  NOT NULL,
  phone_b         VARCHAR(32)  NOT NULL,
  state           VARCHAR(16)  NOT NULL,
  turn_count      INT          NOT NULL,
  turns_done      INT          NOT NULL DEFAULT 0,
  start_at        VARCHAR(40)  NOT NULL,
  next_due_at     VARCHAR(40)  NULL,
  started_at      VARCHAR(40)  NULL,
  finished_at     VARCHAR(40)  NULL,
  error           VARCHAR(255) NOT NULL DEFAULT '',
  created_at      VARCHAR(40)  NOT NULL,
  KEY idx_wa_convo_runs_due (state, next_due_at),
  KEY idx_wa_convo_runs_created (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE wa_convo_turns (
  run_id        VARCHAR(64)  NOT NULL,
  seq           INT          NOT NULL,
  speaker       CHAR(1)      NOT NULL,
  session_id    VARCHAR(64)  NOT NULL,
  to_phone      VARCHAR(32)  NOT NULL,
  body          TEXT         NOT NULL,
  delay_seconds INT          NOT NULL,
  state         VARCHAR(16)  NOT NULL DEFAULT 'pending',
  message_id    VARCHAR(64)  NULL,
  error         VARCHAR(255) NOT NULL DEFAULT '',
  sent_at       VARCHAR(40)  NULL,
  PRIMARY KEY (run_id, seq),
  CONSTRAINT fk_wa_convo_turns_run FOREIGN KEY (run_id)
    REFERENCES wa_convo_runs (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- +goose Down
DROP TABLE IF EXISTS wa_convo_turns;
DROP TABLE IF EXISTS wa_convo_runs;
DROP TABLE IF EXISTS wa_convo_templates;
