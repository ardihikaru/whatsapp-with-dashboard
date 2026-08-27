package db

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// schema_parity_test closes the "two hand-maintained copies of one schema"
// hazard from CLAUDE.md: migrations/*.sql (goose, MySQL, owns production) and
// EnsureSchema (SQLite, owns tests and the e2e run) are edited by hand, in
// separate files, and nothing before this test compared them. A migration
// added to only one side made every SQLite-backed test pass against a schema
// production does not have.

const migrationsDir = "../../migrations"

func readMigrations(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	var sb strings.Builder
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(migrationsDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		sb.Write(b)
		sb.WriteByte('\n')
	}
	return sb.String()
}

var createTableRE = regexp.MustCompile(`(?i)CREATE TABLE\s+(\w+)`)

// TestEveryMigrationTableExistsInTheSQLiteMirror parses every "CREATE TABLE
// <name>" out of migrations/*.sql and asserts the name also appears in
// db.go's schema slice — the table-level half of the two-copies hazard.
func TestEveryMigrationTableExistsInTheSQLiteMirror(t *testing.T) {
	all := readMigrations(t)
	matches := createTableRE.FindAllStringSubmatch(all, -1)
	if len(matches) == 0 {
		t.Fatal("no CREATE TABLE statements found in migrations — regex or fixture is broken")
	}
	seen := map[string]bool{}
	for _, m := range matches {
		seen[strings.ToLower(m[1])] = true
	}

	var tables []string
	for name := range seen {
		tables = append(tables, name)
	}
	sort.Strings(tables)

	for _, name := range tables {
		if !schemaHasTable(name) {
			t.Errorf("migration creates table %q but internal/db.schema has no matching "+
				"CREATE TABLE — add it to the schema slice in db.go", name)
		}
	}
}

func schemaHasTable(name string) bool {
	for _, stmt := range schema {
		lower := strings.ToLower(stmt)
		if strings.Contains(lower, "create table") && strings.Contains(lower, name) {
			return true
		}
	}
	return false
}

var addColumnRE = regexp.MustCompile(`(?i)ADD COLUMN\s+(\w+)`)

// TestEveryColumnAddedByAMigrationExistsInTheSQLiteMirror parses every
// "ADD COLUMN <name>" out of migrations/*.sql (i.e. every column added to a
// table that already existed before that migration) and asserts the column
// name is also wired into an ensureColumn(...) call in db.go. Table-level
// parity alone (the test above) would NOT have caught this: 0004 adds an
// existing table's columns via ALTER, which the CREATE TABLE regex never
// sees.
func TestEveryColumnAddedByAMigrationExistsInTheSQLiteMirror(t *testing.T) {
	all := readMigrations(t)
	matches := addColumnRE.FindAllStringSubmatch(all, -1)
	if len(matches) == 0 {
		t.Fatal("no ADD COLUMN statements found in migrations — regex or fixture is broken")
	}
	seen := map[string]bool{}
	for _, m := range matches {
		seen[strings.ToLower(m[1])] = true
	}

	var cols []string
	for c := range seen {
		cols = append(cols, c)
	}
	sort.Strings(cols)

	dbGoSource, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatalf("read db.go: %v", err)
	}
	src := string(dbGoSource)

	for _, col := range cols {
		// Every ensureColumn call names its column as a quoted Go string
		// literal ("media_kind", "token_expires_at", ...). A migration column
		// with no matching ensureColumn call has nothing quoting its name in
		// db.go at all.
		if !strings.Contains(src, `"`+col+`"`) {
			t.Errorf("migration adds column %q but no ensureColumn(...) call in db.go names it — "+
				"a SQLite dev/e2e database created before this migration would never gain it", col)
		}
	}
}
