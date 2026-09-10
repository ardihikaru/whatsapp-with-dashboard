package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/convo"
)

func tempSQLiteDSN(t *testing.T) string {
	t.Helper()
	return db.SQLiteAppPrefix + filepath.Join(t.TempDir(), "app.db")
}

func countCorpusRows(t *testing.T, dsn string) (templates, links int) {
	t.Helper()
	pool, err := db.OpenAppStore(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open app store to count rows: %v", err)
	}
	defer pool.Close()
	if err := pool.QueryRow(
		`SELECT COUNT(*) FROM wa_convo_templates WHERE origin = ?`, convo.OriginCorpus,
	).Scan(&templates); err != nil {
		t.Fatalf("count templates: %v", err)
	}
	if err := pool.QueryRow(`SELECT COUNT(*) FROM wa_convo_template_links`).Scan(&links); err != nil {
		t.Fatalf("count links: %v", err)
	}
	return templates, links
}

func TestDescribeTargetNeverIncludesAMySQLCredential(t *testing.T) {
	desc, isSQLite, err := describeTarget("wa_user:s3cr3t-p4ssw0rd@tcp(10.10.0.4:5306)/wa_dashboard")
	if err != nil {
		t.Fatalf("describeTarget: %v", err)
	}
	if isSQLite {
		t.Fatal("describeTarget reported a MySQL DSN as sqlite")
	}
	if strings.Contains(desc, "s3cr3t-p4ssw0rd") {
		t.Fatalf("describeTarget leaked the password into %q", desc)
	}
	if !strings.Contains(desc, "10.10.0.4:5306") || !strings.Contains(desc, "wa_dashboard") {
		t.Fatalf("describeTarget = %q, want it to name the host and database", desc)
	}
}

func TestDescribeTargetReportsASQLiteDSNAsIs(t *testing.T) {
	desc, isSQLite, err := describeTarget("sqlite:/tmp/whatever/app.db")
	if err != nil {
		t.Fatalf("describeTarget: %v", err)
	}
	if !isSQLite {
		t.Fatal("describeTarget did not recognise a sqlite: DSN")
	}
	if desc != "sqlite:/tmp/whatever/app.db" {
		t.Fatalf("describeTarget = %q, want the DSN verbatim", desc)
	}
}

// TestRunRefusesANonSQLiteDSNWithoutTheProductionFlag is the whole safety
// story for this command: without -i-know-this-is-production, run must
// refuse before ever calling db.OpenAppStore against what — in this repo —
// is always the shared production MySQL.
func TestRunRefusesANonSQLiteDSNWithoutTheProductionFlag(t *testing.T) {
	var out bytes.Buffer
	err := run([]string{"-dsn", "wa_user:s3cr3t@tcp(10.10.0.4:5306)/wa_dashboard", "-dry-run"}, &out)
	if err == nil {
		t.Fatal("run accepted a MySQL DSN without -i-know-this-is-production")
	}
	if !errors.Is(err, ErrProductionConfirmationRequired) {
		t.Fatalf("run error = %v, want ErrProductionConfirmationRequired", err)
	}
	if strings.Contains(err.Error(), "s3cr3t") || strings.Contains(out.String(), "s3cr3t") {
		t.Fatal("the refusal leaked the DSN's password")
	}
}

// TestDryRunWritesNothingToATempSQLiteStore is the acceptance test named in
// the task: a -dry-run must never insert a row, even though a schema
// exists to insert into (created lazily by db.OpenAppStore the first time
// something DOES open it — countCorpusRows below is that first open).
func TestDryRunWritesNothingToATempSQLiteStore(t *testing.T) {
	dsn := tempSQLiteDSN(t)
	var out bytes.Buffer
	if err := run([]string{"-dsn", dsn, "-dry-run"}, &out); err != nil {
		t.Fatalf("run(-dry-run): %v", err)
	}
	if !strings.Contains(out.String(), "dry-run: wrote nothing") {
		t.Fatalf("dry-run output = %q, want it to say it wrote nothing", out.String())
	}
	templates, links := countCorpusRows(t, dsn)
	if templates != 0 || links != 0 {
		t.Fatalf("dry-run left %d templates and %d links behind, want 0 and 0", templates, links)
	}
}

// TestARealRunInsertsTemplatesAndLinks is this command's whole reason to
// exist: a non-dry-run call against a fresh SQLite store must leave real
// rows behind, in counts consistent with what corpus.Generate produces.
func TestARealRunInsertsTemplatesAndLinks(t *testing.T) {
	dsn := tempSQLiteDSN(t)
	var out bytes.Buffer
	if err := run([]string{"-dsn", dsn}, &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	templates, links := countCorpusRows(t, dsn)
	if templates < 2000 {
		t.Fatalf("inserted %d corpus templates, want at least 2000", templates)
	}
	if links == 0 {
		t.Fatal("inserted 0 links, want at least one edge per topic's arc")
	}
	if !strings.Contains(out.String(), "inserted") {
		t.Fatalf("run output = %q, want a summary line", out.String())
	}
}

// TestReplaceIsIdempotentAcrossTwoRuns is the other acceptance test named
// in the task: running convoseed -replace twice must leave the SAME counts
// behind, not double them — -replace exists precisely so re-seeding a
// deploy is safe to repeat.
func TestReplaceIsIdempotentAcrossTwoRuns(t *testing.T) {
	dsn := tempSQLiteDSN(t)

	var out1 bytes.Buffer
	if err := run([]string{"-dsn", dsn, "-replace"}, &out1); err != nil {
		t.Fatalf("run #1: %v", err)
	}
	templates1, links1 := countCorpusRows(t, dsn)

	var out2 bytes.Buffer
	if err := run([]string{"-dsn", dsn, "-replace"}, &out2); err != nil {
		t.Fatalf("run #2: %v", err)
	}
	templates2, links2 := countCorpusRows(t, dsn)

	if templates1 != templates2 {
		t.Fatalf("template count changed across two -replace runs: %d then %d", templates1, templates2)
	}
	if links1 != links2 {
		t.Fatalf("link count changed across two -replace runs: %d then %d", links1, links2)
	}
	if templates1 == 0 {
		t.Fatal("both runs inserted 0 templates — the test would pass vacuously")
	}
}

// TestWithoutReplaceARunDoublesTheCorpusOnASecondCall documents the
// contrast with the test above: -replace is what makes re-running safe,
// and this pins that the unguarded behaviour it replaces really would
// double the rows, so TestReplaceIsIdempotentAcrossTwoRuns is not passing
// by accident (e.g. because InsertTemplate silently no-ops on a duplicate
// name).
func TestWithoutReplaceARunDoublesTheCorpusOnASecondCall(t *testing.T) {
	dsn := tempSQLiteDSN(t)

	var out bytes.Buffer
	if err := run([]string{"-dsn", dsn, "-limit", "50"}, &out); err != nil {
		t.Fatalf("run #1: %v", err)
	}
	templates1, _ := countCorpusRows(t, dsn)

	out.Reset()
	if err := run([]string{"-dsn", dsn, "-limit", "50"}, &out); err != nil {
		t.Fatalf("run #2: %v", err)
	}
	templates2, _ := countCorpusRows(t, dsn)

	if templates2 != templates1*2 {
		t.Fatalf("two runs without -replace left %d then %d templates, want the second to double the first",
			templates1, templates2)
	}
}

func TestLimitCapsTheNumberOfTemplatesInsertedAndDropsDanglingLinks(t *testing.T) {
	dsn := tempSQLiteDSN(t)
	var out bytes.Buffer
	// 50 keeps the first topic's 48 templates (16 per scene x 3 scenes —
	// see generate.go) whole, PLUS 2 of the next topic's opener-scene
	// templates — whose links point at that next topic's middle scene,
	// which falls OUTSIDE this cut. That is deliberate: it is what makes
	// this test able to fail (confirmed manually: without the dangling-
	// link filter in cmd/convoseed/main.go, this exact -limit value trips
	// a FOREIGN KEY constraint failure inserting one of those two
	// templates' links) rather than passing vacuously the way a
	// topic-aligned limit (48) would.
	if err := run([]string{"-dsn", dsn, "-limit", "50"}, &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	templates, links := countCorpusRows(t, dsn)
	if templates != 50 {
		t.Fatalf("inserted %d templates with -limit 50, want exactly 50", templates)
	}
	if links == 0 {
		t.Fatal("inserted 0 links even though a whole topic's templates were kept")
	}

	// Every link's to_template_id must resolve to one of the inserted
	// templates — a link surviving the -limit cut with a dangling target
	// would either fail the FK insert outright or (if FKs were somehow
	// off) silently orphan an edge. Assert the stronger, positive claim:
	// every link row's endpoints both exist.
	//
	// The SQLite app store is pinned to a single connection (see
	// internal/db.Open's MaxOpenConns(1), required for whatsmeow's
	// sqlstore), so every query below must fully drain and close its
	// *sql.Rows before opening another — an existence check issued while
	// the link rows are still being scanned would deadlock waiting for a
	// connection this same test is holding.
	pool, err := db.OpenAppStore(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open app store: %v", err)
	}
	defer pool.Close()

	type edge struct{ from, to string }
	var edges []edge
	rows, err := pool.Query(`SELECT from_template_id, to_template_id FROM wa_convo_template_links`)
	if err != nil {
		t.Fatalf("query links: %v", err)
	}
	for rows.Next() {
		var e edge
		if err := rows.Scan(&e.from, &e.to); err != nil {
			rows.Close()
			t.Fatalf("scan link row: %v", err)
		}
		edges = append(edges, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate links: %v", err)
	}
	rows.Close()

	ids := map[string]bool{}
	idRows, err := pool.Query(`SELECT id FROM wa_convo_templates`)
	if err != nil {
		t.Fatalf("query template ids: %v", err)
	}
	for idRows.Next() {
		var id string
		if err := idRows.Scan(&id); err != nil {
			idRows.Close()
			t.Fatalf("scan template id: %v", err)
		}
		ids[id] = true
	}
	if err := idRows.Err(); err != nil {
		t.Fatalf("iterate template ids: %v", err)
	}
	idRows.Close()

	for _, e := range edges {
		if !ids[e.from] || !ids[e.to] {
			t.Fatalf("link %s -> %s references a template outside the -limit 50 set", e.from, e.to)
		}
	}
	t.Logf("checked %d links, all resolving within the limited set", len(edges))
}

// TestDSNIsRequired guards the ordinary "forgot to set WSM_APP_DSN" mistake
// with a clear error rather than a nil-pointer panic further down.
func TestDSNIsRequired(t *testing.T) {
	t.Setenv("WSM_APP_DSN", "")
	var out bytes.Buffer
	if err := run([]string{"-dry-run"}, &out); err == nil {
		t.Fatal("run with no -dsn and no WSM_APP_DSN should fail")
	}
}
