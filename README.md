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
- The shared **master-compose-ai** stack running:

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
docker exec -i master-compose-ai-mysqldb-master-1 \
  mysql -uroot -p"$MYSQL_ROOT_PASSWORD" -e "SHOW GRANTS FOR 'wa_dashboard'@'%';"
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

Then open <http://localhost:8800> and sign in with `WSM_ADMIN_USER` and the
password you hashed.

### 5. Pair a device

Sessions → New → Pair, then scan the QR from **a dedicated test number**
(WhatsApp → Linked devices). The first run creates `wsm-store.db`.

## Host mode vs container mode

This is the single easiest thing to get wrong. `127.0.0.1` *inside* a container
is that container's own loopback, not the host's, so the published port 5306 is
unreachable from in there.

| | host (`make run`) | container (`make up`) |
|---|---|---|
| MySQL address | `127.0.0.1:5306` (published port) | `mysqldb-master:3306` (container DNS) |
| SQLite store | `./wsm-store.db` | `/data/wsm.db` on volume `whatsapp-with-dashboard-sqlite` |
| UI | <http://localhost:8800> | <http://127.0.0.1:8800> |
| Set by | `.env` | `docker-compose.yml` |

**One `.env` serves both.** It is written for host mode; `docker-compose.yml`
overrides `WSM_MYSQL_HOST`, `WSM_MYSQL_PORT` and `WSM_DB_PATH` in-container,
because compose gives `environment:` precedence over `env_file:`.

```sh
make up      # docker compose up -d --build
make logs
make down    # stops THIS stack only
```

The compose file joins `master-compose-ai-net` as an **external** network — it is
owned by the other stack and must never be created here. If you get
`network declared as external, but could not be found`, that stack is down.

Migrations can also run in a container, for hosts with no Go toolchain:

```sh
docker compose --profile migrate run --rm migrate up
docker compose --profile migrate run --rm migrate status
```

The app image deliberately does **not** embed `migrations/` and never migrates
on boot: a schema change is an operator action, not a start-up side effect of a
hard singleton.

## Authentication

Two independent doors that share nothing:

- **Browser** — one local admin credential from the environment
  (`WSM_ADMIN_USER` + `WSM_ADMIN_PASSWORD_HASH`), an in-memory session cookie, and
  a per-IP throttle on failed logins (5 attempts, then one per 12s). Sessions are
  in memory, so a restart signs you out. The app **refuses to boot** with no
  credential configured — a default password on a service holding a WhatsApp
  identity is an account takeover waiting for someone to port-forward it.
- **Machine** — API clients created at `/admin/api-clients`, with bcrypt secrets
  and SHA-256-hashed bearer tokens.

A browser session cookie is not accepted on `/api/v1`, and a bearer token is not
accepted on the browser pages.

## Send a message as a machine caller

```sh
TOKEN=$(curl -sS -X POST "http://localhost:8800/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"login_id":"taskmgr","secret":"replace-with-secret"}' | jq -r .token)

curl -sS -X POST "http://localhost:8800/api/v1/messages" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"to":"+6281200000000","text":"Hello\n\nhttps://example.test/task/123"}'
```

Responses: `200` sent/failed, `202` still queued after 15s, `400` validation,
`409` not connected, `429` queue full. Sends are queued per session (cap 100),
token-bucket rate-limited (1 msg/s, burst 3) and retried up to 3 times; delivery
is at-least-once when a send times out.

Swagger UI is at `/docs`, the spec at `/openapi.yaml`, and `/api/v1/health` is
public and unauthenticated.

## Backup and restore

The two stores need different treatment, and only one of them is irreplaceable:

```sh
# SQLite — the keys. Online-backup API, never a cp of a live WAL.
sqlite3 wsm-store.db ".backup '/backup/wsm-$(date +%F).db'"

# MySQL — metadata and API clients.
docker exec master-compose-ai-mysqldb-master-1 \
  mysqldump -uroot -p"$MYSQL_ROOT_PASSWORD" wa_dashboard > wa_dashboard.sql
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
