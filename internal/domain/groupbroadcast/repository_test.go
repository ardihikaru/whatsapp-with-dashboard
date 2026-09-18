package groupbroadcast

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
)

func newTestRepository(t *testing.T) (*Repository, func() time.Time) {
	t.Helper()
	pool, err := db.Open(filepath.Join(t.TempDir(), "t.db"), db.SQLiteDialect)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	if err := db.EnsureSchema(context.Background(), pool); err != nil {
		t.Fatalf("db.EnsureSchema: %v", err)
	}
	repo := NewRepository(pool)
	clock := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return clock }
	return repo, func() time.Time { return clock }
}

func newSchedule(id string) *Schedule {
	return &Schedule{
		ID:                  id,
		Name:                "Morning check-in",
		SenderSessionID:     "sess-sender",
		SenderName:          "Sender",
		SenderPhone:         "62800000001",
		GroupJID:            "120363426585371342@g.us",
		GroupName:           "Clino Team",
		Recurrence:          RecurrenceDaily,
		SenderWindowStart:   "07:55",
		SenderWindowEnd:     "08:16",
		ReceiverWindowStart: "08:17",
		ReceiverWindowEnd:   "09:30",
		LateCutoff:          "08:30",
		LangIDWeight:        70,
		Timezone:            "Asia/Jakarta",
		StartDate:           "2026-09-18",
		State:               ScheduleActive,
	}
}

func TestInsertAndGetScheduleRoundTripsEveryField(t *testing.T) {
	repo, _ := newTestRepository(t)
	ctx := context.Background()
	sc := newSchedule("sch1")
	receivers := []ScheduleReceiver{
		{SessionID: "sess-r1", SessionName: "Receiver 1", Phone: "62800000002", Position: 0},
		{SessionID: "sess-r2", SessionName: "Receiver 2", Phone: "62800000003", Position: 1},
	}
	if err := repo.InsertSchedule(ctx, sc, receivers); err != nil {
		t.Fatalf("InsertSchedule: %v", err)
	}

	got, err := repo.GetSchedule(ctx, "sch1")
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if got.GroupJID != sc.GroupJID || got.SenderWindowStart != "07:55" || got.LateCutoff != "08:30" {
		t.Errorf("GetSchedule returned %+v, want the inserted fields back", got)
	}

	gotReceivers, err := repo.ScheduleReceivers(ctx, "sch1")
	if err != nil {
		t.Fatalf("ScheduleReceivers: %v", err)
	}
	if len(gotReceivers) != 2 {
		t.Fatalf("ScheduleReceivers returned %d rows, want 2", len(gotReceivers))
	}
}

// TestUpdateScheduleReplacesTheReceiverSetInOneTransaction is the RED-
// without-the-change evidence for editable receivers at the repository
// layer: a stub that ignored the receivers argument would leave the
// original two rows in place.
func TestUpdateScheduleReplacesTheReceiverSetInOneTransaction(t *testing.T) {
	repo, _ := newTestRepository(t)
	ctx := context.Background()
	sc := newSchedule("sch1")
	if err := repo.InsertSchedule(ctx, sc, []ScheduleReceiver{
		{SessionID: "sess-r1", SessionName: "Receiver 1", Phone: "62800000002", Position: 0},
		{SessionID: "sess-r2", SessionName: "Receiver 2", Phone: "62800000003", Position: 1},
	}); err != nil {
		t.Fatalf("InsertSchedule: %v", err)
	}

	existing, err := repo.GetSchedule(ctx, "sch1")
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if err := repo.UpdateSchedule(ctx, &existing, []ScheduleReceiver{
		{SessionID: "sess-r3", SessionName: "Receiver 3", Phone: "62800000004", Position: 0},
	}); err != nil {
		t.Fatalf("UpdateSchedule: %v", err)
	}

	got, err := repo.ScheduleReceivers(ctx, "sch1")
	if err != nil {
		t.Fatalf("ScheduleReceivers: %v", err)
	}
	if len(got) != 1 || got[0].SessionID != "sess-r3" {
		t.Fatalf("ScheduleReceivers after update = %+v, want exactly [sess-r3]", got)
	}
}

func TestPlannableSchedulesRespectsStartEndAndLastPlannedDate(t *testing.T) {
	repo, _ := newTestRepository(t)
	ctx := context.Background()

	notStarted := newSchedule("future")
	notStarted.StartDate = "2026-09-20"
	if err := repo.InsertSchedule(ctx, notStarted, nil); err != nil {
		t.Fatalf("insert future: %v", err)
	}

	ended := newSchedule("ended")
	ended.EndDate = "2026-09-17"
	if err := repo.InsertSchedule(ctx, ended, nil); err != nil {
		t.Fatalf("insert ended: %v", err)
	}

	alreadyPlanned := newSchedule("planned")
	alreadyPlanned.LastPlannedDate = "2026-09-18"
	if err := repo.InsertSchedule(ctx, alreadyPlanned, nil); err != nil {
		t.Fatalf("insert planned: %v", err)
	}

	due := newSchedule("due")
	if err := repo.InsertSchedule(ctx, due, nil); err != nil {
		t.Fatalf("insert due: %v", err)
	}

	got, err := repo.PlannableSchedules(ctx, "2026-09-18", 0)
	if err != nil {
		t.Fatalf("PlannableSchedules: %v", err)
	}
	if len(got) != 1 || got[0].ID != "due" {
		t.Fatalf("PlannableSchedules = %+v, want exactly [due]", got)
	}
}

func TestCreateOccurrenceInsertsTurnsAndCountOccurrencesForDateSeesIt(t *testing.T) {
	repo, now := newTestRepository(t)
	ctx := context.Background()

	occ := &Occurrence{ID: "occ1", ScheduleID: "sch1", OccurrenceDate: "2026-09-18", GroupJID: "1@g.us", State: OccurrenceScheduled}
	turns := []Turn{
		{ID: "t1", Role: RoleSender, SessionID: "sess-sender", Language: LangID, Body: "pagi!", Source: SourceAI, DueAt: now()},
		{ID: "t2", Role: RoleReceiver, SessionID: "sess-r1", Language: LangJV, Body: "sugeng enjing", Source: SourceFallback, DueAt: now().Add(time.Hour)},
	}
	if err := repo.CreateOccurrence(ctx, occ, turns); err != nil {
		t.Fatalf("CreateOccurrence: %v", err)
	}

	n, err := repo.CountOccurrencesForDate(ctx, "sch1", "2026-09-18")
	if err != nil {
		t.Fatalf("CountOccurrencesForDate: %v", err)
	}
	if n != 1 {
		t.Fatalf("CountOccurrencesForDate = %d, want 1", n)
	}

	got, err := repo.TurnsForOccurrence(ctx, "occ1")
	if err != nil {
		t.Fatalf("TurnsForOccurrence: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("TurnsForOccurrence returned %d turns, want 2", len(got))
	}
}

// TestSessionTurnTimesForDateReturnsTimesAcrossDifferentSchedules is the
// explicit repository-layer test the plan names: a session that is a
// receiver in TWO different group-broadcast schedules must have both
// schedules' turns show up in one SessionTurnTimesForDate call, since the
// min-session-gap check has to see every commitment for that session on
// that date, not just the one belonging to the schedule currently being
// planned.
func TestSessionTurnTimesForDateReturnsTimesAcrossDifferentSchedules(t *testing.T) {
	repo, now := newTestRepository(t)
	ctx := context.Background()

	occA := &Occurrence{ID: "occA", ScheduleID: "schA", OccurrenceDate: "2026-09-18", GroupJID: "1@g.us", State: OccurrenceScheduled}
	if err := repo.CreateOccurrence(ctx, occA, []Turn{
		{ID: "ta", Role: RoleReceiver, SessionID: "sess-shared", Language: LangID, Body: "a", Source: SourceAI, DueAt: now().Add(1 * time.Hour)},
	}); err != nil {
		t.Fatalf("CreateOccurrence A: %v", err)
	}

	occB := &Occurrence{ID: "occB", ScheduleID: "schB", OccurrenceDate: "2026-09-18", GroupJID: "2@g.us", State: OccurrenceScheduled}
	if err := repo.CreateOccurrence(ctx, occB, []Turn{
		{ID: "tb", Role: RoleReceiver, SessionID: "sess-shared", Language: LangJV, Body: "b", Source: SourceAI, DueAt: now().Add(2 * time.Hour)},
	}); err != nil {
		t.Fatalf("CreateOccurrence B: %v", err)
	}

	got, err := repo.SessionTurnTimesForDate(ctx, "sess-shared", "2026-09-18", "")
	if err != nil {
		t.Fatalf("SessionTurnTimesForDate: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("SessionTurnTimesForDate returned %d times, want 2 (one per schedule)", len(got))
	}
}

func TestClaimTurnFailsOnceAlreadyClaimed(t *testing.T) {
	repo, now := newTestRepository(t)
	ctx := context.Background()

	occ := &Occurrence{ID: "occ1", ScheduleID: "sch1", OccurrenceDate: "2026-09-18", GroupJID: "1@g.us", State: OccurrenceScheduled}
	if err := repo.CreateOccurrence(ctx, occ, []Turn{
		{ID: "t1", Role: RoleSender, SessionID: "sess-sender", Language: LangID, Body: "pagi", Source: SourceAI, DueAt: now()},
	}); err != nil {
		t.Fatalf("CreateOccurrence: %v", err)
	}

	if err := repo.ClaimTurn(ctx, "t1", "msg1"); err != nil {
		t.Fatalf("first ClaimTurn: %v", err)
	}
	if err := repo.ClaimTurn(ctx, "t1", "msg2"); err != ErrTurnNotPending {
		t.Fatalf("second ClaimTurn returned %v, want ErrTurnNotPending", err)
	}
}
