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

export WSM_ADMIN_USER="${WSM_ADMIN_USER:-ardihikaru}"
export WSM_ADMIN_PASSWORD_HASH="$hash"
# Two SQLite files, not one: db.Open pins MaxOpenConns(1) because whatsmeow's
# sqlstore requires a single writer, so sharing it would serialise every page
# query behind the crypto store.
export WSM_APP_DSN="sqlite:$state/app.db"
export WSM_DB_PATH="$state/wsm.db"
export WSM_SQLITE_DIALECT="sqlite"
# 8801, not the usual 8800, so a dev instance already running is left alone.
export WSM_ADDR="${WSM_ADDR:-127.0.0.1:8801}"
export WA_BASE_URL="${WA_BASE_URL:-http://127.0.0.1:8801}"
export WA_COOKIE_SECURE="${WA_COOKIE_SECURE:-false}"
export WSM_DARK_MODE="${WSM_DARK_MODE:-true}"
export WSM_SESSION_TTL="${WSM_SESSION_TTL:-2h}"

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
