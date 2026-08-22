package db

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// appTables are the four tables the app owns. whatsmeow's crypto tables are not
// in this list: they belong to sqlstore.Container.Upgrade, not EnsureSchema.
var appTables = []string{"api_access_tokens", "api_clients", "wa_messages", "wa_sessions"}

func TestSQLiteAppStoreCreatesEveryAppTable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "app.db")

	pool, err := OpenAppStore(ctx, SQLiteAppPrefix+path)
	if err != nil {
		t.Fatalf("OpenAppStore: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	rows, err := pool.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	defer rows.Close()

	got := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	for _, want := range appTables {
		if !got[want] {
			var have []string
			for n := range got {
				have = append(have, n)
			}
			sort.Strings(have)
			t.Errorf("table %q missing; got %v", want, have)
		}
	}
}

// A second OpenAppStore on the same file must not fail: EnsureSchema is the
// idempotent path, and the e2e harness reuses its database across runs.
func TestSQLiteAppStoreIsIdempotentOnASecondOpen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "app.db")

	first, err := OpenAppStore(ctx, SQLiteAppPrefix+path)
	if err != nil {
		t.Fatalf("first OpenAppStore: %v", err)
	}
	first.Close()

	second, err := OpenAppStore(ctx, SQLiteAppPrefix+path)
	if err != nil {
		t.Fatalf("second OpenAppStore: %v", err)
	}
	t.Cleanup(func() { second.Close() })

	// The 0003 column must survive the re-open, since ensureColumn is the only
	// thing that adds it to a SQLite database created before it existed.
	var n int
	if err := second.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('api_clients') WHERE name='token_expires_at'`,
	).Scan(&n); err != nil {
		t.Fatalf("pragma_table_info: %v", err)
	}
	if n != 1 {
		t.Errorf("api_clients.token_expires_at present = %d, want 1", n)
	}
}

// The app store must round-trip a write, not merely own a schema.
func TestSQLiteAppStoreAcceptsAWrite(t *testing.T) {
	ctx := context.Background()
	pool, err := OpenAppStore(ctx, SQLiteAppPrefix+filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("OpenAppStore: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	if _, err := pool.ExecContext(ctx,
		`INSERT INTO wa_sessions (id, name, phone, created_at, updated_at)
		 VALUES ('s1', 'probe', '62800', '2026-08-22T00:00:00Z', '2026-08-22T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var name string
	if err := pool.QueryRowContext(ctx,
		`SELECT name FROM wa_sessions WHERE id='s1'`).Scan(&name); err != nil {
		t.Fatalf("select: %v", err)
	}
	if name != "probe" {
		t.Errorf("name = %q, want %q", name, "probe")
	}
}

func TestAppStoreRejectsAPrefixWithNoPath(t *testing.T) {
	if _, err := OpenAppStore(context.Background(), SQLiteAppPrefix); err == nil {
		t.Fatal("OpenAppStore(\"sqlite:\") returned no error, want one")
	}
}

// Anything without the sqlite: prefix is a MySQL DSN and must fail closed when
// the server is unreachable, never silently fall back to a local file.
func TestNonSQLiteDSNTakesTheMySQLBranchAndFailsClosed(t *testing.T) {
	// Port 1 is reserved and never listening; OpenApp pings with a 5s timeout.
	_, err := OpenAppStore(context.Background(),
		"u:p@tcp(127.0.0.1:1)/db?parseTime=true")
	if err == nil {
		t.Fatal("unreachable MySQL DSN returned no error, want one")
	}
	if got := err.Error(); !strings.Contains(got, "mysql") {
		t.Errorf("error %q does not mention mysql — wrong branch taken", got)
	}
}
