-- 0005_master_session.sql — let a session be flagged as a MASTER number that
-- automatic send routing must never pick.
--
-- Outbound sends can be routed automatically: POST /api/v1/messages,
-- /api/v1/messages/group, /api/v1/messages/media and /api/v1/contacts/check
-- all call Manager.SelectConnected (internal/wa/load_balance.go), which picks
-- whichever connected session has the shortest queue and was least recently
-- used. Every connected session is a candidate today, and the
-- least-recently-selected tiebreak makes rotation move through all of them
-- over time.
--
-- Some numbers must never act as an automatic sender. A "master" number is an
-- operator or brand-identity handset; having automatic routing occasionally
-- pick it exposes it as a bulk sender and puts a device identity at risk that
-- has no second copy anywhere (see CLAUDE.md's singleton-volume hazards).
--
-- is_master is ROUTING intent: "never be picked by automatic session
-- selection". This is deliberately a SEPARATE column from `active`, which is
-- CONNECTION intent — a master session still stays connected and remains
-- directly sendable (naming its session id explicitly keeps working
-- unchanged); is_master only removes it from the automatic candidate pool
-- unless a caller opts in per request.
--
-- Booleans are TINYINT 0/1 per migrations/0001_app_tables.sql:14-15.

-- +goose Up
ALTER TABLE wa_sessions ADD COLUMN is_master TINYINT NOT NULL DEFAULT 0 AFTER active;

-- +goose Down
ALTER TABLE wa_sessions DROP COLUMN is_master;
