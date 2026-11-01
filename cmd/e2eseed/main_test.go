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
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/contact"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/convo"
	statusschedule "github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/statusschedule"
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

// The Convo Scheduling pages (/self-convo/schedules,
// /self-convo/templates' topic filter, a template's own outgoing-links
// table) need real content the same way self-convo's runs do — see this
// file's own header comment. This pins seedConvoScheduling's three
// promises: a full corpus pack lands with real outgoing links, an
// "active" schedule carries several receivers and no end date, and a
// "stalled" schedule carries a reason.
func TestSeedConvoSchedulingWritesACorpusPackAndTwoSchedulesWithTheStatesThePagesRender(t *testing.T) {
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
	// seedSelfConvo's own upload-origin template (no topic) is what gives
	// the unfiltered-vs-filtered assertion below something to narrow FROM
	// — the same two calls run() itself makes, in the same order.
	if err := seedSelfConvo(ctx, pool, base); err != nil {
		t.Fatalf("seedSelfConvo: %v", err)
	}

	nTemplates, nSchedules, err := seedConvoScheduling(ctx, pool, base)
	if err != nil {
		t.Fatalf("seedConvoScheduling: %v", err)
	}
	if nTemplates == 0 {
		t.Fatal("seedConvoScheduling reported 0 templates")
	}
	if nSchedules != 2 {
		t.Fatalf("seedConvoScheduling reported %d schedules, want 2", nSchedules)
	}

	repo := convo.NewRepository(pool)

	// A real corpus topic, filterable, with at least one template that
	// carries an outgoing link — otherwise "a template view shows its
	// outgoing chain links" has nothing to assert against.
	byTopic, err := repo.ListTemplateSummariesByTopic(ctx, e2eConvoTopic, 500)
	if err != nil {
		t.Fatalf("ListTemplateSummariesByTopic: %v", err)
	}
	if len(byTopic) != nTemplates {
		t.Fatalf("ListTemplateSummariesByTopic(%q) returned %d rows, want %d (seedConvoScheduling's own count)",
			e2eConvoTopic, len(byTopic), nTemplates)
	}
	linked := false
	for _, tmpl := range byTopic {
		links, err := repo.TemplateLinks(ctx, tmpl.ID)
		if err != nil {
			t.Fatalf("TemplateLinks(%s): %v", tmpl.ID, err)
		}
		if len(links) > 0 {
			linked = true
			break
		}
	}
	if !linked {
		t.Fatalf("none of the %d seeded %q templates carries an outgoing link", nTemplates, e2eConvoTopic)
	}

	// The unfiltered list must include something OUTSIDE this topic too
	// (seedSelfConvo's own upload-origin template has no topic at all), or
	// the topic filter test has nothing to narrow FROM.
	all, err := repo.ListTemplateSummaries(ctx, 500)
	if err != nil {
		t.Fatalf("ListTemplateSummaries: %v", err)
	}
	if len(all) <= len(byTopic) {
		t.Fatalf("unfiltered template list (%d) is not larger than the %q-filtered list (%d); "+
			"the topic filter would not visibly narrow anything", len(all), e2eConvoTopic, len(byTopic))
	}

	active, err := repo.GetSchedule(ctx, "e2e-convo-sched-active")
	if err != nil {
		t.Fatalf("GetSchedule(active): %v", err)
	}
	if active.State != convo.ScheduleActive {
		t.Errorf("active schedule state = %q, want %q", active.State, convo.ScheduleActive)
	}
	if active.EndDate != "" {
		t.Errorf("active schedule EndDate = %q, want empty (the no-end-date fixture)", active.EndDate)
	}
	if active.Recurrence != convo.RecurrenceWeekly {
		t.Errorf("active schedule Recurrence = %q, want %q", active.Recurrence, convo.RecurrenceWeekly)
	}
	activeReceivers, err := repo.ScheduleReceivers(ctx, active.ID)
	if err != nil {
		t.Fatalf("ScheduleReceivers(active): %v", err)
	}
	if len(activeReceivers) < 2 {
		t.Errorf("active schedule has %d receivers, want at least 2 (\"several\")", len(activeReceivers))
	}

	stalled, err := repo.GetSchedule(ctx, "e2e-convo-sched-stalled")
	if err != nil {
		t.Fatalf("GetSchedule(stalled): %v", err)
	}
	if stalled.State != convo.ScheduleStalled {
		t.Errorf("stalled schedule state = %q, want %q", stalled.State, convo.ScheduleStalled)
	}
	if stalled.PausedReason == "" {
		t.Error("stalled schedule has no PausedReason — schedule_view.html/convo_schedule_rows.html have nothing to render")
	}
}

// TestSeedWritesContactsAndTwoStatusSchedulesTheBrowserSuiteAddresses pins
// what e2e/status-schedules.spec.ts relies on: the contact book the form
// lists, and one active + one paused (with a recorded failure) schedule under
// fixed ids.
func TestSeedWritesContactsAndTwoStatusSchedulesTheBrowserSuiteAddresses(t *testing.T) {
	pool, err := db.Open(filepath.Join(t.TempDir(), "t.db"), db.SQLiteDialect)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()
	if err := db.EnsureSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	nc, ns, err := seedStatusSchedules(ctx, pool, time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("seedStatusSchedules: %v", err)
	}
	if cs, _ := contact.NewRepository(pool).List(ctx); len(cs) != nc || nc < 5 {
		t.Errorf("contacts = %d (reported %d), want at least 5 so the checklist scrolls", len(cs), nc)
	}
	repo := statusschedule.NewRepository(pool)
	active, err := repo.Get(ctx, "e2e-status-sched-active")
	if err != nil || active.State != statusschedule.StateActive || active.RecipientsMode != statusschedule.ModeAll {
		t.Errorf("active schedule = %+v, err %v", active, err)
	}
	paused, err := repo.Get(ctx, "e2e-status-sched-paused")
	if err != nil || paused.State != statusschedule.StatePaused || paused.LastResult != statusschedule.ResultFailed || paused.LastError == "" {
		t.Errorf("paused schedule = %+v, err %v", paused, err)
	}
	if ns != 2 {
		t.Errorf("schedules reported = %d, want 2", ns)
	}
}
