# whatsapp-with-dashboard — module tasks. STANDALONE: no monorepo, no go.work.
#
# TWO STORES (hybrid model, unchanged from the app this was forked from):
#   MySQL  — the app's own tables (wa_sessions, wa_messages, api_clients,
#            api_access_tokens), goose-managed, on the SHARED master-compose-ai
#            stack (container master-compose-ai-mysqldb-master-1, host :5306).
#   SQLite — whatsmeow's crypto/device tables ONLY (WSM_DB_PATH). Never MySQL:
#            whatsmeow ships no MySQL dialect, only SQLite and Postgres.
#
# Env comes from THIS repo's .env (cp .env.example .env). Every target here is
# HOST MODE: it talks to 127.0.0.1:$(WSM_MYSQL_PORT), never the container DNS
# name. Container mode is docker-compose.yml's business.

-include .env
export

GOOSE           := go run github.com/pressly/goose/v3/cmd/goose@v3.24.3
MIGRATIONS_DIR  := migrations
MYSQL_CONTAINER ?= master-compose-ai-mysqldb-master-1
IMAGE           ?= whatsapp-with-dashboard:local

# ONE compose invocation for every container target below, so a mode cannot be
# applied to `up` and forgotten on `logs`. REMOTE_DB=1 adds the overlay for a
# host that runs the app but NOT the shared master-compose-ai stack — the app
# then reaches MySQL at WSM_MYSQL_HOST:WSM_MYSQL_PORT over the private network
# instead of by the container DNS name that only resolves on that stack's host.
# See the header of docker-compose.remote-db.yml.
#
#   make up                 # app and MySQL on the same host
#   make up REMOTE_DB=1     # app here, MySQL over WireGuard
COMPOSE := docker compose$(if $(REMOTE_DB), -f docker-compose.yml -f docker-compose.remote-db.yml,)

# goose connects as the ordinary app user against the app database.
# multiStatements=true is a GOOSE-ONLY flag: the app's own DSN (internal/config)
# deliberately does not set it, because the app never sends multi-statement SQL
# and enabling it would widen the blast radius of any injection for free.
WA_DSN := $(WSM_MYSQL_USER):$(WSM_MYSQL_PASSWORD)@tcp($(WSM_MYSQL_HOST):$(WSM_MYSQL_PORT))/$(WSM_MYSQL_DATABASE)?parseTime=true&charset=utf8mb4&loc=UTC&multiStatements=true

.PHONY: help build test vet hashpw db-bootstrap db-verify db-drop migrate-up migrate-status \
        migrate-down-all run docker-build up down logs e2e-install e2e e2e-headed e2e-report e2e-serve e2e-evidence e2e-reset

help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
	  | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

build: ## Compile everything
	go build ./...

test: ## Unit tests (SQLite-backed; no MySQL required)
	go test ./...

vet: ## go vet
	go vet ./...

hashpw: ## Print a bcrypt hash for WSM_ADMIN_PASSWORD_HASH (password not echoed)
	@stty -echo 2>/dev/null; printf 'password: ' >&2; \
	 read -r pw; stty echo 2>/dev/null; printf '\n' >&2; \
	 printf '%s' "$$pw" | go run ./cmd/hashpw

db-bootstrap: ## Create the app DB + least-privilege user (root, once)
	@test -n "$(WSM_MYSQL_PASSWORD)" || { echo "WSM_MYSQL_PASSWORD is empty — fill in .env first"; exit 1; }
	@test -n "$(MYSQL_ROOT_PASSWORD)" || { echo "MYSQL_ROOT_PASSWORD is empty — copy it from master-compose-ai/.env"; exit 1; }
	sed -e 's/__DB__/$(WSM_MYSQL_DATABASE)/g' \
	    -e 's/__USER__/$(WSM_MYSQL_USER)/g' \
	    -e 's/__PASSWORD__/$(WSM_MYSQL_PASSWORD)/g' \
	    scripts/bootstrap-db.sql.tmpl \
	  | docker exec -i $(MYSQL_CONTAINER) mysql -uroot -p$(MYSQL_ROOT_PASSWORD)
	@echo "created database $(WSM_MYSQL_DATABASE) and user $(WSM_MYSQL_USER)@'%'"

db-verify: ## Show the app user's grants (must be scoped to this schema only)
	@docker exec -i $(MYSQL_CONTAINER) mysql -uroot -p$(MYSQL_ROOT_PASSWORD) -e \
	  "SHOW GRANTS FOR '$(WSM_MYSQL_USER)'@'%';" 2>/dev/null
	@echo
	@echo "Expect ONE privilege line, on \`$(WSM_MYSQL_DATABASE)\`.* — the GRANT USAGE ON *.*"
	@echo "line always accompanies it and is MySQL's no-privileges placeholder, not an"
	@echo "escalation. Real privileges on *.* would violate the shared-host rule."

db-drop: ## Drop THIS app's DB and user (root; DESTRUCTIVE, this schema only)
	docker exec -i $(MYSQL_CONTAINER) mysql -uroot -p$(MYSQL_ROOT_PASSWORD) -e \
	  "DROP DATABASE IF EXISTS \`$(WSM_MYSQL_DATABASE)\`; \
	   DROP USER IF EXISTS '$(WSM_MYSQL_USER)'@'%';"

migrate-up: ## Apply migrations
	$(GOOSE) -dir $(MIGRATIONS_DIR) mysql "$(WA_DSN)" up

migrate-status: ## Show migration status
	$(GOOSE) -dir $(MIGRATIONS_DIR) mysql "$(WA_DSN)" status

migrate-down-all: ## Roll every migration back (DESTRUCTIVE)
	$(GOOSE) -dir $(MIGRATIONS_DIR) mysql "$(WA_DSN)" down-to 0

# `. ./.env` in the SHELL, not make's own `export`, and this is load-bearing.
#
# A bcrypt hash looks like $2a$10$w27Sp... — make expands $2, $1, $w as make
# variables while exporting, so WSM_ADMIN_PASSWORD_HASH reaches the process
# CORRUPTED and every login fails with nothing in any log to say why. Sourcing
# the file in the recipe shell passes it through byte for byte, overriding
# whatever the `export` above mangled. docker compose reads env_file literally
# and is unaffected.
run: ## Run on the HOST (needs db-bootstrap + migrate-up first)
	@set -a; [ -f .env ] && . ./.env; set +a; go run ./cmd/app

# ---------------------------------------------------------------- e2e (browser)
#
# The suite boots its OWN app on 127.0.0.1:8801 via scripts/e2e-env.sh, with the
# app tables in a throwaway SQLite file. It NEVER touches MySQL: the only MySQL
# a dev machine can reach is the shared production server, which holds other
# products' data.
#
# The admin credential comes from _data/paswd (gitignored). The bcrypt-hash
# mangling described above `run` is avoided the same way: scripts/e2e-env.sh
# generates and exports the hash inside its OWN shell, so make never handles a
# $$2a$$10$$... string and cannot expand $$2/$$1 out of it.

e2e-install: ## Install Playwright + Chromium (first run; a few hundred MB)
	npm install --no-audit --no-fund
	npx playwright install chromium
	@echo
	@echo "If the browser fails to start with 'error while loading shared libraries',"
	@echo "run 'npx playwright install-deps chromium' (needs root), or see the"
	@echo "no-root fallback documented at the top of scripts/e2e-run.sh."

e2e: ## Browser tests: phone portrait + desktop, signed in as the admin
	./scripts/e2e-run.sh

e2e-headed: ## Same, with a visible browser
	./scripts/e2e-run.sh --headed

e2e-reset: ## Delete the throwaway e2e SQLite state so the next run reseeds
	rm -rf _data/e2e e2e/.auth
	@echo "e2e state cleared (this NEVER touches MySQL)"

e2e-evidence: ## Re-capture the screenshots in docs/e2e-evidence/ (opt-in; writes binaries)
	WSM_E2E_EVIDENCE=1 ./scripts/e2e-run.sh e2e/screenshots.spec.ts

e2e-report: ## Open the last HTML report
	npx playwright show-report

e2e-serve: ## Just run the app the way the e2e suite does (port 8801, SQLite)
	./scripts/e2e-env.sh

docker-build: ## Build the container image
	docker build -t $(IMAGE) .

up: ## Start the app container (REMOTE_DB=1 for a host without the shared stack)
	$(COMPOSE) up -d --build

# NEVER run `docker compose down` inside master-compose-ai itself — that stack
# holds other products' datastores. This target only stops OUR stack.
down: ## Stop this stack (leaves the shared infra alone)
	$(COMPOSE) down

logs: ## Follow the container logs
	$(COMPOSE) logs -f app
