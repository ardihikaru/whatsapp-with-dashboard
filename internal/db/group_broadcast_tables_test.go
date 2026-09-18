package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestKnownGroupsAndGroupBroadcastTablesAcceptAnInsertAfterEnsureSchema
// asserts the five new tables added by migrations/0010_known_groups.sql and
// migrations/0011_group_broadcast.sql exist in the SQLite mirror
// (internal/db.schema) with a shape that accepts a real row — the same
// table-name-parity-is-not-enough reasoning as
// TestConvoTablesAcceptAnInsertAfterEnsureSchema (convo_tables_test.go).
func TestKnownGroupsAndGroupBroadcastTablesAcceptAnInsertAfterEnsureSchema(t *testing.T) {
	ctx := context.Background()
	pool, err := OpenAppStore(ctx, SQLiteAppPrefix+filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("OpenAppStore: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	if _, err := pool.ExecContext(ctx,
		`INSERT INTO wa_known_groups (id, session_id, group_jid, name, created_at, updated_at)
		 VALUES ('kg1', 'sess-a', '120363426585371342@g.us', 'Clino Team',
		         '2026-09-18T00:00:00Z', '2026-09-18T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert wa_known_groups: %v", err)
	}

	if _, err := pool.ExecContext(ctx,
		`INSERT INTO wa_group_broadcast_schedules
		   (id, name, sender_session_id, sender_name, sender_phone, known_group_id, group_jid, group_name,
		    recurrence, weekdays, interval_days,
		    sender_window_start, sender_window_end, receiver_window_start, receiver_window_end, late_cutoff,
		    lang_id_weight, timezone, start_date, end_date, state, paused_reason, paused_at, last_planned_date,
		    created_at, updated_at)
		 VALUES
		   ('sch1', 'Morning check-in', 'sess-a', 'Session A', '62800000001', 'kg1',
		    '120363426585371342@g.us', 'Clino Team',
		    'daily', '', 0,
		    '07:55', '08:16', '08:17', '09:30', '08:30',
		    70, 'Asia/Jakarta', '2026-09-18', NULL, 'active', '', NULL, NULL,
		    '2026-09-18T00:00:00Z', '2026-09-18T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert wa_group_broadcast_schedules: %v", err)
	}

	if _, err := pool.ExecContext(ctx,
		`INSERT INTO wa_group_broadcast_receivers (schedule_id, session_id, session_name, phone, position)
		 VALUES ('sch1', 'sess-b', 'Session B', '62800000002', 0)`,
	); err != nil {
		t.Fatalf("insert wa_group_broadcast_receivers: %v", err)
	}

	if _, err := pool.ExecContext(ctx,
		`INSERT INTO wa_group_broadcast_occurrences (id, schedule_id, occurrence_date, group_jid, state, created_at)
		 VALUES ('occ1', 'sch1', '2026-09-18', '120363426585371342@g.us', 'scheduled', '2026-09-18T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert wa_group_broadcast_occurrences: %v", err)
	}

	if _, err := pool.ExecContext(ctx,
		`INSERT INTO wa_group_broadcast_turns
		   (id, occurrence_id, role, session_id, session_name, language, is_late, late_reason,
		    body, source, due_at, state, message_id, error, created_at)
		 VALUES
		   ('turn1', 'occ1', 'sender', 'sess-a', 'Session A', 'id', 0, '',
		    'Selamat pagi tim!', 'ai', '2026-09-18T00:58:00Z', 'pending', '', '', '2026-09-18T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert wa_group_broadcast_turns: %v", err)
	}

	var name string
	if err := pool.QueryRowContext(ctx,
		`SELECT name FROM wa_known_groups WHERE id='kg1'`).Scan(&name); err != nil {
		t.Fatalf("select wa_known_groups: %v", err)
	}
	if name != "Clino Team" {
		t.Errorf("name = %q, want %q", name, "Clino Team")
	}

	var state string
	if err := pool.QueryRowContext(ctx,
		`SELECT state FROM wa_group_broadcast_schedules WHERE id='sch1'`).Scan(&state); err != nil {
		t.Fatalf("select wa_group_broadcast_schedules: %v", err)
	}
	if state != "active" {
		t.Errorf("state = %q, want %q", state, "active")
	}

	var body string
	if err := pool.QueryRowContext(ctx,
		`SELECT body FROM wa_group_broadcast_turns WHERE id='turn1'`).Scan(&body); err != nil {
		t.Fatalf("select wa_group_broadcast_turns: %v", err)
	}
	if body != "Selamat pagi tim!" {
		t.Errorf("body = %q, want the turn text", body)
	}
}

// TestDeletingAGroupBroadcastScheduleCascadesToItsReceivers asserts SQLite
// actually enforces the fk_wa_gb_receivers_schedule ON DELETE CASCADE
// declared in migrations/0011_group_broadcast.sql — same evidence-not-
// assumption reasoning as TestDeletingAConvoScheduleCascadesToItsReceivers.
func TestDeletingAGroupBroadcastScheduleCascadesToItsReceivers(t *testing.T) {
	ctx := context.Background()
	pool, err := OpenAppStore(ctx, SQLiteAppPrefix+filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("OpenAppStore: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	if _, err := pool.ExecContext(ctx,
		`INSERT INTO wa_group_broadcast_schedules
		   (id, name, sender_session_id, group_jid, recurrence,
		    sender_window_start, sender_window_end, receiver_window_start, receiver_window_end, late_cutoff,
		    start_date, state, created_at, updated_at)
		 VALUES ('sch1', 'Morning check-in', 'sess-a', '120363426585371342@g.us', 'daily',
		         '07:55', '08:16', '08:17', '09:30', '08:30',
		         '2026-09-18', 'active', '2026-09-18T00:00:00Z', '2026-09-18T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert wa_group_broadcast_schedules: %v", err)
	}
	if _, err := pool.ExecContext(ctx,
		`INSERT INTO wa_group_broadcast_receivers (schedule_id, session_id, session_name, phone, position)
		 VALUES ('sch1', 'sess-b', 'Session B', '62800000002', 0)`,
	); err != nil {
		t.Fatalf("insert wa_group_broadcast_receivers: %v", err)
	}

	if _, err := pool.ExecContext(ctx, `DELETE FROM wa_group_broadcast_schedules WHERE id='sch1'`); err != nil {
		t.Fatalf("delete wa_group_broadcast_schedules: %v", err)
	}

	var remaining int
	if err := pool.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM wa_group_broadcast_receivers WHERE schedule_id='sch1'`).Scan(&remaining); err != nil {
		t.Fatalf("count wa_group_broadcast_receivers: %v", err)
	}
	if remaining != 0 {
		t.Errorf("wa_group_broadcast_receivers rows left after deleting their schedule = %d, want 0 (cascade did not fire)", remaining)
	}
}

// TestDeletingAGroupBroadcastOccurrenceCascadesToItsTurns mirrors
// TestDeletingAConvoRunCascadesToItsTurns for the new occurrence/turns pair.
func TestDeletingAGroupBroadcastOccurrenceCascadesToItsTurns(t *testing.T) {
	ctx := context.Background()
	pool, err := OpenAppStore(ctx, SQLiteAppPrefix+filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("OpenAppStore: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	if _, err := pool.ExecContext(ctx,
		`INSERT INTO wa_group_broadcast_occurrences (id, schedule_id, occurrence_date, group_jid, state, created_at)
		 VALUES ('occ1', 'sch1', '2026-09-18', '120363426585371342@g.us', 'scheduled', '2026-09-18T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert wa_group_broadcast_occurrences: %v", err)
	}
	if _, err := pool.ExecContext(ctx,
		`INSERT INTO wa_group_broadcast_turns
		   (id, occurrence_id, role, session_id, session_name, language, body, source, due_at, state, created_at)
		 VALUES ('turn1', 'occ1', 'sender', 'sess-a', 'Session A', 'id', 'hi', 'ai',
		         '2026-09-18T00:58:00Z', 'pending', '2026-09-18T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert wa_group_broadcast_turns: %v", err)
	}

	if _, err := pool.ExecContext(ctx, `DELETE FROM wa_group_broadcast_occurrences WHERE id='occ1'`); err != nil {
		t.Fatalf("delete wa_group_broadcast_occurrences: %v", err)
	}

	var remaining int
	if err := pool.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM wa_group_broadcast_turns WHERE occurrence_id='occ1'`).Scan(&remaining); err != nil {
		t.Fatalf("count wa_group_broadcast_turns: %v", err)
	}
	if remaining != 0 {
		t.Errorf("wa_group_broadcast_turns rows left after deleting their occurrence = %d, want 0 (cascade did not fire)", remaining)
	}
}

// TestKnownGroupsRejectsADuplicateSessionAndJIDPair asserts the SQLite
// mirror's UNIQUE (session_id, group_jid) constraint from
// migrations/0010_known_groups.sql actually fires.
func TestKnownGroupsRejectsADuplicateSessionAndJIDPair(t *testing.T) {
	ctx := context.Background()
	pool, err := OpenAppStore(ctx, SQLiteAppPrefix+filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("OpenAppStore: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	insert := `INSERT INTO wa_known_groups (id, session_id, group_jid, name, created_at, updated_at)
		 VALUES (?, 'sess-a', '120363426585371342@g.us', 'Clino Team',
		         '2026-09-18T00:00:00Z', '2026-09-18T00:00:00Z')`
	if _, err := pool.ExecContext(ctx, insert, "kg1"); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if _, err := pool.ExecContext(ctx, insert, "kg2"); err == nil {
		t.Fatal("second insert with the same (session_id, group_jid) pair succeeded, want a UNIQUE violation")
	}
}

// TestGroupBroadcastMigrationAndSQLiteMirrorAgreeOnColumnsForEachNewTable
// mirrors TestConvoSchedulingMigrationAndSQLiteMirrorAgreeOnColumnsForEachNewTable
// (convo_tables_test.go) for migrations/0010_known_groups.sql and
// migrations/0011_group_broadcast.sql: it parses each migration's CREATE
// TABLE text for every genuinely new table and diffs it against PRAGMA
// table_info(...) on a database EnsureSchema just built, catching a column
// present in one hand-maintained copy and silently missing from the other —
// exactly the CLAUDE.md "two hand-maintained copies of one schema" hazard
// this migration pair is required to avoid. Deliberately RED without the
// SQLite mirror change: reverting the db.go edit while keeping the
// migration files makes every table below come back with zero columns from
// PRAGMA table_info, which migrationTableColumns' own "want zero" fatal
// check turns into a hard failure rather than a silently-empty comparison.
func TestGroupBroadcastMigrationAndSQLiteMirrorAgreeOnColumnsForEachNewTable(t *testing.T) {
	ctx := context.Background()
	pool, err := OpenAppStore(ctx, SQLiteAppPrefix+filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("OpenAppStore: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	cases := []struct {
		migrationFile string
		tables        []string
	}{
		{"0010_known_groups.sql", []string{"wa_known_groups"}},
		{"0011_group_broadcast.sql", []string{
			"wa_group_broadcast_schedules",
			"wa_group_broadcast_receivers",
			"wa_group_broadcast_occurrences",
			"wa_group_broadcast_turns",
		}},
	}

	for _, c := range cases {
		migrationSQL := readMigrationFile(t, c.migrationFile)
		for _, table := range c.tables {
			wantCols := migrationTableColumns(t, migrationSQL, table)
			if len(wantCols) == 0 {
				t.Fatalf("parsed zero columns for %s out of %s — parser or fixture is broken", table, c.migrationFile)
			}

			rows, err := pool.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%s)`, table))
			if err != nil {
				t.Fatalf("PRAGMA table_info(%s): %v", table, err)
			}
			var gotCols []string
			for rows.Next() {
				var cid, notNull, pk int
				var name, colType string
				var defaultValue any
				if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultValue, &pk); err != nil {
					rows.Close()
					t.Fatalf("scan PRAGMA table_info(%s): %v", table, err)
				}
				gotCols = append(gotCols, strings.ToLower(name))
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				t.Fatalf("PRAGMA table_info(%s) rows: %v", table, err)
			}
			sort.Strings(gotCols)

			if len(gotCols) == 0 {
				t.Fatalf("%s has no columns in the SQLite mirror — table missing from db.go's schema slice", table)
			}
			if strings.Join(wantCols, ",") != strings.Join(gotCols, ",") {
				t.Errorf("%s columns disagree between %s and the SQLite mirror:\n  migration: %v\n  mirror:    %v",
					table, c.migrationFile, wantCols, gotCols)
			}
		}
	}
}

func readMigrationFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(migrationsDir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}
