-- 0009_session_group_code.sql — let a session opt into a named ROUTING SCOPE
-- that sits parallel to 0005's is_master flag, not a second exclusion flag.
--
-- Automatic routing (internal/wa/load_balance.go's Manager.SelectForRoute)
-- already carves the candidate pool by is_master. group_code carves it again,
-- along an orthogonal axis: a session with a non-empty group_code leaves the
-- DEFAULT auto-routing pool entirely — both the plain pool and a global
-- master_only pick — and becomes reachable only by a request that names that
-- same code. This is a SCOPE, not a widening flag like allow_master: naming
-- group "alpha" does not add alpha's sessions alongside the default pool, it
-- replaces the default pool with alpha's. See EligibleForRoute for the single
-- predicate that encodes this, and the table in the layer-1 plan for the six
-- request shapes it must satisfy.
--
-- '' is the ungrouped default, and every existing row gets it via DEFAULT —
-- so a session created before this migration keeps routing exactly as it did
-- the day before, with no backfill required beyond the column existing.
--
-- Normalisation ([a-z0-9_-], lowercase, max 32 chars) is enforced in Go by
-- wa.NormalizeGroupCode — the only writer, via ManagedSession.SetGroupCode —
-- and deliberately NOT by a CHECK constraint here. MySQL CHECK enforcement
-- and SQLite's (used by every test and by e2e) have historically diverged in
-- this codebase, and duplicating the rule in SQL would make the app's own
-- validator no longer the single authority in two different ways: a bypassed
-- write (a raw UPDATE) could still slip past a CHECK if it didn't match this
-- migration's SQL dialect exactly, and a rule change would need updating in
-- two places instead of one.
--
-- No index. Nothing in this codebase runs a SQL WHERE against this column —
-- repository.go's All() is the only SELECT touching wa_sessions at all, and
-- every routing decision filters wasession.Session/wa.Snapshot rows already
-- loaded into internal/wa's Manager.sessions map, in memory. An index here
-- would speed up a query that does not exist; do not add one "for the
-- lookup" without first adding a lookup that needs it.

-- +goose Up
ALTER TABLE wa_sessions ADD COLUMN group_code VARCHAR(32) NOT NULL DEFAULT '' AFTER is_master;

-- +goose Down
ALTER TABLE wa_sessions DROP COLUMN group_code;
