// Command e2eseed fills a DEV/E2E SQLite app store with realistic content.
//
// # WHY THIS EXISTS
//
// The browser suite used to run against four empty tables, which quietly made
// its strongest claim meaningless: "no page scrolls horizontally" is trivially
// true when every table has zero rows. Phone layouts break on CONTENT — a long
// message body, a nine-column row, a client name that will not wrap, enough
// rows to render a pager. Seeding is what gives those assertions something to
// bite on.
//
// # SAFETY
//
// This program refuses to run against anything but a "sqlite:" DSN. The only
// MySQL a dev machine can reach is the SHARED production server, and a seeder
// pointed at it would write junk into live tables. The guard is not advisory:
// there is no flag to override it.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"strings"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/apiclient"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/convo"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/wasession"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/selfconvo"
)

// ErrNotSQLite is returned for any DSN that is not a local SQLite file.
var ErrNotSQLite = errors.New(
	"e2eseed refuses to run against anything but a sqlite: DSN — the only reachable " +
		"MySQL is the SHARED production server")

// checkDSN is the whole safety story, kept separate so it is directly testable.
func checkDSN(dsn string) error {
	if !strings.HasPrefix(dsn, db.SQLiteAppPrefix) {
		return fmt.Errorf("%w (got %q)", ErrNotSQLite, redact(dsn))
	}
	if strings.TrimPrefix(dsn, db.SQLiteAppPrefix) == "" {
		return fmt.Errorf("%s needs a file path", db.SQLiteAppPrefix)
	}
	return nil
}

// redact keeps a rejected MySQL DSN's password out of the error text.
func redact(dsn string) string {
	if i := strings.LastIndex(dsn, "@"); i >= 0 {
		return "***@" + dsn[i+1:]
	}
	return dsn
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("e2eseed: %v", err)
	}
}

func run() error {
	dsn := os.Getenv("WSM_APP_DSN")
	if err := checkDSN(dsn); err != nil {
		return err
	}

	ctx := context.Background()
	pool, err := db.OpenAppStore(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	sessions := wasession.NewRepository(pool)
	messages := message.NewRepository(pool)
	clients := apiclient.NewRepository(pool)

	// Idempotent: a second run must not double the rows, because the e2e
	// database persists between runs and duplicated data changes pager counts.
	existing, err := sessions.All(ctx)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		log.Printf("already seeded (%d sessions) — nothing to do", len(existing))
		return nil
	}

	// Fixed clock. Nothing here may depend on wall-clock time, or the /logs
	// date filters and the dashboard's "last 30 days" windows would drift and
	// the suite would fail on some days and not others.
	base := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)

	if err := seedSessions(ctx, sessions, base); err != nil {
		return err
	}
	clientIDs, err := seedClients(ctx, clients)
	if err != nil {
		return err
	}
	n, err := seedMessages(ctx, messages, clientIDs, base)
	if err != nil {
		return err
	}
	if err := seedSelfConvo(ctx, pool, base); err != nil {
		return err
	}
	log.Printf("seeded %d sessions, %d api clients, %d messages, self-convo template + 3 runs",
		len(seedSessionDefs), len(clientIDs), n)
	return nil
}

// Deliberately awkward content: a name long enough to test truncation, and a
// status mix so the badge colours all render. One session is flagged
// IsMaster: without a seeded master row, the master badge (session_rows.html)
// and the "— master" picker suffix (send.html/calls.html/verify.html) render
// nowhere in the browser suite or any evidence screenshot — see CLAUDE.md's
// "an e2e run against empty tables proves very little".
var seedSessionDefs = []struct {
	ID, Name, Phone, JID, Status string
	Active, IsMaster             bool
}{
	{"e2e-sess-0001", "Nomor Utama Kasir Kemang Selatan", "6285190679041",
		"6285190679041:12@s.whatsapp.net", "connected", true, true},
	{"e2e-sess-0002", "Cadangan Outlet Pondok Indah", "6281234567890",
		"6281234567890:3@s.whatsapp.net", "disconnected", true, false},
	{"e2e-sess-0003", "Sesi Uji Coba", "6289900112233", "", "new", false, false},
}

func seedSessions(ctx context.Context, repo *wasession.SQLRepository, base time.Time) error {
	for i, d := range seedSessionDefs {
		s := &wasession.Session{
			ID: d.ID, Name: d.Name, Phone: d.Phone, JID: d.JID,
			Active: d.Active, IsMaster: d.IsMaster, LastStatus: d.Status,
			CreatedAt: base.Add(-time.Duration(i+1) * 24 * time.Hour),
			UpdatedAt: base,
		}
		if err := repo.Insert(ctx, s); err != nil {
			return fmt.Errorf("insert session %s: %w", d.ID, err)
		}
	}
	return nil
}

// 24 clients: more than the 20-row page size, so the pager actually renders and
// gets laid out on a phone.
func seedClients(ctx context.Context, repo *apiclient.Repository) ([]string, error) {
	ttl := 86400
	names := []string{
		"Kasir Kemang — Integrasi Notifikasi Pesanan Laundry",
		"Backoffice Pondok Indah",
		"OTP Gateway",
	}
	var ids []string
	for i := range 24 {
		name := fmt.Sprintf("Konsumen %02d", i)
		if i < len(names) {
			name = names[i]
		}
		c, err := repo.Create(ctx, name, fmt.Sprintf("e2e-login-%02d", i), "",
			apiclient.TokenSettings{TTLSeconds: &ttl})
		if err != nil {
			return nil, fmt.Errorf("create client %d: %w", i, err)
		}
		ids = append(ids, c.Client.ID)
	}
	return ids, nil
}

// Bodies chosen to stress the layout: a very long one with no early break, one
// with a long unbroken token (a URL), and short ones for contrast.
var seedBodies = []string{
	"Halo kak, cucian Anda dengan nomor order LAUNDRY-2026-08-22-000148 sudah " +
		"selesai dan siap diambil di outlet Kemang Selatan mulai pukul 09.00 " +
		"sampai 21.00. Mohon tunjukkan kode ini saat pengambilan. Terima kasih " +
		"telah menggunakan layanan kami.",
	"Kode OTP Anda 884213. Jangan bagikan kepada siapa pun.",
	"Struk digital: https://wa.mfardiansyah.id/struk/9f2c1b7ae4d84c05a1e6d3f70b8c2a19?ref=LAUNDRY-2026-08-22-000148",
	"Pesanan diterima.",
}

var seedCategories = []string{"notification", "otp", "alert", "manual"}

func seedMessages(
	ctx context.Context, repo *message.SQLRepository, clientIDs []string, base time.Time,
) (int, error) {
	// 45 rows: past the 20-row page size on /logs, and spread across 30 days so
	// the dashboard charts have a real shape instead of a flat line.
	const n = 45
	for i := range n {
		sess := seedSessionDefs[i%2] // only the two paired sessions send
		created := base.Add(-time.Duration(i*16) * time.Hour)
		m := &message.Message{
			ID:          fmt.Sprintf("e2e-msg-%04d", i),
			SessionID:   sess.ID,
			ToPhone:     fmt.Sprintf("62851%08d", 10000000+i),
			Body:        seedBodies[i%len(seedBodies)],
			Category:    seedCategories[i%len(seedCategories)],
			APIClientID: clientIDs[i%len(clientIDs)],
			ClientRef:   fmt.Sprintf("client-uid-%03d", i%7),
			TenantRef:   fmt.Sprintf("tenant-outlet-kemang-selatan-%02d", i%3),
			CreatedAt:   created,
		}
		if err := repo.Insert(ctx, m); err != nil {
			return 0, fmt.Errorf("insert message %d: %w", i, err)
		}
		// A realistic status mix so every badge colour and the failure column
		// are exercised: ~1 in 7 fails, ~1 in 11 stays queued.
		switch {
		case i%7 == 3:
			err := repo.MarkFailed(ctx, m.ID,
				"send timeout after 3 attempts: context deadline exceeded", 3)
			if err != nil {
				return 0, err
			}
		case i%11 == 5:
			// leave queued
		default:
			if err := repo.MarkSent(ctx, m.ID, fmt.Sprintf("3EB0%016X", i), 1); err != nil {
				return 0, err
			}
		}
	}
	return n, nil
}

// seedSelfConvo writes one wa_convo_templates row (the embedded example
// script, revalidated the same way an upload would be) and three
// wa_convo_runs rows over the two paired sessions
// (seedSessionDefs[0]/[1] — e2e-sess-0001/0002), one per state the
// convo_run_rows partial renders a distinct badge for:
//
//   - done: every turn sent, finished_at set.
//   - failed: turns 1-3 sent, turn 4 failed with a reason naming the
//     session, the rest skipped by FinishRun, error set, next_due_at
//     cleared.
//   - scheduled: next_due_at a year past base, so it never becomes due in
//     an e2e run (the flag is disabled there anyway, see scripts/e2e-env.sh)
//     and its Cancel button still renders (convo_run_rows.html's CanCancel
//     is !IsTerminal, true for "scheduled" regardless of next_due_at).
//
// Every run's turns come from selfconvo.Expand (over a selfconvo.Pair built
// from the seeded sessions and a fixed-seed rng, selfconvo.Vary'd first —
// the same sequence Runner.Trigger itself runs), converted via
// selfconvo.ToTurns, rather than a hand-rolled speaker→(session, recipient)
// mapping of this file's own — so these fixtures can never drift from what
// a real trigger actually persists. See
// docs/brief-self-convo.md's review follow-ups ("the e2e seeder
// re-implements Expand") and
// TestSeedWritesATemplateAndThreeRunsWithTheStatesTheRowsPartialRenders,
// which recomputes the same pipeline and checks every seeded turn's
// SessionID/ToPhone against it.
//
// Fixed ids, like every other seed* function in this file: safe to run
// once, which is why the call sits inside run()'s "already seeded" guard
// rather than re-checking on its own (a second direct call collides on
// those ids — see TestSeedSelfConvoIsNotIndependentlyIdempotentSoItMustStayInsideTheExistingSessionsGuard).
func seedSelfConvo(ctx context.Context, pool *sql.DB, base time.Time) error {
	script, err := selfconvo.Validate(selfconvo.ExampleJSON)
	if err != nil {
		return fmt.Errorf("validate embedded example script: %w", err)
	}

	repo := convo.NewRepository(pool)

	tmplCreated := base.Add(-3 * 24 * time.Hour)
	tmpl := &convo.Template{
		ID:        "e2e-convo-tmpl-0001",
		Name:      script.Name,
		TurnCount: len(script.Turns),
		Body:      string(selfconvo.ExampleJSON),
		CreatedAt: tmplCreated,
		UpdatedAt: tmplCreated,
	}
	if err := repo.InsertTemplate(ctx, tmpl); err != nil {
		return fmt.Errorf("insert self-convo template: %w", err)
	}

	// The seeder's two paired sessions (see seedSessionDefs): "a" sends as
	// sessA to sessB's phone and vice versa — selfconvo.Expand's own
	// resolution, run for real below rather than reimplemented.
	sessA, sessB := seedSessionDefs[0], seedSessionDefs[1]
	pair := selfconvo.Pair{
		A: selfconvo.Participant{ID: sessA.ID, Name: sessA.Name, Phone: sessA.Phone, Connected: true},
		B: selfconvo.Participant{ID: sessB.ID, Name: sessB.Name, Phone: sessB.Phone, Connected: true},
	}
	// Fixed seed (matching the one
	// TestSeedWritesATemplateAndThreeRunsWithTheStatesTheRowsPartialRenders
	// recomputes independently) so a re-run of this file always seeds
	// byte-identical turns — Vary and Expand share this ONE rng, drawn from
	// in that order, the same sequence Runner.Trigger itself runs
	// (runner.go's Trigger).
	rng := rand.New(rand.NewPCG(7, 7))
	varied, err := selfconvo.Vary(script, rng)
	if err != nil {
		return fmt.Errorf("vary embedded example script: %w", err)
	}
	expanded := selfconvo.Expand(varied, pair, rng)

	newRun := func(id, state string, startAt time.Time) *convo.Run {
		return &convo.Run{
			ID:           id,
			TemplateID:   tmpl.ID,
			TemplateName: tmpl.Name,
			SessionAID:   sessA.ID,
			SessionBID:   sessB.ID,
			SessionAName: sessA.Name,
			SessionBName: sessB.Name,
			PhoneA:       sessA.Phone,
			PhoneB:       sessB.Phone,
			State:        state,
			TurnCount:    len(expanded),
			StartAt:      startAt,
			CreatedAt:    startAt,
		}
	}

	// --- run 1: done, every turn sent ---
	doneStart := base.Add(-2 * 24 * time.Hour)
	doneRun := newRun("e2e-convo-run-done", convo.RunDone, doneStart)
	doneRun.TurnsDone = len(expanded)
	doneRun.StartedAt = &doneStart
	doneFinished := doneStart.Add(20 * time.Minute)
	doneRun.FinishedAt = &doneFinished
	if err := repo.CreateRun(ctx, doneRun, selfconvo.ToTurns(doneRun.ID, expanded)); err != nil {
		return fmt.Errorf("create done self-convo run: %w", err)
	}
	for i := range expanded {
		sentAt := doneStart.Add(time.Duration(i+1) * time.Minute)
		if err := repo.MarkTurnSent(ctx, doneRun.ID, i+1, sentAt); err != nil {
			return fmt.Errorf("mark done run turn %d sent: %w", i+1, err)
		}
	}

	// --- run 2: failed at turn 4, the rest skipped by FinishRun ---
	failStart := base.Add(-1 * 24 * time.Hour)
	failedRun := newRun("e2e-convo-run-failed", convo.RunRunning, failStart)
	failedRun.TurnsDone = 3
	// CreateRun's initial state is "running" — the send that will fail
	// hasn't happened yet at insert time; FinishRun below flips it to
	// "failed" once turn 4 has actually failed, matching how the real
	// runner reaches this state.
	failedRun.StartedAt = &failStart
	// ~120 chars, names the failing session by name and id — the shape a
	// real ARunFailsWithTheSessionNameWhenAParticipantDisconnectsMidRun
	// failure carries (see the plan's runner failure policy).
	failReason := fmt.Sprintf(
		`send failed for session "%s" (%s): socket disconnected mid-turn after 3 attempts`,
		sessB.Name, sessB.ID)
	if err := repo.CreateRun(ctx, failedRun, selfconvo.ToTurns(failedRun.ID, expanded)); err != nil {
		return fmt.Errorf("create failed self-convo run: %w", err)
	}
	for i := 1; i <= 3; i++ {
		sentAt := failStart.Add(time.Duration(i) * time.Minute)
		if err := repo.MarkTurnSent(ctx, failedRun.ID, i, sentAt); err != nil {
			return fmt.Errorf("mark failed run turn %d sent: %w", i, err)
		}
	}
	if err := repo.MarkTurnFailed(ctx, failedRun.ID, 4, failReason); err != nil {
		return fmt.Errorf("mark failed run turn 4 failed: %w", err)
	}
	// FinishRun sets the terminal state, stamps error/finished_at, clears
	// next_due_at and skips every turn still pending (seq 5..turnCount) —
	// see internal/domain/convo.Repository.FinishRun.
	failFinished := failStart.Add(5 * time.Minute)
	if err := repo.FinishRun(ctx, failedRun.ID, convo.RunFailed, failReason, failFinished); err != nil {
		return fmt.Errorf("finish failed self-convo run: %w", err)
	}

	// --- run 3: scheduled a year out, so the Cancel button still renders
	// (CanCancel is !IsTerminal — true regardless of how far next_due_at is)
	// and it can never fire during an e2e run, whose WSM_SELF_CONVO_ENABLED
	// is false anyway (scripts/e2e-env.sh never sets it).
	scheduledStart := base.Add(365 * 24 * time.Hour)
	scheduledRun := newRun("e2e-convo-run-scheduled", convo.RunScheduled, scheduledStart)
	scheduledRun.NextDueAt = &scheduledStart
	if err := repo.CreateRun(ctx, scheduledRun, selfconvo.ToTurns(scheduledRun.ID, expanded)); err != nil {
		return fmt.Errorf("create scheduled self-convo run: %w", err)
	}

	return nil
}
