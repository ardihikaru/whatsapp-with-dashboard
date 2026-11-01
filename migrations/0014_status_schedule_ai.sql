-- 0014_status_schedule_ai.sql — optional AI-written wording for scheduled
-- status posts (wa_status_schedules, 0013). The operator types only a TOPIC;
-- internal/statussched asks ai-codex-cli (internal/aiclient) for a fresh
-- message when it PLANS each day's slot (never at send time — jobs take many
-- seconds) and the fire posts exactly what was stored.
--
-- ADDITIVE, and every column is nullable or defaulted, on purpose: during a
-- deploy the OLD binary keeps running against the migrated table. Its
-- INSERT/UPDATE statements name their columns explicitly and never mention
-- these, so they are simply left at their defaults (ai_enabled 0 = the
-- schedule behaves exactly as before).
--
--   ai_enabled        1 = write each post with AI. For a text schedule the
--                     AI text replaces `text`; for image/video it becomes the
--                     caption. `text`/`caption` then act only as the FALLBACK
--                     when generation fails (and may be empty: a failed
--                     generation with no fallback skips that day's post).
--   ai_topic          what the operator typed; the model writes in the same
--                     language as the topic.
--   next_text         the exact text/caption planned for the pending slot,
--                     stored at plan time so the fire path makes no AI call.
--                     NULL/'' when nothing is pending.
--   next_text_source  'ai' | 'static' | '' — where next_text came from, shown
--                     on the list.
--   last_ai_text      the most recent AI text, fed back into the next prompt
--                     as "do not repeat or closely paraphrase this".
--
-- TEXT columns carry no DEFAULT (MySQL < 8.0.13 cannot), hence NULL-able;
-- the repository reads them through sql.NullString.
--
-- laundry_wa only — outside the platform conformance inventory (see 0001's
-- header).

-- +goose Up
ALTER TABLE wa_status_schedules ADD COLUMN ai_enabled       TINYINT      NOT NULL DEFAULT 0;
ALTER TABLE wa_status_schedules ADD COLUMN ai_topic         VARCHAR(500) NOT NULL DEFAULT '';
ALTER TABLE wa_status_schedules ADD COLUMN next_text        TEXT         NULL;
ALTER TABLE wa_status_schedules ADD COLUMN next_text_source VARCHAR(16)  NOT NULL DEFAULT '';
ALTER TABLE wa_status_schedules ADD COLUMN last_ai_text     TEXT         NULL;

-- +goose Down
ALTER TABLE wa_status_schedules DROP COLUMN last_ai_text;
ALTER TABLE wa_status_schedules DROP COLUMN next_text_source;
ALTER TABLE wa_status_schedules DROP COLUMN next_text;
ALTER TABLE wa_status_schedules DROP COLUMN ai_topic;
ALTER TABLE wa_status_schedules DROP COLUMN ai_enabled;
