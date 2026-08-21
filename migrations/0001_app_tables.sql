-- 0001_app_tables.sql — the WhatsApp gateway's own app tables, ported from the
-- source module's SQLite DDL (internal/db/db.go) to MySQL 8.0.
--
-- These live in a MODULE-OWNED database (`laundry_wa`), deliberately OUTSIDE the
-- platform's core `laundry` schema: the module has its own non-sqlc write path
-- (raw repos, RFC3339 string timestamps) and is not a tenant/sync entity, so it
-- stays out of the platform's conformance / tenant-lint / sync-golden gates
-- (which enforce a closed-world table inventory on the `laundry` DB).
--
-- whatsmeow's 20 crypto tables are NOT here — they stay in SQLite (the library
-- has no MySQL dialect). This file covers only the four app-owned tables.
--
-- Timestamps are VARCHAR(40) RFC3339/UTC strings, matching the repos verbatim
-- (no behavioural change from SQLite). Booleans are TINYINT (0/1).

-- +goose Up

-- owner_type / client_id categorize WHO owns a WhatsApp number:
--   'platform' — the Platform owner's pool (the only path wired today);
--   'client'   — RESERVED for future client self-service. No client-facing
--                access is implemented yet; every row created today is
--                owner_type='platform', client_id=NULL. client_id will point at
--                the platform `clients` table when self-service lands (kept a
--                plain nullable column now — cross-database FKs are not enforced).
CREATE TABLE wa_sessions (
  id               VARCHAR(64)  NOT NULL,
  name             VARCHAR(190) NOT NULL,
  phone            VARCHAR(32)  NOT NULL,
  jid              VARCHAR(128) NOT NULL DEFAULT '',
  active           TINYINT      NOT NULL DEFAULT 0,
  last_status      VARCHAR(24)  NOT NULL DEFAULT 'new',
  owner_type       ENUM('platform','client') NOT NULL DEFAULT 'platform',
  client_id        BIGINT UNSIGNED NULL,
  created_at       VARCHAR(40)  NOT NULL,
  updated_at       VARCHAR(40)  NOT NULL,
  last_selected_at VARCHAR(40)  NULL,
  PRIMARY KEY (id),
  KEY idx_wa_sessions_owner (owner_type, client_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE wa_messages (
  id            VARCHAR(64)   NOT NULL,
  session_id    VARCHAR(64)   NOT NULL,
  to_phone      VARCHAR(32)   NOT NULL,
  body          TEXT          NOT NULL,
  status        VARCHAR(16)   NOT NULL DEFAULT 'queued',
  wa_message_id VARCHAR(128)  NOT NULL DEFAULT '',
  error         VARCHAR(1024) NOT NULL DEFAULT '',
  attempts      INT           NOT NULL DEFAULT 0,
  created_at    VARCHAR(40)   NOT NULL,
  sent_at       VARCHAR(40)   NULL,
  PRIMARY KEY (id),
  KEY idx_wa_messages_session (session_id, created_at DESC),
  CONSTRAINT fk_wa_messages_session FOREIGN KEY (session_id)
    REFERENCES wa_sessions (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE api_clients (
  id                VARCHAR(64)  NOT NULL,
  name              VARCHAR(190) NOT NULL,
  login_id          VARCHAR(190) NOT NULL,
  description       VARCHAR(512) NOT NULL DEFAULT '',
  secret_hash       VARCHAR(72)  NOT NULL,
  active            TINYINT      NOT NULL DEFAULT 1,
  token_ttl_seconds INT          NULL,
  no_expiry         TINYINT      NOT NULL DEFAULT 0,
  created_at        VARCHAR(40)  NOT NULL,
  updated_at        VARCHAR(40)  NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_api_clients_login (login_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE api_access_tokens (
  token_hash   VARCHAR(64) NOT NULL,
  client_id    VARCHAR(64) NOT NULL,
  issued_at    VARCHAR(40) NOT NULL,
  expires_at   VARCHAR(40) NULL,
  revoked_at   VARCHAR(40) NULL,
  last_used_at VARCHAR(40) NULL,
  PRIMARY KEY (token_hash),
  KEY idx_api_access_tokens_client (client_id),
  CONSTRAINT fk_api_access_tokens_client FOREIGN KEY (client_id)
    REFERENCES api_clients (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- +goose Down
DROP TABLE IF EXISTS api_access_tokens;
DROP TABLE IF EXISTS api_clients;
DROP TABLE IF EXISTS wa_messages;
DROP TABLE IF EXISTS wa_sessions;
