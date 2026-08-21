-- 0003_api_client_expiry_date.sql — let an API client's token expiry be an
-- ABSOLUTE DATE, not only a relative duration.
--
-- token_ttl_seconds answers "how long does a freshly issued token live?", which
-- cannot express "this integration's access ends on 2027-12-31": a token minted
-- the day before that date would still get the full TTL and outlive it. The new
-- column is the cutoff itself — when set, Login() stamps the token with this
-- instant verbatim instead of now+TTL, and a login attempted after it is refused.
--
-- Nullable, so every existing row keeps its duration semantics untouched. The
-- three expiry modes are mutually exclusive and resolved in this order:
--   no_expiry=1        → token never expires
--   token_expires_at   → token expires exactly then
--   token_ttl_seconds  → token expires at issue time + TTL (the default, 1 day)
--
-- RFC3339/UTC VARCHAR(40), matching every other timestamp in this schema.

-- +goose Up
ALTER TABLE api_clients
  ADD COLUMN token_expires_at VARCHAR(40) NULL AFTER no_expiry;

-- +goose Down
ALTER TABLE api_clients
  DROP COLUMN token_expires_at;
