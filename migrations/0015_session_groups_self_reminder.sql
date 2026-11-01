-- 0015_session_groups_self_reminder.sql — a session may belong to SEVERAL
-- routing groups (or none), and exactly one session per host may carry the
-- "self reminder" flag (the default sender for the alerts 0016 introduces).
--
-- MULTIPLE GROUPS
--
-- wa_sessions.group_code (0009) stays exactly as it is — still written as
-- the FIRST group a session belongs to, so an old binary still running
-- between `migrate` and `up` (the additive-migration contract every prior
-- migration's header documents) keeps reading/writing it unchanged and
-- keeps routing exactly as before. The new table below is the full set; the
-- column is now a cached projection of it, not the source of truth.
--
-- wa_session_groups is a pure join table, no surrogate key: (session_id,
-- group_code) IS the row, so a session cannot be assigned the same group
-- twice and "is session X in group Y" is a single indexed lookup (the
-- primary key itself). created_at is carried for operator debugging only —
-- nothing in this codebase queries by it (see 0009's header for why a
-- codebase this size does not add speculative indexes).
--
-- Normalisation ([a-z0-9_-], lowercase, max 32 chars) stays enforced in Go
-- by wa.NormalizeGroupCode — now the single writer via
-- ManagedSession.SetGroupCodes — for the exact reason 0009's header gives
-- for group_code: MySQL/SQLite CHECK enforcement has historically diverged
-- in this codebase, and a raw UPDATE bypassing the app would not
-- necessarily match either dialect's CHECK syntax regardless.
--
-- Backfill: every row that already has a non-empty group_code (every
-- session with ANY group under the old single-column model) gets exactly
-- one wa_session_groups row carrying that same code. A session with
-- group_code = '' (the ungrouped default) gets no row — "no row in
-- wa_session_groups" IS "no groups", there is no empty-string sentinel row
-- to insert or guard against.
--
-- SELF REMINDER
--
-- is_self_reminder is a NEW, ORTHOGONAL flag, parallel to is_master (0005)
-- and group_code (0009): it names the one session, per host, that the
-- alerts in 0016 default to sending FROM. Exclusivity ("at most one row set
-- at a time") is enforced in the application layer — see
-- Manager.SetSelfReminder's doc comment — not by a DB UNIQUE partial index,
-- for the same divergence reason 0009 declines a CHECK constraint: MySQL
-- and SQLite's partial/filtered index syntax do not agree, and duplicating
-- the rule in SQL would make the app's own guard no longer the single
-- authority. DEFAULT 0 means every existing row, and every row a future
-- migrate-then-up window inserts via the old binary, keeps carrying no flag
-- at all until an operator sets one through the new UI (Part 3/4) — the
-- flag is deliberately never seeded here, because the session id that
-- should hold it differs per host (see the plan's "Decisions Ardi made").

-- +goose Up
CREATE TABLE wa_session_groups (
  session_id  VARCHAR(64) NOT NULL,
  group_code  VARCHAR(32) NOT NULL,
  created_at  VARCHAR(40) NOT NULL,
  PRIMARY KEY (session_id, group_code)
);

INSERT INTO wa_session_groups (session_id, group_code, created_at)
  SELECT id, group_code, updated_at FROM wa_sessions WHERE group_code <> '';

ALTER TABLE wa_sessions ADD COLUMN is_self_reminder TINYINT NOT NULL DEFAULT 0 AFTER group_code;

-- +goose Down
ALTER TABLE wa_sessions DROP COLUMN is_self_reminder;
DROP TABLE wa_session_groups;
