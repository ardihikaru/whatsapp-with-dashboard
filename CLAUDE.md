# whatsapp-with-dashboard — working rules

A Go + HTMX WhatsApp gateway and dashboard. Server-rendered `html/template`,
stock `net/http`, hand-written CSS. No SPA, no build step, no framework.

---

## Two hard rules

### 1. Every behavioural change ships with a test that fails without it

Not "a test passes". A test that goes **red when the change is reverted** and
green with it. Write it, watch it fail, then make it pass. If you cannot make it
fail, you have not tested the thing you changed.

```
go test ./...      # or: make test — SQLite-backed, no MySQL needed
go vet ./...
```

Applies to CSS and templates too: markup the layout depends on is asserted in
`internal/handler/nav_mobile_test.go`, and stylesheet wiring in
`internal/renderer/responsive_test.go`. A "just a template tweak" that silently
drops an element is exactly the class of bug those catch.

### 2. Evidence, not opinion

Every claim about this codebase cites something checkable: command output, a
`file:line`, a screenshot, an HTTP status. Never report a result you did not
observe.

- Banned as conclusions: "should work", "looks correct", "probably fine",
  "this will fix it". They describe a hope, not a finding.
- "The tests pass" requires the test output. "The page renders" requires the
  status code or the screenshot.
- If something is unverified, say so plainly and say why. An honest gap beats a
  confident guess, and a guess presented as a result is the worst outcome
  available.
- When a tool or another agent reports something surprising, verify it before
  repeating it. `WSM_TRUSTED_PROXIES` was once reported "missing from
  `.env.example`" and is on line 50.

---

## Hazards this repo has already been bitten by

**`_data/` holds real secrets.** `_data/paswd` is the admin password in
plaintext; `_data/database-cred.json` is **root** on the SHARED production
MySQL. Both are gitignored. Never echo them, never paste them into a file, a
log, a test fixture, a commit or an evidence note. Read them programmatically
(`scripts/e2e-env.sh`, `e2e/auth.setup.ts`) and let the value die with the
process.

**Production MySQL is shared and is not a test target.** `10.10.0.2:5306` is
`master-compose-ai`, holding several other products' data. Do not create a
schema there, do not migrate it, do not point a dev binary at `wa_dashboard`.
For local runs use `WSM_APP_DSN=sqlite:…` (see `internal/db.OpenAppStore`);
`make e2e` does this for you.

**`web/templates/login.html` duplicates `base.html`'s entire `<head>`.** Any
stylesheet or script change has to land in **both**. Guarded by
`TestEveryHeadTemplateLinksTheResponsiveStylesheet`.

**Stylesheet load order is load-bearing.** `base.html` documents that
concatenating the first eight CSS files reproduces the old `minimal.css`
byte-for-byte, and `charts.css` deliberately carries `@media` rules that
override `layout.css`. `responsive.css` is a ninth file loaded last and
overrides by cascade. Do not reorder, and prefer adding a later file to editing
those eight.

**The app and its MySQL no longer share a host.** The app runs on `10.10.0.4`;
the schema stays on the shared MySQL at `10.10.0.2:5306`. So
`docker-compose.yml`'s external `master-compose-ai-net` and its
`WSM_MYSQL_HOST: mysqldb-master` are both wrong there, and `up` fails with
`network declared as external, but could not be found` — which reads as "the
shared stack is down" and is not. `docker-compose.remote-db.yml`
(`make up REMOTE_DB=1`) undoes both. Note `!reset`, not `[]`: compose MERGES a
service's networks, so anything weaker leaves `infra` attached and the lookup
still runs. Every container target goes through `$(COMPOSE)` so the mode cannot
be applied to `up` and forgotten on `logs`; `compose_remote_db_test.go` holds
all of that, including a service added to the base file and forgotten here.

**Two hand-maintained copies of one schema.** `migrations/*.sql` (goose, MySQL,
owns production) and `internal/db.EnsureSchema` (SQLite, owns tests and the e2e
run). A migration added to only one of them makes every SQLite-backed test pass
against a schema production does not have. Change both, in the same commit.

**`make` mangles bcrypt hashes.** `$2a$10$…` gets `$2`/`$1` expanded as make
variables during `export`, so the hash arrives corrupted and every login fails
silently. See the comment above the `run` target. Generate and export the hash
inside a recipe's own shell, never through make's `export`.

**`overflow-x: hidden` on both `html` and `body` kills vertical scrolling.**
Each becomes its own scroll container: `body.scrollHeight` grows to the real
content while `documentElement.scrollHeight` stays clamped to the viewport, so
`window.scrollTo()` does nothing and nothing below the fold is reachable. It
also clamps `documentElement.scrollWidth`, which silently disarms any overflow
test measuring that value. Fix overflow at source; see the comment at the end of
`responsive.css`.

**Measure layout against the CONFIGURED viewport, never `window.innerWidth`.**
Under mobile emulation `innerWidth` inflates to match overflowing content — a
360px phone rendering a 755px table reports 756 — so `right > innerWidth` can
never fail. Use `page.viewportSize().width`.

**`networkidle` is not "the page is ready".** `/sessions` fills its tbody over
HTMX and shows a "Loading…" placeholder meanwhile; measuring then finds an empty
table that cannot overflow. `e2e/overflow.spec.ts` has a `waitForContent()`
helper for this.

**An e2e run against empty tables proves very little.** `cmd/e2eseed` exists so
the browser suite has real content to break on. Two real bugs were invisible
until it did.

**The WhatsApp session is a hard singleton.** One device websocket, one SQLite
crypto store. No replicas, no rolling restarts, and never
`docker compose down -v` — the volume is the paired device's identity, and
losing it means re-pairing the handset by QR.

---

## Layout

| Path | What lives there |
|---|---|
| `cmd/app` | Entry point. Boot order is documented in `main.go` and is a hard requirement. |
| `cmd/hashpw` | bcrypt hash generator; reads stdin, never argv. |
| `internal/auth` | Admin credential, in-memory sessions, cookies, `RequirePage`/`RequireAPI`, `ClientIP`, throttles. |
| `internal/config` | Env-only config. Fails fast on anything malformed. |
| `internal/db` | The two stores + the SQLite app-schema mirror. |
| `internal/domain/*` | Raw `database/sql` repositories. No ORM. |
| `internal/handler` | Routes (`routes_ui.go`, `routes_api.go`) and handlers. |
| `internal/renderer` | `pageDefs` registry mapping a page name to its file set and root template. |
| `internal/wa` | whatsmeow manager, sender, pairing, rate limits. |
| `web/templates`, `web/static` | Embedded via `assets.go`. Not read from disk at runtime. |
| `e2e/` | Playwright browser tests. |
| `docs/e2e-evidence.md` | Append-only log of manual/browser verification runs. |

## Conventions

- **Router**: stock `http.ServeMux` with Go 1.22 method+wildcard patterns. No
  chi/gin/echo. UI routes are mounted under `BasePath`; `/api/v1` is root-mounted
  and deliberately not routed publicly.
- **URLs in templates**: always `{{u "/path"}}`. A hardcoded `/static/...`
  breaks every deployment mounted under a sub-path.
- **CSS**: tokens only. Every colour is a `:root` variable with a
  `[data-theme="dark"]` counterpart; a raw hex in a rule breaks dark mode.
- **JS**: vanilla, vendored, no CDN. HTMX 2.0.4 and ECharts are files in
  `web/static/js`. New behaviour follows `charts-init.js` / `nav-drawer.js`: a
  plain IIFE.
- **Tests**: in-package (`package handler`, not `handler_test`), long
  descriptive names that read as claims, `httptest` driving the handler directly
  (no `httptest.NewServer`), `t.TempDir()`, and clocks injected as parameters
  rather than mocked globally.
- **Auth surfaces do not mix.** A browser cookie is rejected on `/api/v1` and a
  bearer token is rejected on pages. That is deliberate and tested.
