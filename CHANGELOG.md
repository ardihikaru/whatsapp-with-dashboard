# Changelog

## [Unreleased]

### Added
- **`POST /api/v1/sessions/{id}/messages/group` — send to a WhatsApp group through ONE named session**
  (explicit routing, like `POST /api/v1/sessions/{id}/messages`): `group_code`/`master_only`/`allow_master`
  are ignored, so the message can never leave from another handset (the auto route
  `POST /api/v1/messages/group` picks a session and cannot promise which). The device must be a group
  member. wsm-mcp 0.3.0 adds the tool `wsm_session_send_group_message`. Lets Hikaru AI send an approved
  draft to a group from "Ardi New" only.
- **wsm-mcp 0.2.0: read tools for waxum's message search, per-chat history and
  contacts** — `waxum_messages_search`, `waxum_chat_messages`, `waxum_contacts`
  (new `waxum.Client` methods `SearchMessages`, `ChatMessages`, `Contacts`;
  read-only, session-scoped by `session_id`/`phone`). Lets a caller (Hikaru's
  MCP) find and read WhatsApp history by text or by chat.
- **wsm-mcp: one MCP (Streamable HTTP + gRPC) server exposing every WSM
  `/api/v1` route and every exported `internal/waxum.Client` method as a
  tool**, over a failover pool of hosts (mini-PC first, laptop backup),
  deployed independently on clinodb. Session-scoped tools resolve by
  `session_id` or `phone` (the same phone has a different session_id per
  host); destructive/raw-waxum-write tools refuse unless
  `WSM_MCP_ALLOW_DANGEROUS=true`. See `mcp/README.md`, `mcp/TUTORIAL.md`, and
  CLAUDE.md's "wsm-mcp must track every WSM and waxum change" section for the
  sync rule two new tests (`internal/mcp/registry/sync_test.go`) enforce.

### Fixed
- **A suspended/resumed host could see waxum rate-limit every request for
  hours, and WSM used to honour that verbatim.** Observed live 2026-09-13:
  after the laptop resumed from suspend, waxum's tower_governor answered
  every request with `429` and `Retry-After: 7981` (~2.2 hours — a
  monotonic-clock discontinuity in the governor, not a genuinely drained
  token bucket). `internal/waxum/client.go`'s `defaultRetryDelay` honoured
  that header with no upper bound, so a single retried request could sleep
  for hours, and `StartPairing` flips a session to `pairing` BEFORE making
  its setup calls, so the session sat wedged with `wa_sessions.last_status`
  frozen at `new` for as long as the retry slept, with every further
  `POST /sessions/{id}/pair/start` answering a silently-ignored 409 and
  nothing logged anywhere. `defaultRetryDelay` now clamps any honoured
  `Retry-After` to 30s (matching `internal/backoff`'s own ceiling; a value at
  or below the cap is still honoured verbatim), a 429 retry now logs a
  `slog.Warn` (method, path, attempt, delay) so the wait is visible, and
  `StartPairing`'s own setup calls (`CreateSession`, and the `Connect` on the
  "already exists" path) are now bounded by a 45s deadline independent of
  waxum's response — a wedged backend now surfaces as a 503/500 on
  `pair/start` within about a minute instead of hanging the session in
  `pairing` for hours.
- **A brand-new waxum session could fail pairing with "Internal error: migration
  error" and no client left at all.** waxum's `POST /sessions` auto-connects a
  freshly created session in the background; `StartPairing` used to also call
  an explicit `Connect` immediately afterward, and on a NEW session that raced
  the auto-connect bot's own store migrations, spawning a second bot on the
  same fresh `whatsapp.db`. `StartPairing` now skips that explicit `Connect`
  whenever `CreateSession` actually created the row, and only still issues it
  on `waxum.ErrConflict` (a re-pair, where the store already exists and there
  is nothing to race). Separately, `pairPump` now reads waxum's own
  `pair.last_error` and, once it is sustained for two consecutive polls with
  no live client for the attempt at all (`socket_alive: false`,
  `reachability: null`) and no QR ever served, fails the pairing immediately
  with waxum's own message instead of showing "Waiting for WhatsApp…" for the
  full pairing deadline.

### Added
- **`/send`'s automatic routing now has a "Master sessions only" toggle.**
  The routing engine (`wa.Route.MasterOnly`) and the JSON API's `master_only`
  field already supported this end-to-end; the dashboard's own compose page
  had no way to ask for it. Both the text and media panels now carry a
  "Master sessions only" checkbox (`master_only`, next to "Routing group"),
  composable with the existing routing-group picker exactly like the API's
  `group_code` + `master_only` already compose — leaving it unchecked
  behaves exactly as before.
- **Inline "Edit group" on `/sessions`.** A row's routing `group_code` can now
  be changed without leaving the list: an "Edit group" button (every row,
  master or not — `group_code` stays deliberately uncoupled from `is_master`)
  loads an editor into a slot below the table, `#session-group-editor`,
  OUTSIDE the tbody that re-polls itself every 5s — the same reason the Ops
  page's own routing-group card lives outside its own polled panel. Saving
  posts to the existing `/sessions/{id}/group-code` route with a hidden
  `view=list` field; on success the response carries `HX-Trigger:
  sessions-changed`, which the rows tbody now also listens for
  (`hx-trigger="load, every 5s, sessions-changed from:body"`) so the badge
  updates immediately instead of waiting out the next poll. The Ops page's
  own save path is unchanged byte-for-byte.
- **`group_code` routing scopes.** A session can now carry an optional
  `group_code`, and the four auto-routing endpoints (`POST /api/v1/messages`,
  `/messages/group`, `/messages/media` — a multipart form field there — and
  `/contacts/check`) accept one.
  ⚠ **Read this first: assigning a group code REMOVES that session from default
  automatic routing.** The match is exact in both directions — `""` matches only
  `""`, a named group only that same name — so a caller that sends no
  `group_code` will stop seeing a session the moment one is assigned to it. That
  is the entire point of the feature and also the way it will most often bite.
  Within a scope the master rules are unchanged: masters are excluded unless
  `allow_master`, and `group_code` + `master_only` means *that group's* master.
  Like `master_only`, a scope has **no fallback by design** — a `group_code` send
  whose group is offline answers 409 even while a dozen other sessions are
  connected, because quietly routing through one of them would defeat the only
  reason to ask for a scope. The 409 bodies say which of those two situations it
  is, since a scoped failure on a healthy gateway must not read as an outage.
  Codes are trimmed, lower-cased, limited to `[a-z0-9_-]` and 32 characters;
  anything else is a 400 rather than a silently separate cluster. Every send
  response now also reports the decision it made (`session_group_code`, always
  present and `""` when ungrouped; `session_is_master`; `routing`). Operators set
  it on `/sessions/new` and `/sessions/{id}/ops`, and `/sessions` gained a Group
  column; `PUT /api/v1/sessions/{id}/group-code` is the machine equivalent and is
  deliberately not published at the edge. Built because master flags are being
  clustered: one gateway now hosts several independent routing groups, each with
  its own master (`wa.Route`, `wa.EligibleForRoute`, `wa.Manager.SelectForRoute`).
  One incidental change: `SelectMaster` used to rank on `(queue, id)` only while
  every other pick used four keys, contradicting its own doc comment — the two
  are now unified, which is observable only when several sessions in one scope
  are flagged master.
- **`master_only` send routing.** `POST /api/v1/messages` and
  `POST /api/v1/messages/group` accept `master_only: true`, which pins the send
  to the master session and answers **409** when the master is offline — it
  never falls back to another session. It is NOT the same as `allow_master`,
  and the two are easy to confuse: `allow_master` only *widens* automatic
  selection (`allowMaster || !snap.IsMaster`), so a master becomes eligible
  *alongside* every ordinary session and an ordinary one may still win. No
  value of it means "only the master". Added for ops alerting, where "which
  number paged me at 3am" is part of reading the page.
  ⚠ This mode has **no redundancy by design**: on 2026-09-11 the production
  master (6287748383890) was disconnected while three other sessions were live,
  so every `master_only` send would have 409'd. Anyone using it for alerting
  must monitor the master session's own connectivity. Omitting the field leaves
  every existing caller auto-routing exactly as before (`wa.Manager.SelectMaster`,
  `handler.selectForSend`, `noMasterRouteMessage`).
- **Convo Scheduling.** Self Convo (a scripted two-party conversation between
  two paired sessions) can now run on a recurring cadence — daily, weekly, or
  every N days — to several receivers at once, and optionally chain a
  finished conversation into a follow-up drawn from a weighted, cyclic
  template correlation graph instead of simply stopping. Backed by a
  50-topic, ~2,400-template corpus (`internal/selfconvo/corpus`,
  `cmd/convoseed`, `make convo-seed`) generated from a small number of
  hand-authored packs rather than one script per topic. New operator UI at
  `/self-convo/schedules` (create/edit/pause/resume/stop/delete) and
  `/self-convo/templates`' topic filter. Gated by the same
  `WSM_SELF_CONVO_ENABLED` flag Self Convo already used — no new flag. See
  `docs/brief-convo-scheduling.md` for the design reasoning (why fan-out is
  one run per receiver, why the link graph is cyclic and depth-capped, why
  resume never back-fills a missed occurrence).
- **Remote-DB deployment mode.** `docker-compose.remote-db.yml` plus
  `make up REMOTE_DB=1` run the app on a host that does **not** carry the shared
  `master-compose-ai` stack: it undeclares that stack's external network and
  points MySQL at the address `.env` already holds, instead of the container DNS
  name `mysqldb-master` that resolves only on the stack's own host. Without it
  `up` fails with `network declared as external, but could not be found`, which
  reads as "the shared stack is down" and means the opposite. The overlay uses
  `!reset` rather than `[]` because compose *merges* a service's networks, so
  anything weaker leaves the network attached and the lookup still runs. All
  container targets now go through one `$(COMPOSE)` variable so the mode cannot
  be applied to `up` and forgotten on `logs`.

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
