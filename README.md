# WhatsApp with Dashboard

Go + HTMX gateway that manages WhatsApp device sessions (pair by QR or phone
code, activate/deactivate) and sends plain-text messages through them — for a
human operator in the browser, and for machine callers using API-client bearer
tokens.

Standalone fork of the `whatsapp-session-management` module from the
`laundry-saas-v1` monorepo. Two things changed in the fork:

- **The MySQL app store points at the shared `master-compose-ai` stack** instead
  of the laundry dev stack.
- **Login is a local admin credential** instead of OIDC SSO against the
  monorepo's `auth-service`, which does not exist here. The machine bearer-token
  API is unchanged.

Single Go binary; templates, CSS, JS and `openapi.yaml` are all embedded.

A separate process, **wsm-mcp**, exposes the full `/api/v1` and waxum API
surface as MCP (Streamable HTTP + gRPC) tools for other AI agents — see
`mcp/README.md`.

## ⚠️ Read this first

1. **Hard singleton.** One device websocket and one SQLite crypto file. Never
   scale this service and never do a rolling restart — two processes against one
   store make WhatsApp play `StreamReplaced` ping-pong until the session logs
   itself out.
2. **`wsm-store.db` is cryptographic identity material.** Losing it means
   re-pairing the handset by QR, in person. It is gitignored; treat it like a
   credential and back it up with the SQLite online-backup API, never a `cp` of
   a live WAL.
3. **whatsmeow speaks the private multidevice protocol, not the official
   Business API.** There is real ToS and ban risk. Use a dedicated test number.

## Architecture: two stores

The split is not a preference — whatsmeow ships no MySQL dialect, only SQLite
and Postgres, so its crypto tables cannot live in MySQL.

| Store | Holds | Managed by |
|---|---|---|
| **MySQL** (`wa_dashboard`) | `wa_sessions`, `wa_messages`, `api_clients`, `api_access_tokens` | goose (`migrations/`) |
| **SQLite** (`WSM_DB_PATH`) | whatsmeow's ~17 crypto/device tables | `sqlstore.Container.Upgrade()` at boot |

MySQL holds session *metadata*; SQLite holds the *keys*. On boot the manager
reconciles the MySQL rows against the whatsmeow devices.

## Prerequisites

- Go 1.25 (`go.mod` pins it; `GOTOOLCHAIN=auto` fetches it automatically)
- Docker + Docker Compose
- The shared **master-compose-ai** stack running

Get the code:

```sh
git clone git@github.com:ardihikaru/whatsapp-with-dashboard.git
cd whatsapp-with-dashboard
```

> Shell snippets below are POSIX sh (bash/zsh). In **fish**, `VAR=$(...)`
> assignment is written `set VAR (...)`; the `make` targets work unchanged in
> any shell, which is why the steps below prefer them.

Start the shared stack if it is not already up:

```sh
cd /home/ardi/devel/projects/master-compose-ai
docker compose --env-file .env up -d
```

Check it is up before going further — this repo never starts or stops that stack:

```sh
docker ps --filter name=master-compose-ai-mysqldb-master-1
docker network inspect master-compose-ai-net --format '{{.Name}}'
```

> **Never run `docker compose down` — let alone `down -v` — inside
> master-compose-ai.** It holds other products' datastores.

## Set up the connection

### 1. Configure

```sh
cp .env.example .env
```

Fill in three things:

| Variable | How |
|---|---|
| `WSM_MYSQL_PASSWORD` | `openssl rand -hex 12` — **hex only**, see the note below |
| `MYSQL_ROOT_PASSWORD` | copy from `/home/ardi/devel/projects/master-compose-ai/.env` |
| `WSM_ADMIN_PASSWORD_HASH` | `make hashpw` — it prints the finished line to paste |

Two things that will bite you if skipped:

- **Keep the single quotes around the hash.** A bcrypt hash starts `$2a$10$…`,
  and every reader of `.env` eats bare dollars: `make` expands `$2`/`$1` while
  exporting, and `.` in a POSIX shell expands them as positional parameters.
  Single quotes survive make, the shell and docker compose alike. `make hashpw`
  emits the quoted line for you.
- **Keep `WSM_MYSQL_PASSWORD` hex.** `make db-bootstrap` `sed`-substitutes it
  into `scripts/bootstrap-db.sql.tmpl`, so a `/`, `&` or `#` would corrupt the SQL.

### 2. Create the database and its user

The app never connects as root. This creates a `wa_dashboard` schema and a
matching least-privilege user, following the house rule for the shared server —
one schema and one user per product, never a grant on `*.*`:

```sh
make db-bootstrap
```

Verify the grant is scoped:

```sh
make db-verify
```

You should see exactly one privilege line, on `` `wa_dashboard`.* ``. (A
`GRANT USAGE ON *.*` line always accompanies it — that is MySQL's
no-privileges placeholder, not an escalation.) **If you see real privileges on
`*.*`, stop** — that violates the shared-host rule.

> The stack's `config/mysql/init_files/` only runs against a *fresh* data
> directory, and its `data/mysql/data` was initialised long ago — so dropping a
> `.sql` file in there would silently never execute. It has to be applied live,
> which is what `make db-bootstrap` does. phpMyAdmin at <http://127.0.0.1:8082>
> (log in as root) is the GUI alternative.

### 3. Migrate

```sh
make migrate-up
make migrate-status
```

### 4. Run

```sh
make run          # host mode
```

Wait for `listening on http://localhost:8800 (docs at /docs)`, then check it
answers:

```sh
curl -sS http://localhost:8800/api/v1/health     # {"status":"ok",...}
```

Open <http://localhost:8800> and sign in with `WSM_ADMIN_USER` (default `admin`)
and the password you hashed in step 1 — the **password**, not the hash. Stop the
app with `Ctrl-C`.

If it exits at boot instead, the message says which variable is wrong; see
[Troubleshooting](#troubleshooting).

### 5. Pair a device

Sessions → New → Pair, then scan the QR from **a dedicated test number**
(WhatsApp → Linked devices). The first run creates `wsm-store.db`.

## Host mode vs container mode

This is the single easiest thing to get wrong. `127.0.0.1` *inside* a container
is that container's own loopback, not the host's, so the published port 5306 is
unreachable from in there.

| | host (`make run`) | container (`make up`) | remote DB (`make up REMOTE_DB=1`) |
|---|---|---|---|
| MySQL address | `127.0.0.1:5306` (published port) | `mysqldb-master:3306` (container DNS) | `$WSM_MYSQL_HOST:$WSM_MYSQL_PORT` (private network) |
| SQLite store | `./wsm-store.db` | `/data/wsm.db` on volume `whatsapp-with-dashboard-sqlite` | same as container |
| UI | <http://localhost:8800> | <http://127.0.0.1:8800> | same as container |
| Set by | `.env` | `docker-compose.yml` | `docker-compose.remote-db.yml` |

**One `.env` serves all three.** It is written for host mode; `docker-compose.yml`
overrides `WSM_MYSQL_HOST`, `WSM_MYSQL_PORT` and `WSM_DB_PATH` in-container,
because compose gives `environment:` precedence over `env_file:`. The remote-DB
overlay hands `WSM_MYSQL_HOST`/`WSM_MYSQL_PORT` back to `.env` rather than
naming an address of its own, so it is a mode and not one machine's file.

```sh
make up      # docker compose up -d --build
make logs
make down    # stops THIS stack only
```

The compose file joins `master-compose-ai-net` as an **external** network — it is
owned by the other stack and must never be created here. If you get
`network declared as external, but could not be found`, that stack is down.

**Unless the app is not on that stack's host at all.** Then the network is not
down, it is elsewhere, and the same message means something completely
different. `REMOTE_DB=1` adds `docker-compose.remote-db.yml`, which undeclares
the external network and points MySQL at the address `.env` already carries:

```sh
make up   REMOTE_DB=1   # app here, MySQL over the private network
make logs REMOTE_DB=1   # the flag is not optional on the follow-up targets
make down REMOTE_DB=1
```

Every container target goes through one `$(COMPOSE)` variable so the mode cannot
be applied to `up` and forgotten on `logs`; `compose_remote_db_test.go` holds
that line.

Migrations can also run in a container, for hosts with no Go toolchain:

```sh
docker compose --profile migrate run --rm migrate up
docker compose --profile migrate run --rm migrate status
```

The app image deliberately does **not** embed `migrations/` and never migrates
on boot: a schema change is an operator action, not a start-up side effect of a
hard singleton.

## Behind a TLS reverse proxy

The defaults assume nothing is in front of this service. Four settings change
when something is, and leaving any of them out fails in a way that is hard to
read from the symptom:

| Set | To | Leaving it out looks like |
|---|---|---|
| `WA_BASE_URL` | the exact public URL, including any path prefix | login form posts to the wrong path |
| `WA_COOKIE_SECURE` | `true` | the session cookie survives a downgrade to plaintext |
| `WSM_BIND_ADDR` | the one interface the proxy reaches this host on | the proxy gets connection refused |
| `WSM_TRUSTED_PROXIES` | that proxy's address or subnet | see below |

**`WSM_TRUSTED_PROXIES` is not optional in this setup.** The login and API
throttles key on the client address, and behind a proxy every request arrives
from the proxy — so five bad logins from one attacker throttle *everyone* until
they stop. Naming the proxy lets `auth.ClientIP` read `X-Forwarded-For` and key
per client again. It reads the chain right to left and takes the first untrusted
hop, because a client can prepend whatever it likes and the proxy appends the
real address to the right of it. Name only a proxy that **overwrites** the
header (nginx's `$proxy_add_x_forwarded_for` does); trusting one that forwards
the client's own header lets the client pick its own throttle key.

**Do not strip the path prefix.** This service owns its prefix end to end — the
browser surface is mounted under `WA_BASE_URL`'s path and the JSON API stays at
the root. A stripping proxy passes every `curl` check against the API and still
breaks the login POST, because the login page renders its form action from
`BasePath`. The symptom is "the form does nothing", which points nowhere near
the proxy. See `internal/handler/routes.go`.

nginx, terminating TLS and forwarding to a host that publishes with
`WSM_BIND_ADDR=10.10.0.4` and `WSM_EXPOSED_PORT=18801`:

```nginx
map $http_upgrade $wsm_connection_upgrade { default upgrade; '' close; }

location / {
    proxy_pass http://10.10.0.4:18801;
    proxy_http_version 1.1;
    proxy_set_header Host              $host;
    proxy_set_header X-Real-IP         $remote_addr;
    proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto https;
    proxy_set_header Upgrade           $http_upgrade;
    proxy_set_header Connection        $wsm_connection_upgrade;
    proxy_buffering off;
}
```

with `WSM_TRUSTED_PROXIES` naming the address nginx connects from.

**The JSON API is published as an allowlist, not wholesale.** The production edge
proxies exactly the send-and-read surface — `/api/v1/auth/login` and `logout`,
`/api/v1/messages{,/group,/media}`, `/api/v1/inbound`, `/api/v1/contacts/check`,
the `/api/v1/sessions/{id}/` equivalents of the last three, the read-only
`/api/v1/sessions` and `/api/v1/sessions/{id}`, and the unauthenticated
`/api/v1/health` — and answers 404 for everything else under `/api/v1`,
including session create/delete, `/calls/*` and webhook administration. nginx
matches a location by **path**, not method, so publishing the two session
paths above does not also publish `POST /sessions` or `DELETE /sessions/{id}`
(which cannot unlink the device on waxum and burns a linked-device slot per
call): each of those write methods is denied at the edge by its own
`$request_method` guard on the same location, not by the path being absent.
`/api/v1/health` has no credential at all — `routes_api.go` registers it
without `RequireAPI` — so publishing it makes session counts and the backend's
identity readable by anyone. Adding a route to `internal/handler/routes_api.go`
does **not** make it public; the edge has to be edited too. The full config,
with the reasoning for each exclusion and for the location ordering, is
`docs/deploy-nginx-wa.conf.example`, and `nginx_edge_test.go` asserts the shape
of it.

Note that the proxy headers above belong at **server** level, not inside
`location /`. A location inherits them from the server but never from a sibling
location, and one that sets any header of its own discards the whole inherited
set — so headers kept in `location /` leave the API locations with no
`X-Forwarded-For`, and every public caller collapses onto one throttle key.

## Authentication

Two independent doors that share nothing:

- **Browser** — one local admin credential from the environment
  (`WSM_ADMIN_USER` + `WSM_ADMIN_PASSWORD_HASH`), an in-memory session cookie, and
  a per-IP throttle on failed logins (5 attempts, then one per 12s). Sessions are
  in memory, so a restart signs you out. The app **refuses to boot** with no
  credential configured — a default password on a service holding a WhatsApp
  identity is an account takeover waiting for someone to port-forward it.
- **Machine** — API clients created at `/admin/api-clients`, with bcrypt secrets.
  Two interchangeable ways to present them, below.

A browser session cookie is not accepted on `/api/v1`, and an API credential is
not accepted on the browser pages.

### Two ways for a machine caller to authenticate

```sh
# A. Bearer — exchange the credentials for a token once, then reuse it.
Authorization: Bearer <token from POST /api/v1/auth/login>

# B. Basic — send the credentials directly on every request.
Authorization: Basic base64(login_id:secret)
```

Both resolve to the same client, and every `/api/v1` endpoint accepts either.
Pick with this:

| | Bearer | Basic |
|---|---|---|
| Round trips | login first, then reuse | one, always |
| Per-request cost | SHA-256 + indexed lookup | bcrypt, cached 60s (see below) |
| Revoke one credential | yes, per token | no — deactivate the client or rotate its secret |
| Expiry | `token_ttl_seconds` / `token_expires_at` / `no_expiry` | none; the secret is valid until changed |
| Secret on the wire | only at login | **every request** — use TLS |
| `POST /api/v1/auth/logout` | revokes the token | `400`, there is no token to revoke |

**Bearer is still the better default** for anything chatty or long-lived: the
token is cheap to verify and can be revoked on its own. Basic exists for callers
where a two-step exchange is awkward — cron jobs, webhooks, quick `curl`.

Because Basic re-presents the raw secret every time, verifying it means bcrypt
(~80ms of CPU). Successful verifications are therefore cached in memory for **60
seconds**, keyed by login id and a SHA-256 of the secret — the raw secret is
never held. The cache is dropped immediately when a client is deactivated,
deleted, renamed, or has its secret rotated, so revocation still bites at once.
Failed Basic attempts are rate-limited per IP (20, then one every 3s), since a
caller varying the secret on every request would miss the cache by construction.

## Send a message as a machine caller

```sh
# Bearer
TOKEN=$(curl -sS -X POST "http://localhost:8800/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"login_id":"taskmgr","secret":"replace-with-secret"}' | jq -r .token)

curl -sS -X POST "http://localhost:8800/api/v1/messages" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"to":"+6281200000000","text":"Hello\n\nhttps://example.test/task/123"}'

# Basic — same call, no login step
curl -sS -X POST "http://localhost:8800/api/v1/messages" \
  -u "taskmgr:replace-with-secret" -H "Content-Type: application/json" \
  -d '{"to":"+6281200000000","text":"Hello","category":"otp","client_ref":"order-123"}'
```

`category` and `client_ref` are optional and are stored with the message, so you
can trace what was sent for which order in `/logs`.

Responses: `200` sent/failed, `202` still queued after 15s, `400` validation,
`409` not connected, `429` queue full. Sends are queued per session (cap 100),
token-bucket rate-limited (1 msg/s, burst 3) and retried up to 3 times; delivery
is at-least-once when a send times out.

Swagger UI is at `/docs`, the spec at `/openapi.yaml`, and `/api/v1/health` is
public and unauthenticated.

## Self Convo & Convo Scheduling

Two of this deployment's own paired sessions can exchange a scripted
conversation, either once (**Self Convo**, `/self-convo`) or on a recurring
cadence to several receivers at once (**Convo Scheduling**,
`/self-convo/schedules`). Both are gated behind
`WSM_SELF_CONVO_ENABLED=false` by default — two paired identities messaging
each other automatically is exactly the traffic pattern that gets a WhatsApp
account flagged, and neither identity has a second copy anywhere to fall
back on (see `docs/brief-self-convo.md`/`docs/brief-convo-scheduling.md` for
the full design reasoning). Template management (upload, view, delete) works
regardless of the flag; only sending is gated.

A schedule draws its opening line from a large, generated corpus rather than
an operator hand-authoring one script per topic. Materialise it into the
configured store with:

```sh
make convo-seed                    # ~2,400 templates across 50 topics
make convo-seed ARGS="-dry-run"    # preview counts, write nothing
make convo-seed ARGS="-replace"    # regenerate, replacing prior corpus rows
```

`cmd/convoseed` refuses to write to anything but a `sqlite:` DSN unless
`-i-know-this-is-production` is passed — the only reachable MySQL from a dev
machine is the shared production server (see CLAUDE.md).

## Backup and restore

The two stores need different treatment, and only one of them is irreplaceable:

```sh
# SQLite — the keys. Online-backup API, never a cp of a live WAL.
sqlite3 wsm-store.db ".backup '/backup/wsm-$(date +%F).db'"

# MySQL — metadata and API clients.
docker exec master-compose-ai-mysqldb-master-1 mysqldump -uroot \
  -p"$(grep '^MYSQL_ROOT_PASSWORD=' .env | cut -d= -f2-)" wa_dashboard > wa_dashboard.sql
```

In container mode the SQLite file lives on the `whatsapp-with-dashboard-sqlite`
volume; `docker compose down -v` would destroy it and the pairing with it.

## Troubleshooting

| Symptom | Cause |
|---|---|
| `network declared as external, but could not be found` | master-compose-ai is down. Start it there; never `docker network create` it here. |
| `Access denied for user 'wa_dashboard'` | `make db-bootstrap` not run, or `WSM_MYSQL_PASSWORD` does not match what it created. Re-run it — the template re-`ALTER`s the password. |
| Login always fails with a correct password | The bcrypt hash lost its `$` segments. Check `.env` still has the **single quotes** around `WSM_ADMIN_PASSWORD_HASH`. |
| `no admin credential configured` at boot | Set `WSM_ADMIN_PASSWORD_HASH` (or `WSM_ADMIN_PASSWORD`). This is deliberate, not a bug. |
| `/data/wsm.db: permission denied` in the container | The image is missing the `COPY --chown=65532:65532 /seed-data /data` layer, or the volume predates it: `make down && docker volume rm whatsapp-with-dashboard-sqlite && make up`. |
| `dialect "sqlite3" is not registered` | A stale `WSM_SQLITE_DIALECT=sqlite3`. The CGO driver is not compiled in; the only value is `sqlite`. |
| QR expires before scanning | Reload the pairing page; codes are short-lived by design. |

## Development

```sh
make build
make test     # SQLite-backed; needs no MySQL
make vet
```

`make help` lists every target.
