package selfconvo

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/convo"
)

// newTestPlanner wires a Planner over the same Runner/repo/gateway shape
// newTestRunner already provides (sess-a/sess-b both seeded connected) —
// the common case; individual tests add receivers, mark sessions
// unhealthy, or override PlannerOptions as needed.
func newTestPlanner(t *testing.T, clock *fakeClock) (*Planner, *Runner, *convo.Repository, *fakeGateway) {
	t.Helper()
	r, repo, gw := newTestRunner(t, clock)
	p := NewPlanner(repo, r, PlannerOptions{Now: clock.Now, Rand: newDeterministicRand(), PollInterval: time.Minute})
	return p, r, repo, gw
}

// newDailySchedule builds a minimal always-active daily schedule with one
// receiver (sess-b), sender sess-a, window covering the whole day — the
// common shape most of this file's tests start from and then narrow.
func newDailySchedule(id, templateID string) (*convo.Schedule, []convo.ScheduleReceiver) {
	sc := &convo.Schedule{
		ID: id, Name: id, SenderSessionID: "sess-a", SenderName: "Session A", SenderPhone: "628110000001",
		RootTemplateID: templateID, ChainEnabled: false,
		Recurrence: convo.RecurrenceDaily, SetsPerDay: 1,
		WindowStart: "00:00", WindowEnd: "23:59", Timezone: "UTC",
		StartDate: "2026-09-01", State: convo.ScheduleActive,
	}
	receivers := []convo.ScheduleReceiver{
		{SessionID: "sess-b", SessionName: "Session B", Phone: "628110000002"},
	}
	return sc, receivers
}

// ---- idempotency / recurrence ----

func TestPlanDayIsIdempotentForADayCalledTwiceCreatesOneOccurrenceSet(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC))
	p, _, repo, _ := newTestPlanner(t, clock)
	tmpl := seedMinimalTemplate(t, repo, "tpl-a")
	sc, receivers := newDailySchedule("sch-1", tmpl.ID)
	if err := repo.InsertSchedule(context.Background(), sc, receivers); err != nil {
		t.Fatalf("InsertSchedule: %v", err)
	}

	n1, err := p.PlanDay(context.Background(), clock.Now())
	if err != nil {
		t.Fatalf("PlanDay (1st): %v", err)
	}
	if n1 != 1 {
		t.Fatalf("PlanDay (1st) started = %d, want 1", n1)
	}

	n2, err := p.PlanDay(context.Background(), clock.Now())
	if err != nil {
		t.Fatalf("PlanDay (2nd): %v", err)
	}
	if n2 != 0 {
		t.Fatalf("PlanDay (2nd, same day) started = %d, want 0 (idempotent)", n2)
	}

	runs, err := repo.ListRuns(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("len(runs) = %d, want exactly 1 occurrence set", len(runs))
	}
}

// TestPlanDayIsIdempotentAcrossARestartBeforeMarkPlannedRan simulates the
// crash-mid-day case CountRunsForOccurrence exists for: a run already
// exists for today (as if an earlier process created it) but
// last_planned_date was never stamped. A second PlanDay call must not
// create a SECOND set — only mark the day planned.
func TestPlanDayIsIdempotentAcrossARestartBeforeMarkPlannedRan(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC))
	p, _, repo, _ := newTestPlanner(t, clock)
	tmpl := seedMinimalTemplate(t, repo, "tpl-a")
	sc, receivers := newDailySchedule("sch-restart", tmpl.ID)
	if err := repo.InsertSchedule(context.Background(), sc, receivers); err != nil {
		t.Fatalf("InsertSchedule: %v", err)
	}

	today := clock.Now().UTC().Format(dateLayout)
	existing := convo.Run{
		ID: "pre-existing-run", TemplateID: tmpl.ID, TemplateName: tmpl.Name,
		SessionAID: "sess-a", SessionBID: "sess-b", SessionAName: "Session A", SessionBName: "Session B",
		PhoneA: "628110000001", PhoneB: "628110000002", State: convo.RunDone,
		TurnCount: 1, StartAt: clock.Now(), ScheduleID: sc.ID, OccurrenceDate: today,
	}
	if err := repo.CreateRun(context.Background(), &existing, nil); err != nil {
		t.Fatalf("CreateRun (simulated pre-crash run): %v", err)
	}

	n, err := p.PlanDay(context.Background(), clock.Now())
	if err != nil {
		t.Fatalf("PlanDay: %v", err)
	}
	if n != 0 {
		t.Fatalf("PlanDay started = %d, want 0 (a run for today already exists)", n)
	}

	got, err := repo.GetSchedule(context.Background(), sc.ID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if got.LastPlannedDate != today {
		t.Fatalf("LastPlannedDate = %q, want %q (restart safety: still marked planned)", got.LastPlannedDate, today)
	}

	runs, err := repo.ListRuns(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("len(runs) = %d, want still 1 (no duplicate set created)", len(runs))
	}
}

func TestWeeklyScheduleFiresOnlyOnItsConfiguredWeekday(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC))
	p, _, repo, _ := newTestPlanner(t, clock)
	tmpl := seedMinimalTemplate(t, repo, "tpl-a")

	today := isoWeekday(clock.Now())
	other := today%7 + 1 // guaranteed distinct from today, still a valid 1..7

	scYes, recvYes := newDailySchedule("sch-yes", tmpl.ID)
	scYes.Recurrence = convo.RecurrenceWeekly
	scYes.Weekdays = strconv.Itoa(today)
	if err := repo.InsertSchedule(context.Background(), scYes, recvYes); err != nil {
		t.Fatalf("InsertSchedule (yes): %v", err)
	}

	scNo, recvNo := newDailySchedule("sch-no", tmpl.ID)
	scNo.Recurrence = convo.RecurrenceWeekly
	scNo.Weekdays = strconv.Itoa(other)
	if err := repo.InsertSchedule(context.Background(), scNo, recvNo); err != nil {
		t.Fatalf("InsertSchedule (no): %v", err)
	}

	n, err := p.PlanDay(context.Background(), clock.Now())
	if err != nil {
		t.Fatalf("PlanDay: %v", err)
	}
	if n != 1 {
		t.Fatalf("PlanDay started = %d, want 1 (only sch-yes matches today's weekday)", n)
	}

	runs, err := repo.ListRuns(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].ScheduleID != "sch-yes" {
		t.Fatalf("runs = %+v, want exactly one run from sch-yes", runs)
	}
}

func TestIntervalScheduleFiresOnlyOnMultiplesOfIntervalDaysSinceStartDate(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC))
	p, _, repo, _ := newTestPlanner(t, clock)
	tmpl := seedMinimalTemplate(t, repo, "tpl-a")

	// today (09-09) is exactly 2 days after 09-07 -> fires with interval=2.
	scFires, recvFires := newDailySchedule("sch-fires", tmpl.ID)
	scFires.Recurrence = convo.RecurrenceInterval
	scFires.IntervalDays = 2
	scFires.StartDate = "2026-09-07"
	if err := repo.InsertSchedule(context.Background(), scFires, recvFires); err != nil {
		t.Fatalf("InsertSchedule (fires): %v", err)
	}

	// today is exactly 1 day after 09-08 -> does not fire with interval=2.
	scSkips, recvSkips := newDailySchedule("sch-skips", tmpl.ID)
	scSkips.Recurrence = convo.RecurrenceInterval
	scSkips.IntervalDays = 2
	scSkips.StartDate = "2026-09-08"
	if err := repo.InsertSchedule(context.Background(), scSkips, recvSkips); err != nil {
		t.Fatalf("InsertSchedule (skips): %v", err)
	}

	n, err := p.PlanDay(context.Background(), clock.Now())
	if err != nil {
		t.Fatalf("PlanDay: %v", err)
	}
	if n != 1 {
		t.Fatalf("PlanDay started = %d, want 1 (only sch-fires' interval matches today)", n)
	}

	runs, err := repo.ListRuns(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].ScheduleID != "sch-fires" {
		t.Fatalf("runs = %+v, want exactly one run from sch-fires", runs)
	}
}

func TestDailyScheduleAlwaysFires(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC))
	p, _, repo, _ := newTestPlanner(t, clock)
	tmpl := seedMinimalTemplate(t, repo, "tpl-a")
	sc, receivers := newDailySchedule("sch-daily", tmpl.ID)
	if err := repo.InsertSchedule(context.Background(), sc, receivers); err != nil {
		t.Fatalf("InsertSchedule: %v", err)
	}

	n, err := p.PlanDay(context.Background(), clock.Now())
	if err != nil {
		t.Fatalf("PlanDay: %v", err)
	}
	if n != 1 {
		t.Fatalf("PlanDay started = %d, want 1 (daily always fires)", n)
	}
}

// ---- window / timing ----

func TestAStartTimeAlreadyInThePastTodayIsSkippedNotFiredImmediately(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 20, 0, 0, 0, time.UTC)) // planner "boots" at 20:00
	p, _, repo, _ := newTestPlanner(t, clock)
	tmpl := seedMinimalTemplate(t, repo, "tpl-a")
	sc, receivers := newDailySchedule("sch-past", tmpl.ID)
	sc.WindowStart, sc.WindowEnd = "09:00", "17:00" // the whole window is already in the past
	if err := repo.InsertSchedule(context.Background(), sc, receivers); err != nil {
		t.Fatalf("InsertSchedule: %v", err)
	}

	n, err := p.PlanDay(context.Background(), clock.Now())
	if err != nil {
		t.Fatalf("PlanDay: %v", err)
	}
	if n != 0 {
		t.Fatalf("PlanDay started = %d, want 0 (the whole window is already in the past today)", n)
	}

	runs, err := repo.ListRuns(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("len(runs) = %d, want 0 — a past-window start must be skipped, never fired immediately", len(runs))
	}

	got, err := repo.GetSchedule(context.Background(), sc.ID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if got.LastPlannedDate != clock.Now().UTC().Format(dateLayout) {
		t.Errorf("LastPlannedDate = %q, want today (the day was considered, even though nothing fired)", got.LastPlannedDate)
	}
}

func TestEndDateTodayTransitionsTheScheduleToFinishedAfterPlanningTheDay(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC))
	p, _, repo, _ := newTestPlanner(t, clock)
	tmpl := seedMinimalTemplate(t, repo, "tpl-a")
	sc, receivers := newDailySchedule("sch-end", tmpl.ID)
	sc.EndDate = clock.Now().UTC().Format(dateLayout)
	if err := repo.InsertSchedule(context.Background(), sc, receivers); err != nil {
		t.Fatalf("InsertSchedule: %v", err)
	}

	n, err := p.PlanDay(context.Background(), clock.Now())
	if err != nil {
		t.Fatalf("PlanDay: %v", err)
	}
	if n != 1 {
		t.Fatalf("PlanDay started = %d, want 1 (the last day still runs)", n)
	}

	got, err := repo.GetSchedule(context.Background(), sc.ID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if got.State != convo.ScheduleFinished {
		t.Fatalf("State = %q, want finished", got.State)
	}
}

func TestMinSetSpacingIsRespectedAcrossADaysSets(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC))
	p, _, repo, _ := newTestPlanner(t, clock)
	tmpl := seedMinimalTemplate(t, repo, "tpl-a")
	sc, receivers := newDailySchedule("sch-spacing", tmpl.ID)
	sc.SetsPerDay = 4
	sc.WindowStart, sc.WindowEnd = "00:00", "23:59"
	if err := repo.InsertSchedule(context.Background(), sc, receivers); err != nil {
		t.Fatalf("InsertSchedule: %v", err)
	}

	n, err := p.PlanDay(context.Background(), clock.Now())
	if err != nil {
		t.Fatalf("PlanDay: %v", err)
	}
	if n != 4 {
		t.Fatalf("PlanDay started = %d, want 4 (sets_per_day=4, one receiver)", n)
	}

	runs, err := repo.ListRuns(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 4 {
		t.Fatalf("len(runs) = %d, want 4", len(runs))
	}
	starts := make([]time.Time, len(runs))
	for i, r := range runs {
		starts[i] = r.StartAt
	}
	for i := 0; i < len(starts); i++ {
		for j := i + 1; j < len(starts); j++ {
			gap := starts[j].Sub(starts[i])
			if gap < 0 {
				gap = -gap
			}
			if gap < defaultMinSetSpacing {
				t.Fatalf("sets at %v and %v are only %v apart, want at least %v", starts[i], starts[j], gap, defaultMinSetSpacing)
			}
		}
	}
}

// ---- unloadable timezone ----

func TestAnUnloadableTimezoneStallsOnlyItsOwnScheduleOthersStillPlan(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC))
	p, _, repo, _ := newTestPlanner(t, clock)
	tmpl := seedMinimalTemplate(t, repo, "tpl-a")

	scBad, recvBad := newDailySchedule("sch-bad-tz", tmpl.ID)
	scBad.Timezone = "Not/A/Real/Zone"
	if err := repo.InsertSchedule(context.Background(), scBad, recvBad); err != nil {
		t.Fatalf("InsertSchedule (bad tz): %v", err)
	}

	scGood, recvGood := newDailySchedule("sch-good-tz", tmpl.ID)
	if err := repo.InsertSchedule(context.Background(), scGood, recvGood); err != nil {
		t.Fatalf("InsertSchedule (good tz): %v", err)
	}

	n, err := p.PlanDay(context.Background(), clock.Now())
	if err != nil {
		t.Fatalf("PlanDay: %v", err)
	}
	if n != 1 {
		t.Fatalf("PlanDay started = %d, want 1 (only sch-good-tz plans; sch-bad-tz stalls instead of aborting the pass)", n)
	}

	gotBad, err := repo.GetSchedule(context.Background(), scBad.ID)
	if err != nil {
		t.Fatalf("GetSchedule(bad): %v", err)
	}
	if gotBad.State != convo.ScheduleStalled {
		t.Fatalf("sch-bad-tz State = %q, want stalled", gotBad.State)
	}
	if !strings.Contains(gotBad.PausedReason, "Not/A/Real/Zone") {
		t.Errorf("sch-bad-tz PausedReason = %q, want it to name the timezone", gotBad.PausedReason)
	}

	gotGood, err := repo.GetSchedule(context.Background(), scGood.ID)
	if err != nil {
		t.Fatalf("GetSchedule(good): %v", err)
	}
	if gotGood.State != convo.ScheduleActive {
		t.Fatalf("sch-good-tz State = %q, want still active", gotGood.State)
	}
}

// ---- health ----

func TestAnUnhealthyParticipantAtPlanTimeStallsTheScheduleAndCreatesNoRuns(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC))
	p, _, repo, gw := newTestPlanner(t, clock)
	tmpl := seedMinimalTemplate(t, repo, "tpl-a")
	sc, receivers := newDailySchedule("sch-health", tmpl.ID)
	if err := repo.InsertSchedule(context.Background(), sc, receivers); err != nil {
		t.Fatalf("InsertSchedule: %v", err)
	}

	gw.setUnhealthy("sess-b", "session Session B (sess-b) is not connected")

	n, err := p.PlanDay(context.Background(), clock.Now())
	if err != nil {
		t.Fatalf("PlanDay: %v", err)
	}
	if n != 0 {
		t.Fatalf("PlanDay started = %d, want 0", n)
	}

	got, err := repo.GetSchedule(context.Background(), sc.ID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if got.State != convo.ScheduleStalled {
		t.Fatalf("State = %q, want stalled", got.State)
	}
	if !strings.Contains(got.PausedReason, "sess-b") {
		t.Errorf("PausedReason = %q, want it to name sess-b", got.PausedReason)
	}

	runs, err := repo.ListRuns(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("len(runs) = %d, want 0", len(runs))
	}
}

// ---- resume ----

func TestResumeRefusesAStillUnhealthyStalledScheduleWithTheOriginalReasonAndDoesNotBackfillMissedDays(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC))
	p, _, repo, gw := newTestPlanner(t, clock)
	tmpl := seedMinimalTemplate(t, repo, "tpl-a")
	sc, receivers := newDailySchedule("sch-resume", tmpl.ID)
	sc.State = convo.ScheduleStalled
	sc.PausedReason = "session sess-b: not connected"
	sc.LastPlannedDate = "2026-09-05" // several days missed while stalled
	if err := repo.InsertSchedule(context.Background(), sc, receivers); err != nil {
		t.Fatalf("InsertSchedule: %v", err)
	}

	gw.setUnhealthy("sess-b", "still down (a different reason than PausedReason, on purpose)")

	err := p.Resume(context.Background(), sc.ID)
	if err == nil {
		t.Fatal("Resume with sess-b still unhealthy: got nil error, want a refusal")
	}
	if !strings.Contains(err.Error(), "session sess-b: not connected") {
		t.Errorf("Resume error = %q, want it to carry the schedule's ORIGINAL stall reason", err.Error())
	}

	got, err := repo.GetSchedule(context.Background(), sc.ID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if got.State != convo.ScheduleStalled {
		t.Fatalf("State = %q, want still stalled (Resume refused)", got.State)
	}
	if got.LastPlannedDate != "2026-09-05" {
		t.Errorf("LastPlannedDate = %q, want unchanged at %q — Resume must never back-fill missed days",
			got.LastPlannedDate, "2026-09-05")
	}
}

func TestResumeReturnsAStalledScheduleToActiveWhenHealthyAndDoesNotTouchLastPlannedDate(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC))
	p, _, repo, _ := newTestPlanner(t, clock)
	tmpl := seedMinimalTemplate(t, repo, "tpl-a")
	sc, receivers := newDailySchedule("sch-resume-ok", tmpl.ID)
	sc.State = convo.ScheduleStalled
	sc.PausedReason = "session sess-b: not connected"
	sc.LastPlannedDate = "2026-09-05"
	if err := repo.InsertSchedule(context.Background(), sc, receivers); err != nil {
		t.Fatalf("InsertSchedule: %v", err)
	}

	if err := p.Resume(context.Background(), sc.ID); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	got, err := repo.GetSchedule(context.Background(), sc.ID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if got.State != convo.ScheduleActive {
		t.Fatalf("State = %q, want active", got.State)
	}
	if got.PausedReason != "" {
		t.Errorf("PausedReason = %q, want cleared", got.PausedReason)
	}
	if got.LastPlannedDate != "2026-09-05" {
		t.Errorf("LastPlannedDate = %q, want unchanged at %q — Resume never back-fills", got.LastPlannedDate, "2026-09-05")
	}
}

// ---- per-sender daily cap ----

func TestThePerSenderDailyCapStopsFurtherPlanningWithoutStallingTheSchedule(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC))
	r, repo, _ := newTestRunner(t, clock)
	p := NewPlanner(repo, r, PlannerOptions{
		Now: clock.Now, Rand: newDeterministicRand(), PollInterval: time.Minute, MaxMsgsPerDay: 1,
	})
	tmpl := seedMinimalTemplate(t, repo, "tpl-a")
	sc, receivers := newDailySchedule("sch-cap", tmpl.ID)
	if err := repo.InsertSchedule(context.Background(), sc, receivers); err != nil {
		t.Fatalf("InsertSchedule: %v", err)
	}

	today := clock.Now().UTC().Format(dateLayout)
	// A turn already sent FROM sess-a today, already at the cap of 1.
	existing := convo.Run{
		ID: "already-sent", TemplateName: "x", SessionAID: "sess-a", SessionBID: "sess-b",
		SessionAName: "A", SessionBName: "B", PhoneA: "628110000001", PhoneB: "628110000002",
		State: convo.RunDone, TurnCount: 1, StartAt: clock.Now(), OccurrenceDate: today,
	}
	turns := []convo.Turn{
		{Seq: 1, Speaker: "a", SessionID: "sess-a", ToPhone: "628110000002", Body: "hi", State: convo.TurnSent},
	}
	if err := repo.CreateRun(context.Background(), &existing, turns); err != nil {
		t.Fatalf("CreateRun (pre-existing sent turn): %v", err)
	}

	n, err := p.PlanDay(context.Background(), clock.Now())
	if err != nil {
		t.Fatalf("PlanDay: %v", err)
	}
	if n != 0 {
		t.Fatalf("PlanDay started = %d, want 0 (sender already at its daily cap)", n)
	}

	got, err := repo.GetSchedule(context.Background(), sc.ID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if got.State != convo.ScheduleActive {
		t.Fatalf("State = %q, want still active — a full daily quota is not a health problem, must not stall", got.State)
	}
}
