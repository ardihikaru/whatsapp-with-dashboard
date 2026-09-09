package db

import (
	"context"
	"path/filepath"
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
