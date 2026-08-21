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

# goose connects as the ordinary app user against the app database.
# multiStatements=true is a GOOSE-ONLY flag: the app's own DSN (internal/config)
# deliberately does not set it, because the app never sends multi-statement SQL
# and enabling it would widen the blast radius of any injection for free.
WA_DSN := $(WSM_MYSQL_USER):$(WSM_MYSQL_PASSWORD)@tcp($(WSM_MYSQL_HOST):$(WSM_MYSQL_PORT))/$(WSM_MYSQL_DATABASE)?parseTime=true&charset=utf8mb4&loc=UTC&multiStatements=true

.PHONY: help build test vet hashpw db-bootstrap db-drop migrate-up migrate-status \
        migrate-down-all run docker-build up down logs

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

docker-build: ## Build the container image
	docker build -t $(IMAGE) .

up: ## Start the app container on master-compose-ai-net
	docker compose up -d --build

# NEVER run `docker compose down` inside master-compose-ai itself — that stack
# holds other products' datastores. This target only stops OUR stack.
down: ## Stop this stack (leaves the shared infra alone)
	docker compose down

logs: ## Follow the container logs
	docker compose logs -f app
