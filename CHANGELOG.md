# Changelog

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
