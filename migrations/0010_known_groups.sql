-- 0010_known_groups.sql — a small per-session CRUD mapping a friendly name
-- to a real WhatsApp group JID (e.g. "Clino Team" ->
-- 120363426585371342@g.us), fed by the existing live
-- GET /api/v1/sessions/{id}/groups endpoint. This is NOT the same concept as
-- 0009's group_code (a WSM-internal routing scope, unrelated to a real
-- WhatsApp group) — see that migration's header for the routing concept this
-- one must not be confused with.
--
-- session_id is a SNAPSHOT, no FK, same "a deleted session must not make an
-- old row un-renderable" reasoning as every other session reference in this
-- schema (see migrations/0007_self_convo.sql's header for the canonical
-- statement of it). group_jid is stored in its CANONICAL form
-- (wa.ParseGroupJID's output, "<digits>@g.us") so every reader compares
-- apples to apples regardless of whether the operator pasted the bare id or
-- the full JID into the form.
--
-- UNIQUE (session_id, group_jid) stops the same session from saving the same
-- group twice under two different names — a session's known-groups list is a
-- picker source, not a free-form label store, so a duplicate JID would just
-- be two ways to pick the same destination.
--
-- laundry_wa only — outside the platform conformance inventory (see 0001's
-- header).

-- +goose Up
CREATE TABLE wa_known_groups (
  id         VARCHAR(64)  NOT NULL,
  session_id VARCHAR(64)  NOT NULL,
  group_jid  VARCHAR(64)  NOT NULL,
  name       VARCHAR(120) NOT NULL,
  created_at VARCHAR(40)  NOT NULL,
  updated_at VARCHAR(40)  NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_wa_known_groups_session_jid (session_id, group_jid),
  KEY idx_wa_known_groups_session (session_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- +goose Down
DROP TABLE IF EXISTS wa_known_groups;
