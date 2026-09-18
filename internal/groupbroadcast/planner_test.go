package groupbroadcast

import (
	"context"
	"testing"
	"time"

	gb "github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/groupbroadcast"
)

func seedSchedule(t *testing.T, repo *gb.Repository, sc *gb.Schedule, receivers []gb.ScheduleReceiver) {
	t.Helper()
	if err := repo.InsertSchedule(context.Background(), sc, receivers); err != nil {
		t.Fatalf("InsertSchedule: %v", err)
	}
}

func seedConnected(gw *fakeGateway, ids ...string) {
	for _, id := range ids {
		gw.seed(Participant{ID: id, Name: id, Phone: "628110000000", Connected: true})
	}
}

// TestPlanDayIsIdempotentForADayCalledTwiceCreatesOneOccurrence mirrors
// internal/selfconvo's own idempotency test.
func TestPlanDayIsIdempotentForADayCalledTwiceCreatesOneOccurrence(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC))
	p, repo, gw := newTestPlanner(t, clock)
	seedConnected(gw, "sess-sender", "sess-r1")
	sc, receivers := newDailySchedule("sch-1", "sess-r1")
	seedSchedule(t, repo, sc, receivers)

	n1, err := p.PlanDay(context.Background(), clock.Now())
	if err != nil {
		t.Fatalf("PlanDay (1st): %v", err)
	}
	if n1 != 1 {
		t.Fatalf("PlanDay (1st) created = %d, want 1", n1)
	}
	n2, err := p.PlanDay(context.Background(), clock.Now())
	if err != nil {
		t.Fatalf("PlanDay (2nd): %v", err)
	}
	if n2 != 0 {
		t.Fatalf("PlanDay (2nd, same day) created = %d, want 0 (idempotent)", n2)
	}

	got, err := repo.OccurrencesForSchedule(context.Background(), "sch-1", 10)
	if err != nil {
		t.Fatalf("OccurrencesForSchedule: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("occurrences = %d, want exactly 1", len(got))
	}
}

// TestPlanDayDrawsLandInsideConfiguredWindows is the explicit plan
// requirement: every turn's due_at must fall inside its own window
// (sender inside [SenderWindowStart, SenderWindowEnd], every receiver
// inside [ReceiverWindowStart, ReceiverWindowEnd]).
func TestPlanDayDrawsLandInsideConfiguredWindows(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC))
	p, repo, gw := newTestPlanner(t, clock)
	seedConnected(gw, "sess-sender", "sess-r1", "sess-r2", "sess-r3")
	sc, receivers := newDailySchedule("sch-1", "sess-r1", "sess-r2", "sess-r3")
	seedSchedule(t, repo, sc, receivers)

	if _, err := p.PlanDay(context.Background(), clock.Now()); err != nil {
		t.Fatalf("PlanDay: %v", err)
	}

	occs, err := repo.OccurrencesForSchedule(context.Background(), "sch-1", 1)
	if err != nil || len(occs) != 1 {
		t.Fatalf("OccurrencesForSchedule: %v (%d rows)", err, len(occs))
	}
	turns, err := repo.TurnsForOccurrence(context.Background(), occs[0].ID)
	if err != nil {
		t.Fatalf("TurnsForOccurrence: %v", err)
	}
	if len(turns) != 4 {
		t.Fatalf("len(turns) = %d, want 4 (1 sender + 3 receivers)", len(turns))
	}

	senderStart := time.Date(2026, 9, 18, 7, 55, 0, 0, time.UTC)
	senderEnd := time.Date(2026, 9, 18, 8, 16, 0, 0, time.UTC)
	receiverStart := time.Date(2026, 9, 18, 8, 17, 0, 0, time.UTC)
	receiverEnd := time.Date(2026, 9, 18, 9, 30, 0, 0, time.UTC)

	for _, tn := range turns {
		if tn.Role == gb.RoleSender {
			if tn.DueAt.Before(senderStart) || tn.DueAt.After(senderEnd) {
				t.Errorf("sender due_at %s outside window [%s, %s]", tn.DueAt, senderStart, senderEnd)
			}
			continue
		}
		if tn.DueAt.Before(receiverStart) || tn.DueAt.After(receiverEnd) {
			t.Errorf("receiver %s due_at %s outside window [%s, %s]", tn.SessionID, tn.DueAt, receiverStart, receiverEnd)
		}
	}
}

// TestPlanDayReceiverDrawsNeverCollideToTheSecond is the explicit plan
// requirement — the disjoint-bucket construction (drawReceiverTimes)
// guarantees this BY CONSTRUCTION, not merely "unlikely".
func TestPlanDayReceiverDrawsNeverCollideToTheSecond(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC))
	p, repo, gw := newTestPlanner(t, clock)
	seedConnected(gw, "sess-sender", "sess-r1", "sess-r2", "sess-r3", "sess-r4", "sess-r5")
	sc, receivers := newDailySchedule("sch-1", "sess-r1", "sess-r2", "sess-r3", "sess-r4", "sess-r5")
	seedSchedule(t, repo, sc, receivers)

	if _, err := p.PlanDay(context.Background(), clock.Now()); err != nil {
		t.Fatalf("PlanDay: %v", err)
	}

	occs, _ := repo.OccurrencesForSchedule(context.Background(), "sch-1", 1)
	turns, err := repo.TurnsForOccurrence(context.Background(), occs[0].ID)
	if err != nil {
		t.Fatalf("TurnsForOccurrence: %v", err)
	}
	seen := map[int64]bool{}
	for _, tn := range turns {
		if tn.Role != gb.RoleReceiver {
			continue
		}
		key := tn.DueAt.Unix()
		if seen[key] {
			t.Fatalf("two receivers drew the same due_at second: %s", tn.DueAt)
		}
		seen[key] = true
	}
	if len(seen) != 5 {
		t.Fatalf("saw %d distinct receiver times, want 5", len(seen))
	}
}

// TestPlanDayLateReceiverAlwaysCarriesANonEmptyReason forces every receiver
// late (their whole window is after LateCutoff) and asserts LateReason is
// always populated.
func TestPlanDayLateReceiverAlwaysCarriesANonEmptyReason(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC))
	p, repo, gw := newTestPlanner(t, clock)
	seedConnected(gw, "sess-sender", "sess-r1", "sess-r2")
	sc, receivers := newDailySchedule("sch-1", "sess-r1", "sess-r2")
	sc.ReceiverWindowStart, sc.ReceiverWindowEnd = "08:40", "09:00" // entirely after the 08:30 cutoff
	seedSchedule(t, repo, sc, receivers)

	if _, err := p.PlanDay(context.Background(), clock.Now()); err != nil {
		t.Fatalf("PlanDay: %v", err)
	}

	occs, _ := repo.OccurrencesForSchedule(context.Background(), "sch-1", 1)
	turns, err := repo.TurnsForOccurrence(context.Background(), occs[0].ID)
	if err != nil {
		t.Fatalf("TurnsForOccurrence: %v", err)
	}
	found := 0
	for _, tn := range turns {
		if tn.Role != gb.RoleReceiver {
			continue
		}
		found++
		if !tn.IsLate {
			t.Errorf("receiver %s IsLate = false, want true (window is entirely after the cutoff)", tn.SessionID)
		}
		if tn.LateReason == "" {
			t.Errorf("receiver %s LateReason is empty, want a non-empty reason for a late turn", tn.SessionID)
		}
	}
	if found != 2 {
		t.Fatalf("found %d receiver turns, want 2", found)
	}
}

// TestAnUnhealthySenderStallsTheWholeOccurrenceCreatingZeroTurns is the
// RED-without-the-change test the plan names: without the sender-only
// health gate, PlanDay would either create a broken occurrence or crash
// resolving a sender with no known participant.
func TestAnUnhealthySenderStallsTheWholeOccurrenceCreatingZeroTurns(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC))
	p, repo, gw := newTestPlanner(t, clock)
	seedConnected(gw, "sess-sender", "sess-r1")
	gw.setUnhealthy("sess-sender", "session Sender is not connected")
	sc, receivers := newDailySchedule("sch-1", "sess-r1")
	seedSchedule(t, repo, sc, receivers)

	n, err := p.PlanDay(context.Background(), clock.Now())
	if err != nil {
		t.Fatalf("PlanDay: %v", err)
	}
	if n != 0 {
		t.Fatalf("PlanDay created = %d, want 0 (sender unhealthy)", n)
	}

	count, err := repo.CountOccurrencesForDate(context.Background(), "sch-1", "2026-09-18")
	if err != nil {
		t.Fatalf("CountOccurrencesForDate: %v", err)
	}
	if count != 0 {
		t.Fatalf("occurrences created = %d, want 0", count)
	}

	got, err := repo.GetSchedule(context.Background(), "sch-1")
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if got.State != gb.ScheduleStalled {
		t.Errorf("schedule state = %q, want %q", got.State, gb.ScheduleStalled)
	}
}

// TestAnUnhealthyReceiverIsSkippedWhileTheSenderAndOtherReceiversStillPlan
// is the deliberate-deviation test: unlike internal/selfconvo's
// all-or-nothing gate, ONE unhealthy receiver must not stall the whole
// schedule — the sender and every other healthy receiver still get
// planned, and the schedule stays active.
func TestAnUnhealthyReceiverIsSkippedWhileTheSenderAndOtherReceiversStillPlan(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC))
	p, repo, gw := newTestPlanner(t, clock)
	seedConnected(gw, "sess-sender", "sess-r1", "sess-r2")
	gw.setUnhealthy("sess-r1", "session R1 is not connected")
	sc, receivers := newDailySchedule("sch-1", "sess-r1", "sess-r2")
	seedSchedule(t, repo, sc, receivers)

	n, err := p.PlanDay(context.Background(), clock.Now())
	if err != nil {
		t.Fatalf("PlanDay: %v", err)
	}
	if n != 1 {
		t.Fatalf("PlanDay created = %d, want 1 occurrence still planned", n)
	}

	occs, _ := repo.OccurrencesForSchedule(context.Background(), "sch-1", 1)
	turns, err := repo.TurnsForOccurrence(context.Background(), occs[0].ID)
	if err != nil {
		t.Fatalf("TurnsForOccurrence: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("len(turns) = %d, want 2 (sender + healthy receiver only, unhealthy receiver excluded)", len(turns))
	}
	for _, tn := range turns {
		if tn.SessionID == "sess-r1" {
			t.Fatal("a turn was created for the unhealthy receiver sess-r1; want it skipped entirely")
		}
	}

	got, err := repo.GetSchedule(context.Background(), "sch-1")
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if got.State != gb.ScheduleActive {
		t.Errorf("schedule state = %q, want %q (an unhealthy receiver must not stall the schedule)", got.State, gb.ScheduleActive)
	}
}

// TestAPastSenderStartTimeIsSkippedNotFiredImmediately mirrors
// internal/selfconvo/planner.go's own "never back-fill" rule (step 7): a
// planner that runs (or catches up) after the sender's whole window has
// already passed today must plan NOTHING for today, not fire an
// immediately-due occurrence.
func TestAPastSenderStartTimeIsSkippedNotFiredImmediately(t *testing.T) {
	// "Now" is already past the entire sender window (07:55-08:16).
	clock := newFakeClock(time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC))
	p, repo, gw := newTestPlanner(t, clock)
	seedConnected(gw, "sess-sender", "sess-r1")
	sc, receivers := newDailySchedule("sch-1", "sess-r1")
	seedSchedule(t, repo, sc, receivers)

	n, err := p.PlanDay(context.Background(), clock.Now())
	if err != nil {
		t.Fatalf("PlanDay: %v", err)
	}
	if n != 0 {
		t.Fatalf("PlanDay created = %d, want 0 (past window skipped, not fired immediately)", n)
	}
	count, err := repo.CountOccurrencesForDate(context.Background(), "sch-1", "2026-09-18")
	if err != nil {
		t.Fatalf("CountOccurrencesForDate: %v", err)
	}
	if count != 0 {
		t.Fatalf("occurrences created = %d, want 0", count)
	}

	got, err := repo.GetSchedule(context.Background(), "sch-1")
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if got.LastPlannedDate != "2026-09-18" {
		t.Errorf("LastPlannedDate = %q, want today stamped so the planner does not retry endlessly", got.LastPlannedDate)
	}
	if got.State != gb.ScheduleActive {
		t.Errorf("schedule state = %q, want %q (a skipped-for-today window is not a health stall)", got.State, gb.ScheduleActive)
	}
}

// TestDrawWithGapRedrawsAwayFromAnExistingSessionTime is the explicit
// MinSessionGap test the plan names: seeded with a pre-existing turn time
// for a session, every later draw for that SAME session must stay at
// least the configured gap away.
func TestDrawWithGapRedrawsAwayFromAnExistingSessionTime(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC))
	p, repo, _ := newTestPlanner(t, clock)
	p.minSessionGap = 25 * time.Second

	existingAt := time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC)
	occ := &gb.Occurrence{ID: "occ-existing", ScheduleID: "sch-other", OccurrenceDate: "2026-09-18", GroupJID: "1@g.us", State: gb.OccurrenceScheduled}
	if err := repo.CreateOccurrence(context.Background(), occ, []gb.Turn{
		{ID: "t-existing", Role: gb.RoleReceiver, SessionID: "sess-shared", Language: gb.LangID, Body: "x", Source: gb.SourceFallback, DueAt: existingAt},
	}); err != nil {
		t.Fatalf("CreateOccurrence: %v", err)
	}

	// The window is wide enough that plenty of it sits outside the 25s
	// gap around existingAt, so drawWithGap's redraw loop can always find
	// a valid candidate — the assertion below is what actually proves the
	// gap was enforced, regardless of how many redraws it took.
	windowStart := time.Date(2026, 9, 18, 7, 0, 0, 0, time.UTC)
	windowEnd := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	draw := func() time.Time { return drawUniform(p.rng, windowStart, windowEnd) }

	for i := 0; i < 20; i++ {
		candidate := draw()
		got, err := p.drawWithGap(context.Background(), "sess-shared", "2026-09-18", windowStart, windowEnd, draw, candidate)
		if err != nil {
			t.Fatalf("drawWithGap: %v", err)
		}
		d := got.Sub(existingAt)
		if d < 0 {
			d = -d
		}
		if d < p.minSessionGap {
			t.Fatalf("drawWithGap returned %s, only %s away from the existing time %s (want >= %s)",
				got, d, existingAt, p.minSessionGap)
		}
	}
}

// TestDrawWithGapNudgesForwardAndClampsToTheWindowWhenTooNarrow covers the
// documented fallback: a window too narrow to redraw out of the gap still
// returns a value clamped to windowEnd rather than looping forever.
func TestDrawWithGapNudgesForwardAndClampsToTheWindowWhenTooNarrow(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC))
	p, repo, _ := newTestPlanner(t, clock)
	p.minSessionGap = 25 * time.Second

	existingAt := time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC)
	occ := &gb.Occurrence{ID: "occ-existing", ScheduleID: "sch-other", OccurrenceDate: "2026-09-18", GroupJID: "1@g.us", State: gb.OccurrenceScheduled}
	if err := repo.CreateOccurrence(context.Background(), occ, []gb.Turn{
		{ID: "t-existing", Role: gb.RoleReceiver, SessionID: "sess-shared", Language: gb.LangID, Body: "x", Source: gb.SourceFallback, DueAt: existingAt},
	}); err != nil {
		t.Fatalf("CreateOccurrence: %v", err)
	}

	// A window of only 5s, entirely inside the 25s gap around existingAt —
	// no draw can ever satisfy the gap here, forcing the nudge-and-clamp
	// fallback.
	windowStart := time.Date(2026, 9, 18, 8, 0, 2, 0, time.UTC)
	windowEnd := time.Date(2026, 9, 18, 8, 0, 7, 0, time.UTC)
	draw := func() time.Time { return drawUniform(p.rng, windowStart, windowEnd) }

	got, err := p.drawWithGap(context.Background(), "sess-shared", "2026-09-18", windowStart, windowEnd, draw, draw())
	if err != nil {
		t.Fatalf("drawWithGap: %v", err)
	}
	if got.After(windowEnd) {
		t.Fatalf("drawWithGap returned %s, past windowEnd %s — must be clamped", got, windowEnd)
	}
}
