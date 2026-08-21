// Package db opens the module's two stores (hybrid model, docs/07 §2):
//   - Open()    → SQLite, drives the whatsmeow sqlstore crypto tables (and, in
//     unit tests, the app tables via EnsureSchema);
//   - OpenApp() → MySQL, drives the app's own tables in production. The app
//     schema is created by goose migrations, NOT EnsureSchema, against MySQL.
package db

import (
	"context"
	"database/sql"
	"fmt"
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
	  sent_at       TEXT
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
	// 0002 attribution columns, for a SQLite dev DB created before they existed.
	for _, col := range []string{"api_client_id", "client_ref", "tenant_ref", "category"} {
		if err := ensureColumn(ctx, pool, "wa_messages", col, "TEXT"); err != nil {
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
