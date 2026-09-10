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

// TestConvoTablesAcceptAnInsertAfterEnsureSchema asserts the three Self Convo
// tables added by migrations/0007_self_convo.sql exist in the SQLite mirror
// (internal/db.schema) with a shape that accepts a real row — table-name
// parity alone (TestEveryMigrationTableExistsInTheSQLiteMirror) does not
// prove the columns line up.
func TestConvoTablesAcceptAnInsertAfterEnsureSchema(t *testing.T) {
	ctx := context.Background()
	pool, err := OpenAppStore(ctx, SQLiteAppPrefix+filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("OpenAppStore: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	if _, err := pool.ExecContext(ctx,
		`INSERT INTO wa_convo_templates (id, name, turn_count, body, created_at, updated_at)
		 VALUES ('t1', 'Weekend plans', 4, '{"version":1}', '2026-09-09T00:00:00Z', '2026-09-09T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert wa_convo_templates: %v", err)
	}

	if _, err := pool.ExecContext(ctx,
		`INSERT INTO wa_convo_runs
		   (id, template_id, template_name, session_a_id, session_b_id,
		    session_a_name, session_b_name, phone_a, phone_b, state,
		    turn_count, turns_done, start_at, next_due_at, created_at)
		 VALUES
		   ('r1', 't1', 'Weekend plans', 'sess-a', 'sess-b',
		    'Session A', 'Session B', '62800000001', '62800000002', 'scheduled',
		    4, 0, '2026-09-09T00:00:00Z', '2026-09-09T00:00:00Z', '2026-09-09T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert wa_convo_runs: %v", err)
	}

	if _, err := pool.ExecContext(ctx,
		`INSERT INTO wa_convo_turns
		   (run_id, seq, speaker, session_id, to_phone, body, delay_seconds, state)
		 VALUES ('r1', 1, 'a', 'sess-a', '62800000002', 'Halo, sibuk gak Sabtu?', 0, 'pending')`,
	); err != nil {
		t.Fatalf("insert wa_convo_turns: %v", err)
	}

	var name string
	if err := pool.QueryRowContext(ctx,
		`SELECT name FROM wa_convo_templates WHERE id='t1'`).Scan(&name); err != nil {
		t.Fatalf("select wa_convo_templates: %v", err)
	}
	if name != "Weekend plans" {
		t.Errorf("name = %q, want %q", name, "Weekend plans")
	}

	var state string
	if err := pool.QueryRowContext(ctx,
		`SELECT state FROM wa_convo_runs WHERE id='r1'`).Scan(&state); err != nil {
		t.Fatalf("select wa_convo_runs: %v", err)
	}
	if state != "scheduled" {
		t.Errorf("state = %q, want %q", state, "scheduled")
	}

	var body string
	if err := pool.QueryRowContext(ctx,
		`SELECT body FROM wa_convo_turns WHERE run_id='r1' AND seq=1`).Scan(&body); err != nil {
		t.Fatalf("select wa_convo_turns: %v", err)
	}
	if body != "Halo, sibuk gak Sabtu?" {
		t.Errorf("body = %q, want the turn text", body)
	}
}

// TestDeletingAConvoRunCascadesToItsTurns asserts SQLite actually enforces
// the fk_wa_convo_turns_run ON DELETE CASCADE declared in the schema.
// db.Open sets `_pragma=foreign_keys(1)` on every connection it opens (see
// Open's DSN), so — unlike a bare modernc.org/sqlite connection, which
// leaves FK enforcement off by default — this is expected to actually
// enforce the cascade here, and this test is the evidence for that claim
// rather than an assumption.
func TestDeletingAConvoRunCascadesToItsTurns(t *testing.T) {
	ctx := context.Background()
	pool, err := OpenAppStore(ctx, SQLiteAppPrefix+filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("OpenAppStore: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	if _, err := pool.ExecContext(ctx,
		`INSERT INTO wa_convo_runs
		   (id, template_name, session_a_id, session_b_id, phone_a, phone_b,
		    state, turn_count, start_at, created_at)
		 VALUES ('r1', 'Weekend plans', 'sess-a', 'sess-b', '62800000001', '62800000002',
		    'done', 2, '2026-09-09T00:00:00Z', '2026-09-09T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert wa_convo_runs: %v", err)
	}
	for _, seq := range []int{1, 2} {
		if _, err := pool.ExecContext(ctx,
			`INSERT INTO wa_convo_turns
			   (run_id, seq, speaker, session_id, to_phone, body, delay_seconds, state)
			 VALUES ('r1', ?, 'a', 'sess-a', '62800000002', 'turn', 0, 'sent')`, seq,
		); err != nil {
			t.Fatalf("insert wa_convo_turns seq=%d: %v", seq, err)
		}
	}

	if _, err := pool.ExecContext(ctx, `DELETE FROM wa_convo_runs WHERE id='r1'`); err != nil {
		t.Fatalf("delete wa_convo_runs: %v", err)
	}

	var remaining int
	if err := pool.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM wa_convo_turns WHERE run_id='r1'`).Scan(&remaining); err != nil {
		t.Fatalf("count wa_convo_turns: %v", err)
	}
	if remaining != 0 {
		t.Errorf("wa_convo_turns rows left after deleting their run = %d, want 0 (cascade did not fire)", remaining)
	}
}

// TestConvoSchedulingTablesAcceptAnInsertAfterEnsureSchema asserts the
// additive columns and two new tables from
// migrations/0008_convo_scheduling.sql exist in the SQLite mirror with a
// shape that accepts a real row — the same table-name-parity-is-not-enough
// reasoning as TestConvoTablesAcceptAnInsertAfterEnsureSchema above.
func TestConvoSchedulingTablesAcceptAnInsertAfterEnsureSchema(t *testing.T) {
	ctx := context.Background()
	pool, err := OpenAppStore(ctx, SQLiteAppPrefix+filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("OpenAppStore: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	if _, err := pool.ExecContext(ctx,
		`INSERT INTO wa_convo_templates (id, name, topic, stage, origin, turn_count, body, created_at, updated_at)
		 VALUES ('t1', 'Futsal opener', 'futsal', 'opener', 'upload', 2, '{"version":1}',
		         '2026-09-09T00:00:00Z', '2026-09-09T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert wa_convo_templates with topic/stage/origin: %v", err)
	}
	if _, err := pool.ExecContext(ctx,
		`INSERT INTO wa_convo_templates (id, name, topic, stage, origin, turn_count, body, created_at, updated_at)
		 VALUES ('t2', 'Futsal middle', 'futsal', 'middle', 'corpus', 2, '{"version":1}',
		         '2026-09-09T00:00:00Z', '2026-09-09T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert wa_convo_templates t2: %v", err)
	}

	if _, err := pool.ExecContext(ctx,
		`INSERT INTO wa_convo_runs
		   (id, template_id, template_name, schedule_id, occurrence_date, chain_prev_run_id,
		    chain_depth, topic, paraphrase_seed,
		    session_a_id, session_b_id, session_a_name, session_b_name, phone_a, phone_b, state,
		    turn_count, turns_done, start_at, created_at)
		 VALUES
		   ('r1', 't1', 'Futsal opener', 'sch1', '2026-09-09', NULL, 0, 'futsal', 42,
		    'sess-a', 'sess-b', 'Session A', 'Session B', '62800000001', '62800000002', 'scheduled',
		    2, 0, '2026-09-09T00:00:00Z', '2026-09-09T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert wa_convo_runs with scheduling columns: %v", err)
	}

	if _, err := pool.ExecContext(ctx,
		`INSERT INTO wa_convo_template_links
		   (from_template_id, to_template_id, weight, min_gap_seconds, max_gap_seconds, created_at)
		 VALUES ('t1', 't2', 3, 60, 300, '2026-09-09T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert wa_convo_template_links: %v", err)
	}

	if _, err := pool.ExecContext(ctx,
		`INSERT INTO wa_convo_schedules
		   (id, name, sender_session_id, sender_name, sender_phone, topic, root_template_id,
		    chain_enabled, recurrence, weekdays, interval_days, sets_per_day,
		    window_start, window_end, timezone, start_date, end_date, state,
		    paused_reason, paused_at, last_planned_date, created_at, updated_at)
		 VALUES
		   ('sch1', 'Weekend futsal', 'sess-a', 'Session A', '62800000001', 'futsal', 't1',
		    1, 'daily', '', 0, 1,
		    '08:00', '20:00', 'UTC', '2026-09-01', NULL, 'active',
		    '', NULL, NULL, '2026-09-09T00:00:00Z', '2026-09-09T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert wa_convo_schedules: %v", err)
	}

	if _, err := pool.ExecContext(ctx,
		`INSERT INTO wa_convo_schedule_receivers (schedule_id, session_id, session_name, phone, position)
		 VALUES ('sch1', 'sess-b', 'Session B', '62800000002', 0)`,
	); err != nil {
		t.Fatalf("insert wa_convo_schedule_receivers: %v", err)
	}

	var topic string
	if err := pool.QueryRowContext(ctx,
		`SELECT topic FROM wa_convo_templates WHERE id='t1'`).Scan(&topic); err != nil {
		t.Fatalf("select wa_convo_templates.topic: %v", err)
	}
	if topic != "futsal" {
		t.Errorf("topic = %q, want %q", topic, "futsal")
	}

	var scheduleID string
	if err := pool.QueryRowContext(ctx,
		`SELECT schedule_id FROM wa_convo_runs WHERE id='r1'`).Scan(&scheduleID); err != nil {
		t.Fatalf("select wa_convo_runs.schedule_id: %v", err)
	}
	if scheduleID != "sch1" {
		t.Errorf("schedule_id = %q, want %q", scheduleID, "sch1")
	}

	var weight int
	if err := pool.QueryRowContext(ctx,
		`SELECT weight FROM wa_convo_template_links WHERE from_template_id='t1' AND to_template_id='t2'`,
	).Scan(&weight); err != nil {
		t.Fatalf("select wa_convo_template_links.weight: %v", err)
	}
	if weight != 3 {
		t.Errorf("weight = %d, want 3", weight)
	}

	var state string
	if err := pool.QueryRowContext(ctx,
		`SELECT state FROM wa_convo_schedules WHERE id='sch1'`).Scan(&state); err != nil {
		t.Fatalf("select wa_convo_schedules.state: %v", err)
	}
	if state != "active" {
		t.Errorf("state = %q, want %q", state, "active")
	}

	var receiverPhone string
	if err := pool.QueryRowContext(ctx,
		`SELECT phone FROM wa_convo_schedule_receivers WHERE schedule_id='sch1' AND session_id='sess-b'`,
	).Scan(&receiverPhone); err != nil {
		t.Fatalf("select wa_convo_schedule_receivers.phone: %v", err)
	}
	if receiverPhone != "62800000002" {
		t.Errorf("phone = %q, want %q", receiverPhone, "62800000002")
	}
}

// TestDeletingAConvoScheduleCascadesToItsReceivers asserts SQLite actually
// enforces the fk_wa_convo_schedule_receivers_schedule ON DELETE CASCADE
// declared in migrations/0008_convo_scheduling.sql, the same evidence-not-
// assumption reasoning as TestDeletingAConvoRunCascadesToItsTurns above.
func TestDeletingAConvoScheduleCascadesToItsReceivers(t *testing.T) {
	ctx := context.Background()
	pool, err := OpenAppStore(ctx, SQLiteAppPrefix+filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("OpenAppStore: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	if _, err := pool.ExecContext(ctx,
		`INSERT INTO wa_convo_schedules
		   (id, name, sender_session_id, recurrence, sets_per_day, window_start, window_end,
		    start_date, state, created_at, updated_at)
		 VALUES ('sch1', 'Weekend futsal', 'sess-a', 'daily', 1, '08:00', '20:00',
		         '2026-09-01', 'active', '2026-09-09T00:00:00Z', '2026-09-09T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert wa_convo_schedules: %v", err)
	}
	if _, err := pool.ExecContext(ctx,
		`INSERT INTO wa_convo_schedule_receivers (schedule_id, session_id, session_name, phone, position)
		 VALUES ('sch1', 'sess-b', 'Session B', '62800000002', 0)`,
	); err != nil {
		t.Fatalf("insert wa_convo_schedule_receivers: %v", err)
	}

	if _, err := pool.ExecContext(ctx, `DELETE FROM wa_convo_schedules WHERE id='sch1'`); err != nil {
		t.Fatalf("delete wa_convo_schedules: %v", err)
	}

	var remaining int
	if err := pool.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM wa_convo_schedule_receivers WHERE schedule_id='sch1'`).Scan(&remaining); err != nil {
		t.Fatalf("count wa_convo_schedule_receivers: %v", err)
	}
	if remaining != 0 {
		t.Errorf("wa_convo_schedule_receivers rows left after deleting their schedule = %d, want 0 (cascade did not fire)", remaining)
	}
}

// TestDeletingAConvoTemplateCascadesToItsLinksInBothDirections asserts the
// two link foreign keys declared in migrations/0008_convo_scheduling.sql
// (fk_wa_convo_template_links_from and _to) both cascade, whichever
// endpoint of the link is deleted.
func TestDeletingAConvoTemplateCascadesToItsLinksInBothDirections(t *testing.T) {
	ctx := context.Background()
	pool, err := OpenAppStore(ctx, SQLiteAppPrefix+filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("OpenAppStore: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	for _, id := range []string{"t1", "t2", "t3"} {
		if _, err := pool.ExecContext(ctx,
			`INSERT INTO wa_convo_templates (id, name, turn_count, body, created_at, updated_at)
			 VALUES (?, 'T', 1, '{}', '2026-09-09T00:00:00Z', '2026-09-09T00:00:00Z')`, id,
		); err != nil {
			t.Fatalf("insert wa_convo_templates %s: %v", id, err)
		}
	}
	if _, err := pool.ExecContext(ctx,
		`INSERT INTO wa_convo_template_links
		   (from_template_id, to_template_id, weight, min_gap_seconds, max_gap_seconds, created_at)
		 VALUES ('t1', 't2', 1, 0, 0, '2026-09-09T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert link t1->t2: %v", err)
	}
	if _, err := pool.ExecContext(ctx,
		`INSERT INTO wa_convo_template_links
		   (from_template_id, to_template_id, weight, min_gap_seconds, max_gap_seconds, created_at)
		 VALUES ('t3', 't2', 1, 0, 0, '2026-09-09T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert link t3->t2: %v", err)
	}

	if _, err := pool.ExecContext(ctx, `DELETE FROM wa_convo_templates WHERE id='t1'`); err != nil {
		t.Fatalf("delete t1: %v", err)
	}
	var afterFromDelete int
	if err := pool.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM wa_convo_template_links WHERE from_template_id='t1'`).Scan(&afterFromDelete); err != nil {
		t.Fatalf("count after deleting from-side: %v", err)
	}
	if afterFromDelete != 0 {
		t.Errorf("links from t1 left after deleting t1 = %d, want 0 (from-side cascade did not fire)", afterFromDelete)
	}

	if _, err := pool.ExecContext(ctx, `DELETE FROM wa_convo_templates WHERE id='t2'`); err != nil {
		t.Fatalf("delete t2: %v", err)
	}
	var afterToDelete int
	if err := pool.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM wa_convo_template_links WHERE to_template_id='t2'`).Scan(&afterToDelete); err != nil {
		t.Fatalf("count after deleting to-side: %v", err)
	}
	if afterToDelete != 0 {
		t.Errorf("links to t2 left after deleting t2 = %d, want 0 (to-side cascade did not fire)", afterToDelete)
	}
}

// migrationTableColumns parses "CREATE TABLE <name> (...)" out of migration
// source text and returns the column names it declares — the same
// file-content-assertion precedent as schema_parity_test.go's
// createTableRE/addColumnRE, but parsing the FULL column list of one named
// table rather than just its name, so it can be compared against the
// mirror's actual column set rather than only that the table exists.
func migrationTableColumns(t *testing.T, sqlText, table string) []string {
	t.Helper()
	marker := "CREATE TABLE " + table + " ("
	start := strings.Index(sqlText, marker)
	if start == -1 {
		t.Fatalf("no %q in migration text", marker)
	}
	body := sqlText[start+len(marker):]
	depth := 1
	end := -1
	for i, r := range body {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				end = i
			}
		}
		if end != -1 {
			break
		}
	}
	if end == -1 {
		t.Fatalf("unbalanced parens parsing CREATE TABLE %s", table)
	}
	block := body[:end]

	var cols []string
	for _, rawLine := range splitTopLevel(block) {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		upper := strings.ToUpper(line)
		if strings.HasPrefix(upper, "PRIMARY KEY") ||
			strings.HasPrefix(upper, "KEY ") ||
			strings.HasPrefix(upper, "CONSTRAINT") ||
			strings.HasPrefix(upper, "UNIQUE") ||
			strings.HasPrefix(upper, "FOREIGN KEY") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		cols = append(cols, strings.ToLower(fields[0]))
	}
	sort.Strings(cols)
	return cols
}

// splitTopLevel splits s on commas that are not nested inside their own
// parens — e.g. "a VARCHAR(64), b INT" splits into ["a VARCHAR(64)", " b
// INT"] without breaking on the comma an argument list to VARCHAR(...)
// might one day contain.
func splitTopLevel(s string) []string {
	var out []string
	depth := 0
	last := 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[last:i])
				last = i + 1
			}
		}
	}
	out = append(out, s[last:])
	return out
}

// TestConvoSchedulingMigrationAndSQLiteMirrorAgreeOnColumnsForEachNewTable
// is the one schema_parity_test.go's table-name/column-name parity tests do
// NOT catch: those only prove a migration's table/added-column NAMES also
// appear somewhere in db.go, not that a brand-new table's FULL column list
// (order aside) matches between the two hand-maintained copies. This test
// parses migrations/0008_convo_scheduling.sql's CREATE TABLE text for each
// of the three genuinely new tables and diffs it against PRAGMA
// table_info(...) on a database EnsureSchema just built, catching e.g. a
// column present in one copy and silently missing from the other.
func TestConvoSchedulingMigrationAndSQLiteMirrorAgreeOnColumnsForEachNewTable(t *testing.T) {
	migrationSQL, err := os.ReadFile(filepath.Join(migrationsDir, "0008_convo_scheduling.sql"))
	if err != nil {
		t.Fatalf("read 0008_convo_scheduling.sql: %v", err)
	}

	ctx := context.Background()
	pool, err := OpenAppStore(ctx, SQLiteAppPrefix+filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("OpenAppStore: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	for _, table := range []string{
		"wa_convo_template_links",
		"wa_convo_schedules",
		"wa_convo_schedule_receivers",
	} {
		wantCols := migrationTableColumns(t, string(migrationSQL), table)
		if len(wantCols) == 0 {
			t.Fatalf("parsed zero columns for %s out of the migration — parser or fixture is broken", table)
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
			t.Errorf("%s columns disagree between the migration and the SQLite mirror:\n  migration: %v\n  mirror:    %v",
				table, wantCols, gotCols)
		}
	}
}
