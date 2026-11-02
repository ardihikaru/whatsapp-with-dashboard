// Package db opens the module's two stores (hybrid model, docs/07 §2):
//   - Open()    → SQLite, drives the whatsmeow sqlstore crypto tables (and, in
//     unit tests, the app tables via EnsureSchema);
//   - OpenApp() → MySQL, drives the app's own tables in production. The app
//     schema is created by goose migrations, NOT EnsureSchema, against MySQL.
//
// OpenAppStore() sits in front of OpenApp and lets a DEV/E2E run put the app
// tables in SQLite instead ("sqlite:<path>"), so the binary boots with no MySQL
// reachable at all. Production never takes that branch.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql" // MySQL app store
	_ "modernc.org/sqlite"             // whatsmeow crypto store (pure Go, no CGO)
)

// SQLiteDialect is the only SQLite driver this binary registers.
//
// It was github.com/mattn/go-sqlite3 (CGO) until Fase 9. The swap was made so the
// image could be distroless/static like the other three Go services instead of a
// libc snowflake needing apk build-base in CI and shipping a shell in production.
// Keeping mattn as a "fallback" would have been a lie: CGO_ENABLED=0 compiles that
// driver to a stub that errors at first use, so the fallback would exist in the
// source and not in the artifact.
//
// The swap was gated on evidence, not on the docs: a REAL mattn-written device
// store was opened under this driver and whatsmeow's own GetAllDevices returned
// the same paired device (same JID, same pushname) that mattn returned from the
// same bytes. Re-run that check before ever swapping back.
const SQLiteDialect = "sqlite"

// OpenApp returns a pooled MySQL connection for the app tables. Unlike the
// SQLite store there is no single-writer constraint here — MySQL is the shared
// server; whatsmeow's one-process rule applies only to the SQLite store above.
func OpenApp(dsn string) (*sql.DB, error) {
	pool, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql app store: %w", err)
	}
	pool.SetConnMaxLifetime(3 * time.Minute)
	pool.SetMaxOpenConns(10)
	pool.SetMaxIdleConns(5)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.PingContext(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping mysql app store: %w", err)
	}
	return pool, nil
}

// SQLiteAppPrefix marks an app-store DSN as SQLite rather than MySQL.
//
// DEV AND E2E ONLY. In production the app tables live in MySQL and their schema
// is owned by the goose migrations in migrations/; EnsureSchema is a second,
// hand-maintained copy of that schema in SQLite dialect. Pointing a real
// deployment at sqlite: would silently run it against a schema no migration has
// ever touched, so OpenAppStore logs a warning every time it takes this branch.
const SQLiteAppPrefix = "sqlite:"

// OpenAppStore picks the app-table store from the DSN. "sqlite:<path>" opens the
// pure-Go SQLite store and creates the schema in place; any other DSN is handed
// to OpenApp as MySQL.
//
// The SQLite branch deliberately opens its OWN file, separate from WSM_DB_PATH:
// Open pins MaxOpenConns(1) because whatsmeow's sqlstore demands a single
// writer, and sharing that one connection with the app's own queries would
// serialise every dashboard read behind the crypto store.
func OpenAppStore(ctx context.Context, dsn string) (*sql.DB, error) {
	path, ok := strings.CutPrefix(dsn, SQLiteAppPrefix)
	if !ok {
		return OpenApp(dsn)
	}
	if path == "" {
		return nil, fmt.Errorf("open sqlite app store: %s needs a file path", SQLiteAppPrefix)
	}
	pool, err := Open(path, SQLiteDialect)
	if err != nil {
		return nil, err
	}
	if err := EnsureSchema(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// Open returns a SQLite pool with WAL, foreign keys, and a 5s busy timeout.
// MaxOpenConns(1) makes it the single writer, which whatsmeow's sqlstore
// requires (exactly one process/connection per store). In production it backs
// only the whatsmeow crypto tables; in unit tests EnsureSchema also puts the
// app tables here.
//
// dialect is still a parameter because whatsmeow's sqlstore.NewWithDB needs the
// same string, so the caller must hold it — but SQLiteDialect is the only value
// that works. An old WSM_SQLITE_DIALECT=sqlite3 in someone's .env is refused
// loudly here rather than failing later inside the crypto store.
func Open(path, dialect string) (*sql.DB, error) {
	if dialect != SQLiteDialect {
		return nil, fmt.Errorf(
			"open sqlite %s: dialect %q is not registered — the mattn/go-sqlite3 (CGO) driver was "+
				"removed in Fase 9 so the image could be distroless; set WSM_SQLITE_DIALECT=%s",
			path, dialect, SQLiteDialect)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", path)
	pool, err := sql.Open(dialect, dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	pool.SetMaxOpenConns(1)
	pool.SetConnMaxIdleTime(0)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.PingContext(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping sqlite %s: %w", path, err)
	}
	return pool, nil
}

// schema is the idempotent app DDL (docs/07 §2). The whatsmeow_* tables are
// created separately by sqlstore Container.Upgrade over the same pool.
var schema = []string{
	// owner_type/client_id categorize who owns the number: 'platform' (the
	// Platform owner's pool, the only path wired today) or 'client' (RESERVED
	// for future client self-service — no client access is implemented yet).
	`CREATE TABLE IF NOT EXISTS wa_sessions (
	  id          TEXT PRIMARY KEY,
	  name        TEXT NOT NULL,
	  phone       TEXT NOT NULL,
	  jid         TEXT NOT NULL DEFAULT '',
	  active      INTEGER NOT NULL DEFAULT 0,
	  is_master   INTEGER NOT NULL DEFAULT 0,
	  group_code  TEXT NOT NULL DEFAULT '',
	  last_status TEXT NOT NULL DEFAULT 'new',
	  owner_type  TEXT NOT NULL DEFAULT 'platform',
	  client_id   INTEGER,
	  created_at  TEXT NOT NULL,
	  updated_at  TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS wa_messages (
	  id            TEXT PRIMARY KEY,
	  session_id    TEXT NOT NULL REFERENCES wa_sessions(id) ON DELETE CASCADE,
	  api_client_id TEXT,
	  client_ref    TEXT,
	  tenant_ref    TEXT,
	  category      TEXT,
	  to_phone      TEXT NOT NULL,
	  body          TEXT NOT NULL,
	  status        TEXT NOT NULL DEFAULT 'queued',
	  wa_message_id TEXT NOT NULL DEFAULT '',
	  error         TEXT NOT NULL DEFAULT '',
	  attempts      INTEGER NOT NULL DEFAULT 0,
	  created_at    TEXT NOT NULL,
	  sent_at       TEXT,
	  media_kind       TEXT,
	  media_filename   TEXT,
	  media_mime       TEXT,
	  media_size       INTEGER,
	  media_object_key TEXT
	)`,
	`CREATE INDEX IF NOT EXISTS idx_wa_messages_session
	  ON wa_messages(session_id, created_at DESC)`,
	`CREATE TABLE IF NOT EXISTS api_clients (
	  id                TEXT PRIMARY KEY,
	  name              TEXT NOT NULL,
	  login_id          TEXT NOT NULL UNIQUE,
	  description       TEXT NOT NULL DEFAULT '',
	  secret_hash       TEXT NOT NULL,
	  active            INTEGER NOT NULL DEFAULT 1,
	  token_ttl_seconds INTEGER,
	  no_expiry         INTEGER NOT NULL DEFAULT 0,
	  token_expires_at  TEXT,
	  created_at        TEXT NOT NULL,
	  updated_at        TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_api_clients_login
	  ON api_clients(login_id)`,
	`CREATE TABLE IF NOT EXISTS api_access_tokens (
	  token_hash   TEXT PRIMARY KEY,
	  client_id    TEXT NOT NULL REFERENCES api_clients(id) ON DELETE CASCADE,
	  issued_at    TEXT NOT NULL,
	  expires_at   TEXT,
	  revoked_at   TEXT,
	  last_used_at TEXT
	)`,
	`CREATE INDEX IF NOT EXISTS idx_api_access_tokens_client
	  ON api_access_tokens(client_id)`,
	// 0004: calls are a separate table from wa_messages (see that migration's
	// header for why) — no body, no wa_message_id, its own terminal states.
	`CREATE TABLE IF NOT EXISTS wa_calls (
	  id            TEXT PRIMARY KEY,
	  session_id    TEXT NOT NULL REFERENCES wa_sessions(id) ON DELETE CASCADE,
	  api_client_id TEXT,
	  to_phone      TEXT NOT NULL,
	  kind          TEXT NOT NULL,
	  text          TEXT,
	  voice         TEXT,
	  audio_url     TEXT,
	  call_id       TEXT NOT NULL DEFAULT '',
	  recording_url TEXT NOT NULL DEFAULT '',
	  status        TEXT NOT NULL DEFAULT 'placing',
	  error         TEXT NOT NULL DEFAULT '',
	  created_at    TEXT NOT NULL,
	  ended_at      TEXT
	)`,
	`CREATE INDEX IF NOT EXISTS idx_wa_calls_session
	  ON wa_calls(session_id, created_at DESC)`,
	// 0006: customer replies. Deliberately NO FK to wa_sessions (unlike
	// wa_calls above) — an arrival whose session was later deleted must
	// still persist; see migrations/0006_inbound_messages.sql's header.
	`CREATE TABLE IF NOT EXISTS wa_inbound_messages (
	  id                    TEXT PRIMARY KEY,
	  session_id            TEXT NOT NULL,
	  from_phone            TEXT NOT NULL,
	  from_jid              TEXT NOT NULL,
	  from_lid              TEXT,
	  body                  TEXT,
	  received_at           TEXT NOT NULL,
	  client_ref            TEXT,
	  tenant_ref            TEXT,
	  replies_to_message_id TEXT,
	  created_at            TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_wa_inbound_received
	  ON wa_inbound_messages(received_at)`,
	`CREATE INDEX IF NOT EXISTS idx_wa_inbound_from
	  ON wa_inbound_messages(from_phone, received_at)`,
	`CREATE INDEX IF NOT EXISTS idx_wa_inbound_client
	  ON wa_inbound_messages(client_ref, received_at)`,
	// 0007: Self Convo. Three tables, not columns on wa_messages — a run is
	// a schedule, not a send; see migrations/0007_self_convo.sql's header.
	// No FK from wa_convo_runs to wa_sessions (same reasoning as 0006 above
	// — session names/phones are snapshotted so a deleted session still
	// renders); wa_convo_turns DOES cascade off its own run.
	`CREATE TABLE IF NOT EXISTS wa_convo_templates (
	  id          TEXT PRIMARY KEY,
	  name        TEXT NOT NULL,
	  topic       TEXT NOT NULL DEFAULT '',
	  stage       TEXT NOT NULL DEFAULT '',
	  origin      TEXT NOT NULL DEFAULT 'upload',
	  turn_count  INTEGER NOT NULL,
	  body        TEXT NOT NULL,
	  created_at  TEXT NOT NULL,
	  updated_at  TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_wa_convo_templates_created
	  ON wa_convo_templates(created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_wa_convo_templates_topic_stage
	  ON wa_convo_templates(topic, stage)`,
	`CREATE TABLE IF NOT EXISTS wa_convo_runs (
	  id                TEXT PRIMARY KEY,
	  template_id       TEXT,
	  template_name     TEXT NOT NULL,
	  schedule_id       TEXT,
	  occurrence_date   TEXT NOT NULL DEFAULT '',
	  chain_prev_run_id TEXT,
	  chain_depth       INTEGER NOT NULL DEFAULT 0,
	  topic             TEXT NOT NULL DEFAULT '',
	  paraphrase_seed   INTEGER NOT NULL DEFAULT 0,
	  session_a_id    TEXT NOT NULL,
	  session_b_id    TEXT NOT NULL,
	  session_a_name  TEXT NOT NULL DEFAULT '',
	  session_b_name  TEXT NOT NULL DEFAULT '',
	  phone_a         TEXT NOT NULL,
	  phone_b         TEXT NOT NULL,
	  state           TEXT NOT NULL,
	  turn_count      INTEGER NOT NULL,
	  turns_done      INTEGER NOT NULL DEFAULT 0,
	  start_at        TEXT NOT NULL,
	  next_due_at     TEXT,
	  started_at      TEXT,
	  finished_at     TEXT,
	  error           TEXT NOT NULL DEFAULT '',
	  created_at      TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_wa_convo_runs_due
	  ON wa_convo_runs(state, next_due_at)`,
	`CREATE INDEX IF NOT EXISTS idx_wa_convo_runs_created
	  ON wa_convo_runs(created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_wa_convo_runs_schedule_date
	  ON wa_convo_runs(schedule_id, occurrence_date)`,
	`CREATE TABLE IF NOT EXISTS wa_convo_turns (
	  run_id        TEXT NOT NULL REFERENCES wa_convo_runs(id) ON DELETE CASCADE,
	  seq           INTEGER NOT NULL,
	  speaker       TEXT NOT NULL,
	  session_id    TEXT NOT NULL,
	  to_phone      TEXT NOT NULL,
	  body          TEXT NOT NULL,
	  delay_seconds INTEGER NOT NULL,
	  state         TEXT NOT NULL DEFAULT 'pending',
	  message_id    TEXT,
	  error         TEXT NOT NULL DEFAULT '',
	  sent_at       TEXT,
	  PRIMARY KEY (run_id, seq)
	)`,
	// 0008: Convo scheduling. wa_convo_template_links cascades off BOTH its
	// endpoints (unlike wa_convo_runs' snapshot columns above) — a link has
	// no audit value once either template it connects is gone; see
	// migrations/0008_convo_scheduling.sql's header. wa_convo_schedules'
	// receivers cascade off their schedule the same way wa_convo_turns
	// cascades off its run.
	`CREATE TABLE IF NOT EXISTS wa_convo_template_links (
	  from_template_id TEXT NOT NULL REFERENCES wa_convo_templates(id) ON DELETE CASCADE,
	  to_template_id   TEXT NOT NULL REFERENCES wa_convo_templates(id) ON DELETE CASCADE,
	  weight           INTEGER NOT NULL DEFAULT 1,
	  min_gap_seconds  INTEGER NOT NULL DEFAULT 0,
	  max_gap_seconds  INTEGER NOT NULL DEFAULT 0,
	  created_at       TEXT NOT NULL,
	  PRIMARY KEY (from_template_id, to_template_id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_wa_convo_template_links_to
	  ON wa_convo_template_links(to_template_id)`,
	`CREATE TABLE IF NOT EXISTS wa_convo_schedules (
	  id                TEXT PRIMARY KEY,
	  name              TEXT NOT NULL,
	  sender_session_id TEXT NOT NULL,
	  sender_name       TEXT NOT NULL DEFAULT '',
	  sender_phone      TEXT NOT NULL DEFAULT '',
	  topic             TEXT NOT NULL DEFAULT '',
	  root_template_id  TEXT,
	  chain_enabled     INTEGER NOT NULL DEFAULT 1,
	  recurrence        TEXT NOT NULL,
	  weekdays          TEXT NOT NULL DEFAULT '',
	  interval_days     INTEGER NOT NULL DEFAULT 0,
	  sets_per_day      INTEGER NOT NULL DEFAULT 1,
	  window_start      TEXT NOT NULL,
	  window_end        TEXT NOT NULL,
	  timezone          TEXT NOT NULL DEFAULT 'UTC',
	  start_date        TEXT NOT NULL,
	  end_date          TEXT,
	  state             TEXT NOT NULL,
	  paused_reason     TEXT NOT NULL DEFAULT '',
	  paused_at         TEXT,
	  last_planned_date TEXT,
	  created_at        TEXT NOT NULL,
	  updated_at        TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_wa_convo_schedules_state_planned
	  ON wa_convo_schedules(state, last_planned_date)`,
	`CREATE TABLE IF NOT EXISTS wa_convo_schedule_receivers (
	  schedule_id  TEXT NOT NULL REFERENCES wa_convo_schedules(id) ON DELETE CASCADE,
	  session_id   TEXT NOT NULL,
	  session_name TEXT NOT NULL DEFAULT '',
	  phone        TEXT NOT NULL DEFAULT '',
	  position     INTEGER NOT NULL DEFAULT 0,
	  PRIMARY KEY (schedule_id, session_id)
	)`,
	// 0010: known groups — a small per-session CRUD mapping a friendly name
	// to a real WhatsApp group JID. See migrations/0010_known_groups.sql's
	// header.
	`CREATE TABLE IF NOT EXISTS wa_known_groups (
	  id         TEXT PRIMARY KEY,
	  session_id TEXT NOT NULL,
	  group_jid  TEXT NOT NULL,
	  name       TEXT NOT NULL,
	  created_at TEXT NOT NULL,
	  updated_at TEXT NOT NULL,
	  UNIQUE (session_id, group_jid)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_wa_known_groups_session
	  ON wa_known_groups(session_id)`,
	// 0011: Group Broadcast Schedule. Four tables, parallel to (but
	// deliberately separate from) 0008's Convo scheduling tables above —
	// see migrations/0011_group_broadcast.sql's header for why.
	`CREATE TABLE IF NOT EXISTS wa_group_broadcast_schedules (
	  id                    TEXT PRIMARY KEY,
	  name                  TEXT NOT NULL,
	  sender_session_id     TEXT NOT NULL,
	  sender_name           TEXT NOT NULL DEFAULT '',
	  sender_phone          TEXT NOT NULL DEFAULT '',
	  known_group_id        TEXT,
	  group_jid             TEXT NOT NULL,
	  group_name            TEXT NOT NULL DEFAULT '',
	  recurrence            TEXT NOT NULL,
	  weekdays              TEXT NOT NULL DEFAULT '',
	  interval_days         INTEGER NOT NULL DEFAULT 0,
	  sender_window_start   TEXT NOT NULL DEFAULT '07:55',
	  sender_window_end     TEXT NOT NULL DEFAULT '08:16',
	  receiver_window_start TEXT NOT NULL DEFAULT '08:17',
	  receiver_window_end   TEXT NOT NULL DEFAULT '09:30',
	  late_cutoff           TEXT NOT NULL DEFAULT '08:30',
	  lang_id_weight        INTEGER NOT NULL DEFAULT 70,
	  timezone              TEXT NOT NULL DEFAULT 'Asia/Jakarta',
	  start_date            TEXT NOT NULL,
	  end_date              TEXT,
	  state                 TEXT NOT NULL,
	  paused_reason         TEXT NOT NULL DEFAULT '',
	  paused_at             TEXT,
	  last_planned_date     TEXT,
	  created_at            TEXT NOT NULL,
	  updated_at            TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_wa_gb_schedules_state_planned
	  ON wa_group_broadcast_schedules(state, last_planned_date)`,
	`CREATE TABLE IF NOT EXISTS wa_group_broadcast_receivers (
	  schedule_id  TEXT NOT NULL REFERENCES wa_group_broadcast_schedules(id) ON DELETE CASCADE,
	  session_id   TEXT NOT NULL,
	  session_name TEXT NOT NULL DEFAULT '',
	  phone        TEXT NOT NULL DEFAULT '',
	  position     INTEGER NOT NULL DEFAULT 0,
	  PRIMARY KEY (schedule_id, session_id)
	)`,
	`CREATE TABLE IF NOT EXISTS wa_group_broadcast_occurrences (
	  id              TEXT PRIMARY KEY,
	  schedule_id     TEXT NOT NULL,
	  occurrence_date TEXT NOT NULL,
	  group_jid       TEXT NOT NULL,
	  state           TEXT NOT NULL DEFAULT 'scheduled',
	  created_at      TEXT NOT NULL,
	  finished_at     TEXT,
	  UNIQUE (schedule_id, occurrence_date)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_wa_gb_occ_schedule_date
	  ON wa_group_broadcast_occurrences(schedule_id, occurrence_date)`,
	`CREATE TABLE IF NOT EXISTS wa_group_broadcast_turns (
	  id            TEXT PRIMARY KEY,
	  occurrence_id TEXT NOT NULL REFERENCES wa_group_broadcast_occurrences(id) ON DELETE CASCADE,
	  role          TEXT NOT NULL,
	  session_id    TEXT NOT NULL,
	  session_name  TEXT NOT NULL DEFAULT '',
	  language      TEXT NOT NULL,
	  is_late       INTEGER NOT NULL DEFAULT 0,
	  late_reason   TEXT NOT NULL DEFAULT '',
	  body          TEXT NOT NULL,
	  source        TEXT NOT NULL,
	  due_at        TEXT NOT NULL,
	  state         TEXT NOT NULL DEFAULT 'pending',
	  message_id    TEXT NOT NULL DEFAULT '',
	  error         TEXT NOT NULL DEFAULT '',
	  sent_at       TEXT,
	  created_at    TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_wa_gb_turns_due
	  ON wa_group_broadcast_turns(state, due_at)`,
	`CREATE INDEX IF NOT EXISTS idx_wa_gb_turns_occurrence
	  ON wa_group_broadcast_turns(occurrence_id)`,
	`CREATE INDEX IF NOT EXISTS idx_wa_gb_turns_session_due
	  ON wa_group_broadcast_turns(session_id, due_at)`,
	// 0012: owner-managed Contacts — a global (not per-session) name/phone
	// book with optional aliases and a last-known WhatsApp verification
	// status. See migrations/0012_contacts.sql's header.
	`CREATE TABLE IF NOT EXISTS wa_contacts (
	  id          TEXT PRIMARY KEY,
	  name        TEXT NOT NULL,
	  phone       TEXT NOT NULL UNIQUE,
	  aliases     TEXT NOT NULL DEFAULT '',
	  jid         TEXT NOT NULL DEFAULT '',
	  wa_status   TEXT NOT NULL DEFAULT 'unknown',
	  verified_at TEXT NOT NULL DEFAULT '',
	  created_at  TEXT NOT NULL,
	  updated_at  TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_wa_contacts_name
	  ON wa_contacts(name)`,
	// 0013: scheduled WhatsApp status posts — one table, no occurrence/turn
	// tables. See migrations/0013_status_schedules.sql's header.
	`CREATE TABLE IF NOT EXISTS wa_status_schedules (
	  id                 TEXT PRIMARY KEY,
	  name               TEXT NOT NULL,
	  session_id         TEXT NOT NULL,
	  kind               TEXT NOT NULL,
	  text               TEXT NOT NULL DEFAULT '',
	  bg_argb            INTEGER NOT NULL DEFAULT 0,
	  caption            TEXT NOT NULL DEFAULT '',
	  media_object_key   TEXT NOT NULL DEFAULT '',
	  media_content_type TEXT NOT NULL DEFAULT '',
	  media_filename     TEXT NOT NULL DEFAULT '',
	  weekdays           TEXT NOT NULL DEFAULT '',
	  window_start       TEXT NOT NULL DEFAULT '07:00',
	  window_end         TEXT NOT NULL DEFAULT '10:00',
	  timezone           TEXT NOT NULL DEFAULT 'Asia/Jakarta',
	  start_date         TEXT NOT NULL,
	  end_date           TEXT,
	  recipients_mode    TEXT NOT NULL DEFAULT 'all',
	  recipients_json    TEXT NOT NULL DEFAULT '',
	  state              TEXT NOT NULL,
	  paused_reason      TEXT NOT NULL DEFAULT '',
	  last_planned_date  TEXT,
	  next_fire_at       TEXT NOT NULL DEFAULT '',
	  last_fired_at      TEXT NOT NULL DEFAULT '',
	  last_result        TEXT NOT NULL DEFAULT '',
	  last_error         TEXT NOT NULL DEFAULT '',
	  created_at         TEXT NOT NULL,
	  updated_at         TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_wa_status_schedules_state_fire
	  ON wa_status_schedules(state, next_fire_at)`,
	// 0015: multiple routing groups per session. See that migration's
	// header — wa_sessions.group_code stays as the cached first group;
	// this table is the full set.
	`CREATE TABLE IF NOT EXISTS wa_session_groups (
	  session_id  TEXT NOT NULL,
	  group_code  TEXT NOT NULL,
	  created_at  TEXT NOT NULL,
	  PRIMARY KEY (session_id, group_code)
	)`,
	// 0016: operator-configurable session alerting (logout alert + daily
	// report). Single row, id always 1 — see that migration's header.
	`CREATE TABLE IF NOT EXISTS wa_session_alert_settings (
	  id                    INTEGER PRIMARY KEY,
	  logout_alert_enabled  INTEGER NOT NULL DEFAULT 0,
	  daily_report_enabled  INTEGER NOT NULL DEFAULT 0,
	  daily_time            TEXT NOT NULL DEFAULT '08:00',
	  timezone              TEXT NOT NULL DEFAULT 'Asia/Jakarta',
	  target_group_jid      TEXT NOT NULL DEFAULT '',
	  target_group_name     TEXT NOT NULL DEFAULT '',
	  sender_session_id     TEXT NOT NULL DEFAULT '',
	  last_daily_date       TEXT NOT NULL DEFAULT '',
	  updated_at            TEXT NOT NULL
	)`,
}

// EnsureSchema creates the app tables and index if missing, inside one
// transaction, so a second boot is a no-op.
func EnsureSchema(ctx context.Context, pool *sql.DB) error {
	tx, err := pool.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range schema {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("ensure schema: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := ensureColumn(ctx, pool, "wa_sessions", "last_selected_at", "TEXT"); err != nil {
		return err
	}
	// 0005 master-session flag, same reason.
	if err := ensureColumn(ctx, pool, "wa_sessions", "is_master", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	// 0009 routing group code, same reason.
	if err := ensureColumn(ctx, pool, "wa_sessions", "group_code", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// 0015 self-reminder flag, same reason.
	if err := ensureColumn(ctx, pool, "wa_sessions", "is_self_reminder", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	// 0002 attribution columns, for a SQLite dev DB created before they existed.
	for _, col := range []string{"api_client_id", "client_ref", "tenant_ref", "category"} {
		if err := ensureColumn(ctx, pool, "wa_messages", col, "TEXT"); err != nil {
			return err
		}
	}
	// 0003 absolute token cutoff, same reason.
	if err := ensureColumn(ctx, pool, "api_clients", "token_expires_at", "TEXT"); err != nil {
		return err
	}
	// 0004 media columns, same reason. media_size is INTEGER (BIGINT in MySQL);
	// the rest are TEXT like every other nullable string column in this schema.
	for _, col := range []string{"media_kind", "media_filename", "media_mime", "media_object_key"} {
		if err := ensureColumn(ctx, pool, "wa_messages", col, "TEXT"); err != nil {
			return err
		}
	}
	if err := ensureColumn(ctx, pool, "wa_messages", "media_size", "INTEGER"); err != nil {
		return err
	}
	// 0008 Convo scheduling additive columns, same reason as every other
	// ensureColumn call here: a SQLite dev/e2e database created before this
	// migration must still gain them.
	for _, col := range []struct{ name, typ string }{
		{"topic", "TEXT NOT NULL DEFAULT ''"},
		{"stage", "TEXT NOT NULL DEFAULT ''"},
		{"origin", "TEXT NOT NULL DEFAULT 'upload'"},
	} {
		if err := ensureColumn(ctx, pool, "wa_convo_templates", col.name, col.typ); err != nil {
			return err
		}
	}
	for _, col := range []struct{ name, typ string }{
		{"schedule_id", "TEXT"},
		{"occurrence_date", "TEXT NOT NULL DEFAULT ''"},
		{"chain_prev_run_id", "TEXT"},
		{"chain_depth", "INTEGER NOT NULL DEFAULT 0"},
		{"topic", "TEXT NOT NULL DEFAULT ''"},
		{"paraphrase_seed", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := ensureColumn(ctx, pool, "wa_convo_runs", col.name, col.typ); err != nil {
			return err
		}
	}
	// 0014 AI-written status wording, same reason as every ensureColumn call
	// here (a SQLite dev/e2e database created before the migration must gain
	// them). TEXT columns are nullable, mirroring the MySQL side.
	for _, col := range []struct{ name, typ string }{
		{"ai_enabled", "INTEGER NOT NULL DEFAULT 0"},
		{"ai_topic", "TEXT NOT NULL DEFAULT ''"},
		{"next_text", "TEXT"},
		{"next_text_source", "TEXT NOT NULL DEFAULT ''"},
		{"last_ai_text", "TEXT"},
	} {
		if err := ensureColumn(ctx, pool, "wa_status_schedules", col.name, col.typ); err != nil {
			return err
		}
	}
	return nil
}

func ensureColumn(ctx context.Context, pool *sql.DB, table, column, typ string) error {
	rows, err := pool.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, colType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = pool.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, column, typ))
	return err
}
