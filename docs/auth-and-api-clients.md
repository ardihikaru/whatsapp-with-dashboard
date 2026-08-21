# Auth and API Clients

## Browser admin

The web UI is guarded by ONE local admin credential, supplied by the
environment: `WSM_ADMIN_USER` (default `admin`) and `WSM_ADMIN_PASSWORD_HASH`, a
bcrypt hash generated with `make hashpw`. `WSM_ADMIN_PASSWORD` is accepted as a
plaintext dev fallback and hashed once at boot. With neither set the app refuses
to start.

A successful sign-in creates an in-memory session cookie, so a restart signs the
operator out. Failed logins are rate-limited per client IP (5 attempts, then one
per 12 seconds).

That cookie is valid for the admin PAGES ONLY. The browser session and the API
bearer token below are separate, non-interchangeable credentials: a cookie is
rejected on `/api/v1`, and a bearer token is rejected on the pages.

## JSON API

Machine callers use local API-client credentials, presented in either of two
ways. Both resolve to the same client, and every `/api/v1` endpoint accepts
either:

- `Authorization: Bearer <token>` — obtained from `POST /api/v1/auth/login`.
  Verified with one SHA-256 and an indexed lookup, revocable per token, and
  subject to `token_ttl_seconds` / `no_expiry`.
- `Authorization: Basic base64(login_id:secret)` — the credential sent directly
  on every request. No token is minted, so `POST /api/v1/auth/logout` returns
  `400`: cutting off a Basic caller means deactivating the client or rotating
  its secret.

Bearer remains the better default for chatty or long-lived callers. Basic is for
cases where a two-step exchange is awkward.

Basic costs a bcrypt compare per request, so successful verifications are cached
in memory for 60 seconds, keyed by login id plus a SHA-256 of the secret (the raw
secret is never retained). Deactivating, deleting, renaming or rotating a client
drops its cached entries immediately, so revocation is not delayed by the cache.
Failed Basic attempts are throttled per IP, because a caller varying the secret
misses the cache by construction.

Basic sends the secret on every request: use TLS outside a trusted network.

To create the credentials:

1. Open `/admin/api-clients`.
2. Create a client with a name, login id, description, and token expiry.
3. Store the generated secret immediately. WSM only shows it once.
4. Call `POST /api/v1/auth/login` with the login id and secret.
5. Use the returned access token as `Authorization: Bearer <token>`.

WSM stores only hashes for API-client secrets and access tokens. It rejects
inactive clients, expired tokens, revoked tokens, browser session bearer
tokens, and the removed `WSM_API_TOKEN` static bearer flow.

## Expiry

API clients support these token expiry settings:

- `1 day`
- `7 days`
- `30 days`
- `custom duration`, stored as seconds
- `No expiration`

For `No expiration`, issued access tokens return `"expires_at": null` and stay
valid until revoked, the client is disabled, the secret is rotated, or the
client is deleted. The UI displays this setting as `Forever active`.

## Secret rotation

Rotating a client secret returns a new one-time secret and revokes all existing
tokens for that client. Existing callers must log in again with the new secret.
