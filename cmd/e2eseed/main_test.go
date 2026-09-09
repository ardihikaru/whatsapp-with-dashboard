package main

import (
	"context"
	"errors"
	"math/rand/v2"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/convo"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/wasession"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/selfconvo"
)

// The guard is the only thing standing between this seeder and the SHARED
// production MySQL. If it ever stops rejecting a MySQL DSN, a test run writes
// junk into live tables belonging to other products.
func TestSeederRefusesEveryNonSQLiteDSN(t *testing.T) {
	for _, dsn := range []string{
		"",
		"user:pass@tcp(10.10.0.2:5306)/wa_dashboard?parseTime=true",
		"root:hunter2@tcp(127.0.0.1:3306)/wa_dashboard",
		"mysql://user:pass@host/db",
		"postgres://user:pass@host/db",
		"sqlite-ish:/tmp/x.db", // prefix must match exactly
		" sqlite:/tmp/x.db",    // no leading-space tolerance
		"SQLITE:/tmp/x.db",     // case-sensitive on purpose
	} {
		t.Run(dsn, func(t *testing.T) {
			err := checkDSN(dsn)
			if err == nil {
				t.Fatalf("checkDSN(%q) allowed a non-SQLite DSN", dsn)
			}
			if dsn != "" && !errors.Is(err, ErrNotSQLite) {
				t.Errorf("checkDSN(%q) = %v, want ErrNotSQLite", dsn, err)
			}
		})
	}
}

// A rejected MySQL DSN must not carry its password into a log line.
func TestRejectionDoesNotEchoTheDSNPassword(t *testing.T) {
	err := checkDSN("wa_user:s3cr3t-p4ssw0rd@tcp(10.10.0.2:5306)/wa_dashboard")
	if err == nil {
		t.Fatal("expected a rejection")
	}
	if strings.Contains(err.Error(), "s3cr3t-p4ssw0rd") {
		t.Errorf("the rejection leaked the password: %v", err)
	}
	if !strings.Contains(err.Error(), "10.10.0.2:5306") {
		t.Errorf("the rejection should still name the host it refused: %v", err)
	}
}

func TestSeederAcceptsASQLitePath(t *testing.T) {
	if err := checkDSN("sqlite:/tmp/e2e/app.db"); err != nil {
		t.Errorf("checkDSN rejected a valid SQLite DSN: %v", err)
	}
}

// "sqlite:" with nothing after it would open a file literally named "", which
// fails later and further from the cause.
func TestSeederRejectsAPrefixWithNoPath(t *testing.T) {
	if err := checkDSN("sqlite:"); err == nil {
		t.Fatal("checkDSN(\"sqlite:\") allowed an empty path")
	}
}

// The Self Convo pages (/self-convo, /self-convo/templates) need real
// content the same way every other table does — see this file's own header
// comment ("an e2e run against empty tables proves very little"). This test
// pins that seedSelfConvo writes one template built from the embedded
// example script plus three runs whose states exercise every badge colour
// and both terminal-progress shapes the convo_run_rows partial renders:
// a finished "done" run, a partially-sent "failed" run with a named error,
// and a "scheduled" run far enough in the future that its Cancel button
// still renders.
func TestSeedWritesATemplateAndThreeRunsWithTheStatesTheRowsPartialRenders(t *testing.T) {
	pool, err := db.Open(filepath.Join(t.TempDir(), "t.db"), db.SQLiteDialect)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer pool.Close()
	ctx := context.Background()
	if err := db.EnsureSchema(ctx, pool); err != nil {
		t.Fatalf("db.EnsureSchema: %v", err)
	}

	base := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	if err := seedSessions(ctx, wasession.NewRepository(pool), base); err != nil {
		t.Fatalf("seedSessions: %v", err)
	}

	if err := seedSelfConvo(ctx, pool, base); err != nil {
		t.Fatalf("seedSelfConvo: %v", err)
	}

	repo := convo.NewRepository(pool)

	templates, err := repo.ListTemplateSummaries(ctx, 50)
	if err != nil {
		t.Fatalf("ListTemplateSummaries: %v", err)
	}
	if len(templates) != 1 {
		t.Fatalf("ListTemplateSummaries returned %d templates, want 1", len(templates))
	}
	if templates[0].Name == "" {
		t.Errorf("seeded template has no name")
	}
	if templates[0].TurnCount < 2 {
		t.Errorf("seeded template TurnCount = %d, want >= 2", templates[0].TurnCount)
	}

	runs, err := repo.ListRuns(ctx, 10)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("ListRuns returned %d runs, want 3", len(runs))
	}

	byState := map[string]convo.Run{}
	for _, r := range runs {
		byState[r.State] = r
	}

	done, ok := byState[convo.RunDone]
	if !ok {
		t.Fatal("no run in state done")
	}
	if done.TurnsDone != done.TurnCount {
		t.Errorf("done run TurnsDone = %d, TurnCount = %d, want equal", done.TurnsDone, done.TurnCount)
	}
	if done.FinishedAt == nil {
		t.Error("done run has no FinishedAt")
	}
	doneTurns, err := repo.Turns(ctx, done.ID)
	if err != nil {
		t.Fatalf("Turns(done): %v", err)
	}
	for _, tn := range doneTurns {
		if tn.State != convo.TurnSent {
			t.Errorf("done run turn %d state = %q, want %q", tn.Seq, tn.State, convo.TurnSent)
		}
	}

	// seedSelfConvo must build its turns through selfconvo.Expand (over
	// selfconvo.Pair{A: sessA, B: sessB} and the SAME fixed-seed rng it
	// feeds Vary first), not its own hand-rolled speaker→(session,
	// recipient) mapping — see this file's own header comment ("an e2e run
	// against empty tables proves very little") and
	// docs/brief-self-convo.md's review follow-ups ("the e2e seeder
	// re-implements Expand"). Recomputing the same Validate→Vary→Expand
	// pipeline here, over the same pair and the same rng seed the real
	// seeder uses, and comparing every turn's SessionID/ToPhone catches a
	// hand-rolled mapping that drifts from Expand's own.
	wantScript, err := selfconvo.Validate(selfconvo.ExampleJSON)
	if err != nil {
		t.Fatalf("Validate(ExampleJSON): %v", err)
	}
	rng := rand.New(rand.NewPCG(7, 7))
	wantVaried, err := selfconvo.Vary(wantScript, rng)
	if err != nil {
		t.Fatalf("Vary: %v", err)
	}
	pair := selfconvo.Pair{
		A: selfconvo.Participant{ID: seedSessionDefs[0].ID, Name: seedSessionDefs[0].Name, Phone: seedSessionDefs[0].Phone, Connected: true},
		B: selfconvo.Participant{ID: seedSessionDefs[1].ID, Name: seedSessionDefs[1].Name, Phone: seedSessionDefs[1].Phone, Connected: true},
	}
	wantTurns := selfconvo.ToTurns("want", selfconvo.Expand(wantVaried, pair, rng))
	if len(wantTurns) != len(doneTurns) {
		t.Fatalf("selfconvo.Expand produced %d turns, seeded done run has %d", len(wantTurns), len(doneTurns))
	}
	for i, tn := range doneTurns {
		want := wantTurns[i]
		if tn.SessionID != want.SessionID || tn.ToPhone != want.ToPhone {
			t.Errorf("done run turn %d SessionID/ToPhone = %q/%q, want %q/%q (selfconvo.Expand's own mapping for this pair)",
				tn.Seq, tn.SessionID, tn.ToPhone, want.SessionID, want.ToPhone)
		}
	}
	// Expand always forces the FIRST turn's delay to 0 (nothing to wait for
	// before start_at, expand.go's own doc comment) — a hand-rolled mapping
	// that instead gives every turn the same fixed pacing gap would miss
	// this.
	if doneTurns[0].DelaySeconds != 0 {
		t.Errorf("done run turn 1 delay_seconds = %d, want 0 (selfconvo.Expand forces the first turn's delay to 0)",
			doneTurns[0].DelaySeconds)
	}

	failed, ok := byState[convo.RunFailed]
	if !ok {
		t.Fatal("no run in state failed")
	}
	if failed.TurnsDone != 3 {
		t.Errorf("failed run TurnsDone = %d, want 3", failed.TurnsDone)
	}
	if failed.Error == "" {
		t.Error("failed run has no Error")
	}
	if failed.NextDueAt != nil {
		t.Errorf("failed run NextDueAt = %v, want nil", failed.NextDueAt)
	}
	failedTurns, err := repo.Turns(ctx, failed.ID)
	if err != nil {
		t.Fatalf("Turns(failed): %v", err)
	}
	for _, tn := range failedTurns {
		switch {
		case tn.Seq <= 3:
			if tn.State != convo.TurnSent {
				t.Errorf("failed run turn %d state = %q, want %q", tn.Seq, tn.State, convo.TurnSent)
			}
		case tn.Seq == 4:
			if tn.State != convo.TurnFailed {
				t.Errorf("failed run turn %d state = %q, want %q", tn.Seq, tn.State, convo.TurnFailed)
			}
			if len(tn.Error) < 40 {
				t.Errorf("failed run turn 4 error is only %d chars, want a real named reason", len(tn.Error))
			}
		default:
			if tn.State != convo.TurnSkipped {
				t.Errorf("failed run turn %d state = %q, want %q", tn.Seq, tn.State, convo.TurnSkipped)
			}
		}
	}

	scheduled, ok := byState[convo.RunScheduled]
	if !ok {
		t.Fatal("no run in state scheduled")
	}
	if scheduled.NextDueAt == nil {
		t.Fatal("scheduled run has no NextDueAt")
	}
	if !scheduled.NextDueAt.After(base.Add(300 * 24 * time.Hour)) {
		t.Errorf("scheduled run NextDueAt = %v, want at least ~a year past base %v",
			scheduled.NextDueAt, base)
	}
}

// seedSelfConvo uses fixed ids (the same convention as seedMessages'
// "e2e-msg-%04d") and is therefore only safe to run once per database, same
// as every other seed* function in this file — running run()'s "already
// seeded" guard around it, not seedSelfConvo re-checking on its own, is what
// keeps the overall seeder idempotent. This pins that a second direct call
// fails loudly (a primary-key collision) rather than silently duplicating
// rows, so a future refactor that moved the call outside the guard would be
// caught by run()'s own behaviour, not masked by seedSelfConvo quietly
// upserting.
func TestSeedSelfConvoIsNotIndependentlyIdempotentSoItMustStayInsideTheExistingSessionsGuard(t *testing.T) {
	pool, err := db.Open(filepath.Join(t.TempDir(), "t.db"), db.SQLiteDialect)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer pool.Close()
	ctx := context.Background()
	if err := db.EnsureSchema(ctx, pool); err != nil {
		t.Fatalf("db.EnsureSchema: %v", err)
	}
	base := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	if err := seedSessions(ctx, wasession.NewRepository(pool), base); err != nil {
		t.Fatalf("seedSessions: %v", err)
	}
	if err := seedSelfConvo(ctx, pool, base); err != nil {
		t.Fatalf("first seedSelfConvo call: %v", err)
	}
	if err := seedSelfConvo(ctx, pool, base); err == nil {
		t.Fatal("a second seedSelfConvo call over the same database succeeded; " +
			"it must collide on its fixed ids so run()'s existing-sessions guard is the only thing keeping this idempotent")
	}
}
