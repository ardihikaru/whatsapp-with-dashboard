package db

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var statusScheduleAIColumns = []string{"ai_enabled", "ai_topic", "next_text", "next_text_source", "last_ai_text"}

// TestEnsureSchemaAddsTheAIColumnsToALegacyStatusSchedulesTable: a SQLite
// database created at 0013 (every existing e2e/dev file) must gain the 0014
// columns in place, and an existing row must come out as "AI off".
func TestEnsureSchemaAddsTheAIColumnsToALegacyStatusSchedulesTable(t *testing.T) {
	ctx := context.Background()
	pool, err := Open(filepath.Join(t.TempDir(), "old.db"), SQLiteDialect)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	// The table exactly as 0013 left it (no AI columns).
	if _, err := pool.ExecContext(ctx, `CREATE TABLE wa_status_schedules (
	  id TEXT PRIMARY KEY, name TEXT NOT NULL, session_id TEXT NOT NULL, kind TEXT NOT NULL,
	  text TEXT NOT NULL DEFAULT '', bg_argb INTEGER NOT NULL DEFAULT 0, caption TEXT NOT NULL DEFAULT '',
	  media_object_key TEXT NOT NULL DEFAULT '', media_content_type TEXT NOT NULL DEFAULT '',
	  media_filename TEXT NOT NULL DEFAULT '', weekdays TEXT NOT NULL DEFAULT '',
	  window_start TEXT NOT NULL DEFAULT '07:00', window_end TEXT NOT NULL DEFAULT '10:00',
	  timezone TEXT NOT NULL DEFAULT 'Asia/Jakarta', start_date TEXT NOT NULL, end_date TEXT,
	  recipients_mode TEXT NOT NULL DEFAULT 'all', recipients_json TEXT NOT NULL DEFAULT '',
	  state TEXT NOT NULL, paused_reason TEXT NOT NULL DEFAULT '', last_planned_date TEXT,
	  next_fire_at TEXT NOT NULL DEFAULT '', last_fired_at TEXT NOT NULL DEFAULT '',
	  last_result TEXT NOT NULL DEFAULT '', last_error TEXT NOT NULL DEFAULT '',
	  created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO wa_status_schedules (id,name,session_id,kind,start_date,state,created_at,updated_at)
	  VALUES ('old','n','s','text','2026-01-01','active','x','x')`); err != nil {
		t.Fatal(err)
	}
	if err := EnsureSchema(ctx, pool); err != nil {
		t.Fatalf("EnsureSchema on a 0013-era db: %v", err)
	}
	for _, col := range statusScheduleAIColumns {
		var n int
		if err := pool.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('wa_status_schedules') WHERE name = ?`, col).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("wa_status_schedules.%s present = %d, want 1", col, n)
		}
	}
	var enabled int
	if err := pool.QueryRowContext(ctx, `SELECT ai_enabled FROM wa_status_schedules WHERE id='old'`).Scan(&enabled); err != nil || enabled != 0 {
		t.Errorf("pre-existing row ai_enabled = %d (err %v), want 0", enabled, err)
	}
}

// TestMigration0014AddsEveryAIColumnTheMirrorHas pins the other half of the
// two-copies rule: the goose file names exactly the columns EnsureSchema adds.
func TestMigration0014AddsEveryAIColumnTheMirrorHas(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(migrationsDir, "0014_status_schedule_ai.sql"))
	if err != nil {
		t.Fatal(err)
	}
	up := strings.Split(string(b), "-- +goose Down")[0]
	for _, col := range statusScheduleAIColumns {
		if !strings.Contains(up, "ADD COLUMN "+col) {
			t.Errorf("0014 does not ADD COLUMN %s", col)
		}
	}
	if !strings.Contains(up, "ai_enabled       TINYINT      NOT NULL DEFAULT 0") {
		t.Error("ai_enabled must be NOT NULL DEFAULT 0 so the old binary's inserts keep working")
	}
}
