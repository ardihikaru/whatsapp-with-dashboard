# Changelog

## [Unreleased]

### Added
- **`WSM_TRUSTED_PROXIES`** — comma-separated CIDRs / bare IPs whose
  `X-Forwarded-For` the app will believe. Empty by default, which keeps the
  previous behaviour exactly: `auth.ClientIP` reads the connecting address and
  nothing else. It matters behind a reverse proxy, where every request arrives
  from the proxy and the login/API throttles therefore collapse onto one key —
  five bad logins from any one attacker used to throttle every user at once.
  The chain is walked right to left and the first untrusted hop wins, since a
  client controls the leftmost entries and the proxy appends the real address to
  their right.
- **`WSM_BIND_ADDR`** — the host address `docker-compose.yml` publishes on,
  defaulting to `127.0.0.1` as before. Previously hardcoded, which left no way
  to reach the service from a reverse proxy on another host without editing a
  tracked file.
- A "Behind a TLS reverse proxy" section in the README covering both, plus
  `WA_BASE_URL`, `WA_COOKIE_SECURE` and the do-not-strip-the-prefix rule.
- **Multi-year token presets** on `/admin/api-clients`: "Masa berlaku access
  token" now offers 1 tahun, 3 tahun and 5 tahun alongside the existing 1/7/30
  hari, so a long-lived integration no longer has to be expressed in seconds.
- **"Durasi khusus" is now a mode picker** rather than a bare seconds box:
  - *Berdasarkan tanggal* — a date picker. This is an ABSOLUTE cutoff, stored in
    the new `api_clients.token_expires_at` column: every token issued to that
    client expires at the end of the chosen day no matter when it was minted,
    and once the date has passed `POST /api/v1/auth/login` returns `401`
    (`api client access period has ended`) instead of handing back a token that
    is already dead.
  - *Durasi dalam tahun* / *Durasi dalam bulan* — a count, stored as a TTL
    (1 tahun = 365 hari, 1 bulan = 30 hari, matching the existing 30-hari
    preset).
  - *Durasi dalam detik* — the previous behaviour, kept so clients already
    holding an arbitrary TTL stay editable without their value being rounded.
- Migration `0003_api_client_expiry_date.sql` for the new column; the SQLite
  dev/test schema picks it up automatically.

## [0.1.0] — 2026-08-21

Forked from `laundry-saas-v1/apps/whatsapp-session-management` into a standalone
repository. The gateway itself — whatsmeow pairing, the send pipeline, the HTMX
dashboard, the bearer-token JSON API — is unchanged. What changed is everything
that tied it to the monorepo:

### Changed
- **Module path** is now `github.com/ardihikaru/whatsapp-with-dashboard`. No
  `go.work`, no `replace` directives: the module is self-contained.
- **MySQL app store** points at the shared `master-compose-ai` stack
  (`127.0.0.1:5306` on the host, `mysqldb-master:3306` in a container) with its
  own `wa_dashboard` schema and a least-privilege user, instead of the laundry
  dev stack's `laundry_wa` on `:6306`.
- **Browser login is a local admin credential** (`WSM_ADMIN_USER` +
  `WSM_ADMIN_PASSWORD_HASH`, bcrypt) instead of an OIDC code flow against
  `auth-service`. The app fails fast at boot when no credential is configured.
  Failed logins are throttled per IP.
- **Dockerfile** builds from the repo root as a single context, and pre-creates
  `/data` owned by the distroless `nonroot` uid — without which the container
  cannot write its SQLite store on a fresh volume.
- **Makefile** reads a local `.env` and targets the shared MySQL container.

### Added
- `docker-compose.yml` joining `master-compose-ai-net` as an external network,
  with an opt-in `migrate` profile for hosts without a Go toolchain.
- `scripts/bootstrap-db.sql.tmpl` + `make db-bootstrap` to create the schema and
  its scoped user.
- `cmd/hashpw` + `make hashpw` to generate the admin password hash.
- `.env.example`, `.dockerignore`, and a README covering the connection setup.
- Tests for the local login, the credential check and the throttle.

### Removed
- `internal/sso` (the OIDC relying party) and the `pkg/jwtx` dependency, along
  with the `WA_AUTH_ISSUER`, `WA_JWKS_URL`, `WA_AUDIENCE`, `WA_OAUTH_CLIENT_ID`
  and `WA_OAUTH_CLIENT_SECRET` settings and the `GET /auth/callback` route.
- The `platform_admin` role gate and the default sidebar link to the platform
  backoffice, neither of which has a counterpart here.
