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
