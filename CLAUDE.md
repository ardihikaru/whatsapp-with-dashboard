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

**Production MySQL is shared and is not a test target.** Since 2026-08-27 it
is `10.10.0.4:5306` — the `master-compose-ai` stack MOVED to the app host, and
`/home/sismedika/devel/projects/infra-prod`,
`master-compose-ai-mysqldb-master-1` and the `master-compose-ai-net` network
are all here now. It is still SHARED, holding several other products' data, so
everything this entry ever said still applies: do not create a schema there,
do not migrate it outside a real deploy, do not point a dev binary at
`wa_dashboard`. Being local makes it easier to damage, not safer. For local
runs use `WSM_APP_DSN=sqlite:…` (see `internal/db.OpenAppStore`); `make e2e`
does this for you.

**A waxum session can wedge in `connecting` forever, and waxum's own logs
will tell you it is fine.** Observed 2026-08-27: `GET /sessions/{id}/status`
returned `{"status":"connecting","is_logged_in":true,"socket_alive":false}`
unchanged for as long as it was sampled, while waxum simultaneously logged
`Session …: Connected` and emitted a `connected` event on every reconnect
attempt, with no error, no 429 and no logout anywhere in its output.
`mapWaxumStatus` requires `is_logged_in && socket_alive`, so WSM correctly
reported it disconnected — and looked wrong doing so.

Two things make this expensive to debug:

  - **`/status` is the oracle, not waxum's logs.** The logs describe an
    intent to connect; `socket_alive` describes the outcome. They disagree,
    and only one of them is what WSM reads.
  - **WSM's own reconnect does NOT clear it.** The supervisor calls waxum's
    `Connect`, which produces another cheerful `connected` event and leaves
    the socket exactly as dead as before. The app will loop like that
    indefinitely. Only restarting the waxum PROCESS cleared it
    (`docker compose … restart waxum` — never `down -v`, the volume is the
    paired device's identity). Recovery needed no re-pairing.

**"Connected" means three different things here, and they can disagree.**
`s.state` (statusPump's verdict), `Live()` (an atomic fed by the same poll),
and `s.sendQ != nil` (whether a sender loop exists) are independent. Two of
them drive every operator-facing signal — `/sessions`, `/api/v1/health`,
`SelectConnected` — and the third drives whether a send actually works.
Nothing reads `sendQ` except `enqueue`, so a nil queue is invisible until
someone tries to send. That is exactly how the waxum backend shipped able to
send only between a pairing and the next restart (fixed: `Connect` now calls
`startSenderLocked`, see `TestAWaxumSessionCanSendAfterConnectingWithoutPairing`).
When "everything says connected but nothing sends", check all three before
concluding anything — and note that a wedged socket and a nil queue present
identically.

**`web/templates/login.html` duplicates `base.html`'s entire `<head>`.** Any
stylesheet or script change has to land in **both**. Guarded by
`TestEveryHeadTemplateLinksTheResponsiveStylesheet`.

**Stylesheet load order is load-bearing.** `base.html` documents that
concatenating the first eight CSS files reproduces the old `minimal.css`
byte-for-byte, and `charts.css` deliberately carries `@media` rules that
override `layout.css`. `responsive.css` is a ninth file loaded last and
overrides by cascade. Do not reorder, and prefer adding a later file to editing
those eight.

**The app and its MySQL share a host again (2026-08-27), and this entry used
to say the opposite.** The shared stack moved onto `10.10.0.4`, so
`master-compose-ai-net` and `mysqldb-master` now DO resolve here — the failure
this entry was written about (`network declared as external, but could not be
found`, which reads as "the shared stack is down" and is not) no longer
reproduces on this host. `make up REMOTE_DB=1` remains correct and is what the
2026-08-27 deploy used: the overlay resets the network and points services at
`WSM_MYSQL_HOST` from `.env`, which is `10.10.0.4`. NOT verified since the
move: whether plain `make up` (no overlay) also works now. Do not assume it
does because the network exists — check before relying on it.
`docker-compose.remote-db.yml` (`make up REMOTE_DB=1`) undoes both. Note `!reset`, not `[]`: compose MERGES a
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
losing it means re-pairing the handset by QR. Since 2026-08-23 there is no
second copy anywhere: the old host's volume was deleted after the move, so
`whatsapp-with-dashboard-sqlite` on the app host is the only one that exists.
If you ever need to move it again, stop the container FIRST — a clean stop
checkpoints the WAL, and a live `wsm.db` copied alongside its `-wal` is a
gamble with that identity — then `sha256sum` both ends before deleting anything.

**waxum's volume is identity material too, and waxum cannot be replicated at
all.** `WSM_WA_BACKEND=waxum` (the default) keeps no whatsmeow SQLite store,
but waxum's own registry sits partly in MySQL and partly under
`WHATSAPP_STORAGE_PATH` on the `waxum-sessions` volume — a per-session SQLite
`whatsapp.db` plus an instance-lock file. Same rule as `wsm-sqlite`: never
`docker compose down -v`. Unlike the app, waxum can't even be scaled or rolled
read-only — the instance lock forbids a second process pointed at one storage
dir, so there is no rolling-restart story for it either.

**Deleting a session does NOT unlink the device — on waxum, nothing can.**
`meowBackend.Logout` calls `cli.Logout(ctx)`, a real WhatsApp unlink.
`waxumBackend.Logout` calls waxum's `DeleteSession`, and waxum's API has no
unlink call to offer: `connect`, `disconnect`, `reconnect`, `pair`, `qr`,
`DELETE` — that is the whole teardown surface (checked 2026-09-02 against the
live OpenAPI document of `0.12.2-uploaded-media`). Since
`config.DefaultWABackend` is `waxum`, the default behaviour is that a deleted
session disappears from the dashboard while the device stays in the handset's
**Linked devices** list forever.

WhatsApp caps an account's linked companion devices at a small number, so this
leaks a slot per create-pair-delete cycle, and the failure it eventually
produces looks nothing like its cause: a freshly paired device connects,
syncs contacts and app-state normally for seconds to half a minute, then
WhatsApp sends `LoggedOut` — and does it again on every retry. Locally
everything is healthy (device row present in the session's `whatsapp.db`,
prekeys uploaded, diesel migrations applied, clock NTP-synced), so it reads as
"pairing is broken on this host" when the account is simply full. Observed on
`wa-phkg` 2026-09-02, where two pair-then-delete cycles inside one minute
preceded exactly that.

`session_rows.html`'s delete confirmation now says this to the operator; it
used to claim the opposite, and
`TestTheDeleteConfirmationDoesNotPromiseAnUnlinkTheWaxumBackendCannotDo` keeps
it from coming back. **Before concluding anything about a session that pairs
and is then logged out, count the handset's linked devices and clear the
orphans** — no amount of evidence gathered on the host can see them.

**Rollback from waxum to whatsmeow is asymmetric.** Setting
`WSM_WA_BACKEND=whatsmeow` restores the old in-process transport at the next
boot, but a device paired *through waxum* has its credentials in waxum's
volume, not whatsmeow's — flipping the flag back does not bring them with it.
The flag buys a clean rollback for a session that hasn't paired under waxum
yet; after it has, rolling back means re-pairing that handset by QR, the same
as losing the volume outright.

**`WAXUM_TOKEN` / `SUPERADMIN_TOKEN` is a root credential for every session on
that waxum instance**, compared in constant time and bypassing JWT entirely.
Treat it exactly like the `_data/` secrets above: never echo it, never write
it into a file, a log, a test fixture, or a commit. `.env.example` documents
the variable but ships no value.

**`docker compose config` without `--quiet` prints every secret in `.env`,
for every service.** Every service in `docker-compose.yml` uses `env_file:
[.env]`, and compose renders that file inline into each service's output —
not just the one you meant to inspect. That has already put
`MYSQL_ROOT_PASSWORD` (root on the same shared production MySQL as the
`_data/` entry above), `WSM_MYSQL_PASSWORD`, `WSM_MINIO_ACCESS_KEY`, and
`WSM_MINIO_SECRET_KEY` into a plaintext log, more than once before anyone
noticed. Only `docker compose config --quiet` is safe to run here — it
validates parsing and prints nothing, and it's the only form that belongs in
a command, a recipe, or an evidence note. If you genuinely need one rendered
value, filter for that single key on a service that carries no secrets;
never dump the whole document. Same rule as the `_data/` entry above,
tripped through a different door: read it programmatically, let the value
die with the process.

**The `db-*` Makefile recipes reach MySQL over TCP, never `docker exec`.**
`db-bootstrap`, `db-verify`, `db-drop`, `db-bootstrap-waxum`, and
`db-verify-waxum` dial `mysql --protocol=TCP` at
`WSM_MYSQL_HOST:WSM_MYSQL_PORT` — the same address the app itself uses — and
never `docker exec` into a MySQL container. The original reason was topological — MySQL's container lived on a
different host, so `docker exec -i master-compose-ai-mysqldb-master-1 …` had
no container to find. Since the 2026-08-27 move that container IS on this
host, so `docker exec` would now appear to work. Keep TCP anyway: the recipes
must keep working from any host, they read `WSM_MYSQL_HOST`/`_PORT` from
`.env` like the app itself does, and a `docker exec` form silently couples
them to one machine's container names. A future move back would break it, and
the failure would read as a docker-permissions problem (no access to
`/var/run/docker.sock`, or "No such container") rather than the topology
problem it actually is.

**waxum rate-limits per peer IP with a very small default burst, and a 429 is
invisible in its own logs.** waxum's tower_governor middleware defaults to a
burst of only ~3 requests before answering 429 — live measurement, FROM THE
APP CONTAINER'S OWN NETWORK NAMESPACE: 60 rapid `GET /api/v1/sessions` got 57
x HTTP 429 and 3 x HTTP 200. `docker-compose.yml`'s `waxum` service now sets
`RATE_LIMIT_PER_SECOND`/`RATE_LIMIT_BURST` generously (waxum is reachable only
on the compose network, so there's no reason to keep its public-internet
defaults), and `internal/wa`'s pairing/status pollers were restructured to
poll far less aggressively (`backend_waxum.go`'s `pairPump`, point 7) — but if
either regresses, five things about this hazard are easy to get wrong:
  - **`RATE_LIMIT_PER_SECOND` is not a rate.** tower_governor's
    `GovernorConfigBuilder::per_second()` sets the REPLENISH INTERVAL, in
    seconds, for one token, not requests-per-second — waxum's own default of
    20 means one token every 20 seconds. A bigger number is TIGHTER, not
    looser; "raising" it toward 50 makes pairing fail faster, exactly
    backwards from the intent. Keep it small (near 1) and tune generosity
    through `RATE_LIMIT_BURST` instead.
  - **429s never show up in waxum's own logs.** tower_http's trace layer only
    classifies 5xx as a failure; a 429 logs like a normal request. The only
    way to see them is to measure the response codes yourself.
  - **A single 429 must never be fatal.** It used to be mapped to
    `wa.ErrQueueFull` ("send queue full") — a completely different condition
    (sender.go's per-session send channel being full) — and aborted pairing
    outright with a misleading log line. See `wa.ErrBackendBusy` and
    `internal/waxum.Client`'s retry-with-backoff in `do()`.
  - **Measure the limit from the SAME network namespace the real traffic
    comes from.** Testing from a different container (or the host) gives the
    bucket a fresh IP and therefore a fresh token count — that reads as "the
    limit is fine" and is not; it is the exact false confidence that let this
    incident go undiagnosed for a while.
  - **Measuring right after restarting waxum is misleading too.** The bucket
    starts full, so a burst right after a restart succeeds regardless of the
    configured rate and proves nothing. A valid measurement drains the burst
    first, then waits a known idle period and counts successes against that.

**waxum's `MediaData` is `#[serde(untagged)]` and variant order is
load-bearing.** In stock 0.12.2 `Url` is declared first with no
`deny_unknown_fields`, so a full pre-upload descriptor matched `Url`, the
other six fields were silently dropped, waxum fetched the descriptor's
`mmg.whatsapp.net` URL — which is the **encrypted** blob and passes the SSRF
guard — and sent an undecryptable file while the API answered 200. Media
sends therefore require the patched fork (`ardihikaru/waxum`), not
`fdciabdul/waxum:0.12.2`: a silent-success failure mode like this one does
not announce itself, it just produces a file the recipient's client can
never open.

**waxum's SSRF guard (`src/net_guard.rs`) has no env escape hatch** and
rejects any URL resolving to loopback/RFC1918/ULA/CGNAT/link-local. It
applies to send-media-by-URL, webhook registration *and* delivery, and call
audio playback. This is why media uses the upload-descriptor path
(`internal/waxum/media.go`'s `UploadMedia`/`SendUploaded`) rather than
sending a URL, and why WSM consumes events by dialing OUT to waxum's SSE
stream (`internal/wa/backend_waxum_events.go`) instead of registering a
webhook pointed back at itself — a private callback URL is exactly what the
guard exists to refuse.

**waxum raises axum's body limit only on `/media/upload` (100 MB) and
`/import` (512 MB).** Everything else keeps the 2 MB default, so a
base64-inline media send fails above roughly a 1.5 MB file (base64 costs
~33% over raw bytes) — another reason the multipart upload-descriptor path
is the one this app uses, not a JSON body carrying the file inline.

**An SSE event must not write session state.** `backend_waxum_events.go`'s
event pump pokes `statusPump` awake on a connection-relevant event; it never
calls `applyStatus` itself, which stays the sole authority over
`ManagedSession.State`. An unthrottled poke would collapse
`loggedOutHysteresisPolls`' evidence window into milliseconds and reopen the
exact status-flap incident hysteresis exists to absorb — hence
`pokeMinInterval` rate-limiting how often one event can wake the poller
early.

**`WSM_CALLS_ENABLED` defaults to false.** Automated outbound calling risks
a ban on a paired device identity that has no second copy anywhere (see the
whatsmeow/waxum singleton-volume entries above) — every `/calls/*` endpoint
answers 501 and the `/calls` page says so plainly until an operator opts in
deliberately.

**An upload larger than the edge's `client_max_body_size` produces a bare
413 with NO app-side trace at all — no log line, no `wa_messages` row —
because nginx never proxies the request in the first place.** This is
exactly what happened to a 1.6 MB video send: it failed with "content too
large" and looked, from inside the app, like the request had simply never
arrived — every send that had ever worked before it was, by coincidence,
under nginx's 1 MiB default. `docs/deploy-nginx-wa.conf.example` now sets
`client_max_body_size 110m`, deliberately ABOVE `WSM_MEDIA_MAX_BYTES` (100
MiB in production), so the app's own kind-aware error is what an operator
sees, not nginx's bare 413 — but the app's per-kind limits
(`internal/config/media.go`'s `WSM_MEDIA_MAX_BYTES_{IMAGE,VIDEO,AUDIO,
DOCUMENT}`) are irrelevant until the edge is raised to admit the request at
all. If a media send ever again looks like the app silently ignored it —
nothing in this app's own logs, nothing in `wa_messages` — check the edge's
`client_max_body_size` before the app's config.

**The public API is an ALLOWLIST at the edge, and the edge is not in this
repo.** Since 2026-09-04 `https://wa.mfardiansyah.id` proxies a fixed set of
`/api/v1` routes — auth login/logout, `/messages{,/group,/media}`, `/inbound`,
`/contacts/check`, and the `/sessions/{id}/` equivalents of the last three —
and answers 404 for everything else, `/api/v1/health` included. Adding a route
to `routes_api.go` therefore does **nothing** publicly: the live file is
`/etc/nginx/conf.d/wa.conf` on `ssh clino` (103.127.134.25, NOT `rnd-v2-vpn`,
which has no nginx at all), mirrored in `docs/deploy-nginx-wa.conf.example` and
asserted by `nginx_edge_test.go`. Three ways this bites:

  - **`^~` outranks regex, so a `^~ /api/v1/` deny silently kills every regex
    allow rule below it** — nginx resolves exact `=`, then the longest `^~`
    prefix (which STOPS the search), then regexes in file order. The deny is
    written `~ ^/api/v1/` and must stay LAST; an allow rule appended after it
    is dead code. Both failures reload cleanly and pass `nginx -t`.
  - **`proxy_set_header` in a location discards the whole inherited set.** The
    headers are at server level for exactly this reason. Put them back inside
    `location /` and every API location loses `X-Forwarded-For`, so
    `auth.ClientIP` falls back to the WireGuard peer `10.10.0.1` for all
    callers alike — and since the failed-Basic throttle keys on that value, one
    attacker throttles every API client at once.
  - **The edge is SHARED with nine other products.** `map` and `limit_req_zone`
    names are global across `conf.d`; a collision fails the reload for the
    whole box. Everything in `wa.conf` is `wa_`-prefixed. Always
    `sudo nginx -t` before `systemctl reload nginx`, and back the file up first
    — the rollback is a `cp` of the `.bak.<stamp>` and a reload.

Note the app has **no rate limit on the bearer path at all** (only failed HTTP
Basic attempts are throttled, `internal/auth/middleware.go`), and the edge box
runs neither fail2ban nor ufw — so `limit_req` in `wa.conf` is the only brake
that exists on an authenticated public caller.

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
- **Code changes go through the `senior-fullstack-htmx-go` agent, on Sonnet
  5.** It carries this repo's hard rules, house patterns, and (since
  2026-08-23) Emil Kowalski's design/animation skill set for UI and CSS
  work — model pinned in its own frontmatter. Don't use a generic agent or a
  different model for code changes here.
