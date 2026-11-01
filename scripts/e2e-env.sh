#!/usr/bin/env bash
# Boot the app for the Playwright suite, hermetically.
#
# WHAT THIS DELIBERATELY DOES NOT DO: touch MySQL. The only MySQL reachable from
# a dev machine is the SHARED production server (see _data/database-cred.json),
# which holds other products' data. The app tables go in a throwaway SQLite file
# instead, via WSM_APP_DSN=sqlite:… — see internal/db.OpenAppStore.
#
# Usage:  scripts/e2e-env.sh [command ...]      (default: go run ./cmd/app)
#
# The admin password is read from _data/paswd (gitignored) and converted to a
# bcrypt hash by cmd/hashpw, which reads stdin and never argv. The plaintext is
# never printed, never exported, and never reaches a process argument list.
set -euo pipefail
set +x

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo"

pw_file="${WSM_E2E_PASSWORD_FILE:-$repo/_data/paswd}"
if [ ! -r "$pw_file" ]; then
  echo "e2e-env: cannot read $pw_file — the admin password lives there (gitignored)." >&2
  exit 1
fi

# tr -d '\n' and not $(<file): a trailing newline in the file would be hashed as
# part of the password, and the browser would then never be able to sign in.
hash="$(tr -d '\r\n' < "$pw_file" | go run ./cmd/hashpw)"
if [ -z "$hash" ]; then
  echo "e2e-env: cmd/hashpw produced no hash" >&2
  exit 1
fi

state="${WSM_E2E_STATE_DIR:-$repo/_data/e2e}"
mkdir -p "$state"

# config.Load() (internal/config/config.go) has required all four
# WSM_MINIO_* keys unconditionally since MinIO object storage landed, even
# though this suite never uploads media and this script never sourced
# WSM_MINIO_* before. Without this, go run ./cmd/app dies inside
# config.Load() with "WSM_MINIO_ENDPOINT is required" and no hint that the
# e2e harness is the thing that needs it.
#
# .env is NOT sourced wholesale — it also carries WSM_ADDR, WA_BASE_URL and
# WA_COOKIE_SECURE, which would override the hermetic localhost:8801 values
# exported below, plus every production secret. Only these four keys are
# pulled out, one grep each, and the values die with this process — same
# rule as pw_file above and the _data/ hazard CLAUDE.md opens with. Never
# echoed, never set -x'd, never written to a log.
env_file="${WSM_E2E_ENV_FILE:-$repo/.env}"
for key in WSM_MINIO_ENDPOINT WSM_MINIO_ACCESS_KEY WSM_MINIO_SECRET_KEY WSM_MINIO_BUCKET; do
  if [ -n "${!key:-}" ]; then
    continue # already set in the calling environment; respect it
  fi
  if [ ! -r "$env_file" ]; then
    echo "e2e-env: cannot read $env_file, and $key is not set. The app now" \
      "requires WSM_MINIO_ENDPOINT, WSM_MINIO_ACCESS_KEY, WSM_MINIO_SECRET_KEY" \
      "and WSM_MINIO_BUCKET to boot at all (internal/config/config.go)." \
      "Populate .env (see .env.example) or export all four yourself." >&2
    exit 1
  fi
  value="$(grep -E "^${key}=" "$env_file" | tail -n1 | cut -d= -f2-)"
  if [ -z "$value" ]; then
    echo "e2e-env: $key is missing or empty in $env_file. The app now" \
      "requires WSM_MINIO_ENDPOINT, WSM_MINIO_ACCESS_KEY, WSM_MINIO_SECRET_KEY" \
      "and WSM_MINIO_BUCKET to boot at all (internal/config/config.go)." >&2
    exit 1
  fi
  export "$key=$value"
done

export WSM_ADMIN_USER="${WSM_ADMIN_USER:-ardihikaru}"
export WSM_ADMIN_PASSWORD_HASH="$hash"
# Two SQLite files, not one: db.Open pins MaxOpenConns(1) because whatsmeow's
# sqlstore requires a single writer, so sharing it would serialise every page
# query behind the crypto store.
export WSM_APP_DSN="sqlite:$state/app.db"
export WSM_DB_PATH="$state/wsm.db"
export WSM_SQLITE_DIALECT="sqlite"
# 8801, not the usual 8800, so a dev instance already running is left alone.
#
# FORCED, not merely defaulted with ${WSM_ADDR:-...}: `make e2e`/`make
# e2e-serve` run this recipe with .env already exported wholesale
# (Makefile's own `-include .env` + `export`, near its top), and .env sets
# WSM_ADDR=:8800, WA_BASE_URL to the production hostname, and
# WA_COOKIE_SECURE=true — so a plain ${VAR:-default} here would silently
# keep THOSE values instead of applying the hermetic ones: the app would
# bind :8800 (colliding with any dev instance already there, or with
# nothing at all — either way not what playwright.config.ts's readiness
# probe polls at 127.0.0.1:8801, so the run hangs to its 120s timeout
# instead of ever starting), and a cookie marked Secure over plain HTTP
# means the browser never stores it and every test fails at login (see
# playwright.config.ts's own comment on why BASE_URL must be
# 127.0.0.1:8801, not the public host). An override hook still exists, but
# under WSM_E2E_*-prefixed names distinct from the app's own — precisely so
# .env's values, leaked in through Make, cannot reach these three through
# the very names they already occupy.
export WSM_ADDR="${WSM_E2E_ADDR:-127.0.0.1:8801}"
export WA_BASE_URL="${WSM_E2E_BASE_URL:-http://127.0.0.1:8801}"
export WA_COOKIE_SECURE="${WSM_E2E_COOKIE_SECURE:-false}"
export WSM_DARK_MODE="${WSM_DARK_MODE:-true}"
export WSM_SESSION_TTL="${WSM_SESSION_TTL:-2h}"
# WSM_WA_BACKEND now defaults to "waxum" (internal/config.DefaultWABackend),
# which needs WSM_WAXUM_URL/TOKEN pointed at a running waxum process. This
# suite never pairs a device, so it stays on the in-process whatsmeow backend
# instead — hermetic, no second process to boot, and no coverage lost.
export WSM_WA_BACKEND="${WSM_WA_BACKEND:-whatsmeow}"

# AI-written status posts: /status/schedules only offers the "Write each post
# with AI" switch when the ai-codex-cli client is configured, and the browser
# suite needs it enabled to drive it. These are FIXED dummies, not defaults
# (the caller's real WSM_CODEX_CLI_* must never be reachable from a test run):
# port 9 (discard) refuses connections, so a schedule that ever reached
# generation would fail fast and fall back, never call a real service.
export WSM_CODEX_CLI_URL="http://127.0.0.1:9"
export WSM_CODEX_CLI_LOGIN_ID="e2e-dummy"
export WSM_CODEX_CLI_SECRET_KEY="e2e-dummy"

# Seed realistic content. An empty database makes the layout assertions
# meaningless — "no page scrolls horizontally" is trivially true with zero rows,
# and phone layouts break on CONTENT. cmd/e2eseed is idempotent and refuses any
# DSN that is not sqlite:, so it can never reach the shared production MySQL.
if [ "${WSM_E2E_SEED:-1}" = "1" ]; then
  go run ./cmd/e2eseed
fi

if [ "$#" -eq 0 ]; then
  exec go run ./cmd/app
fi
exec "$@"
