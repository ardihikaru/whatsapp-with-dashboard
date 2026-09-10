package convo

import (
	"context"
	"errors"
	"testing"
	"time"
)

func newSchedule(id, state string, base time.Time) *Schedule {
	return &Schedule{
		ID:              id,
		Name:            "Weekend chat",
		SenderSessionID: "sess-sender",
		SenderName:      "Sender",
		SenderPhone:     "62800000001",
		Topic:           "futsal",
		ChainEnabled:    true,
		Recurrence:      RecurrenceDaily,
		SetsPerDay:      1,
		WindowStart:     "08:00",
		WindowEnd:       "20:00",
		Timezone:        "UTC",
		StartDate:       base.Format("2006-01-02"),
		State:           state,
	}
}

func newReceivers(scheduleID string) []ScheduleReceiver {
	return []ScheduleReceiver{
		{ScheduleID: scheduleID, SessionID: "sess-r1", SessionName: "Receiver 1", Phone: "62800000002", Position: 0},
		{ScheduleID: scheduleID, SessionID: "sess-r2", SessionName: "Receiver 2", Phone: "62800000003", Position: 1},
	}
}

func TestScheduleRoundTripsWithItsReceiversAndUpdateReplacesTheReceiverSet(t *testing.T) {
	repo, now := newTestRepository(t)
	ctx := context.Background()

	sc := newSchedule("sch1", ScheduleActive, now())
	if err := repo.InsertSchedule(ctx, sc, newReceivers("sch1")); err != nil {
		t.Fatalf("InsertSchedule: %v", err)
	}
	if sc.CreatedAt.IsZero() || sc.UpdatedAt.IsZero() {
		t.Fatal("InsertSchedule left CreatedAt/UpdatedAt zero, want the injected clock")
	}

	got, err := repo.GetSchedule(ctx, "sch1")
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if got.Name != "Weekend chat" || got.Topic != "futsal" || !got.ChainEnabled {
		t.Errorf("GetSchedule = %+v, want the inserted schedule back", got)
	}
	if got.EndDate != "" {
		t.Errorf("EndDate = %q, want empty (unlimited)", got.EndDate)
	}

	recs, err := repo.ScheduleReceivers(ctx, "sch1")
	if err != nil {
		t.Fatalf("ScheduleReceivers: %v", err)
	}
	if len(recs) != 2 || recs[0].SessionID != "sess-r1" || recs[1].SessionID != "sess-r2" {
		t.Fatalf("ScheduleReceivers = %+v, want the two inserted receivers in position order", recs)
	}

	// UpdateSchedule replaces the receiver set: drop sess-r1, keep sess-r2,
	// add a brand new sess-r3.
	sc.Name = "Weekend chat v2"
	newRecs := []ScheduleReceiver{
		{SessionID: "sess-r2", SessionName: "Receiver 2", Phone: "62800000003", Position: 0},
		{SessionID: "sess-r3", SessionName: "Receiver 3", Phone: "62800000004", Position: 1},
	}
	if err := repo.UpdateSchedule(ctx, sc, newRecs); err != nil {
		t.Fatalf("UpdateSchedule: %v", err)
	}

	updated, err := repo.GetSchedule(ctx, "sch1")
	if err != nil {
		t.Fatalf("GetSchedule after update: %v", err)
	}
	if updated.Name != "Weekend chat v2" {
		t.Errorf("Name after update = %q, want %q", updated.Name, "Weekend chat v2")
	}

	recsAfter, err := repo.ScheduleReceivers(ctx, "sch1")
	if err != nil {
		t.Fatalf("ScheduleReceivers after update: %v", err)
	}
	if len(recsAfter) != 2 {
		t.Fatalf("ScheduleReceivers after update = %+v, want exactly 2 rows", recsAfter)
	}
	ids := map[string]bool{}
	for _, r := range recsAfter {
		ids[r.SessionID] = true
	}
	if ids["sess-r1"] {
		t.Error("sess-r1 still present after UpdateSchedule dropped it, want it removed")
	}
	if !ids["sess-r2"] || !ids["sess-r3"] {
		t.Errorf("ScheduleReceivers after update = %+v, want sess-r2 and sess-r3", recsAfter)
	}
}

func TestPlannableSchedulesExcludesPausedStalledEndedAndAlreadyPlannedAndIncludesUnlimitedEndDate(t *testing.T) {
	repo, now := newTestRepository(t)
	ctx := context.Background()
	today := now().Format("2006-01-02")
	yesterday := now().Add(-24 * time.Hour).Format("2006-01-02")
	tomorrow := now().Add(24 * time.Hour).Format("2006-01-02")

	cases := []struct {
		id    string
		setup func(*Schedule)
	}{
		{"paused", func(s *Schedule) { s.State = SchedulePaused }},
		{"stalled", func(s *Schedule) { s.State = ScheduleStalled }},
		{"ended", func(s *Schedule) { s.EndDate = yesterday }},
		{"not-started", func(s *Schedule) { s.StartDate = tomorrow }},
		{"already-planned", func(s *Schedule) { s.LastPlannedDate = today }},
		{"unlimited", func(s *Schedule) {}}, // EndDate left "" (NULL) — must be INCLUDED
	}

	for _, c := range cases {
		sc := newSchedule(c.id, ScheduleActive, now())
		c.setup(sc)
		if err := repo.InsertSchedule(ctx, sc, nil); err != nil {
			t.Fatalf("InsertSchedule %s: %v", c.id, err)
		}
	}

	got, err := repo.PlannableSchedules(ctx, today, 50)
	if err != nil {
		t.Fatalf("PlannableSchedules: %v", err)
	}
	if len(got) != 1 || got[0].ID != "unlimited" {
		var ids []string
		for _, s := range got {
			ids = append(ids, s.ID)
		}
		t.Fatalf("PlannableSchedules = %v, want exactly [unlimited]", ids)
	}
}

func TestSetScheduleStateRefusesToReanimateAStoppedSchedule(t *testing.T) {
	repo, now := newTestRepository(t)
	ctx := context.Background()

	sc := newSchedule("sch1", ScheduleStopped, now())
	if err := repo.InsertSchedule(ctx, sc, nil); err != nil {
		t.Fatalf("InsertSchedule: %v", err)
	}

	if err := repo.SetScheduleState(ctx, "sch1", ScheduleActive, "", now()); !errors.Is(err, ErrScheduleFinished) {
		t.Errorf("SetScheduleState on a stopped schedule = %v, want ErrScheduleFinished", err)
	}

	// A schedule that is still active can transition freely, and a stall
	// reason/paused_at are recorded.
	sc2 := newSchedule("sch2", ScheduleActive, now())
	if err := repo.InsertSchedule(ctx, sc2, nil); err != nil {
		t.Fatalf("InsertSchedule sch2: %v", err)
	}
	if err := repo.SetScheduleState(ctx, "sch2", ScheduleStalled, "session down", now()); err != nil {
		t.Fatalf("SetScheduleState sch2 -> stalled: %v", err)
	}
	got, err := repo.GetSchedule(ctx, "sch2")
	if err != nil {
		t.Fatalf("GetSchedule sch2: %v", err)
	}
	if got.State != ScheduleStalled {
		t.Errorf("state = %q, want %q", got.State, ScheduleStalled)
	}
	if got.PausedReason != "session down" {
		t.Errorf("PausedReason = %q, want %q", got.PausedReason, "session down")
	}
	if got.PausedAt == nil {
		t.Error("PausedAt is nil after stalling, want a stamp")
	}
}

func TestDeletingAScheduleCascadesItsReceiversButLeavesAlreadyCreatedRunsIntact(t *testing.T) {
	repo, now := newTestRepository(t)
	ctx := context.Background()

	sc := newSchedule("sch1", ScheduleActive, now())
	if err := repo.InsertSchedule(ctx, sc, newReceivers("sch1")); err != nil {
		t.Fatalf("InsertSchedule: %v", err)
	}

	run := newRun("r1", RunDone, now())
	run.ScheduleID = "sch1"
	run.OccurrenceDate = now().Format("2006-01-02")
	if err := repo.CreateRun(ctx, run, nil); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	if err := repo.DeleteSchedule(ctx, "sch1"); err != nil {
		t.Fatalf("DeleteSchedule: %v", err)
	}

	if _, err := repo.GetSchedule(ctx, "sch1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSchedule after delete = %v, want ErrNotFound", err)
	}

	recs, err := repo.ScheduleReceivers(ctx, "sch1")
	if err != nil {
		t.Fatalf("ScheduleReceivers after delete: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("ScheduleReceivers after delete = %+v, want none (cascade did not fire)", recs)
	}

	gotRun, err := repo.GetRun(ctx, "r1")
	if err != nil {
		t.Fatalf("GetRun after deleting its schedule: %v", err)
	}
	if gotRun.ScheduleID != "sch1" {
		t.Errorf("run.ScheduleID after deleting the schedule = %q, want it unchanged (%q)", gotRun.ScheduleID, "sch1")
	}
}

func TestDeletingATemplateCascadesItsLinks(t *testing.T) {
	repo, now := newTestRepository(t)
	ctx := context.Background()

	from := &Template{ID: "tpl-from", Name: "Opener", TurnCount: 1, Body: "{}"}
	to := &Template{ID: "tpl-to", Name: "Middle", TurnCount: 1, Body: "{}"}
	if err := repo.InsertTemplate(ctx, from); err != nil {
		t.Fatalf("InsertTemplate from: %v", err)
	}
	if err := repo.InsertTemplate(ctx, to); err != nil {
		t.Fatalf("InsertTemplate to: %v", err)
	}

	links := []TemplateLink{{ToTemplateID: "tpl-to", Weight: 3, MinGapSeconds: 60, MaxGapSeconds: 300, CreatedAt: now()}}
	if err := repo.ReplaceTemplateLinks(ctx, "tpl-from", links); err != nil {
		t.Fatalf("ReplaceTemplateLinks: %v", err)
	}

	if err := repo.DeleteTemplate(ctx, "tpl-from"); err != nil {
		t.Fatalf("DeleteTemplate: %v", err)
	}

	got, err := repo.TemplateLinks(ctx, "tpl-from")
	if err != nil {
		t.Fatalf("TemplateLinks after deleting its from-template: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("TemplateLinks after deleting tpl-from = %+v, want none (cascade did not fire)", got)
	}
}

func TestTemplateLinksReturnsWeightsAndGapWindowsUnchanged(t *testing.T) {
	repo, now := newTestRepository(t)
	ctx := context.Background()

	from := &Template{ID: "tpl-from", Name: "Opener", TurnCount: 1, Body: "{}"}
	to1 := &Template{ID: "tpl-to1", Name: "Middle 1", TurnCount: 1, Body: "{}"}
	to2 := &Template{ID: "tpl-to2", Name: "Middle 2", TurnCount: 1, Body: "{}"}
	for _, tpl := range []*Template{from, to1, to2} {
		if err := repo.InsertTemplate(ctx, tpl); err != nil {
			t.Fatalf("InsertTemplate %s: %v", tpl.ID, err)
		}
	}

	links := []TemplateLink{
		{ToTemplateID: "tpl-to1", Weight: 5, MinGapSeconds: 30, MaxGapSeconds: 90, CreatedAt: now()},
		{ToTemplateID: "tpl-to2", Weight: 1, MinGapSeconds: 600, MaxGapSeconds: 1800, CreatedAt: now()},
	}
	if err := repo.ReplaceTemplateLinks(ctx, "tpl-from", links); err != nil {
		t.Fatalf("ReplaceTemplateLinks: %v", err)
	}

	got, err := repo.TemplateLinks(ctx, "tpl-from")
	if err != nil {
		t.Fatalf("TemplateLinks: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(TemplateLinks) = %d, want 2", len(got))
	}
	byTo := map[string]TemplateLink{}
	for _, l := range got {
		byTo[l.ToTemplateID] = l
	}
	if byTo["tpl-to1"].Weight != 5 || byTo["tpl-to1"].MinGapSeconds != 30 || byTo["tpl-to1"].MaxGapSeconds != 90 {
		t.Errorf("tpl-to1 link = %+v, want weight=5 min=30 max=90", byTo["tpl-to1"])
	}
	if byTo["tpl-to2"].Weight != 1 || byTo["tpl-to2"].MinGapSeconds != 600 || byTo["tpl-to2"].MaxGapSeconds != 1800 {
		t.Errorf("tpl-to2 link = %+v, want weight=1 min=600 max=1800", byTo["tpl-to2"])
	}

	// ReplaceTemplateLinks is a full swap, not a merge — replacing with a
	// single link drops the other.
	if err := repo.ReplaceTemplateLinks(ctx, "tpl-from", []TemplateLink{
		{ToTemplateID: "tpl-to1", Weight: 9, MinGapSeconds: 1, MaxGapSeconds: 2, CreatedAt: now()},
	}); err != nil {
		t.Fatalf("ReplaceTemplateLinks (swap): %v", err)
	}
	got, err = repo.TemplateLinks(ctx, "tpl-from")
	if err != nil {
		t.Fatalf("TemplateLinks after swap: %v", err)
	}
	if len(got) != 1 || got[0].ToTemplateID != "tpl-to1" || got[0].Weight != 9 {
		t.Fatalf("TemplateLinks after swap = %+v, want exactly one link to tpl-to1 weight=9", got)
	}
}

func TestCountRunsForOccurrenceCountsOnlyThatScheduleAndThatDate(t *testing.T) {
	repo, now := newTestRepository(t)
	ctx := context.Background()

	mk := func(id, scheduleID, date string) {
		run := newRun(id, RunDone, now())
		run.ScheduleID = scheduleID
		run.OccurrenceDate = date
		if err := repo.CreateRun(ctx, run, nil); err != nil {
			t.Fatalf("CreateRun %s: %v", id, err)
		}
	}
	mk("r1", "sch1", "2026-09-09")
	mk("r2", "sch1", "2026-09-09")
	mk("r3", "sch1", "2026-09-10") // same schedule, different day
	mk("r4", "sch2", "2026-09-09") // different schedule, same day

	n, err := repo.CountRunsForOccurrence(ctx, "sch1", "2026-09-09")
	if err != nil {
		t.Fatalf("CountRunsForOccurrence: %v", err)
	}
	if n != 2 {
		t.Errorf("CountRunsForOccurrence(sch1, 2026-09-09) = %d, want 2", n)
	}

	n, err = repo.CountRunsForOccurrence(ctx, "sch1", "2026-09-10")
	if err != nil {
		t.Fatalf("CountRunsForOccurrence: %v", err)
	}
	if n != 1 {
		t.Errorf("CountRunsForOccurrence(sch1, 2026-09-10) = %d, want 1", n)
	}

	n, err = repo.CountRunsForOccurrence(ctx, "sch2", "2026-09-09")
	if err != nil {
		t.Fatalf("CountRunsForOccurrence: %v", err)
	}
	if n != 1 {
		t.Errorf("CountRunsForOccurrence(sch2, 2026-09-09) = %d, want 1", n)
	}
}

// TestARunCreatedWithNoScheduleFieldsStillRoundTrips pins the manual-trigger
// path: every field this phase added to Run defaults to its zero value and
// CreateRun/GetRun must not choke on that, or on reading it back.
func TestARunCreatedWithNoScheduleFieldsStillRoundTrips(t *testing.T) {
	repo, now := newTestRepository(t)
	ctx := context.Background()

	run := newRun("r1", RunScheduled, now())
	if err := repo.CreateRun(ctx, run, newTurns("r1")); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	got, err := repo.GetRun(ctx, "r1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.ScheduleID != "" {
		t.Errorf("ScheduleID = %q, want empty for a manually triggered run", got.ScheduleID)
	}
	if got.OccurrenceDate != "" {
		t.Errorf("OccurrenceDate = %q, want empty", got.OccurrenceDate)
	}
	if got.ChainPrevRunID != "" {
		t.Errorf("ChainPrevRunID = %q, want empty", got.ChainPrevRunID)
	}
	if got.ChainDepth != 0 {
		t.Errorf("ChainDepth = %d, want 0", got.ChainDepth)
	}
	if got.Topic != "" {
		t.Errorf("Topic = %q, want empty", got.Topic)
	}
	if got.ParaphraseSeed != 0 {
		t.Errorf("ParaphraseSeed = %d, want 0", got.ParaphraseSeed)
	}
}

// TestCountSenderTurnsForDateCountsOnlyThatSendersTurnsOnThatOccurrenceDate
// pins the planner's per-sender daily cap query: it must count turns SENT
// FROM the given session (never turns sent TO it, i.e. the other speaker's
// own outbound turns in the same run), scoped to occurrence_date, and a
// manually triggered run (occurrence_date == "") must never contribute.
func TestCountSenderTurnsForDateCountsOnlyThatSendersTurnsOnThatOccurrenceDate(t *testing.T) {
	repo, now := newTestRepository(t)
	ctx := context.Background()

	mk := func(id, occurrenceDate string) {
		run := newRun(id, RunDone, now())
		run.OccurrenceDate = occurrenceDate
		// newTurns: seq1 from sess-a, seq2 from sess-b — one turn per
		// speaker per run.
		if err := repo.CreateRun(ctx, run, newTurns(id)); err != nil {
			t.Fatalf("CreateRun %s: %v", id, err)
		}
	}
	mk("r1", "2026-09-09")
	mk("r2", "2026-09-09")
	mk("r3", "2026-09-10") // same sender, different day
	mk("r4", "")           // manual trigger — must not count toward any day

	n, err := repo.CountSenderTurnsForDate(ctx, "sess-a", "2026-09-09")
	if err != nil {
		t.Fatalf("CountSenderTurnsForDate: %v", err)
	}
	if n != 2 {
		t.Errorf("CountSenderTurnsForDate(sess-a, 2026-09-09) = %d, want 2 (one turn from sess-a per run r1/r2)", n)
	}

	n, err = repo.CountSenderTurnsForDate(ctx, "sess-b", "2026-09-09")
	if err != nil {
		t.Fatalf("CountSenderTurnsForDate: %v", err)
	}
	if n != 2 {
		t.Errorf("CountSenderTurnsForDate(sess-b, 2026-09-09) = %d, want 2", n)
	}

	n, err = repo.CountSenderTurnsForDate(ctx, "sess-a", "2026-09-10")
	if err != nil {
		t.Fatalf("CountSenderTurnsForDate: %v", err)
	}
	if n != 1 {
		t.Errorf("CountSenderTurnsForDate(sess-a, 2026-09-10) = %d, want 1 (r4's manually-triggered occurrence_date=\"\" must never be counted against a real day)", n)
	}
}
