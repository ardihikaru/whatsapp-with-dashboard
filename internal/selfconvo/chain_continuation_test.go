package selfconvo

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/convo"
)

// minimalScriptJSON is a tiny, always-valid two-turn script body for this
// file's chain/fan-out tests, where the exact turn content doesn't matter —
// only that Validate accepts it and Expand produces one turn per speaker.
const minimalScriptJSON = `{
	"version": 1,
	"turns": [
		{ "speaker": "a", "text": "Halo" },
		{ "speaker": "b", "text": "Hai" }
	]
}`

func mustValidateScript(t *testing.T, raw string) Script {
	t.Helper()
	s, err := Validate([]byte(raw))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	return s
}

func seedMinimalTemplate(t *testing.T, repo *convo.Repository, id string) convo.Template {
	t.Helper()
	tmpl := convo.Template{ID: id, Name: id, TurnCount: 2, Body: minimalScriptJSON}
	if err := repo.InsertTemplate(context.Background(), &tmpl); err != nil {
		t.Fatalf("InsertTemplate(%s): %v", id, err)
	}
	return tmpl
}

// seedTestSchedule inserts a minimal always-daily, always-active schedule
// with no receivers of its own — this file's chain/health tests build runs
// directly via createScheduledRun/Trigger rather than through the planner,
// so only the fields continueChain/processRun's health gate actually read
// (SenderSessionID, ChainEnabled, State) need to be realistic.
func seedTestSchedule(t *testing.T, repo *convo.Repository, id string, chainEnabled bool, rootTemplateID string) convo.Schedule {
	t.Helper()
	sc := &convo.Schedule{
		ID: id, Name: id, SenderSessionID: "sess-a", SenderName: "Session A", SenderPhone: "628110000001",
		RootTemplateID: rootTemplateID, ChainEnabled: chainEnabled,
		Recurrence: convo.RecurrenceDaily, SetsPerDay: 1,
		WindowStart: "00:00", WindowEnd: "23:59", Timezone: "UTC",
		StartDate: "2026-09-01", State: convo.ScheduleActive,
	}
	if err := repo.InsertSchedule(context.Background(), sc, nil); err != nil {
		t.Fatalf("InsertSchedule: %v", err)
	}
	return *sc
}

// driveRunToDone advances clock to runID's own StartAt if it hasn't arrived
// yet (a chain successor's StartAt is in the future relative to when it was
// created), then Ticks it through every one of its turns, advancing the
// clock by each turn's own DelaySeconds in between — the same shape
// TestTicksSendTurnsInSequenceAlternatingBetweenTheTwoChosenSessions drives
// a run by hand, generalised for reuse across this file's tests.
func driveRunToDone(t *testing.T, r *Runner, repo *convo.Repository, clock *fakeClock, runID string) {
	t.Helper()
	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetRun(%s): %v", runID, err)
	}
	if clock.Now().Before(run.StartAt) {
		clock.Advance(run.StartAt.Sub(clock.Now()))
	}
	turns, err := repo.Turns(context.Background(), runID)
	if err != nil {
		t.Fatalf("Turns(%s): %v", runID, err)
	}
	for i := range turns {
		if i > 0 {
			clock.Advance(time.Duration(turns[i].DelaySeconds) * time.Second)
		}
		if _, err := r.Tick(context.Background()); err != nil {
			t.Fatalf("Tick: %v", err)
		}
	}
}

// ---- chain continuation ----

// TestAFinishedScheduledRunCreatesItsSuccessorWithTheSamePairAndIncrementedChainDepth
// is the RED-first regression for continueChain: a run that reaches RunDone
// with a non-empty ScheduleID whose schedule has ChainEnabled must schedule
// a successor over the link graph — same pair, ChainDepth+1, a gap inside
// the chosen edge's window.
func TestAFinishedScheduledRunCreatesItsSuccessorWithTheSamePairAndIncrementedChainDepth(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	r, repo, _ := newTestRunner(t, clock)
	tmplA := seedMinimalTemplate(t, repo, "tpl-a")
	tmplB := seedMinimalTemplate(t, repo, "tpl-b")
	sched := seedTestSchedule(t, repo, "sch-1", true, tmplA.ID)

	if err := repo.ReplaceTemplateLinks(context.Background(), tmplA.ID, []convo.TemplateLink{
		{ToTemplateID: tmplB.ID, Weight: 1, MinGapSeconds: 30, MaxGapSeconds: 90},
	}); err != nil {
		t.Fatalf("ReplaceTemplateLinks: %v", err)
	}

	run, err := r.createScheduledRun(context.Background(), scheduledRunRequest{
		Template: tmplA, Script: mustValidateScript(t, minimalScriptJSON),
		Pair: Pair{
			A: Participant{ID: "sess-a", Name: "Session A", Phone: "628110000001"},
			B: Participant{ID: "sess-b", Name: "Session B", Phone: "628110000002"},
		},
		StartAt: clock.Now(), ScheduleID: sched.ID, OccurrenceDate: "2026-09-09",
	})
	if err != nil {
		t.Fatalf("createScheduledRun: %v", err)
	}

	driveRunToDone(t, r, repo, clock, run.ID)
	sendTime := clock.Now()

	final, err := repo.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if final.State != convo.RunDone {
		t.Fatalf("run.State = %q, want done", final.State)
	}

	runs, err := repo.ListRuns(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	var successor *convo.Run
	for i := range runs {
		if runs[i].ChainPrevRunID == run.ID {
			successor = &runs[i]
		}
	}
	if successor == nil {
		t.Fatalf("no successor run created; runs = %+v", runs)
	}
	if successor.TemplateID != tmplB.ID {
		t.Errorf("successor.TemplateID = %q, want %q", successor.TemplateID, tmplB.ID)
	}
	if successor.SessionAID != run.SessionAID || successor.SessionBID != run.SessionBID {
		t.Errorf("successor pair = %s/%s, want the SAME pair %s/%s",
			successor.SessionAID, successor.SessionBID, run.SessionAID, run.SessionBID)
	}
	if successor.ChainDepth != run.ChainDepth+1 {
		t.Errorf("successor.ChainDepth = %d, want %d", successor.ChainDepth, run.ChainDepth+1)
	}
	if successor.ScheduleID != sched.ID || successor.OccurrenceDate != "2026-09-09" {
		t.Errorf("successor ScheduleID/OccurrenceDate = %q/%q, want %q/%q",
			successor.ScheduleID, successor.OccurrenceDate, sched.ID, "2026-09-09")
	}
	gotGap := successor.StartAt.Sub(sendTime)
	if gotGap < 30*time.Second || gotGap > 90*time.Second {
		t.Errorf("successor start gap = %v, want within the edge's [30s, 90s] window", gotGap)
	}
}

// TestAFinishedManualRunCreatesNoSuccessor pins the other half: a run with
// no ScheduleID (Trigger's normal path) must never chain, even when its
// template happens to have outgoing links.
func TestAFinishedManualRunCreatesNoSuccessor(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	r, repo, _ := newTestRunner(t, clock)
	tmplA := seedMinimalTemplate(t, repo, "tpl-a")
	tmplB := seedMinimalTemplate(t, repo, "tpl-b")
	if err := repo.ReplaceTemplateLinks(context.Background(), tmplA.ID, []convo.TemplateLink{
		{ToTemplateID: tmplB.ID, Weight: 1, MinGapSeconds: 30, MaxGapSeconds: 90},
	}); err != nil {
		t.Fatalf("ReplaceTemplateLinks: %v", err)
	}

	run, err := r.Trigger(context.Background(), TriggerRequest{
		TemplateID: tmplA.ID, TickedSessionIDs: []string{"sess-a", "sess-b"},
	})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}

	driveRunToDone(t, r, repo, clock, run.ID)

	runs, err := repo.ListRuns(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("len(runs) after a manual run finishes = %d, want 1 (no chain successor)", len(runs))
	}
}

// TestMaxChainDepthStopsTheChainWithoutMarkingAnythingFailed pins
// Options.MaxChainDepth: with a cap of 1, the initial run (depth 0) may
// chain to depth 1, but depth 1 must not chain further — and neither run
// may ever be marked failed for it; reaching the cap is the normal end of a
// day's conversation.
func TestMaxChainDepthStopsTheChainWithoutMarkingAnythingFailed(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	pool := newTestPool(t)
	repo := convo.NewRepository(pool)
	gw := newFakeGateway()
	gw.seed(Participant{ID: "sess-a", Name: "Session A", Phone: "628110000001", Connected: true})
	gw.seed(Participant{ID: "sess-b", Name: "Session B", Phone: "628110000002", Connected: true})
	r := New(repo, gw, Options{
		Now: clock.Now, Rand: newDeterministicRand(), PollInterval: 5 * time.Second, MaxChainDepth: 1,
	})

	tmplA := seedMinimalTemplate(t, repo, "tpl-a")
	tmplB := seedMinimalTemplate(t, repo, "tpl-b")
	sched := seedTestSchedule(t, repo, "sch-depth", true, tmplA.ID)
	if err := repo.ReplaceTemplateLinks(context.Background(), tmplA.ID, []convo.TemplateLink{
		{ToTemplateID: tmplB.ID, Weight: 1, MinGapSeconds: 5, MaxGapSeconds: 5},
	}); err != nil {
		t.Fatalf("ReplaceTemplateLinks(a->b): %v", err)
	}
	if err := repo.ReplaceTemplateLinks(context.Background(), tmplB.ID, []convo.TemplateLink{
		{ToTemplateID: tmplA.ID, Weight: 1, MinGapSeconds: 5, MaxGapSeconds: 5},
	}); err != nil {
		t.Fatalf("ReplaceTemplateLinks(b->a): %v", err)
	}

	initial, err := r.createScheduledRun(context.Background(), scheduledRunRequest{
		Template: tmplA, Script: mustValidateScript(t, minimalScriptJSON),
		Pair: Pair{
			A: Participant{ID: "sess-a", Name: "Session A", Phone: "628110000001"},
			B: Participant{ID: "sess-b", Name: "Session B", Phone: "628110000002"},
		},
		StartAt: clock.Now(), ScheduleID: sched.ID, OccurrenceDate: "2026-09-09",
	})
	if err != nil {
		t.Fatalf("createScheduledRun: %v", err)
	}

	driveRunToDone(t, r, repo, clock, initial.ID)

	runs, err := repo.ListRuns(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("len(runs) after the first run finishes = %d, want 2 (initial + one successor at depth 1)", len(runs))
	}
	var successorID string
	for _, rn := range runs {
		if rn.ChainPrevRunID == initial.ID {
			successorID = rn.ID
			if rn.ChainDepth != 1 {
				t.Fatalf("successor ChainDepth = %d, want 1", rn.ChainDepth)
			}
		}
	}
	if successorID == "" {
		t.Fatal("no successor created at depth 1")
	}

	driveRunToDone(t, r, repo, clock, successorID)

	runsAfter, err := repo.ListRuns(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runsAfter) != 2 {
		t.Fatalf("len(runs) after the depth-1 run finishes = %d, want still 2 (MaxChainDepth=1 must stop the chain here)", len(runsAfter))
	}
	for _, rn := range runsAfter {
		if rn.State != convo.RunDone {
			t.Fatalf("run %s State = %q, want done (MaxChainDepth ends a chain quietly, never as a failure)", rn.ID, rn.State)
		}
	}
}

// TestARunsWordingIsReproducibleFromItsStoredParaphraseSeed proves
// createScheduledRun's central claim: re-expanding the SAME script/pair with
// the run's own stored ParaphraseSeed reproduces byte-identical turn bodies.
func TestARunsWordingIsReproducibleFromItsStoredParaphraseSeed(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	r, repo, _ := newTestRunner(t, clock)
	tmpl := seedExampleTemplate(t, repo)

	run, err := r.Trigger(context.Background(), TriggerRequest{
		TemplateID: tmpl.ID, TickedSessionIDs: []string{"sess-a", "sess-b"},
	})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}

	stored, err := repo.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	turns, err := repo.Turns(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}

	script := mustValidateScript(t, string(ExampleJSON))
	pair := Pair{
		A: Participant{ID: run.SessionAID, Name: run.SessionAName, Phone: run.PhoneA},
		B: Participant{ID: run.SessionBID, Name: run.SessionBName, Phone: run.PhoneB},
	}
	_, reExpanded, err := r.expandWithSeed(script, pair, stored.ParaphraseSeed)
	if err != nil {
		t.Fatalf("expandWithSeed: %v", err)
	}

	if len(reExpanded) != len(turns) {
		t.Fatalf("len(reExpanded) = %d, want %d", len(reExpanded), len(turns))
	}
	for i, et := range reExpanded {
		if et.Body != turns[i].Body {
			t.Fatalf("turn %d: re-expanded body = %q, want stored body %q", i, et.Body, turns[i].Body)
		}
	}
}

// ---- multi-receiver fan-out ----

// TestStartOccurrenceFansOutOneRunPerReceiverWithDistinctBodiesAndStartTimes
// pins StartOccurrence's contract: one run per receiver, sender always A,
// distinct wording per receiver (independent ParaphraseSeed/Vary draw) and
// distinct, non-colliding StartAt per receiver.
func TestStartOccurrenceFansOutOneRunPerReceiverWithDistinctBodiesAndStartTimes(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	r, repo, gw := newTestRunner(t, clock)
	gw.seed(Participant{ID: "sess-c", Name: "Session C", Phone: "628110000003", Connected: true})
	gw.seed(Participant{ID: "sess-d", Name: "Session D", Phone: "628110000004", Connected: true})

	raw := `{
		"version": 1,
		"vars": { "place": ["Bandung", "Bogor", "Cimahi", "Garut", "Sumedang"] },
		"turns": [
			{ "speaker": "a", "text": "Mau ke {{place}} gak?" },
			{ "speaker": "b", "text": "Boleh" }
		]
	}`
	script := mustValidateScript(t, raw)
	tmpl := convo.Template{ID: "tpl-fanout", Name: "fanout", TurnCount: len(script.Turns), Body: raw}
	if err := repo.InsertTemplate(context.Background(), &tmpl); err != nil {
		t.Fatalf("InsertTemplate: %v", err)
	}

	sched := seedTestSchedule(t, repo, "sch-fanout", false, tmpl.ID)
	receivers := []convo.ScheduleReceiver{
		{SessionID: "sess-b", SessionName: "Session B", Phone: "628110000002", Position: 0},
		{SessionID: "sess-c", SessionName: "Session C", Phone: "628110000003", Position: 1},
		{SessionID: "sess-d", SessionName: "Session D", Phone: "628110000004", Position: 2},
	}

	runs, err := r.StartOccurrence(context.Background(), sched, receivers, tmpl.ID, "2026-09-09", clock.Now())
	if err != nil {
		t.Fatalf("StartOccurrence: %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("len(runs) = %d, want 3", len(runs))
	}

	seenBodies := map[string]bool{}
	seenStarts := map[int64]bool{}
	for _, run := range runs {
		if run.SessionAID != sched.SenderSessionID {
			t.Errorf("run %s SessionAID = %q, want sender %q", run.ID, run.SessionAID, sched.SenderSessionID)
		}
		if run.SessionBID == sched.SenderSessionID {
			t.Errorf("run %s SessionBID == sender, want a receiver", run.ID)
		}
		turns, err := repo.Turns(context.Background(), run.ID)
		if err != nil {
			t.Fatalf("Turns(%s): %v", run.ID, err)
		}
		if len(turns) == 0 {
			t.Fatalf("run %s has no turns", run.ID)
		}
		seenBodies[turns[0].Body] = true

		key := run.StartAt.UnixNano()
		if seenStarts[key] {
			t.Errorf("run %s StartAt %v collides with another receiver's — want every receiver's start distinct",
				run.ID, run.StartAt)
		}
		seenStarts[key] = true
	}
	if len(seenBodies) < 2 {
		t.Errorf("all 3 receivers got the identical first-turn body %v, want at least 2 distinct wordings", seenBodies)
	}
}

// ---- health gate ----

// TestAnUnhealthyParticipantMidRunFailsTheScheduledRunAndStallsItsSchedule
// pins processRun's schedule health gate: a scheduled run whose participant
// Gateway.Healthy reports unhealthy must fail with a reason naming the
// session AND move its schedule to stalled with that same reason.
func TestAnUnhealthyParticipantMidRunFailsTheScheduledRunAndStallsItsSchedule(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	r, repo, gw := newTestRunner(t, clock)
	tmpl := seedExampleTemplate(t, repo)
	sched := seedTestSchedule(t, repo, "sch-health", false, tmpl.ID)

	run, err := r.createScheduledRun(context.Background(), scheduledRunRequest{
		Template: tmpl, Script: mustValidateScript(t, string(ExampleJSON)),
		Pair: Pair{
			A: Participant{ID: "sess-a", Name: "Session A", Phone: "628110000001"},
			B: Participant{ID: "sess-b", Name: "Session B", Phone: "628110000002"},
		},
		StartAt: clock.Now(), ScheduleID: sched.ID, OccurrenceDate: "2026-09-09",
	})
	if err != nil {
		t.Fatalf("createScheduledRun: %v", err)
	}

	gw.setUnhealthy("sess-b", "session Session B (sess-b) is not connected")

	if _, err := r.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	final, err := repo.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if final.State != convo.RunFailed {
		t.Fatalf("run.State = %q, want failed", final.State)
	}
	if !strings.Contains(final.Error, "sess-b") {
		t.Errorf("run.Error = %q, want it to name sess-b", final.Error)
	}

	gotSched, err := repo.GetSchedule(context.Background(), sched.ID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if gotSched.State != convo.ScheduleStalled {
		t.Fatalf("schedule.State = %q, want stalled", gotSched.State)
	}
	if !strings.Contains(gotSched.PausedReason, "sess-b") {
		t.Errorf("schedule.PausedReason = %q, want it to name sess-b", gotSched.PausedReason)
	}
}

// TestAManualRunIsUnaffectedByHealthEvenWhenAParticipantIsMarkedUnhealthy
// pins the other half: a manually triggered run (empty ScheduleID) must
// never even consult Gateway.Healthy — only Participant.Connected, exactly
// as before this phase.
func TestAManualRunIsUnaffectedByHealthEvenWhenAParticipantIsMarkedUnhealthy(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	r, repo, gw := newTestRunner(t, clock)
	tmpl := seedExampleTemplate(t, repo)

	run, err := r.Trigger(context.Background(), TriggerRequest{
		TemplateID: tmpl.ID, TickedSessionIDs: []string{"sess-a", "sess-b"},
	})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}

	// Still Connected (Participant/pa.Connected checks pass) but marked
	// unhealthy — a manual run must never ask Gateway.Healthy at all.
	gw.setUnhealthy("sess-b", "should never be consulted for a manual run")

	if _, err := r.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got, err := repo.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State == convo.RunFailed {
		t.Fatal("manual run failed after marking a participant unhealthy — the manual path must not consult Gateway.Healthy at all")
	}
}
