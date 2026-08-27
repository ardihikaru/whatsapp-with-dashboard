-- 0004_media_and_calls.sql — persistence for the two capabilities that
-- motivated the whatsmeow→waxum migration: media attachments and voice calls.
--
-- wa_messages gains five NULLABLE media columns. NULL, not a sentinel default
-- like 'text' or 0: a plain text message genuinely has no media, and NULL says
-- that plainly rather than requiring every reader to know a magic value means
-- "not media". The caption is NOT a new column — it reuses the existing `body`
-- column a media message already has (image/video/document sends carry a
-- caption exactly where a text send carries its message), so a future change
-- must not add a separate `caption` column.
--
--   media_kind       — image | video | audio | document (internal/wa.MediaKind)
--   media_filename    — original filename; required by waxum for document sends
--   media_mime        — MIME type as reported by the uploader
--   media_size        — byte size of the uploaded object
--   media_object_key  — this app's own MinIO key for the archived original
--                        (waxum holds the canonical WhatsApp-side copy; this
--                        is WSM's independent archive, best-effort per the
--                        upload path's own rules)
--
-- wa_calls is a SEPARATE TABLE, not more columns bolted onto wa_messages,
-- because a call is not a message with media attached: it has no body, no
-- wa_message_id, a different and larger terminal-state set (placing/placed/
-- failed/terminated vs queued/sent/failed), and carries fields a message never
-- will (call_id, recording_url, voice). Same session-scoping shape as
-- wa_messages (session_id FK, ON DELETE CASCADE, api_client_id attribution
-- following the same "set only from the authenticated bearer principal, never
-- the request body" rule as wa_messages.api_client_id) so it reads like a
-- sibling table, not a parallel invention.
--
-- Timestamps are RFC3339 VARCHAR(40) strings, matching every other table in
-- this schema. laundry_wa only — outside the platform conformance inventory
-- (see 0001's header).

-- +goose Up
ALTER TABLE wa_messages
  ADD COLUMN media_kind       VARCHAR(16)  NULL,
  ADD COLUMN media_filename   VARCHAR(255) NULL,
  ADD COLUMN media_mime       VARCHAR(128) NULL,
  ADD COLUMN media_size       BIGINT       NULL,
  ADD COLUMN media_object_key VARCHAR(512) NULL;

CREATE TABLE wa_calls (
  id             VARCHAR(64)   NOT NULL,
  session_id     VARCHAR(64)   NOT NULL,
  api_client_id  VARCHAR(64)   NULL,
  to_phone       VARCHAR(32)   NOT NULL,
  kind           VARCHAR(16)   NOT NULL,
  text           TEXT          NULL,
  voice          VARCHAR(64)   NULL,
  audio_url      VARCHAR(1024) NULL,
  call_id        VARCHAR(128)  NOT NULL DEFAULT '',
  recording_url  VARCHAR(1024) NOT NULL DEFAULT '',
  status         VARCHAR(16)   NOT NULL DEFAULT 'placing',
  error          VARCHAR(1024) NOT NULL DEFAULT '',
  created_at     VARCHAR(40)   NOT NULL,
  ended_at       VARCHAR(40)   NULL,
  PRIMARY KEY (id),
  KEY idx_wa_calls_session (session_id, created_at DESC),
  CONSTRAINT fk_wa_calls_session FOREIGN KEY (session_id)
    REFERENCES wa_sessions (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- +goose Down
DROP TABLE IF EXISTS wa_calls;

ALTER TABLE wa_messages
  DROP COLUMN media_kind,
  DROP COLUMN media_filename,
  DROP COLUMN media_mime,
  DROP COLUMN media_size,
  DROP COLUMN media_object_key;
