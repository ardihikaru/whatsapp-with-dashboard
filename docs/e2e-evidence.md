# E2E Evidence

## 2026-07-06 API-client smoke

Base URL: `http://localhost:8800`

API client login id: `taskmgr`

Secret handling: the one-time API-client secret was used only for the login
request and was not written into this evidence file.

Results:

| Check | Result |
|---|---|
| `GET /api/v1/health` | `200`, body reported `status=ok`, `sessions=1`, `connected=1` |
| `POST /api/v1/auth/login` | `200`, returned bearer token length `43`, `expires_at=null` |
| `GET /api/v1/sessions` with bearer token | `200`, returned `1` session, `1` connected |
| `GET /api/v1/sessions` without token | `401` |
| `GET /api/v1/sessions` with invalid token | `401` |
| `POST /api/v1/messages` | Skipped real delivery: a connected session exists, but `WSM_TEST_TO` was not set |
| `POST /api/v1/auth/logout` | `204` |
| `GET /api/v1/sessions` with revoked token | `401` |

Notes:

- The API-client no-expiry setting was verified through `expires_at=null`.
- Revocation was verified by logging out and retrying a protected endpoint with
  the same bearer token.
- Real WhatsApp delivery was intentionally not attempted without an explicit
  test destination number in `WSM_TEST_TO`.

## 2026-07-06 API-client real-send smoke

Base URL: `http://localhost:8800`

API client login id: `taskmgr`

Destination: `WSM_TEST_TO=+62 851-9067-9041`

Secret handling: the API-client secret was used only for the login request and
was not written into this evidence file.

Message text prefix: `WSM API-client real-send E2E`

Results:

| Check | Result |
|---|---|
| `GET /api/v1/health` | `200`, body reported `status=ok`, `sessions=1`, `connected=1` |
| `POST /api/v1/auth/login` | `200`, returned bearer token length `43`, `expires_at=null` |
| `GET /api/v1/sessions` with bearer token | `200`, returned `1` connected session |
| `POST /api/v1/messages` automatic routing | `200`, `status=sent` |
| Selected session | `fd559ba8f0278ec233ced9d2b4b24b75`, `nomer HP ardi` |
| Message id | `47cde72ee689b6ba1abf71e410b284df` |
| WhatsApp message id | `3EB0E05D0CD94B0F440594` |
| `GET /api/v1/sessions/{id}/messages?limit=5` | `200`, matched message row with `status=sent`, `attempts=1` |
| `POST /api/v1/auth/logout` | `204` |

Notes:

- This run exercised the automatic load-balanced send endpoint against a real
  connected WhatsApp session.
- The persisted audit row matched the API response message id and WhatsApp
  message id.

## 2026-08-22 mobile-portrait navigation smoke

Base URL: `http://127.0.0.1:8801` (a throwaway instance booted by
`scripts/e2e-env.sh`; app tables in SQLite, **no MySQL contacted**)

Browser: Playwright 1.62.1, Chromium 151.0.7922.34, headless

Viewports: `mobile-portrait` 390×844 · `mobile-small` 360×740 · `desktop`
1280×800 (the two phone projects run with `isMobile` and `hasTouch`)

Browser user: admin `ardihikaru`, signed in through `POST /login`

Secret handling: the admin password was read from `_data/paswd` and used only
for the login request. The `setup` project runs with `trace`, `screenshot` and
`video` forced off so no DOM snapshot could capture it, and it was not written
into this evidence file.

Results:

| Check | Result |
|---|---|
| `GET /api/v1/health` (readiness) | `200` |
| `POST /login` as `ardihikaru` | `303`, `wsm_session` cookie set |
| `GET /dashboard` authenticated | `200`, drawer markup present |
| Playwright suite, all projects | **39 passed, 0 failed** (46.7s, system libraries, default headless shell) |
| Phone: sidebar off-screen on load, hamburger visible | pass, both viewports |
| Phone: tap hamburger → all 6 destinations visible | pass — Dashboard, Sessions, Send Message, Message Log, API Clients, API Docs |
| Phone: all 4 section subheaders survive | pass — Overview, WhatsApp, Admin, Docs |
| Phone: every destination hit-testable at ≥36px | pass, both viewports |
| Phone: `aria-expanded` false → true → false | pass |
| Phone: backdrop tap closes | pass (`pointer-events` auto → none) |
| Phone: `Escape` closes and returns focus to the toggle | pass |
| Phone: closed drawer carries `inert` | pass |
| Phone: nav link navigates **and** closes the drawer | pass (`/logs`) |
| Phone: `body { overflow: hidden }` behind an open drawer | pass |
| No horizontal scroll on 6 pages × 3 viewports | pass (`scrollWidth <= innerWidth + 1`) |
| Wide log table scrolls inside `.table-scroll`, not the document | pass |
| Desktop: sidebar in flow, hamburger hidden, backdrop `display:none` | pass |
| `go test ./...` | pass |
| `go vet ./...` | clean |

Screenshots: written to `docs/e2e-evidence/` — `*-dashboard-closed.png`,
`*-drawer-open.png`, `*-logs.png`, `*-logs-table.png`. They are NOT tracked in
git (8.6MB of regenerable binaries); recreate them with `make e2e-evidence`.

Notes:

- Before this change the only mobile rule in the codebase was
  `@media (max-width: 900px) { .sidebar { display: none } }` at the end of
  `charts.css`, which removed the navigation on phones with nothing in its
  place. All six destinations were unreachable in portrait.
- The drawer is progressive: `nav-drawer.js` sets `data-nav-js` on `<html>`, and
  only then does the off-canvas CSS apply. With JavaScript unavailable the
  sidebar renders as a block above the content, so the menu stays reachable.
- Three failures during development were **test** bugs, not product bugs, and
  were fixed in the specs: `toBeInViewport()` returned mid-transition while the
  drawer was still sliding; `.nav-backdrop` is `position: fixed; inset: 0` so it
  is never geometrically off-screen (its closed state is `pointer-events: none`);
  and the wide-table check used `.table-scroll.first()`, which selects a
  "Rincian" breakdown card — a plain `.table` with no width floor — instead of
  the message table. It now selects `.table-scroll:has(.table-wide)`.
- Chromium's shared libraries were initially missing on this host and were
  unpacked into a private prefix, because an unrelated `apt install` held the
  dpkg lock. `npx playwright install-deps` has since been run, so the suite was
  re-verified end to end against the SYSTEM libraries with that prefix removed:
  **39 passed, 0 failed** on the default `chrome-headless-shell`. The private
  prefix is gone; `scripts/e2e-run.sh` keeps the no-root fallback documented for
  other hosts, where it is now a no-op.
- `make` is not installed on the machine this run was performed on, so the
  targets were exercised through their underlying commands
  (`go test ./...`, `./scripts/e2e-run.sh`).

## 2026-08-22 mobile-portrait nav smoke — re-run with seeded data (supersedes the entry above)

Base URL: `http://127.0.0.1:8801` (throwaway instance, app tables in SQLite,
**no MySQL contacted**)

Browser: Playwright 1.62.1, Chromium, system libraries via
`npx playwright install-deps`

Browser user: admin `ardihikaru`. Secret handling: unchanged — the password was
used only for the login request and is not written here.

**Why this entry exists.** The run above was performed against four EMPTY
tables. Its strongest claim — "no horizontal scroll on 6 pages × 3 viewports" —
was close to vacuous, because a table with zero rows cannot overflow. Seeding
realistic content (`cmd/e2eseed`: 3 sessions, 24 API clients, 45 messages with
long bodies, unbreakable URLs and long tenant names) exposed **two real bugs**
that the earlier run reported as passing.

Results:

| Check | Result |
|---|---|
| `go test -count=1 ./...` | pass (incl. new `cmd/e2eseed` guard tests) |
| `go vet ./...` | clean |
| Playwright, all projects, cold start | **51 passed, 0 failed** (1.2m) |
| Seeded content reaches the pages | 45 message rows, badges sent/failed/queued, "Menampilkan 1–20 dari 24" pager |
| Phone: drawer opens, all 6 destinations + 4 subheaders | pass |
| Phone: every page scrolls to its bottom | pass (new check) |
| Phone: no element wider than the configured viewport | pass (new check) |
| Every table inside `.table-scroll` | pass (new check) |
| Wide log table overflows its card (1590px in a 318px scroller) | pass |
| Desktop unchanged | pass |

### Bug 1 — the phone layout could not scroll at all (introduced by this change)

`responsive.css` ended with a belt-and-braces
`@media (max-width: 900px) { html, body { overflow-x: hidden } }`. Setting
`overflow-x` on **both** `html` and `body` makes each its own scroll container.
Measured on `/logs` at 360×740:

```
body.scrollHeight            = 11935
documentElement.scrollHeight =   740   (clamped to the viewport)
window.scrollTo(0, 100000) -> window.scrollY = 0
last table row at top = 11674 — unreachable
```

Only the first screenful of every page was viewable. The rule is gone; sideways
scroll is prevented at source instead, and the file carries a comment saying why
it must not come back.

**It also disarmed the test that should have caught it**: the same clamping pins
`documentElement.scrollWidth` to the viewport, so the overflow assertion was
reading the symptom as proof of health.

### Bug 2 — `/sessions` overflowed sideways (pre-existing)

`web/templates/sessions/list.html` held the only bare `<table class="table">` in
the app; every other table page wraps in `.table-scroll`. With real rows the
unbreakable JID `6285190679041:12@s.whatsapp.net` forced the table to 719px
inside a 336px card, and the document to **755px on a 360px phone**. Now wrapped
in `.table-scroll` with `.table-wide`, matching `/logs` and `/admin/api-clients`.

### Three test weaknesses fixed (each verified by re-introducing the bug)

1. `.table-scroll.first()` selected a "Rincian" breakdown card — a plain
   `.table` with no width floor — instead of the message table. Now
   `.table-scroll:has(.table-wide)`.
2. Overflow was measured against `window.innerWidth`, which **inflates on mobile
   emulation to match overflowing content**: a 360px phone rendering a 755px
   table reports `innerWidth` 756, so `right > innerWidth` can never fail. Now
   measured against Playwright's configured `page.viewportSize().width`.
3. `waitForLoadState("networkidle")` settled while `/sessions` still showed its
   "Loading…" placeholder, so the layout was measured on an empty table. A
   `waitForContent()` helper now waits for the HTMX-filled rows.

Notes:

- The two `WARN boot reconnect failed` lines are expected: seeded sessions carry
  a JID but the local SQLite whatsmeow store holds no crypto device for them.
  They do not affect the pages under test.
- `cmd/e2eseed` refuses any DSN without the `sqlite:` prefix, with no override
  flag, and redacts a rejected DSN's password. Four tests cover that guard.

## 2026-08-23 production deploy verification

Target: **https://wa.mfardiansyah.id** (live), and the private origin
`http://10.10.0.2:18801` over WireGuard.

Deployed commit: `1c535ac` (merge of `feat/mobile-portrait-nav`). Image rebuilt
and container recreated with `docker compose up -d --build`; the named volume
holding whatsmeow's crypto identity was preserved (no `down -v`).

Browser user: admin `ardihikaru`. Secret handling unchanged — used only for the
login request, never written here.

| Check | Result |
|---|---|
| `/api/v1/health` (private) | `200`, `{"connected":1,"sessions":1,"status":"ok"}` |
| WhatsApp session after restart | **reconnected** — `connected:1`, no re-pairing |
| `/static/css/responsive.css` private / public | `404` → `200` / `404` → `200` |
| `/static/js/nav-drawer.js` private / public | `404` → `200` / `404` → `200` |
| Served `responsive.css` is the real file | contains the "DELIBERATELY NOT HERE" guard comment |
| `/login` links `responsive.css` | yes (the duplicated-`<head>` trap holds in prod) |
| `POST /login` as `ardihikaru` (public, HTTPS) | `303`, session cookie set |
| `GET /dashboard` drawer markup | all 8 markers present |
| Playwright vs LIVE production, 390×844 + 360×740 + 1280×800 | **53 passed, 0 failed** (1.4m) |

Notes:

- **A `git pull` on the VM is not a deploy.** Templates, CSS and JS are compiled
  into the binary by `go:embed`, so the pulled source sat at `1c535ac` while the
  running image was still the one built 2026-08-22 11:06 UTC and the new assets
  returned 404. Only `docker compose up -d --build` changed that. Check
  `docker images whatsapp-with-dashboard:local --format '{{.CreatedSince}}'`
  against the commit, not `git log`, when confirming a release.
- **Browser tests against production must use HTTPS.** Prod sets
  `WA_COOKIE_SECURE=true`, so over plain `http://10.10.0.2:18801` a real browser
  silently declines to store `wsm_session` and every test fails at login, while
  curl (which does not enforce the Secure attribute the same way) succeeds. The
  private port is fine for curl and for the health probe, not for a browser.
- **The public readiness probe cannot be `/api/v1/health`**: the edge returns
  404 for `/api/v1/*` by design. `WSM_E2E_NO_SERVER=1` skips Playwright's
  `webServer` block so an already-deployed target can be tested directly.
  (Amended 2026-09-10: `/api/v1/health` is now deliberately published,
  unauthenticated, for exactly this purpose — see
  `docs/deploy-nginx-wa.conf.example`'s `wa_health` zone. This note described
  the config as it stood on 2026-08-22 and no longer describes the live edge;
  left in place as the record of what was true then.)

## 2026-08-23 host migration 10.10.0.2 → 10.10.0.4

The app tier moved to a second private VM. The MySQL schema did **not** move: it
stays on the shared `master-compose-ai` stack at `10.10.0.2:5306`, which the app
now reaches over WireGuard instead of by container DNS.

Deployed commit: `40e5c24`, plus the `docker-compose.remote-db.yml` overlay and
the `REMOTE_DB=1` Makefile wiring this migration required. Image built on the new
host before any downtime began.

Target: **https://wa.mfardiansyah.id** (live), private origin
`http://10.10.0.4:18801`. Browser user: admin `ardihikaru`, credential read from
`_data/paswd` by `e2e/auth.setup.ts` and never written here.

Downtime **03:39:39Z → 03:41:54Z (2m15s)**, from stopping the old container to
the first `200` through the edge.

| Check | Result |
|---|---|
| Grants permit the new origin | user is `wa_dashboard@%` — host-agnostic, so a new origin needs no grant change |
| `10.10.0.2:5306` reachable from the new host | TCP open |
| Old container stopped cleanly (`stop`, never `down -v`) | WAL checkpointed: `wsm.db-wal`/`-shm` gone, `wsm.db` alone remains |
| Identity volume copied byte for byte | `sha256 8fc776b2…f2e5` identical on both hosts, ownership `65532` preserved |
| WhatsApp session on the new host | **`Successfully authenticated`** — no QR, no re-pairing |
| `/api/v1/health` (private, `10.10.0.4:18801`) | `200`, `{"connected":1,"sessions":1,"status":"ok"}` |
| `nginx -t` on the shared edge before reload | `syntax is ok` / `test is successful` |
| `/login`, `/docs`, `/openapi.yaml` (public) | `200`, `200`, `200` |
| `/api/v1/health` (public) | `404` — still no public API route |
| `/` (public) | `302` to the login page |
| Old origin `10.10.0.2:18801` | connection **refused** — nothing serving behind the switch |
| Playwright vs LIVE production, 390×844 + 360×740 + 1280×800 | **53 passed, 0 failed** (1.8m) |

Notes:

- **Same 53 as the pre-migration run above**, so the move is a move: no test
  changed its verdict crossing hosts.
- **The app log is the proof that the edge switched**, not the `200`. The public
  probes appear in the new container's log at `03:41:54`–`03:41:55` with the
  paths and status codes that came back. A `200` alone cannot tell the two hosts
  apart; a request that never reached the new app would still have served it
  from the old one.
- **Nothing was created on the shared MySQL.** The only statement issued against
  it was `SHOW GRANTS`, read-only, and the app's own traffic.
- **The old container was left `exited`, not removed, and its volume intact.**
  That was the rollback: start it there and point the edge's `map` back.
  **Retired the same day** — see the follow-up below.
- `docker compose config` interpolates `env_file`, so its output carries the
  admin bcrypt hash and the MySQL password in clear. Grep it for the keys you
  want; never page the whole thing into a terminal or a log.

### Follow-up, same day: the rollback was retired

After ~2h of live traffic on the new host (session re-authenticated at
`04:54:17Z`, health still `{"connected":1,"sessions":1,"status":"ok"}`), the old
container and the volume `whatsapp-with-dashboard-sqlite` on `10.10.0.2` were
deleted on request.

| Check | Result |
|---|---|
| Any other container referencing that volume | none — only the stopped `whatsapp-with-dashboard-app-1` |
| Container removed | `docker compose down` in this repo's dir on .2; the external network and the shared stack untouched |
| Volume removed | `docker volume rm whatsapp-with-dashboard-sqlite`, by name, not `down -v` |
| Gone from the daemon | `docker volume inspect` → `no such volume` |
| Gone from disk | no `whatsapp-*` directory under `/var/lib/docker/volumes` |
| Shared MySQL after the deletion | `master-compose-ai-mysqldb-master-1  Up 8 days` |
| Production after the deletion | `/login` `200`, `/docs` `200`, health `connected:1` |

**The consequence, stated plainly: there is no rollback any more, and the volume
on `10.10.0.4` is the only copy of the paired device identity that exists.** The
standing "never `down -v`" rule was already a hard one; it is now the difference
between a restart and re-pairing the handset by QR in person.

One correction to the transcript of this run: a `sudo test -d` used to check the
mountpoint on .2 failed for want of a password, so its `||` branch printed
"directory removed" without having looked. The on-disk check in the table above
reads `/var/lib/docker/volumes` through a container instead, which does run as
root. A failed check that prints the reassuring branch is worth more suspicion
than a check that errors out loudly.

## 2026-08-27 Phase 9 finishing pass — media/calls/ops/webhooks e2e coverage

Base URL: `http://127.0.0.1:8801` (throwaway instance, app tables in SQLite,
**no MySQL contacted**). `WSM_WA_BACKEND=whatsmeow` (scripts/e2e-env.sh's own
pin) — this run proves the DEGRADED path for every Phase 6a/9 surface, never
the real feature; see `e2e/media.spec.ts`'s header comment for why.

Browser: Playwright 1.62.1, Chromium, system libraries via
`npx playwright install-deps`.

Browser user: admin `ardihikaru`. Secret handling: unchanged.

**Why this run needed a manual step first.** `go run ./cmd/app` (what
`scripts/e2e-env.sh` execs) failed to boot with `WSM_MINIO_ENDPOINT is
required` — MinIO became an unconditional boot requirement in an earlier
phase (`internal/config.loadMinio`), but neither `scripts/e2e-env.sh` nor
`playwright.config.ts` was updated to supply it, and `.env` is never sourced
by a bare `go run` (only `docker compose`'s `env_file:` reads it). Worked
around by exporting only the four `WSM_MINIO_*` keys from `.env` into the
shell running `./scripts/e2e-run.sh` — deliberately NOT sourcing the whole
file, since it also carries production's `WSM_ADDR`/`WA_BASE_URL`/
`WA_COOKIE_SECURE` values that would have broken the hermetic 8801 setup.
This gap is real and unfixed; see the phase report for the recommendation.

Results:

| Check | Result |
|---|---|
| `go build ./...` | clean |
| `go vet ./...` | clean |
| `go clean -testcache && go test ./...` | pass, all packages |
| Playwright, all projects (mobile-portrait, mobile-small, desktop) | **72 passed, 0 failed** (31 skipped — desktop/phone-only guards + opt-in evidence capture) (1.3m) |
| `/send` with no live-connected session (whatsmeow device store empty at boot) | "No connected sessions." shown; media tab and file input absent, pinned rather than untested |
| `/calls` (`WSM_CALLS_ENABLED=false` default) | "disabled on this deployment" shown |
| `/sessions/{id}/ops` panel (backend has no `SessionOps`) | "not supported by this backend" shown, after the panel's own `load` poll |
| `/sessions/{id}/webhooks` (backend has no `WebhookAdmin`) | "not supported by this backend" shown synchronously |
| New pages at 390×844 / 360×740 / 1280×800: no element wider than the configured viewport | pass, `/calls` + `/sessions/{id}/ops` + `/sessions/{id}/webhooks` |
| Same pages: scrolls to bottom | skipped — degraded-copy layout fits on one screen at every tested viewport |

Notes:

- **The media tab was never reached, and that is reported rather than
  papered over.** `cmd/e2eseed`'s sessions carry `LastStatus="connected"` in
  the DB, but the app reconciles LIVE state at boot by asking the backend;
  the boot log for this run shows `WARN boot reconnect failed
  session=e2e-sess-0001 err="whatsmeow device missing for paired session
  (re-pair required)"` for both seeded sessions, so
  `wa.ManagedSession.Connected` is never true here. `send.html` gates its
  whole tabbed form — text and media alike — behind `{{if .Connected}}`.
  Reaching the media tab, or a live call, needs a real paired device or a
  real waxum instance, both out of reach from this environment.
- **Phase 1's red-then-green evidence** (the five previously-untested
  session-ops/webhook actions — auto-reconnect, history-sync, appstate
  resync, webhook register, DLQ replay) is a `go test` result, not a
  Playwright one; see the phase report for the failing-then-passing output.

## 2026-08-27 Cleanup pass — `opsAction` error surfacing, and `make e2e` boots hermetically

Two bugs the Phase 9 finishing pass documented rather than fixed.

**Bug 1 — a failed session-ops action told the operator nothing.**
`internal/handler/ops_ui.go`'s `opsAction` only `slog.Warn`ed a failed
Pause/Resume/auto-reconnect/history-sync/appstate-resync and re-rendered an
ordinary, error-free panel — the button did nothing and the page said
nothing. `renderOpsPanel` now takes the action's error and puts it on the
panel's own `Error` field, the same way `loadOpsPanel`'s three read calls
already do; `errors.Is(err, wa.ErrUnsupported)` reads as "not supported by
this backend" rather than a raw error string.
`TestSessionOpsAppStateResyncReachesTheBackendWithNoFilter`'s doc comment,
which deliberately pinned the swallow as documented-not-fixed, now says so
in the past tense and points at the new test.
`TestAFailedSessionOpsActionTellsTheOperator`
(`internal/handler/session_admin_pages_test.go`) is red against the old
`renderOpsPanel` (confirmed by temporarily reverting it: the panel rendered
with no `alert-error` and no error text) and green against the fix.

**Bug 2 — `make e2e` could not boot the app, in two separate ways.**
`scripts/e2e-env.sh` exported no `WSM_MINIO_*` values, but `config.Load()`
has required all four unconditionally since MinIO landed, so `go run
./cmd/app` died with `WSM_MINIO_ENDPOINT is required` on a clean checkout.
Fixed by pulling exactly those four keys out of `.env` (never sourced
wholesale) with one `grep` each, exported, and left to die with the
process — same rule as `_data/paswd`'s handling just above it in the same
file. Absent `.env` and an unset key both fail fast with a message naming
all four keys, rather than the opaque `config.Load()` error.

Running `make e2e` end-to-end (not exercised in the Phase 9 evidence above,
which bypassed `make` and ran `./scripts/e2e-run.sh` directly after
exporting the MinIO keys by hand) surfaced a **second, independent**
blocker: this repo's `Makefile` does `-include .env` + `export` near its
top, so every target — including `e2e` — runs with `.env` exported
wholesale into the recipe's own environment already, before
`scripts/e2e-env.sh` ever runs. `scripts/e2e-env.sh`'s
`${WSM_ADDR:-127.0.0.1:8801}`-style defaults only apply when a variable is
*unset*, so `.env`'s own `WSM_ADDR=:8800`, `WA_BASE_URL=<production
host>`, and `WA_COOKIE_SECURE=true` all leaked straight through: the app
bound `:8800` instead of `127.0.0.1:8801` (where Playwright's readiness
probe and every test's `baseURL` actually point), and — had it bound the
right port — a `Secure` cookie over plain HTTP would have failed every
login. Fixed by forcing those three, with an override hook under distinct
`WSM_E2E_ADDR` / `WSM_E2E_BASE_URL` / `WSM_E2E_COOKIE_SECURE` names instead
of the app's own — so `.env`, leaked in through `make`, cannot reach them
through the names it already occupies.

Base URL: `http://127.0.0.1:8801` (throwaway instance, app tables in
SQLite, no MySQL contacted). `WSM_WA_BACKEND=whatsmeow` (unchanged pin).

Browser: Playwright 1.62.1, Chromium, system libraries via
`npx playwright install-deps`.

Results:

| Check | Result |
|---|---|
| `go build ./...` | clean |
| `go vet ./...` | clean |
| `go clean -testcache && go test ./...` | pass, all packages |
| `TestAFailedSessionOpsActionTellsTheOperator` | red against the pre-fix `renderOpsPanel` (verified by temporary revert), green against the fix |
| `make e2e` (no manual exports, straight from a `make` invocation) | exit 0; app listens on `http://127.0.0.1:8801` (not `:8800`); **72 passed, 0 failed** (31 skipped — desktop/phone-only guards + opt-in evidence capture) (1.4m) |

Notes:

- The media-tab / live-connection gap this cleanup pass was explicitly told
  NOT to fix (`cmd/e2eseed`'s sessions never reconcile to `Connected` under
  whatsmeow, so `/send`'s media tab stays structurally unreachable here) is
  unchanged and still correctly documented in `e2e/media.spec.ts` and the
  Phase 9 entry above.

---

## 2026-08-31 — `GET /api/v1/inbound` shipped to production

Closes the incident in `docs/brief-inbound-api.md`: the Clino consumer's
`confirm-receipts` cron had been logging
`wsmclient: list inbound: status 404: 404 page not found` on every run
since it moved to this central gateway.

### The brief's design was wrong, and live verification is what caught it

The brief specified persisting replies from waxum's `message` SSE event and
flagged that payload shape as UNVERIFIED. It cannot work.
`/api/v1/events/tail` serializes a `ConsoleEvent` whose payload field is
truncated to 160 characters (`src/state.rs`, `push_event`) — a truncated
slice of JSON is not parseable, and the message body is simply not in the
stream. The SSE stream stays a nudge, never an authority, exactly as
`internal/wa/backend_waxum_events.go` already documented itself.

The real source is `GET /api/v1/sessions/{id}/messages` (full rows, keyset
cursor, newest first). Two further facts, both from probing the live
deployment (image `ardihikaru/waxum:0.12.2-uploaded-media`, from inside the
waxum container) and neither documented anywhere before:

- **Every inbound `sender_jid` is a `@lid`**, many with a device suffix.
  Across 60 live rows: `sender_jid` `@lid` 100% (41 with `:NN`), `chat_jid`
  `@lid` for DMs, plus `@g.us` and `status@broadcast`. A LID is not a phone
  number, so `NormalizePhone` on it yields digits that can never match
  `wa_messages.to_phone` — **every tier stamp would have been NULL**, the
  consumer would have correctly ignored all of it, and the feature would
  have looked shipped while delivering nothing. Resolved via
  `GET /sessions/{id}/contacts/{jid}/lid` (verified `HTTP 200`, returns a
  bare MSISDN), cached per sender.
- **`msg_timestamp` carries no zone marker** (`"2026-08-31 07:46:44"`), so
  it needs `ParseInLocation(..., time.UTC)`; `time.Parse` would shift every
  `received_at` by the host's offset.

### Deploy

| Step | Result |
|---|---|
| `go build ./...`, `go vet ./...` | clean |
| `go test ./...` | pass, all packages |
| `go test ./internal/wa/ ./internal/handler/ -race` | pass |
| `gofmt -l` over every changed file | clean |
| migration `0006_inbound_messages.sql` on production MySQL | `OK 0006_inbound_messages.sql (66.43ms)`, `successfully migrated database to version: 6` |
| app container rebuilt + restarted (waxum untouched) | `Up`, `listening on http://localhost:8800` |
| `GET /api/v1/inbound` unauthenticated | `401 {"error":"unauthorized"}` — route registered (a genuinely absent route still returns `404`, checked alongside) |

**A stale-image trap worth recording:** the first `--profile migrate run up`
answered `goose: no migrations to run. current version: 5` and looked like
success. `Dockerfile.migrate` bakes `migrations/` in at BUILD time, so the
cached image still held only 0001–0005. `docker compose --profile migrate
build migrate` first, then `up`. A migration that reports "nothing to do"
right after you added one is the failure, not the outcome.

### The app restart wedged the WhatsApp session — the 2026-08-27 hazard, again

After the restart, `GET /sessions/{id}/status` returned
`{"status":"connecting","is_logged_in":true,"socket_alive":false}` and
`GET .../messages` answered `503 Client not connected`. WSM's supervisor
looped `auto-reconnect attempt 1/5, 2/5, 3/5` without clearing it, exactly
as CLAUDE.md's entry predicts. It was healthy ~13 minutes earlier.

`docker compose restart waxum` (the waxum PROCESS only — never `down -v`,
the volume is the paired device's identity) cleared it within one poll:
`{"status":"logged_in","is_logged_in":true,"socket_alive":true}`. **No
re-pairing was needed**, matching the 2026-08-27 recovery. `/api/v1/health`
then reported `{"backend":{"name":"waxum","reachable":true},"connected":1,
"sessions":1,"status":"ok"}`.

### End-to-end proof, from the consumer's own logs

```
09:06  WARN confirm-receipts: reply poll failed … status 404: 404 page not found
       done polled=0 …
09:08  done polled=0 …                     ← WARN gone; app restarted 09:06:59
09:14  done polled=0 …                     ← session still wedged
09:16  done polled=140 matched=0 ambiguous=0 foreign_tier=1 unmatched=139 …
```

and, on this side, `GET /api/v1/inbound -> 200 (59ms)`.

`foreign_tier=1` is the part that matters most: one ingested row carried a
`client_ref` this consumer could not resolve in its own database, and it
skipped the row rather than acting on it. That is the security property the
brief calls "the tier stamp" working in both halves at once — this gateway
stamped honestly, and the consumer ignored what was not its own. It also
proves LID→phone resolution succeeded, since an unresolved sender can never
be stamped at all.

`matched=0` / `unmatched=139` is expected: the pump's first ticks drained
waxum's existing history, which is backlog, not answers to currently-open
receipt questions.

Not verified here: a live customer replying "sudah" to a delivered order and
moving it to `Sudah Diambil` with `confirm_source = customer_reply`. That
needs a real reply to a real open question and cannot be manufactured from
this side.

## 2026-09-01 PHKG deploy bundle — image transfer to `wa-phkg`

Preparing a second, independent deployment on `wa-phkg` (`192.168.20.10`,
`/home/sim/devel/projects/whatsapp-service`) **without copying source code**.
The images that production is running were saved on `rnd-v2-vpn`, carried
through this laptop, and loaded on the target; the deployment bundle
(`deploy/phkg/`) was written and rsynced alongside them.

### What production was running, and what arrived

`docker ps` / `docker image inspect` on `rnd-v2-vpn`, then
`docker images` on `wa-phkg` after `load-images.sh`:

| Image | Prod ID | ID on `wa-phkg` | tar.gz |
|---|---|---|---|
| `whatsapp-with-dashboard:local` | `d7b40a798051` | `d7b40a798051` | 11,426,610 B |
| `whatsapp-with-dashboard-migrate:latest` | `7c09964d975f` | `7c09964d975f` | 28,324,997 B |
| `ardihikaru/waxum:0.12.2-uploaded-media` | `e7c51aa15c99` | `e7c51aa15c99` | 63,703,097 B |

`sha256sum -c SHA256SUMS` returned `OK` for all three files on the laptop
**and** on `wa-phkg`, against sums computed on the production host — so the
check covers `docker save`, both `scp` hops and the target's disk. The
production scratch directory was removed afterwards. Nothing on production was
started, stopped or rebuilt: `docker save` and `docker image inspect` are the
only commands that touched it.

`load-images.sh` also applied the dated aliases
`whatsapp-with-dashboard:phkg-20260901` and
`whatsapp-with-dashboard-migrate:phkg-20260901`, which is what
`deploy/phkg/docker-compose.yml` names — `:local` and `:latest` identify
nothing on a host with neither this repo nor a registry.

### The bundle parses, and resolves to those exact images

On `wa-phkg`, with every guarded variable given a throwaway value in a temp
directory:

```
docker compose config --quiet     -> no output (valid)
docker compose config --services  -> waxum, app
docker compose config --volumes   -> wsm-sqlite, waxum-sessions
docker compose --profile migrate config --images
  -> ardihikaru/waxum:0.12.2-uploaded-media
     whatsapp-with-dashboard-migrate:phkg-20260901
     whatsapp-with-dashboard:phkg-20260901
```

Only the `--quiet` form was ever run. `docker compose config` without it
renders the whole `.env` inline for every service, which is how a MySQL root
password has reached a plaintext log here before.

### What is deliberately NOT done

`docker compose up` was **not** run, and this deployment cannot start yet. Two
things are still missing and both are hard boot requirements:

- **MySQL** — `WSM_MYSQL_*` and `WAXUM_MYSQL_*`. Schema must also be migrated
  first; the binary never migrates on boot.
- **MinIO** — `WSM_MINIO_ENDPOINT/ACCESS_KEY/SECRET_KEY/BUCKET`. Not optional:
  `cmd/app` runs a live `storage.EnsureBuckets` before serving and
  `log.Fatalf`s if MinIO is unreachable or the credential is wrong. There is
  no degraded mode, and `WSM_MEDIA_ARCHIVE=false` does not relax it.

With `.env` copied from the template and still empty, `docker compose config
--quiet` on `wa-phkg` fails and names each one, e.g.

```
error while interpolating services.app.environment.WSM_MINIO_ENDPOINT:
  required variable WSM_MINIO_ENDPOINT is missing a value: isi WSM_MINIO_ENDPOINT di .env
```

That is the intended state: the guards fire at config time, before a container
starts, rather than as a crash loop on a host where nobody has the source.

No production credential was copied. `WAXUM_TOKEN`, the database passwords, the
MinIO keys and the admin hash are all left empty for PHKG to generate — the
production `WAXUM_TOKEN` in particular is a root credential for every session
on the production waxum instance.

No WhatsApp identity was copied either: both volumes start empty and the
handset will be paired by QR on `wa-phkg`. The session is a hard singleton, so
duplicating a store across two hosts logs the session out rather than cloning
it.

### Repo side

`go vet ./...` clean; `go test ./...` all packages `ok`, including the new
`compose_deploy_phkg_test.go`. Each of its claims was checked red-on-revert by
temporarily reintroducing the mistake it guards — a `build:` key and a `:local`
tag, an external network declaration, a published waxum port, a deleted
`migrate` service, a secret value in `.env.template`, and a missing
`load-images.sh` — every one failed with the intended message, and the suite
went green again on restore.

### 2026-09-01, lanjutan: MySQL untuk PHKG

The MySQL for this deployment is the HIS v3 server (credentials read
programmatically from `~/.config/staging-his-v3-db/`, never echoed and never
written anywhere). It is a shared box: `SHOW DATABASES` lists ~35 schemas
across several unrelated products, so the house rule applies in full — one
schema, one least-privilege user, nothing on `*.*`.

Created with the repo's own `scripts/bootstrap-db.sql.tmpl`, run twice:

| Schema | User | Grants |
|---|---|---|
| `wa_dashboard_phkg` | `wa_dashboard_phkg`@`%` | `SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, REFERENCES, INDEX, ALTER, CREATE TEMPORARY TABLES` on that schema only |
| `waxum_phkg` | `waxum_phkg`@`%` | same, on `waxum_phkg` only |

The `_phkg` suffix follows the convention already on that server
(`pec_dev_phkg_20250811`) and leaves room for a second WA deployment later.

The root credential from the file was used ONLY to create these two and never
reaches any `.env`. Isolation checked by logging in as the app user itself:

```
SHOW DATABASES   -> information_schema, performance_schema, wa_dashboard_phkg
SELECT … FROM his_v3.migrations
  -> ERROR 1142 (42000): SELECT command denied to user 'wa_dashboard_phkg'@…
```

Reachability from `wa-phkg` itself (not from this laptop — the app runs there):
the server's port answers and returns a MySQL handshake banner reading
`8.0.33`.

> Correction worth recording: the first reachability test reported NOT
> REACHABLE and was wrong. `ssh host 'bash -s' <<EOF` reads the SCRIPT from
> stdin, so the piped host/port were consumed as script lines and `read` got
> garbage. A test whose inputs never arrive fails closed and reads exactly like
> a firewall block. Put the values in the script body, not a second stdin.

Migrations were then applied FROM `wa-phkg`, as the least-privilege user, which
also proves that user can do everything the schema needs:

```
docker run --rm --env-file .env whatsapp-with-dashboard-migrate:phkg-20260901 up
  -> OK 0001_app_tables … 0006_inbound_messages
  -> goose: successfully migrated database to version: 6
```

`SHOW TABLES` as the app user: `api_access_tokens`, `api_clients`,
`goose_db_version`, `wa_calls`, `wa_inbound_messages`, `wa_messages`,
`wa_sessions`. `waxum_phkg` is still empty by design — waxum creates its own
tables on first boot.

Not `docker compose --profile migrate run`: compose interpolates EVERY service
before running one, so `app`'s `${WSM_MINIO_*:?}` guards block a migration that
legitimately precedes MinIO. `deploy/phkg/README.md` now documents the direct
`docker run` form for exactly that window.

Still blocking `up`: `WSM_MINIO_*`, `WAXUM_TOKEN`, `WSM_ADMIN_USER` /
`WSM_ADMIN_PASSWORD_HASH`. The generated database passwords exist in exactly
one place, `/home/sim/devel/projects/whatsapp-service/.env` on `wa-phkg`, mode
600; the working copy on this laptop was shredded.

### 2026-09-01, lanjutan: MinIO untuk PHKG

The credential offered for this (`~/.config/minio/mfardiansyah.json`,
`https://s3.mfardiansyah.id`) turned out to be **`minioadmin` — the MinIO root
account**. `mc admin info` and `mc admin accesskey ls` both answered, and it
could `stat` every unrelated bucket on that server: `animora-prod`,
`clino-backup-prod`, `clinical-documents`, `his-temp`, `penpot-assets`, and a
dozen more. Putting it in `.env` on the PHKG host would have given a WhatsApp
gateway root over all of them.

Its `bucketName` field is also still the literal placeholder
`<you-define-yourself>`, and its `tempBucketName` is `temp` — a real shared
scratch bucket holding unrelated HTML reports, and the same bucket the
PRODUCTION WA deployment already writes into.

So the MySQL pattern was applied again: dedicated buckets, one dedicated user,
scoped policy, root used only to create them and never written anywhere.

| Bucket | Policy `whatsapp-phkg`, user `whatsapp-phkg` |
|---|---|
| `whatsapp-phkg` | `s3:ListBucket, GetBucketLocation, CreateBucket` on the bucket; `s3:GetObject, PutObject` on `/*` |
| `whatsapp-phkg-temp` | same |

The action list is exactly what `internal/storage` calls — `BucketExists`,
`MakeBucket`, `PutObject`, `GetObject`, `PresignedGetObject`. No delete, no
object listing: the code never does either.

Verified positively and negatively with that key:

```
mc ls                      -> whatsapp-phkg/, whatsapp-phkg-temp/   (only these two)
mc cp / mc cat             -> OK on both buckets
mc share download 168h     -> presigned URL issued, Expire: 7 days
mc cat  clinical-documents -> permission denied
mc cp   temp/              -> permission denied
mc cp   whatsapp-ardi/     -> permission denied   (production WA's bucket)
mc cat  clino-backup-prod  -> permission denied
mc mb   should-fail-bucket -> Access Denied
```

End-to-end from the deploy host itself: a presigned POST issued with the PHKG
key was executed **on `wa-phkg`** and returned `HTTP 204`, and the object read
back carried the text that host had written. Network path and credential
authority are therefore both proven from where the app will actually run, not
only from this laptop. `https://s3.mfardiansyah.id/minio/health/live` answers
`HTTP 200` from `wa-phkg` with a valid certificate in 0.59s. Test objects were
removed afterwards with the admin key — the PHKG key cannot delete, by design.

> Two process corrections worth recording. First, an `mc` verification pass ran
> with an EMPTY `MC_HOST_wa` because the secrets file was sourced without
> `set -a`, so `mc` silently treated `wa/...` as a LOCAL PATH: it "succeeded" at
> every positive check and "created" a bucket that should have been denied,
> while really just making directories under the repo. An alias that does not
> resolve does not fail — it falls back to the filesystem. Assert the host
> variable is non-empty before trusting a single line of the output, and read
> the error text (`Requested path /home/... not found` was the tell). The stray
> directories were removed. Second, the first end-to-end attempt reported
> `HTTP 403`: `mc share upload` emits a POST-policy form, and only the URL had
> been extracted and replayed as a `PUT`. That was the harness being wrong, not
> the policy.

`WSM_MINIO_TEMP_BUCKET` is deliberately `whatsapp-phkg-temp`, overriding the
template's `temp`, so this deployment shares a scratch bucket with nobody.

Still blocking `up`: `WAXUM_TOKEN`, `WSM_ADMIN_USER`,
`WSM_ADMIN_PASSWORD_HASH`.

### 2026-09-01, lanjutan: `.env` lengkap, compose valid

`WAXUM_TOKEN` generated fresh (`openssl rand -hex 32`) — never production's,
which is a root credential for every session on the production waxum instance.

`WSM_ADMIN_USER` / `WSM_ADMIN_PASSWORD_HASH` come from
`~/.config/waxum/phkg.json`. The password was piped straight into
`go run ./cmd/hashpw` from python, never through argv, so it reached no shell
history and no `ps` listing. The resulting hash was checked round-trip before
being written:

```
bcrypt cost            : 10
cocok                  : True
password salah ditolak : True
```

Written into `.env` single-quoted, because a bcrypt hash is `$2a$10$…` and
compose's env_file parser interpolates `$` in an unquoted value — the same trap
`cmd/hashpw` prints a warning about. Values crossed the ssh boundary base64'd
so the LOCAL shell could not expand those dollars while building the heredoc.

```
docker compose config --quiet   -> no output. VALID.
```

That is the first time this bundle has parsed end to end. `WSM_TRUSTED_PROXIES`
and `WA_PLATFORM_URL` remain empty on purpose (no proxy in front of this host,
no platform link to show); both are optional and empty is meaningful for each.

Not yet done: `docker compose up`. Nothing blocks it now.

### 2026-09-01, `docker compose up` — the stack is running on `wa-phkg`

Started by the operator. Both containers up, waxum healthy:

```
app     whatsapp-with-dashboard:phkg-20260901    Up   127.0.0.1:18801->8800/tcp
waxum   ardihikaru/waxum:0.12.2-uploaded-media   Up (healthy)   3451/tcp
```

Three boot claims, each with its own evidence:

- **MinIO** — `minio: connected to https://s3.mfardiansyah.id (bucket=whatsapp-phkg,
  temp bucket=whatsapp-phkg-temp)`. That line is printed AFTER
  `storage.EnsureBuckets`, so the scoped `whatsapp-phkg` key demonstrably
  satisfies the live check that `log.Fatalf`s when it fails.
- **App MySQL** — served `GET /api/v1/health -> 200`, which reads the store.
- **waxum MySQL** — `waxum_phkg` now holds 10 tables (`sessions`, `messages`,
  `contacts`, `tokens`, `webhooks`, `webhook_dlq`, `blast_jobs`,
  `blast_recipients`, `scheduled_messages`, `token_sessions`), created by waxum
  itself under the least-privilege `waxum_phkg` user. That user's grants are
  therefore proven sufficient, not just plausible.

#### `backend.reachable:false` is expected here, and it is worth knowing why

`/api/v1/health` answers
`{"backend":{"name":"waxum","reachable":false},"connected":0,"sessions":0,"status":"ok"}`
and keeps answering it. That is NOT a broken link between the two containers:

```
docker compose exec waxum curl http://waxum:3451/health          -> 200
docker compose exec waxum curl http://waxum:3451/api/v1/sessions -> 401 (no token; correct)
/proc/net/tcp inside waxum: 1 ESTABLISHED connection to :3451     <- the SSE stream is OPEN
```

`backend_waxum_events.go`'s `eventPump` stores `gatewayReachable = true` only
inside the per-event callback. With zero sessions, waxum emits no events at
all, so the flag never flips even while the stream sits open and healthy. It
will flip on the first event after a handset is paired.

The three `connection refused` warnings at 12:50:19–12:50:22 are boot ORDER,
not boot failure: the app came up at 12:50:18 and waxum finished starting at
12:50:24. That is precisely why the compose file uses a plain `depends_on`
rather than `condition: service_healthy` — the pump backed off, reconnected,
and has logged nothing since.

#### One real gap, unrelated to WSM

waxum logs: `JWT_SECRET is not set; using a random secret generated for this
process only. Tokens will stop validating after a restart.` WSM never uses
waxum's JWT — it authenticates with `SUPERADMIN_TOKEN`, which bypasses JWT
entirely — so this does not affect this app. It does mean any JWT waxum issues
to some other client would die on the next restart. Neither
`docker-compose.yml` nor `deploy/phkg/docker-compose.yml` sets `JWT_SECRET`
today.

Not done yet: pairing. The dashboard is loopback-only; reach it with
`ssh -L 18801:127.0.0.1:18801 wa-phkg`.

### 2026-09-02 Access control on the published dashboard port

`WSM_BIND_ADDR` was moved from `127.0.0.1` to `192.168.20.10` so the dashboard
is reachable from the network — deliberately the one LAN interface, not
`0.0.0.0`, which would also hand it to `docker0` and every other interface on
the box. Verified: `ss -tln` shows exactly one listener, `192.168.20.10:18801`,
and `127.0.0.1:18801` now answers nothing. `WA_BASE_URL` moved with it, since
that variable derives the mount prefix and every absolute URL.

Access is then restricted in the `DOCKER-USER` chain:

| # | Source | Action |
|---|---|---|
| 1 | `172.18.0.0/16` | RETURN (container-to-container) |
| 2 | `192.168.20.0/24` | RETURN (PHKG LAN) |
| 3 | `10.212.134.0/24` | RETURN (FortiClient pool) |
| 4 | `0.0.0.0/0` | **DROP** |

All on `tcp dpt:8800` toward `172.18.0.0/16` — the CONTAINER port, because the
packet is already DNAT'ed by the time it reaches this chain. Matching the whole
subnet rather than the container's current IP keeps the rules correct across a
`docker compose up -d` that renumbers it.

**`ufw` cannot do this job and would have failed silently.** It is active on
this host, but a Docker-published port never traverses ufw's INPUT chain:
docker DNATs in PREROUTING and the packet is then FORWARDed. `ufw deny 18801`
would read as applied while the port stayed open to everyone.

The FortiClient pool was derived, not guessed: this laptop's `ppp0` is
`10.212.134.5`, and `wa-phkg` reports `SSH_CLIENT=10.212.134.5` — identical, so
there is no NAT between them and the `/24` around it is the pool.

#### The part that actually needed proving

Rules can be installed on a path packets never take, and then the DROP guards
nothing. First check after install showed all four counters at zero, so nothing
was concluded. Two requests were then made from this laptop
(`10.212.134.5` → `http://192.168.20.10:18801`, both `HTTP 200`), and the
counters moved:

```
3       13   874 RETURN  10.212.134.0/24  172.18.0.0/16  tcp dpt:8800
4        0     0 DROP    0.0.0.0/0        172.18.0.0/16  tcp dpt:8800
```

That is the claim worth having: traffic to the published port really does
traverse `DOCKER-USER`, so rule 4 is enforcement rather than decoration.

Honest limit: rule 4 is proven **by construction** — traversal is measured, and
it is the last rule with a `0.0.0.0/0` source — not by observing a real
connection being dropped. No host exists that is outside both allowed ranges
AND able to route to `192.168.20.10`, so that test could not be performed from
here.

Persistence is a systemd unit (`wa-phkg-firewall.service`, `After=docker.service`)
rather than `iptables-persistent`: the latter restores the whole table at a boot
stage that can precede docker creating its own chains, making the outcome
order-dependent. The unit needs no apt and no network.

`deploy/phkg/` now carries `firewall.sh`, `wa-phkg-firewall.service`,
`install-firewall.sh` and `check-firewall.sh`, all four named in
`TestPHKGBundleIsComplete` — checked red-on-revert by removing `firewall.sh`.

---

## 2026-09-02 `wa-phkg` dipindah ke MySQL + MinIO internal PHKG

Sampai hari ini `wa-phkg` berjalan di atas dua backing service di luar LAN PHKG:
MySQL `202.10.39.46:31006` dan MinIO `https://s3.mfardiansyah.id`. Keduanya
dipindah ke infrastruktur internal — MySQL `192.168.20.15:3306`, MinIO
`http://192.168.20.12:9000`.

### Kenapa ini cutover konfigurasi, bukan migrasi data

Gerbangnya dijalankan dua kali: sekali saat merencanakan, sekali lagi tepat
sebelum `docker compose down`. Keduanya nol:

```
waxum.sessions            0        app.wa_sessions           0
waxum.tokens              0        app.wa_messages           0
                                   app.wa_inbound_messages   0
                                   app.api_clients           0
MinIO whatsapp-phkg       0 B, 0 objects
MinIO whatsapp-phkg-temp  0 B, 0 objects
```

`waxum.sessions = 0` adalah yang paling penting dari daftar itu: belum pernah
ada handset yang di-pair, jadi memindahkan waxum ke registry MySQL kosong tidak
membuat isi volume `whatsapp-service-phkg-waxum` yatim. Kalau angka itu bukan
nol, rencananya adalah berhenti — bukan melanjutkan lalu scan QR ulang.

Tetap dibuat cadangan sebelum apa pun disentuh: `.env.bak-20260902` di host,
plus `mysqldump` kedua schema lama (7 dan 10 `CREATE TABLE`, `stderr` kosong) ke
`~/backup/wa-phkg-20260902/` di luar repo.

### Database dibuat oleh `wa_user` sendiri, tanpa root

`bootstrap-db.sql.tmpl` tidak bisa dipakai di `.15`: templat itu butuh root
untuk `CREATE USER`/`GRANT`, dan yang kita pegang hanya `wa_user`. Grant-nya
sudah membatasi nama yang mungkin:

```
GRANT USAGE ON *.* TO `wa_user`@`%`
GRANT ALL PRIVILEGES ON `waxum`.* TO `wa_user`@`%`
GRANT ALL PRIVILEGES ON `wa_dashboard`.* TO `wa_user`@`%`
```

Grant level-database memuat `CREATE`, jadi `wa_user` boleh membuat persis dua
nama itu dan tidak ada yang lain. Sesudah `CREATE DATABASE`:

```
wa_dashboard    utf8mb4   utf8mb4_0900_ai_ci
waxum           utf8mb4   utf8mb4_0900_ai_ci
```

### Migrasi dan boot

`docker run --rm --env-file .env …migrate:phkg-20260901`: enam migration
`Pending` → `OK` → `successfully migrated database to version: 6`, sama dengan
versi schema lama. Sesudah `docker compose up -d`:

```
SERVICE   STATE     STATUS                   PORTS
app       running   Up 8 seconds             192.168.20.10:18801->8800/tcp
waxum     running   Up 8 seconds (healthy)   3451/tcp

app-1 | minio: connected to http://192.168.20.12:9000 (bucket=whatsapp-phkg, temp bucket=whatsapp-phkg-temp)
app-1 | listening on http://localhost:8800 (docs at /docs)
```

Baris `minio: connected` itu sendiri sudah bukti: app melakukan cek jaringan
hidup ke MinIO saat boot dan `log.Fatalf` kalau gagal, jadi proses yang hidup
tidak mungkin salah alamat. `SHOW TABLES` di `.15` — `wa_dashboard` 7 tabel
(goose `MAX(version_id)` = 6), `waxum` 10 tabel, sama persis dengan schema lama;
yang sepuluh itu dibuat waxum sendiri saat boot, tidak ada migrasi tangan.

### `reachable:false` yang bukan kegagalan

`GET /api/v1/health` menjawab 200 dengan
`{"backend":{"name":"waxum","reachable":false},…}`, dan log app hanya memuat
SATU baris `waxum event stream ended … connection refused` dari detik pertama
boot. Itu terbaca seperti app tidak bisa menghubungi waxum. Bukan:

```
$ docker run --rm --network container:whatsapp-service-app-1 nicolaka/netshoot ss -tnp
State  Recv-Q Send-Q   Local Address:Port      Peer Address:Port
ESTAB  0      0           172.18.0.3:42770       172.18.0.2:3451
```

Dari namespace jaringan app sendiri, koneksi SSE ke waxum **ESTABLISHED**. Baris
`connection refused` itu satu percobaan pertama yang mendahului listener waxum;
percobaan berikutnya berhasil dan tidak menghasilkan baris log sama sekali,
karena hanya kegagalan yang di-log. `reachable` sendiri hanya menjadi `true` di
dalam callback event (`backend_waxum_events.go`'s `eventPump`), jadi dengan nol
sesi ter-pair — waxum tidak memancarkan event apa pun — `false` adalah keadaan
mantap yang benar, bukan gejala. Ini perlu ditulis karena bentuk gejalanya sama
persis dengan waxum yang benar-benar mati.

### Jalur tulis app → MySQL baru, dibuktikan lewat UI

Boot dan migrasi membuktikan tulis oleh `migrate` dan oleh waxum, bukan oleh app
sendiri. `GET /api/v1/health` tidak menutup celah itu — handler-nya membaca
`Manager.Counts()` in-memory dan doc comment-nya menyebut sendiri "No DB ping
needed". Jadi: login lewat `POST /login` (303 → `/dashboard`), lalu

```
GET /dashboard             -> 200
GET /admin/api-clients     -> 200
GET /sessions/rows         -> 200
GET /logs/rows             -> 200
POST /admin/api-clients    -> 200
```

dan barisnya dibaca langsung dari `192.168.20.15`, bukan dari halaman yang baru
saja menulisnya:

```
        id: 0e6e1780ba058b5875c4412ce2a5d782
      name: cutover-writecheck-20260902
  login_id: cutover-writecheck-20260902
    active: 1
created_at: 2026-09-02T05:51:53Z
```

Baris itu lalu dihapus lewat UI (`POST …/delete` → 303); `api_clients` dan
`api_access_tokens` kembali ke 0 baris.

### Bucket, dan bucket yang HAMPIR ditulisi

`EnsureBuckets` membuat keduanya saat boot:

```
[2026-09-02 12:47:59 WIB]  0B  whatsapp-phkg/
[2026-09-02 12:47:59 WIB]  0B  whatsapp-phkg-temp/
```

Yang membuat ini layak dicatat adalah tetangganya. `.12` dipakai bersama produk
lain, dan `mc du tgt/temp` menjawab **29MiB, 19784 objects** — bucket `temp` di
sana milik orang lain dan berisi. `config.DefaultMinioTempBucket` adalah persis
`temp`, dan `WSM_MINIO_ACCESS_KEY` di host ini adalah key **admin** MinIO
(`mc admin info` berhasil). Jadi `.env` yang kehilangan satu baris
`WSM_MINIO_TEMP_BUCKET` tidak akan gagal — ia akan menulis media temporer
WhatsApp ke bucket produk lain, dengan kredensial yang juga bisa menghapus
isinya, dan tidak ada satu pun error yang muncul. `deploy/phkg/.env.template`
dulu mengirim `WSM_MINIO_TEMP_BUCKET=temp`; sekarang `whatsapp-phkg-temp`,
dijaga `TestPHKGEnvTemplateScopesTheMinioTempBucket` — dicek merah-saat-revert
dengan mengembalikan baris itu ke `temp`:

```
--- FAIL: TestPHKGEnvTemplateScopesTheMinioTempBucket (0.00s)
    compose_deploy_phkg_test.go:210: deploy/phkg/.env.template sets
    WSM_MINIO_TEMP_BUCKET="temp", want a product-scoped bucket …
```

### Batas jujur

- **Pairing handset dan kirim pesan/media belum dilakukan** — butuh telepon
  fisik untuk memindai QR. Jadi jalur `app → waxum → WhatsApp` dan jalur
  `PutObject` ke bucket `.12` **belum** dibuktikan end-to-end; yang terbukti
  hari ini adalah bahwa app menulis ke MySQL baru dan membuat bucket di MinIO
  baru.
- **Penghitung `DOCKER-USER` tidak dibaca ulang** sesudah container dibuat
  ulang (butuh sudo di host). Aturannya mencocokkan subnet `172.18.0.0/16`,
  bukan IP container, jadi seharusnya tetap berlaku — tetapi "seharusnya" itu
  belum diukur, dan catatan 2026-09-02 di atas justru menyuruh membaca kolom
  `pkts`, bukan sekadar mendaftar aturannya.
- **`JWT_SECRET` tidak diset di waxum** (`WARN … using a random secret
  generated for this process only`). Sudah begitu sebelum pemindahan ini dan
  tidak disentuh, tapi terlihat di log yang sama jadi dicatat di sini.

### 2026-09-02, lanjutan: sesi "BOT IT" di `wa-phkg` pair lalu `Logged_out`

Sesi pertama yang pernah di-pair di host ini masuk ke `Disconnected` lalu
`Logged_out`. Pemindahan MySQL/MinIO beberapa jam sebelumnya adalah tersangka
yang jelas, dan **bukan** penyebabnya.

Yang benar-benar terjadi, dari log kedua service:

```
08:37:03  diesel: 29 migration ter-apply di whatsapp.db sesi 62fbc6…
08:37:16  waxum: Session 62fbc6…: Connected   (+ contact_update, pin/archive/
          mute_update, call_log_sync — sinkronisasi normal berjalan)
08:37:28  event terakhir; socket mati diam-diam, tanpa satu baris log
08:37:57  3 x HTTP 503 — POST /send menemukan sesi yang sudah mati
08:38:04  waxum: QR code received            ← klien kembali tak punya kredensial
08:39:18  waxum: Logged out: LoggedOut       (setelah sempat reconnect + sync)
```

Store lokalnya sehat, jadi ini bukan storage dan bukan hasil pemindahan:

```
device            1 baris        prekeys          811
identities        1              app_state_keys    18
__diesel_schema_migrations  29 baris, semua 08:37:03
clock: host & container NTP-synced, selisih <1 s terhadap laptop
```

Sesi **reconnect dan sync lagi** pukul 08:39:18 sebelum di-`LoggedOut` — jadi
kredensialnya sampai ke WhatsApp dan **WhatsApp yang menolaknya**. Badge
`Logged_out` di UI benar: `mapWaxumStatus` point 3 (paired + `waiting_for_qr` +
`is_logged_in:false`) memang berarti perangkatnya di-unlink dari sisi handset.

Dan pemindahan MySQL/MinIO tidak mungkin jadi sebabnya karena tidak ada
baseline yang rusak: `waxum_phkg.sessions` = 0 saat perencanaan **dan** tepat
sebelum `docker compose down`, jadi belum pernah ada sesi yang hidup di host
ini. Ini pairing pertama yang pernah dicoba di sini.

**Temuan yang bisa diperbaiki di repo ini.** waxum tidak punya endpoint unlink
sama sekali — seluruh permukaan teardown-nya, dibaca dari dokumen OpenAPI
live-nya, adalah:

```
/api/v1/sessions/{session_id}            (DELETE)
/api/v1/sessions/{session_id}/connect
/api/v1/sessions/{session_id}/connect/wait
/api/v1/sessions/{session_id}/disconnect
/api/v1/sessions/{session_id}/reconnect
```

Sementara `session_rows.html` selama ini mengonfirmasi penghapusan dengan
kalimat **"This unlinks the device from WhatsApp."** Itu benar hanya untuk
`WSM_WA_BACKEND=whatsmeow` (`cli.Logout(ctx)`), dan salah untuk default
`waxum` (`config.DefaultWABackend`), yang cuma memurnikan storage waxum. Jadi
setiap siklus create-pair-delete meninggalkan satu perangkat tertaut di
handset — dan operatornya baru saja diberi tahu bahwa itu sudah dilepas.
Operator di host ini melakukan dua siklus dalam satu menit (08:36:53 delete,
08:37:02 buat lagi) tepat sebelum gejalanya muncul.

Diperbaiki: kalimat konfirmasinya sekarang mengatakan yang sebenarnya dan
menyuruh membuka **Linked devices** di handset; doc comment
`waxumBackend.Logout` tidak lagi mengklaim "revokes the paired device";
`config.go` tidak lagi menyebut whatsmeow sebagai default padahal
`DefaultWABackend = "waxum"` seratus baris di atasnya. Dijaga
`TestTheDeleteConfirmationDoesNotPromiseAnUnlinkTheWaxumBackendCannotDo`,
dicek merah-saat-revert dengan mengembalikan kalimat lamanya:

```
--- FAIL: TestTheDeleteConfirmationDoesNotPromiseAnUnlinkTheWaxumBackendCannotDo
    sessions_test.go:169: the delete confirmation still promises an unlink …
```

`go test ./...` dan `go vet ./...` hijau.

**Batas jujur.** Bahwa akunnya sudah mentok batas linked-device WhatsApp adalah
kesimpulan yang paling cocok dengan bukti — `device_registry` mencatat daftar
perangkat akun ini sebagai device 0 (handset) plus companion 10, 11, 12, 14
pada 08:37:14 — tapi **tidak bisa dibuktikan dari host ini**. WhatsApp tidak
menyebutkan alasan logout-nya, dan jumlah maupun identitas perangkat tertaut
hanya terlihat di handset. Perbaikan di repo ini menghilangkan kebohongan yang
membuat orang tidak melihat ke sana; ia tidak memulihkan sesinya. Juga: image
yang berjalan di `wa-phkg` masih membawa kalimat konfirmasi yang lama sampai
image baru dibangun dan dimuat ke sana.

---

## 2026-09-04 — the public JSON API, published as an allowlist

Until today `https://wa.mfardiansyah.id` answered `404` for every `/api/v1`
path: the edge carried `location ^~ /api/v1/ { return 404; }` and every
consumer dialled `10.10.0.4:18801` over WireGuard instead. It now proxies a
fixed allowlist — auth login/logout, `/messages{,/group,/media}`, `/inbound`,
`/contacts/check`, and the `/sessions/{id}/` equivalents of the last three —
and still `404`s everything else.

**The edge is `clino` (103.127.134.25), not `rnd-v2-vpn`.** `rnd-v2-vpn` has no
nginx installed at all (`which nginx` → nothing); it runs the app container.
The live file is `/etc/nginx/conf.d/wa.conf` on `clino`, mirrored in
`docs/deploy-nginx-wa.conf.example` and now asserted by `nginx_edge_test.go`.

Secret handling: the `naufal` credential was read programmatically from
`~/.config/wsm/naufal.json` into a `curl -K` config file in a scratch dir and
never echoed. The minted token is recorded below only by its length.

### The deploy attempt that failed first, and why it matters

`nginx -t` rejected the first install outright:

```
nginx: [emerg] unknown directive "1,64}/(messages|messages/media|contacts/check)$" in /etc/nginx/conf.d/wa.conf:179
nginx: configuration file /etc/nginx/nginx.conf test failed
```

nginx's parser treats `{` and `}` as block delimiters **before** a location
regex is ever compiled, so the repetition quantifier in
`[A-Za-z0-9_-]{1,64}` ended the location early. Quoting the whole regex is the
fix. The install script restored the backup and reloaded nothing; verified
`md5sum /etc/nginx/conf.d/wa.conf` → `050ae6a3…`, byte-identical to the
original, and `https://wa.mfardiansyah.id/ -> 302` throughout. This matters
beyond the typo: **the edge is shared with nine other products**, and a failed
`nginx -t` there is a failed reload for all of them.

Second attempt: `nginx -t` successful, `systemctl reload nginx`, `RELOADED`.

### Published routes — reached the app (JSON bodies, not nginx HTML)

```
POST /api/v1/auth/login  (correct creds)   -> 200  token minted, 43 chars, expires_at 2026-09-11T07:07:52Z
POST /api/v1/messages    (bearer, bad to)  -> 400  {"error":"phone number too short — use international format like +62 812-0000-0000"}
POST /api/v1/messages    (Basic, bad to)   -> 400  same
GET  /api/v1/inbound     (bearer, since=)  -> 200  [{"id":"3B1359632D90D4F415ED","from_phone":"628232324xxxx",…}]
GET  /api/v1/inbound     (Basic, no since) -> 400  {"error":"since is required (RFC3339)"}
POST /api/v1/contacts/check (Basic)        -> 400  {"error":"phones is required"}
POST /api/v1/sessions/{id}/messages        -> 400  {"error":"phone number too short …}
POST /api/v1/auth/logout (bearer)          -> 204
POST /api/v1/messages    (revoked token)   -> 401  {"error":"unauthorized"}
POST /api/v1/messages    (no credential)   -> 401  {"error":"unauthorized"}
```

The `400`s are the point: a validation error in JSON proves the request was
proxied and authenticated. Nothing was sent — no `wa_messages` row was created
by any of these.

### Unpublished routes — stopped at the edge (nginx HTML, never proxied)

```
GET    /api/v1/health                       -> 404 nginx
GET    /api/v1/sessions            (Basic)  -> 404 nginx
GET    /api/v1/stats               (Basic)  -> 404 nginx
DELETE /api/v1/sessions/000…000    (Basic)  -> 404 nginx
POST   /api/v1/sessions/{id}/calls/tts      -> 404 nginx
GET    /api/v1/sessions/{id}/webhooks       -> 404 nginx
```

`DELETE` was probed with a bogus all-zero id precisely so that a wrong
allowlist could not have deleted a real session. The `404` bodies are nginx's
HTML page, not the app's `{"error":…}` — that distinction is the whole check,
since the app returns `404` JSON for an unknown session too.

Unchanged: `GET / -> 302` (dashboard), `GET /openapi.yaml -> 200`.

### Rate limits

```
15 rapid logins, wrong secret  ->  401 x6  then  429 x9      (wa_api_auth, 12r/m burst 5)
30 rapid sends, valid Basic    ->  400 x24 then  429 x6      (wa_api, 1r/s burst 20)
```

### The real client IP survives the hop — the reason the headers moved

The proxy headers used to live inside `location / {}`. A location never
inherits them from a sibling, so the new `/api/v1` blocks would have received
none — no `X-Forwarded-For` — and `auth.ClientIP` would have fallen back to
`RemoteAddr`, which is the WireGuard peer `10.10.0.1` for **every** public
caller alike. The failed-Basic throttle keys on that value, so one attacker
would have locked out every API client at once. Hoisting them to server level
is what prevents it; `TestTheEdgeSetsProxyHeadersAtServerLevelSoAPILocationsInheritThem`
keeps them there.

The app logs no client address, so this was measured through the throttle
itself. Holding one IP down with failed Basic attempts paced at ~1.05s (fast
enough to outrun the app's 1-per-3s refill, slow enough to clear the edge's
1r/s), while probing from a second public IP at the same moment:

```
laptop  103.161.62.130 : 401 429(Retry-After) 401 429 429 401 429 401 429 429 401 429
clino   103.127.134.25 : 401 401 401
```

Those 429s carry `Retry-After`, so they are the APP's throttle, not nginx's.
A second address getting clean 401s while the first is throttled is only
possible if the two are keyed separately — i.e. `X-Forwarded-For` is arriving
and `WSM_TRUSTED_PROXIES=10.10.0.0/24` is trusting the `10.10.0.1` hop that
sets it.

### Repo side

`nginx_edge_test.go` is new. All four tests confirmed **red** against the
unmodified config before the change and green after:

```
--- FAIL: TestTheEdgeDeniesEveryAPIRouteItDoesNotExplicitlyPublish
--- FAIL: TestTheEdgePublishesOnlyTheSendAndInboundAPIRoutes
--- FAIL: TestTheEdgeSetsProxyHeadersAtServerLevelSoAPILocationsInheritThem
--- FAIL: TestTheEdgeRateLimitsTheAPIWithWaPrefixedZones
```

`go build ./...`, `go vet ./...` and `go test ./...` all green.

**Not verified.** No real message was sent through the public route — every
send probe used a deliberately invalid `to` so that nothing left the gateway.
That the published surface can actually deliver a message is inherited from the
private-route evidence above, not re-measured here.

**Rollback**, if the public API ever needs to be withdrawn:

```
sudo cp /etc/nginx/conf.d/wa.conf.bak.20260904140717 /etc/nginx/conf.d/wa.conf
sudo nginx -t && sudo systemctl reload nginx
```

### Same day — the redeploy onto `rnd-v2-vpn`, and the waxum wedge for the third time

`git pull` to `9fdcd7c`, then `make up REMOTE_DB=1` (which is `up -d --build`).
The running stack was confirmed to be in overlay mode first, from the container
itself rather than assumed:

```
config_files = docker-compose.yml,docker-compose.remote-db.yml
networks     = whatsapp-with-dashboard_default
```

Only `app` was rebuilt and recreated; `waxum` was left running. Volumes
`whatsapp-with-dashboard-sqlite` and `whatsapp-with-dashboard-waxum` were listed
before and after and are unchanged. No `down`, no `-v`.

**The app restart wedged the session again** — the 2026-08-27 hazard, now on its
third recorded occurrence. Before: `connected:1`. For 60s after: `connected:0`,
with waxum's `/status` oracle reading

```
{"status":"connecting","is_logged_in":true,"socket_alive":false,…}
```

while waxum's own logs repeated `Session …: Connected` and emitted a `connected`
event on every retry — no error, no 429, no logout. `mapWaxumStatus` requires
`is_logged_in && socket_alive`, so WSM correctly reported it disconnected.

One detail this run adds to the hazard note: **waxum kept receiving live traffic
throughout**. Real inbound `message`, `receipt`, `chat_presence` and
`device_list_update` events arrived at 07:15:52, 07:16:17 and 07:16:47 while
`socket_alive` was still `false`. So the flag can be wedged on a connection that
is demonstrably carrying data — "socket_alive:false" is not proof the websocket
is dead, only that WSM will treat the session as unusable.

Recovery was the documented one, `docker compose … restart waxum`:

```
15s..90s  health -> {"connected":1,"sessions":1,…}
          /status -> {"status":"logged_in","is_logged_in":true,"socket_alive":true,…}
```

Stable across six polls at 15s intervals. No re-pairing, volumes untouched.

Public surface re-checked against the freshly built binary — `POST
/api/v1/messages` (Basic) `-> 400` JSON, `GET /api/v1/inbound -> 200`,
`/api/v1/health -> 404` nginx, `/api/v1/sessions -> 404` nginx, `/ -> 302`, and
the session reads `state:"connected"` on the private route.

**Not verified: that this session can actually SEND.** Nothing in this run put a
message on the wire. Per the hazard note above, `s.state`, `Live()` and
`s.sendQ != nil` are independent, nothing but `enqueue` reads `sendQ`, and a
wedged socket and a nil queue present identically — so "connected" here is
evidence about the first two only.

## 2026-09-09 — the `connecting`/`socket_alive:false` wedge, root-caused and fixed

Host `rnd-v2-vpn` (10.10.0.4). The wedge the 2026-08-27 and 2026-09-04 entries
above describe is a race INSIDE waxum, and this run identified it, fixed it,
and reproduced the trigger against the fixed build.

**The report.** Session `59c9…` (`6285132148259`, push_name `Ardi`) would not
come back after a Deactivate. Its `/status`, sampled repeatedly over 78
minutes and never once different:

```
{"status":"connecting","is_logged_in":true,"socket_alive":false,
 "paused":false,"phone_number":"6285132148259","push_name":"Ardi",
 "reachability":null}
```

`reachability:null` is the field that names it — waxum documents it as null
"when there is no live client in this process at all". Meanwhile waxum logged
`Session 59c9…: Connected` on every one of WSM's reconnect attempts, always
DOUBLED (two lines ~0.3s apart), while WSM logged `auto-reconnect attempt …
(attempt 1/5)` in a loop. The operator's actions, from WSM's own log:

```
23:07:24 POST /sessions/59c9…/deactivate -> 200
23:07:25 POST /sessions/59c9…/activate   -> 200      <- one second later
23:07:29 POST /sessions/59c9…/activate   -> 200
23:07:48 POST /sessions/59c9…/activate   -> 200
```

**Root cause**, read off the fork's source at `7013726`: `disconnect_session`
returns as soon as it has asked the client to stop, but the bot task is still
parked in `bot.run().await`. The tail of `connect_client`
(`src/handlers/sessions.rs`) then ran `runtime.set_client(None)` with no
ownership check, so the OLD bot's teardown evicted the NEW bot that the
Activate one second later had just installed. Slot empty, permanently; every
further `connect` built another bot that connected (the doubled `Connected`
lines are orphan + newcomer) and was evicted by its predecessor in turn.
waxum's own reconnect watchdog cannot rescue this: it bails at
`let Some(client) = runtime.get_client() else { continue }`.

**Recovery**, the documented one, at 00:44 UTC:

```
docker compose -f docker-compose.yml -f docker-compose.remote-db.yml restart waxum
→ /status {"status":"logged_in","is_logged_in":true,"socket_alive":true,…}
```

Stable across 7 polls at 25s intervals; WSM logged `state transition
session=59c9… from=disconnected to=connected` at 00:45:00. No re-pairing.

**The fix**, built on the host as `ardihikaru/waxum:0.12.2-client-slot-ownership`
from `7013726` plus a patch: `SessionState::clear_client_if` (`Arc::ptr_eq`)
so a run-loop teardown only clears the slot it still owns, and a same-owner
guard at the top of `handle_event` so a superseded bot cannot write another
bot's session state. `WAXUM_TAG` bumped in the host's `.env` (previous value
backed up to `.env.bak.20260909012414`).

**Reproduction against the fixed build** — the exact trigger, one cycle then
three back-to-back:

```
01:27:43 logged_in/socket_alive:true
01:27:44 POST /disconnect -> 200 ; +1s POST /connect -> 200
01:27:56 logged_in socket_alive:true      (stable across 4 polls / 48s)

3x [ POST /disconnect -> 200 ; +1s POST /connect -> 200 ; +4s ]
01:29:23 logged_in socket_alive:true      (stable across 5 polls / 75s)
```

waxum now logs exactly ONE `Connected` per connect (01:27:45, 01:28:54,
01:29:00, 01:29:05) where the old build logged two — the orphan signature is
gone. Under the old build this same sequence produced the permanent wedge.

**WSM-side change in this repo**, so a future occurrence is not silent:
`internal/waxum.Status.Reachability` is now a `*string` (a plain `string`
cannot tell a JSON `null` from a real value — `encoding/json` leaves the field
untouched on null), and `internal/wa/backend_waxum.go`'s
`isOrphanedClientWedge` escalates a sustained `is_logged_in && !socket_alive
&& reachability == null` to `markStalled` with a reason naming the restart.
`mapWaxumStatus` is unchanged — its `disconnected` verdict was always correct.

**Not verified: that this session can SEND.** As with the 2026-09-04 entry,
nothing here put a message on the wire; `sendQ` was not exercised.

**Not verified: that the patch survives a `docker compose pull`.** The fixed
image exists only on this host, built from `~/waxum-slotfix`; the patch has
NOT been pushed to the `ardihikaru/waxum` fork or to Docker Hub. A rebuild or
a pull of `0.12.2-uploaded-media` reintroduces the bug.

## 2026-09-09 — Self Convo pages, e2e overflow + nav on three viewports

Base URL: `http://127.0.0.1:8801` (throwaway instance booted by
`scripts/e2e-env.sh`, app tables in a fresh SQLite file, `WSM_WA_BACKEND`
defaulting to `whatsmeow` there — **no MySQL contacted**).
`WSM_SELF_CONVO_ENABLED` is unset (false), same as every other feature flag
this script never sets — this run therefore proves `/self-convo` and
`/self-convo/templates` render and lay out correctly with the flag OFF, the
state the e2e box is always in; the trigger/cancel-enabled path (Verification
steps 1-4 in the plan) still needs a dev box with the flag set and two real
paired sessions, which this environment has neither of.

Browser: Playwright 1.62.1 (`package.json`), Chromium, launched with no extra
system packages — `npx playwright install-deps --dry-run
chromium` reported 11 missing packages (fonts + Xvfb/X server, cosmetic for a
headless run), and a direct `chromium.launch()` smoke check succeeded without
them, so no privileged step or private-prefix unpack was needed this time.

Browser user: admin `ardihikaru`, from a throwaway `_data/paswd`-shaped file
written under this session's scratch directory (never inside the repo, never
named `_data/paswd`), pointed to via `WSM_E2E_PASSWORD_FILE`.

**This host has no `.env` `WSM_MINIO_*` values at all** (checked by grep,
value never read), and the only reachable MinIO container is the shared
production one (`master-compose-ai-minio-master-1`) — not used. Instead: a
disposable `minio/minio` container, `wsm-e2e-minio`, bound to
`127.0.0.1:19000` only, throwaway root credentials generated for this run and
exported straight into the shell running Playwright (never written to a
file, `.env`, or this document). The app auto-creates its bucket on boot
(`internal/storage.EnsureBuckets`), so no bucket bootstrap step was needed.

Changed for this task: `cmd/e2eseed/main.go` (`seedSelfConvo`, called inside
`run()`'s existing "sessions already seeded → skip" guard — it is not
independently idempotent, pinned by
`TestSeedSelfConvoIsNotIndependentlyIdempotentSoItMustStayInsideTheExistingSessionsGuard`),
`e2e/overflow.spec.ts` (`PAGES` += `/self-convo`, `/self-convo/templates`;
`waitForContent` += `#convo-run-rows`, `#convo-template-rows`), and
`e2e/nav-mobile.spec.ts` (`DESTINATIONS` += `"Self Convo"`, after
"Verify Number").

Results:

| Check | Result |
|---|---|
| `go test ./cmd/e2eseed/...` (seed test, red-then-green) | RED first: `undefined: seedSelfConvo` (compile failure) with the test present and the function missing; GREEN after — `ok github.com/ardihikaru/whatsapp-with-dashboard/cmd/e2eseed 0.062s` |
| `go test ./...` | `ok` for every package (18 packages, 2 with no test files) |
| `go vet ./...` | clean |
| Playwright `e2e/overflow.spec.ts` + `e2e/nav-mobile.spec.ts`, all projects (`setup`, `mobile-portrait`, `mobile-small`, `desktop`) | **75 passed, 0 failed** (31 skipped — desktop/phone-only drawer guards and the "page fits on one screen, nothing to scroll" skip on short pages) in 1.5m |
| `/self-convo`, mobile-portrait (390×844) | no element wider than the viewport — pass; scrolled to its bottom — pass |
| `/self-convo/templates`, mobile-portrait | no element wider than the viewport — pass; scrolled to its bottom — SKIPPED (content fits on one screen at this viewport, not a failure) |
| `/self-convo`, `/self-convo/templates`, mobile-small (360×740) | both checks pass on both pages |
| `/self-convo`, `/self-convo/templates`, desktop (1280×800) | both checks pass on both pages |
| `"Self Convo"` drawer destination, mobile-portrait + mobile-small | visible, ≥36px tall, not covered by another element (`nav-mobile.spec.ts`'s per-destination loop) |

No CSS fix was needed: `web/static/css/self-convo.css` (already landed in an
earlier step per the plan) did not overflow either viewport with the seeded
content (one template row, three run rows spanning `scheduled`/`done`/
`failed` badges, an ~117-character error string, and the full 10-turn example
script as the template body) — the overflow assertions above are the
measurement that would have caught it if it had.

**`e2e/screenshots.spec.ts` was not changed.** It iterates no page list — it
visits only `/dashboard` and `/logs` by name — so there was nothing to add
per this task's own instruction ("add the two pages if it iterates a page
list").

**`e2e/nav-mobile.spec.ts`'s `DESTINATIONS` was left short of "Voice Calls"
and "System Log"**, which `internal/handler/nav_mobile_test.go`'s own list
does carry. Checked before deciding: every use of `DESTINATIONS` in this file
loops per-name (`for (const name of DESTINATIONS) { await
expect(sidebar.getByRole("link", { name, exact: true })).toBeVisible(); }`
and the tappability loop below it) — containment, never a comparison against
the drawer's full rendered set — so per this task's instruction the two
missing names were left out rather than added opportunistically.

Teardown: `docker stop wsm-e2e-minio && docker rm wsm-e2e-minio` (confirmed
gone from `docker ps -a`; only the pre-existing
`master-compose-ai-minio-master-1` remains), the throwaway password file
deleted from the scratch directory, the dev app process on `:8801` killed,
`make e2e-reset` run (`_data/e2e`, `e2e/.auth` removed).

**No secret was written into this file.** The admin password, the throwaway
MinIO credentials, and the session cookie value were all read/generated and
used only inside the process that needed them, and none of the three appears
above.

**Not verified: the trigger/cancel path with the flag enabled.** This
environment never sets `WSM_SELF_CONVO_ENABLED` and pairs no real WhatsApp
session, so the plan's own Verification steps 1-4 (a live trigger, `/logs`
showing `category=self_convo`, a mid-run cancel, an app restart mid-run) were
not exercised here and need a dev box with two paired sessions per the plan's
own instructions, not this e2e box.

## 2026-09-09 — Self Convo shipped to production (`rnd-v2-vpn`)

Host `rnd-v2-vpn` (10.10.0.4), stack `whatsapp-with-dashboard` (base +
`docker-compose.remote-db.yml`). Commit `88f29e8`. No secret was read, echoed
or written into this file; `.env` was never opened.

| Step | Result |
|---|---|
| `git pull --ff-only` | `d0ab16e` → `88f29e8` |
| `--profile migrate build migrate` (fresh, per the 2026-08-31 stale-image trap) | `migrate Built` |
| `migrate status` before | `0001`–`0006` applied, `Pending -- 0007_self_convo.sql` |
| `migrate up` on the shared production MySQL (`10.10.0.4:5306`) | `OK 0007_self_convo.sql (78.21ms)`, `successfully migrated database to version: 7` |
| `make up REMOTE_DB=1` | `app Built`; `app-1 Recreated/Started`; `waxum-1 Running` (not restarted) |
| App boot log | `minio: connected …`, `self-convo: disabled (set WSM_SELF_CONVO_ENABLED=true to enable it)`, `listening on http://localhost:8800` |
| `GET http://10.10.0.4:18801/api/v1/health` | `200` `{"backend":{"name":"waxum","reachable":true},"connected":1,"sessions":2,"status":"ok"}` |
| `GET http://10.10.0.4:18801/self-convo` unauthenticated | `303 → /login` (page is behind `RequirePage`) |
| `GET https://wa.mfardiansyah.id/login` (edge) | `200` |

Notes:
- The app's published port binds `10.10.0.4:18801`, not loopback — a
  `curl 127.0.0.1:18801` answers connection refused and proves nothing.
  `docker port` names the real address.
- `WSM_SELF_CONVO_ENABLED` is NOT set in production `.env`, so the feature is
  deployed but off: the pages render with the disabled notice and the runner
  is never started. Enabling it is a deliberate, separate step.
- The waxum container was left running (the paired identity lives in its
  volume; `make up` only recreated `app`).

**Not verified:** a real trigger between two paired sessions (the flag is
off), and `wa-phkg` — unreachable from the deploying laptop at this time
(`No route to host`; it needs the PHKG LAN or the FortiClient VPN), and its
deploy path is an image bundle, not `git pull`.

### Same day — `WSM_SELF_CONVO_ENABLED=true` switched on in production

Operator's decision, same host and stack. `.env` was backed up to
`.env.bak.20260909-173835` (mode 600) and the flag appended with `printf`;
no other value was read or printed.

| Step | Result |
|---|---|
| `grep -c '^WSM_SELF_CONVO_ENABLED=true$' .env` | `1` |
| `docker compose … up -d --no-build app` | `app-1 Started` — **and `waxum-1 Started`**, see note |
| App boot log | `self-convo runner started`, `listening on http://localhost:8800` |
| Health right after | `200`, `connected:0, sessions:2` (waxum had just restarted) |
| Health at 17:39:23 (24 s later) | `200`, `connected:1, sessions:2` |
| App log 17:39:14 | `wa: state transition session=59c9… from=disconnected to=connected` |

Notes:
- **`up -d app` recreated `waxum` too.** Every service carries
  `env_file: [.env]`, so an `.env` change makes compose recreate waxum as well
  as `app` (and `app` depends on it). That is a waxum PROCESS restart — the
  documented, identity-preserving recovery, never `down -v` — but it does drop
  every session for the duration of the reconnect. The paired session came
  back on its own within 25 s; WSM's own `statusPump` verdict (the state
  transition line above), not waxum's `Connected` log, is the evidence.
  Next time, prefer `docker compose … up -d --no-deps app` to leave waxum
  alone when only the app's env changed.
- The second session (`sessions:2`, `connected:1`) was disconnected before
  this deploy as well (health showed `connected:1` at 17:20, pre-flag); this
  change did not alter it.

**Not verified:** an actual Self Convo run in production — no trigger was
pressed. The runner is started and idle; `/self-convo` now renders without
the disabled notice for a logged-in operator (not screenshotted here).

## 2026-09-10 — session reads and the health probe published at the edge

`GET /api/v1/sessions` answered `404` from the public edge, so listing sessions
meant dialling `10.10.0.4:18801` over WireGuard. Published today, together with
`GET /api/v1/sessions/{id}` and `GET /api/v1/health`, so an external integration
can discover a session id and then send with it over the public name alone.

**The send half was already public and needed no change.** Probed before
touching anything, unauthenticated:

```
GET /api/v1/sessions/000…000/messages   -> 401 application/json {"error":"unauthorized"}
GET /api/v1/messages                    -> 405 Method Not Allowed
GET /api/v1/sessions                    -> 404 text/html  (nginx)
GET /api/v1/health                      -> 404 text/html  (nginx)
```

The session-scoped regex is method-agnostic, so `POST /sessions/{id}/messages`
had been reachable since 2026-09-04; only the listing was missing.

**Writes share the published paths and are held back by method, not by path.**
nginx resolves a location by path alone, so `location = /api/v1/sessions` would
have published `POST` (create) and the detail regex would have published
`DELETE` — which burns a linked-device slot and cannot unlink on waxum. Each new
block therefore opens with
`if ($request_method !~ ^(GET|HEAD)$) { return 404; }`. That runs in nginx's
rewrite phase, before `limit_req` and before `proxy_pass`, so a gated write is
answered by nginx, never proxied, and draws no rate-limit budget.
`limit_except` was rejected: it yields 403, not 404, and `return` is not
permitted inside it.

`/api/v1/health` is the one route registered without `RequireAPI`, so it now
answers the entire internet with session counts and the backend's identity. It
was published deliberately, for uptime probing from outside the tunnel, and put
behind its own `wa_health` zone so an anonymous scraper cannot spend the budget
authenticated sends draw on.

### Deploy

Host `clino` (103.127.134.25), nginx 1.18.0. The live file and the repo mirror
were byte-identical before the change (`358082ac453c73885c5c910b0fbadd9f`, 11160
bytes), so the mirror was a safe editing base.

```
sudo cp -a /etc/nginx/conf.d/wa.conf /etc/nginx/conf.d/wa.conf.bak.20260910005405
   backup md5 358082ac453c73885c5c910b0fbadd9f  (equals the pre-change live file)
installed     md5 22a7afb4fdee5ac8d8b9fe0423d52898  (equals the repo mirror)
sudo nginx -t   -> syntax is ok, test is successful
sudo systemctl reload nginx  -> active, RELOADED
```

### Published — reached the app (JSON, not nginx HTML)

```
GET  /api/v1/health                   (no credential)  -> 200 application/json
       {"backend":{"name":"waxum","reachable":true},"connected":2,"sessions":2,"status":"ok"}
GET  /api/v1/sessions                 (Basic)          -> 200 application/json  2 sessions, both connected
GET  /api/v1/sessions/{id}            (Basic)          -> 200 application/json  state=connected
HEAD /api/v1/sessions                 (Basic)          -> 200
GET  /api/v1/sessions/000…000         (Basic)          -> 404 application/json {"error":"session not found"}
GET  /api/v1/sessions                 (no credential)  -> 401 application/json {"error":"unauthorized"}
```

The `{"error":"session not found"}` line is the strongest single proof the
detail regex is published: only the app can produce that body.

### Stopped at the edge (nginx HTML — never proxied)

```
POST   /api/v1/sessions                            -> 404 text/html
DELETE /api/v1/sessions/000…000                    -> 404 text/html
PUT    /api/v1/sessions/000…000                    -> 404 text/html
GET    /api/v1/stats                               -> 404 text/html
GET    /api/v1/sessions/000…000/pairing            -> 404 text/html
GET    /api/v1/sessions/000…000/calls/tts          -> 404 text/html
GET    /api/v1/gateway/health                      -> 404 text/html
```

Every write probe was sent unauthenticated and against an all-zero session id,
so no outcome could have reached a real session even had the gate been absent.

Secret handling: the `naufal` credential was read programmatically from
`~/.config/wsm/naufal.json` into a `curl -K` file in a scratch dir, `chmod 600`,
deleted after the run, and never echoed.

### Repo side

`TestTheEdgeGatesThePublishedSessionReadRoutesToSafeMethodsOnly` is new.
Mutation-tested rather than merely observed green — with the gate line deleted
from the detail block it fails naming DELETE and the linked-device slot, and
with `/api/v1/health` moved onto `wa_api` the rate-limit test fails naming the
shared budget. The config was restored byte-identical after each mutation.
`go vet ./...` and `go test ./...` green, 0 packages failing.

**Not verified.** No message was sent through the public route in this run.
The `wa_health` vs `wa_api` budget separation was reasoned from nginx's phase
order and asserted in the test, but not measured live — no 429 was provoked.
The `1r/s` read throttle was not measured either, and only one source IP was
used throughout.

**Rollback**, if the reads or the anonymous health probe need to be withdrawn:

```
sudo cp /etc/nginx/conf.d/wa.conf.bak.20260910005405 /etc/nginx/conf.d/wa.conf
sudo nginx -t && sudo systemctl reload nginx
```

### Same day — the app redeployed onto `rnd-v2-vpn`

The commit above changes no app behaviour: `routes_api.go` is comment-only and
the sole compiled change is `nginx_edge_test.go`, which never ships in the
binary. The redeploy was requested anyway, so it is recorded for what it cost
rather than for what it changed.

`git pull --ff-only` to `b63dc18`, then `make up REMOTE_DB=1` on
`/home/sismedika/devel/projects/whatsapp-with-dashboard`. Only `app` was
recreated — `waxum` stayed `Up (healthy)` throughout, which is the outcome to
want: its volume is the paired device's identity.

The restart did drop both sessions, and they came back on their own:

```
immediately after start   connected=0  sessions=2   both state=disconnected
t+25s                     connected=1
t+75s                     connected=2   — same as before the redeploy
```

No `error`/`panic`/`fatal` lines in `docker compose logs app`, and no waxum
wedge: this is the ordinary reconnect, not the `is_logged_in && !socket_alive`
fingerprint. Worth noting the ~75s window is real, though — an app restart on
this stack costs about a minute of send capacity even when nothing goes wrong.

The public surface was re-probed after the redeploy and is unchanged:
`GET /api/v1/health` 200 anonymously, `GET /api/v1/sessions` 200 with the
credential, `DELETE /api/v1/sessions/000…000` still 404 `text/html` from nginx.
