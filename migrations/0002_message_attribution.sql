-- 0002_message_attribution.sql — attribute each outbound message to WHO/WHAT sent it.
--
-- The message-log UI breaks outbound volume down by API consumer, laundry client,
-- and tenant/outlet. wa_messages carried none of that: ownership was only reachable
-- by joining wa_sessions (always owner_type='platform', client_id=NULL today). These
-- four nullable columns are the attribution seam:
--
--   api_client_id — the WSM api_clients.id that requested the send (set from the
--                   authenticated bearer principal; NULL for the human /send page).
--   client_ref    — laundry client uid the message is about (caller-supplied).
--   tenant_ref    — laundry tenant/outlet uid the message is about (caller-supplied).
--   category      — otp | notification | alert | manual.
--
-- All nullable: existing rows and un-attributed sends simply carry NULL. laundry_wa
-- only — outside the platform conformance inventory (see 0001 header). Timestamps
-- stay RFC3339 VARCHAR(40) strings; the new indexes lead with the ref so a
-- per-client / per-tenant / per-consumer log scan is index-ordered by recency.

-- +goose Up
ALTER TABLE wa_messages
  ADD COLUMN api_client_id VARCHAR(64) NULL AFTER session_id,
  ADD COLUMN client_ref    VARCHAR(64) NULL AFTER api_client_id,
  ADD COLUMN tenant_ref    VARCHAR(64) NULL AFTER client_ref,
  ADD COLUMN category      VARCHAR(32) NULL AFTER tenant_ref;

ALTER TABLE wa_messages
  ADD KEY idx_wa_messages_created (created_at DESC),
  ADD KEY idx_wa_messages_client (client_ref, created_at DESC),
  ADD KEY idx_wa_messages_tenant (tenant_ref, created_at DESC),
  ADD KEY idx_wa_messages_apiclient (api_client_id, created_at DESC),
  ADD KEY idx_wa_messages_category (category, created_at DESC);

-- +goose Down
ALTER TABLE wa_messages
  DROP KEY idx_wa_messages_created,
  DROP KEY idx_wa_messages_client,
  DROP KEY idx_wa_messages_tenant,
  DROP KEY idx_wa_messages_apiclient,
  DROP KEY idx_wa_messages_category;

ALTER TABLE wa_messages
  DROP COLUMN api_client_id,
  DROP COLUMN client_ref,
  DROP COLUMN tenant_ref,
  DROP COLUMN category;
