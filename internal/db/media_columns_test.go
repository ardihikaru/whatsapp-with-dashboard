package db

import (
	"context"
	"path/filepath"
	"testing"
)

// TestEnsureSchemaAddsTheMediaColumnsToAnOlderDatabase is the upgrade path for
// every existing dev/e2e SQLite database: it creates wa_messages exactly as
// 0001+0002+0003 (and no further) left it, with none of the 0004 media
// columns, then runs EnsureSchema and asserts each one was added in place —
// mirroring how TestSQLiteAppStoreIsIdempotentOnASecondOpen already proves
// this for the 0003 column.
func TestEnsureSchemaAddsTheMediaColumnsToAnOlderDatabase(t *testing.T) {
	ctx := context.Background()
	pool, err := Open(filepath.Join(t.TempDir(), "old.db"), SQLiteDialect)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	// wa_messages as it existed right after 0003, before this phase's 0004.
	if _, err := pool.ExecContext(ctx, `CREATE TABLE wa_messages (
	  id            TEXT PRIMARY KEY,
	  session_id    TEXT NOT NULL,
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
	)`); err != nil {
		t.Fatalf("create legacy wa_messages: %v", err)
	}
	// wa_sessions must exist too — EnsureSchema's own CREATE TABLE IF NOT
	// EXISTS would otherwise create it fresh (with every current column) and
	// mask a wa_messages-specific regression. wa_calls's FK also needs it.
	if _, err := pool.ExecContext(ctx, `CREATE TABLE wa_sessions (
	  id TEXT PRIMARY KEY, name TEXT, phone TEXT, created_at TEXT, updated_at TEXT
	)`); err != nil {
		t.Fatalf("create legacy wa_sessions: %v", err)
	}

	if err := EnsureSchema(ctx, pool); err != nil {
		t.Fatalf("EnsureSchema on legacy db: %v", err)
	}

	for _, col := range []string{"media_kind", "media_filename", "media_mime", "media_size", "media_object_key"} {
		var n int
		if err := pool.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM pragma_table_info('wa_messages') WHERE name = ?`, col,
		).Scan(&n); err != nil {
			t.Fatalf("pragma_table_info(%s): %v", col, err)
		}
		if n != 1 {
			t.Errorf("wa_messages.%s present = %d, want 1 — EnsureSchema did not add it to a pre-0004 database", col, n)
		}
	}
}
