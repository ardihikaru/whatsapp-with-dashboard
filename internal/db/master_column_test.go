package db

import (
	"context"
	"path/filepath"
	"testing"
)

// TestEnsureSchemaAddsIsMasterToALegacySessionsTable is the upgrade path for
// every existing dev/e2e SQLite database: it creates wa_sessions exactly as
// it existed before this phase's 0005 migration (no is_master column), then
// runs EnsureSchema and asserts the column was added in place — mirroring
// TestEnsureSchemaAddsTheMediaColumnsToAnOlderDatabase (media_columns_test.go)
// for the 0004 media columns.
func TestEnsureSchemaAddsIsMasterToALegacySessionsTable(t *testing.T) {
	ctx := context.Background()
	pool, err := Open(filepath.Join(t.TempDir(), "old.db"), SQLiteDialect)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	// wa_sessions as it existed right after 0001-0004, before this phase's
	// 0005 is_master column.
	if _, err := pool.ExecContext(ctx, `CREATE TABLE wa_sessions (
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
	)`); err != nil {
		t.Fatalf("create legacy wa_sessions: %v", err)
	}

	if err := EnsureSchema(ctx, pool); err != nil {
		t.Fatalf("EnsureSchema on legacy db: %v", err)
	}

	var n int
	if err := pool.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('wa_sessions') WHERE name = 'is_master'`,
	).Scan(&n); err != nil {
		t.Fatalf("pragma_table_info(is_master): %v", err)
	}
	if n != 1 {
		t.Errorf("wa_sessions.is_master present = %d, want 1 — EnsureSchema did not add it to a pre-0005 database", n)
	}
}
