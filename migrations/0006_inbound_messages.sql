-- 0006_inbound_messages.sql — persist customer replies so a consumer can read
-- them back over GET /api/v1/inbound instead of only ever guessing from a
-- 4-hour timeout (see docs/brief-inbound-api.md for the incident this closes:
-- the consumer's own reader was retired with its old per-product gateway on
-- 2026-08-29, and this central gateway never grew a replacement).
--
-- wa_inbound_messages is deliberately its own table, not more columns on
-- wa_messages, for the same reason wa_calls is (0004's header): an inbound
-- reply is not an outbound send with a flag flipped — it has no queued/sent
-- lifecycle, no api_client_id attribution at write time, and a different
-- natural key (WhatsApp's own message id on the arrival, not one we mint).
--
-- Departures from the reference DDL in docs/brief-inbound-api.md, and why:
--
--   * id is the WhatsApp message id itself (not a generated one), used as
--     the natural idempotency key: the event stream that feeds this table is
--     lossy by design (see CLAUDE.md's SSE-event hazards) and a redelivered
--     "message" event must land as the same row, not a duplicate. See
--     Insert()'s doc comment in internal/domain/inbound/repository.go for how
--     that is enforced PORTABLY — the reference's INSERT IGNORE is MySQL-only
--     and no repository in this codebase carries dialect-specific SQL today.
--
--   * from_lid (nullable) is NOT in the reference DDL. It exists because of a
--     verified production fact: on the live waxum deployment every inbound
--     sender_jid observed so far is a "@lid" JID (a WhatsApp linked-identity
--     id), never a phone JID. A LID is not a phone number and cannot be
--     matched against wa_messages.to_phone directly — it has to be resolved
--     to an MSISDN first, and that resolution can fail. from_phone is set to
--     '' (empty string, NOT NULL — see below) whenever resolution fails,
--     and from_lid keeps the raw, unresolved identity around so a reply that
--     can never be stamped with a client_ref is still diagnosable instead of
--     silently vanishing into an empty from_phone with no trace of why.
--
--   * from_phone is NOT NULL VARCHAR(32) storing normalized digits, defaulting
--     to the empty string (rather than NULL) when the LID could not be
--     resolved. This keeps every index and every `WHERE from_phone = ?` query
--     (LatestOutboundRefs's phone match, idx_wa_inbound_from) working over a
--     single, always-present type instead of also having to special-case NULL
--     on every read.
--
--   * NO foreign key to wa_sessions, unlike wa_calls' fk_wa_calls_session in
--     0004. A call is placed BY a session that still exists at write time; an
--     inbound reply can arrive well after its session has been deleted (a
--     re-paired or decommissioned number), and the reply is still worth
--     keeping — a consumer's confirm-receipt match does not care whether the
--     session that received it still exists. Failing the insert because the
--     owning session is gone would silently drop a customer's own word, which
--     is exactly the claim this table exists to make obtainable.
--
--   * client_ref/tenant_ref are the STAMP, not caller input: they are copied
--     from the most recent matching wa_messages row within a bounded window
--     (LatestOutboundRefs), never supplied by whatever produced the arrival.
--     Un-stamped (both NULL) is the safe default — see the brief's "tier
--     stamp is the security property" section.
--
--   * replies_to_message_id (nullable) records which wa_messages.id the stamp
--     came from, so a stamped row is auditable back to the specific outbound
--     send it was attributed to, not just to "some send within 24h".
--
-- Indexes mirror the reference: received_at alone for a time-window scan
-- (Since's WHERE received_at >= ?), (from_phone, received_at) for the
-- stamp lookup's phone match, (client_ref, received_at) for a per-consumer
-- log scan — same "index leads with the ref" shape as 0002's wa_messages
-- indexes.
--
-- Timestamps are RFC3339 VARCHAR(40) strings, matching every other table in
-- this schema. received_at comes from the WhatsApp envelope, not our clock —
-- see internal/domain/inbound.Message's ReceivedAt field for why that
-- matters to the consumer's since= window. laundry_wa only — outside the
-- platform conformance inventory (see 0001's header).

-- +goose Up
CREATE TABLE wa_inbound_messages (
  id                     VARCHAR(128) NOT NULL PRIMARY KEY,
  session_id             VARCHAR(64)  NOT NULL,
  from_phone             VARCHAR(32)  NOT NULL COMMENT 'normalized digits; empty when the sender LID could not be resolved',
  from_jid               VARCHAR(128) NOT NULL,
  from_lid               VARCHAR(128) NULL COMMENT 'raw @lid sender identity, kept for diagnosis when from_phone is empty',
  body                   TEXT         NULL,
  received_at            VARCHAR(40)  NOT NULL COMMENT 'RFC3339, from the WhatsApp envelope not our clock',
  client_ref             VARCHAR(64)  NULL,
  tenant_ref             VARCHAR(64)  NULL,
  replies_to_message_id  VARCHAR(128) NULL COMMENT 'wa_messages.id the stamp came from',
  created_at             VARCHAR(40)  NOT NULL,
  KEY idx_wa_inbound_received (received_at),
  KEY idx_wa_inbound_from (from_phone, received_at),
  KEY idx_wa_inbound_client (client_ref, received_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- +goose Down
DROP TABLE IF EXISTS wa_inbound_messages;
