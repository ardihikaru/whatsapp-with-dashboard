# Changelog

## [Unreleased]

### Fixed
- **`/sessions` scrolled the whole page sideways on a phone.** It held the only
  bare `<table class="table">` in the app; every other table page wraps in
  `.table-scroll`. With real rows the unbreakable JID
  `6285190679041:12@s.whatsapp.net` forced the table to 719px inside a 336px
  card and the document to 755px on a 360px viewport. Now wrapped, matching
  `/logs` and `/admin/api-clients`.

### Added
- **Phone-portrait navigation.** Below 900px the sidebar was `display: none`
  with nothing in its place, so Dashboard, Sessions, Send, Logs, API Clients and
  API Docs were all unreachable on a phone. The same sidebar markup now becomes
  an off-canvas drawer opened by a hamburger in the header, keeping every
  destination, all four section subheaders and the footer link. Closes on the
  backdrop, on `Escape`, on choosing a destination, and on crossing back to
  desktop width; carries `aria-expanded`/`inert` and returns focus to the
  toggle. New `web/static/css/responsive.css` (loaded **ninth**, so the eight
  files that concatenate to the historical `minimal.css` stay byte-for-byte
  intact) and `web/static/js/nav-drawer.js`.
  - Progressive: the drawer CSS is scoped to `html[data-nav-js]`, which the
    script sets on load. Without JavaScript the sidebar renders as a block above
    the content, so the menu is reachable either way.
  - Narrow-viewport fixes along the way: `.chart-grid` used
    `minmax(360px, 1fr)`, which overflowed every viewport under ~392px; the
    header now ellipsises its title and drops the username below 480px (it is
    shown in the drawer instead); filters stack rather than squeezing.
- **`WSM_APP_DSN=sqlite:<path>`** — a dev/e2e-only app store. `db.OpenAppStore`
  puts the app tables in SQLite via the existing `EnsureSchema`, so the binary
  boots with no MySQL reachable and the browser tests never touch the SHARED
  production server. The app logs a warning on every boot that takes this branch.
- **Playwright browser tests** (`e2e/`, `playwright.config.ts`) at 390×844,
  360×740 and 1280×800, signing in as the real admin from the gitignored
  `_data/paswd`. `make e2e-install`, `make e2e`, `make e2e-headed`,
  `make e2e-report`, `make e2e-serve`; boot via `scripts/e2e-env.sh` on port
  8801. The credential is typed only in the setup project, which runs with
  trace, screenshot and video off.
- **`cmd/e2eseed`** — seeds a dev/e2e SQLite store with realistic content (3
  sessions, 24 API clients, 45 messages with long bodies, unbreakable URLs and
  long tenant names). It refuses any DSN without the `sqlite:` prefix, with no
  override flag, so it can never reach the shared production MySQL. The browser
  suite previously ran against four empty tables, which made its layout
  assertions close to meaningless — a table with no rows cannot overflow.
- **`CLAUDE.md`, `.claude/agents/senior-fullstack-htmx-go.md` and the
  `htmx-go-fullstack` skill** — the repo's working rules (a test that fails
  without the change; evidence rather than opinion) plus the conventions and
  hazards that were previously only in commit messages and code comments.

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
