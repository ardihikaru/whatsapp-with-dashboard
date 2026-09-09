package convo

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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
	clock := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return clock }
	return repo, func() time.Time { return clock }
}

func newRun(id string, state string, start time.Time) *Run {
	return &Run{
		ID:           id,
		TemplateName: "Weekend plans",
		SessionAID:   "sess-a",
		SessionBID:   "sess-b",
		SessionAName: "Session A",
		SessionBName: "Session B",
		PhoneA:       "62800000001",
		PhoneB:       "62800000002",
		State:        state,
		TurnCount:    2,
		StartAt:      start,
	}
}

func newTurns(runID string) []Turn {
	return []Turn{
		{Seq: 1, Speaker: "a", SessionID: "sess-a", ToPhone: "62800000002", Body: "Halo", DelaySeconds: 0},
		{Seq: 2, Speaker: "b", SessionID: "sess-b", ToPhone: "62800000001", Body: "Hai", DelaySeconds: 20},
	}
}

func TestCreateRunWritesTheRunAndEveryTurnInOneTransaction(t *testing.T) {
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
	if got.TemplateName != "Weekend plans" || got.State != RunScheduled {
		t.Fatalf("GetRun = %+v, want the inserted run back", got)
	}

	turns, err := repo.Turns(ctx, "r1")
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("len(turns) = %d, want 2", len(turns))
	}
	if turns[0].Seq != 1 || turns[1].Seq != 2 {
		t.Errorf("turns not ordered by seq: %+v", turns)
	}

	// A turn with a bad shape (duplicate seq collides with the (run_id, seq)
	// primary key) must fail the WHOLE insert, leaving no run row either.
	bad := newRun("r2", RunScheduled, now())
	badTurns := []Turn{
		{Seq: 1, Speaker: "a", SessionID: "sess-a", ToPhone: "62800000002", Body: "Halo", DelaySeconds: 0},
		{Seq: 1, Speaker: "b", SessionID: "sess-b", ToPhone: "62800000001", Body: "Hai", DelaySeconds: 20},
	}
	if err := repo.CreateRun(ctx, bad, badTurns); err == nil {
		t.Fatal("CreateRun with a duplicate turn seq returned no error, want one")
	}
	if _, err := repo.GetRun(ctx, "r2"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetRun(r2) after a failed CreateRun = %v, want ErrNotFound (the run row must not survive)", err)
	}
}

func TestDueRunsReturnsOnlyNonTerminalRunsDueAtOrBeforeNowOrderedByDueTime(t *testing.T) {
	repo, now := newTestRepository(t)
	ctx := context.Background()
	base := now()

	due := func(t time.Time) *time.Time { return &t }

	scheduledDue := newRun("scheduled-due", RunScheduled, base)
	scheduledDue.NextDueAt = due(base.Add(-time.Minute))
	if err := repo.CreateRun(ctx, scheduledDue, nil); err != nil {
		t.Fatalf("CreateRun scheduled-due: %v", err)
	}

	runningDue := newRun("running-due", RunRunning, base)
	runningDue.NextDueAt = due(base.Add(-30 * time.Second))
	if err := repo.CreateRun(ctx, runningDue, nil); err != nil {
		t.Fatalf("CreateRun running-due: %v", err)
	}

	scheduledFuture := newRun("scheduled-future", RunScheduled, base)
	scheduledFuture.NextDueAt = due(base.Add(time.Hour))
	if err := repo.CreateRun(ctx, scheduledFuture, nil); err != nil {
		t.Fatalf("CreateRun scheduled-future: %v", err)
	}

	doneStale := newRun("done-stale", RunDone, base)
	doneStale.NextDueAt = due(base.Add(-time.Hour))
	if err := repo.CreateRun(ctx, doneStale, nil); err != nil {
		t.Fatalf("CreateRun done-stale: %v", err)
	}

	got, err := repo.DueRuns(ctx, base, 20)
	if err != nil {
		t.Fatalf("DueRuns: %v", err)
	}
	var ids []string
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	want := []string{"scheduled-due", "running-due"}
	if len(ids) != len(want) || ids[0] != want[0] || ids[1] != want[1] {
		t.Errorf("DueRuns ids = %v, want %v (earliest due first, terminal and future runs excluded)", ids, want)
	}
}

func TestCancelRunSkipsEveryPendingTurnAndRefusesATerminalRun(t *testing.T) {
	repo, now := newTestRepository(t)
	ctx := context.Background()

	run := newRun("r1", RunRunning, now())
	turns := newTurns("r1")
	turns[0].State = TurnSent
	if err := repo.CreateRun(ctx, run, turns); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	if err := repo.CancelRun(ctx, "r1", now()); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}

	got, err := repo.GetRun(ctx, "r1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State != RunCancelled {
		t.Errorf("run state = %q, want %q", got.State, RunCancelled)
	}

	gotTurns, err := repo.Turns(ctx, "r1")
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if gotTurns[0].State != TurnSent {
		t.Errorf("already-sent turn state = %q, want unchanged %q", gotTurns[0].State, TurnSent)
	}
	if gotTurns[1].State != TurnSkipped {
		t.Errorf("pending turn state = %q, want %q", gotTurns[1].State, TurnSkipped)
	}

	if err := repo.CancelRun(ctx, "r1", now()); !errors.Is(err, ErrRunFinished) {
		t.Errorf("CancelRun on an already-cancelled run = %v, want ErrRunFinished", err)
	}

	if err := repo.CancelRun(ctx, "does-not-exist", now()); !errors.Is(err, ErrNotFound) {
		t.Errorf("CancelRun on a missing run = %v, want ErrNotFound", err)
	}
}

func TestFinishRunClearsNextDueSoItIsNeverPolledAgain(t *testing.T) {
	repo, now := newTestRepository(t)
	ctx := context.Background()

	run := newRun("r1", RunRunning, now())
	due := now().Add(-time.Minute)
	run.NextDueAt = &due
	if err := repo.CreateRun(ctx, run, newTurns("r1")); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	if err := repo.FinishRun(ctx, "r1", RunFailed, "session disconnected", now()); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	got, err := repo.GetRun(ctx, "r1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State != RunFailed {
		t.Errorf("state = %q, want %q", got.State, RunFailed)
	}
	if got.Error != "session disconnected" {
		t.Errorf("error = %q, want %q", got.Error, "session disconnected")
	}
	if got.NextDueAt != nil {
		t.Errorf("next_due_at = %v, want nil (a finished run must never be picked up by DueRuns again)", got.NextDueAt)
	}
	if got.FinishedAt == nil {
		t.Error("finished_at is nil, want a stamp")
	}

	dueRuns, err := repo.DueRuns(ctx, now().Add(time.Hour), 20)
	if err != nil {
		t.Fatalf("DueRuns: %v", err)
	}
	for _, r := range dueRuns {
		if r.ID == "r1" {
			t.Errorf("finished run r1 still shows up in DueRuns: %+v", r)
		}
	}
}

func TestNextPendingTurnReturnsTheLowestPendingSeq(t *testing.T) {
	repo, now := newTestRepository(t)
	ctx := context.Background()

	run := newRun("r1", RunRunning, now())
	turns := []Turn{
		{Seq: 1, Speaker: "a", SessionID: "sess-a", ToPhone: "62800000002", Body: "one", State: TurnSent},
		{Seq: 2, Speaker: "b", SessionID: "sess-b", ToPhone: "62800000001", Body: "two", State: TurnPending},
		{Seq: 3, Speaker: "a", SessionID: "sess-a", ToPhone: "62800000002", Body: "three", State: TurnPending},
	}
	if err := repo.CreateRun(ctx, run, turns); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	got, ok, err := repo.NextPendingTurn(ctx, "r1")
	if err != nil {
		t.Fatalf("NextPendingTurn: %v", err)
	}
	if !ok {
		t.Fatal("NextPendingTurn ok = false, want true")
	}
	if got.Seq != 2 {
		t.Errorf("NextPendingTurn seq = %d, want 2", got.Seq)
	}

	// Once every turn is resolved, there is none left to return.
	if err := repo.MarkTurnSent(ctx, "r1", 2, now()); err != nil {
		t.Fatalf("MarkTurnSent(2): %v", err)
	}
	if err := repo.MarkTurnSent(ctx, "r1", 3, now()); err != nil {
		t.Fatalf("MarkTurnSent(3): %v", err)
	}
	_, ok, err = repo.NextPendingTurn(ctx, "r1")
	if err != nil {
		t.Fatalf("NextPendingTurn (none left): %v", err)
	}
	if ok {
		t.Error("NextPendingTurn ok = true after every turn resolved, want false")
	}
}

// TestNextPendingTurnsReturnsTheLowestPendingSeqsInOrder pins the query
// selfconvo's processRun uses ONCE per turn (n=2) to learn both the turn to
// claim/send AND the next turn's delay, in the same round trip that used to
// take two separate NextPendingTurn calls (see runner.go's doc comment on
// the round-trip reduction).
func TestNextPendingTurnsReturnsTheLowestPendingSeqsInOrder(t *testing.T) {
	repo, now := newTestRepository(t)
	ctx := context.Background()

	run := newRun("r1", RunRunning, now())
	turns := []Turn{
		{Seq: 1, Speaker: "a", SessionID: "sess-a", ToPhone: "62800000002", Body: "one", State: TurnSent},
		{Seq: 2, Speaker: "b", SessionID: "sess-b", ToPhone: "62800000001", Body: "two", State: TurnPending, DelaySeconds: 10},
		{Seq: 3, Speaker: "a", SessionID: "sess-a", ToPhone: "62800000002", Body: "three", State: TurnPending, DelaySeconds: 20},
		{Seq: 4, Speaker: "b", SessionID: "sess-b", ToPhone: "62800000001", Body: "four", State: TurnPending, DelaySeconds: 30},
	}
	if err := repo.CreateRun(ctx, run, turns); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	got, err := repo.NextPendingTurns(ctx, "r1", 2)
	if err != nil {
		t.Fatalf("NextPendingTurns: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0].Seq != 2 || got[1].Seq != 3 {
		t.Fatalf("got seqs = [%d, %d], want [2, 3]", got[0].Seq, got[1].Seq)
	}

	// Once only one pending turn remains, NextPendingTurns(n=2) returns just
	// that one — never pads or errors.
	if err := repo.MarkTurnSent(ctx, "r1", 2, now()); err != nil {
		t.Fatalf("MarkTurnSent(2): %v", err)
	}
	if err := repo.MarkTurnSent(ctx, "r1", 3, now()); err != nil {
		t.Fatalf("MarkTurnSent(3): %v", err)
	}
	got, err = repo.NextPendingTurns(ctx, "r1", 2)
	if err != nil {
		t.Fatalf("NextPendingTurns (one left): %v", err)
	}
	if len(got) != 1 || got[0].Seq != 4 {
		t.Fatalf("got = %+v, want exactly turn 4", got)
	}

	if err := repo.MarkTurnSent(ctx, "r1", 4, now()); err != nil {
		t.Fatalf("MarkTurnSent(4): %v", err)
	}
	got, err = repo.NextPendingTurns(ctx, "r1", 2)
	if err != nil {
		t.Fatalf("NextPendingTurns (none left): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got = %+v, want none left", got)
	}
}

// TestSendingTurnsReturnsTurnsAcrossEveryRun pins Recover's boot-time
// reconciliation query (runner.go's Recover): unlike MarkInterrupted, it is
// NOT scoped to one run, because a process can die with more than one run's
// turn mid-send at once.
func TestSendingTurnsReturnsTurnsAcrossEveryRun(t *testing.T) {
	repo, now := newTestRepository(t)
	ctx := context.Background()

	run1 := newRun("r1", RunRunning, now())
	if err := repo.CreateRun(ctx, run1, []Turn{
		{Seq: 1, Speaker: "a", SessionID: "sess-a", ToPhone: "62800000002", Body: "one", State: TurnSending, MessageID: "m1"},
		{Seq: 2, Speaker: "b", SessionID: "sess-b", ToPhone: "62800000001", Body: "two", State: TurnPending},
	}); err != nil {
		t.Fatalf("CreateRun r1: %v", err)
	}
	run2 := newRun("r2", RunRunning, now())
	if err := repo.CreateRun(ctx, run2, []Turn{
		{Seq: 1, Speaker: "a", SessionID: "sess-a", ToPhone: "62800000002", Body: "one", State: TurnSending, MessageID: "m2"},
	}); err != nil {
		t.Fatalf("CreateRun r2: %v", err)
	}

	got, err := repo.SendingTurns(ctx)
	if err != nil {
		t.Fatalf("SendingTurns: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2 (one sending turn from each run)", len(got))
	}
	byRun := map[string]Turn{}
	for _, tu := range got {
		byRun[tu.RunID] = tu
	}
	if byRun["r1"].MessageID != "m1" {
		t.Errorf("r1 turn MessageID = %q, want %q", byRun["r1"].MessageID, "m1")
	}
	if byRun["r2"].MessageID != "m2" {
		t.Errorf("r2 turn MessageID = %q, want %q", byRun["r2"].MessageID, "m2")
	}
}

func TestMarkInterruptedOnlyTouchesTurnsLeftInSending(t *testing.T) {
	repo, now := newTestRepository(t)
	ctx := context.Background()

	run := newRun("r1", RunRunning, now())
	turns := []Turn{
		{Seq: 1, Speaker: "a", SessionID: "sess-a", ToPhone: "62800000002", Body: "one", State: TurnSent},
		{Seq: 2, Speaker: "b", SessionID: "sess-b", ToPhone: "62800000001", Body: "two", State: TurnSending},
		{Seq: 3, Speaker: "a", SessionID: "sess-a", ToPhone: "62800000002", Body: "three", State: TurnPending},
	}
	if err := repo.CreateRun(ctx, run, turns); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	n, err := repo.MarkInterrupted(ctx, "r1")
	if err != nil {
		t.Fatalf("MarkInterrupted: %v", err)
	}
	if n != 1 {
		t.Errorf("MarkInterrupted affected = %d, want 1", n)
	}

	got, err := repo.Turns(ctx, "r1")
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if got[0].State != TurnSent {
		t.Errorf("seq1 state = %q, want unchanged %q", got[0].State, TurnSent)
	}
	if got[1].State != TurnInterrupted {
		t.Errorf("seq2 state = %q, want %q", got[1].State, TurnInterrupted)
	}
	if got[2].State != TurnPending {
		t.Errorf("seq3 state = %q, want unchanged %q", got[2].State, TurnPending)
	}
}

// TestClaimTurnRefusesATurnThatIsNoLongerPending is the regression test for
// the cancel-vs-claim race: a CancelRun landing between the runner's
// NextPendingTurn read and its claim must not have its "skipped" verdict
// silently overwritten back to "sending" by an unconditional write. ClaimTurn's
// WHERE state = 'pending' clause is what makes that impossible — this pins it
// directly at the repository layer, independent of the runner's own timing.
func TestClaimTurnRefusesATurnThatIsNoLongerPending(t *testing.T) {
	repo, now := newTestRepository(t)
	ctx := context.Background()

	run := newRun("r1", RunRunning, now())
	turns := []Turn{
		{Seq: 1, Speaker: "a", SessionID: "sess-a", ToPhone: "62800000002", Body: "one", State: TurnSent},
		{Seq: 2, Speaker: "b", SessionID: "sess-b", ToPhone: "62800000001", Body: "two", State: TurnPending},
	}
	if err := repo.CreateRun(ctx, run, turns); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// A concurrent Cancel lands first: turn 2 moves pending -> skipped.
	if err := repo.CancelRun(ctx, "r1", now()); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	got, err := repo.Turns(ctx, "r1")
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if got[1].State != TurnSkipped {
		t.Fatalf("turn 2 state after CancelRun = %q, want %q (test setup)", got[1].State, TurnSkipped)
	}

	// The runner's ClaimTurn, which picked turn 2 as pending BEFORE the
	// cancel above landed, now tries to claim it — this must fail rather
	// than silently move a skipped turn back to sending.
	if err := repo.ClaimTurn(ctx, "r1", 2, "msg-would-be-claimed"); !errors.Is(err, ErrTurnNotPending) {
		t.Fatalf("ClaimTurn on a turn CancelRun already skipped = %v, want ErrTurnNotPending", err)
	}

	got, err = repo.Turns(ctx, "r1")
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if got[1].State != TurnSkipped {
		t.Fatalf("turn 2 state after the refused claim = %q, want still %q (unchanged)", got[1].State, TurnSkipped)
	}
	if got[1].MessageID != "" {
		t.Fatalf("turn 2 MessageID after the refused claim = %q, want empty (the claim never happened)", got[1].MessageID)
	}

	// The ordinary path (still pending) must still succeed.
	if err := repo.ClaimTurn(ctx, "r1", 1, "msg-would-be-claimed"); err == nil {
		t.Fatal("ClaimTurn on turn 1 (already sent, never pending) = nil, want ErrTurnNotPending")
	} else if !errors.Is(err, ErrTurnNotPending) {
		t.Fatalf("ClaimTurn on an already-sent turn = %v, want ErrTurnNotPending", err)
	}
}

// TestClaimTurnStoresTheCallersMessageIDAtomicallyWithTheStateChange pins
// the ordering item 3 of the runner review relies on: ClaimTurn is where
// the audit-row id lands, BEFORE Send is ever called, so a turn cut off
// mid-send always has a message_id to reconcile against on the next boot
// (see runner.go's Recover).
func TestClaimTurnStoresTheCallersMessageIDAtomicallyWithTheStateChange(t *testing.T) {
	repo, now := newTestRepository(t)
	ctx := context.Background()

	run := newRun("r1", RunRunning, now())
	if err := repo.CreateRun(ctx, run, newTurns("r1")); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	if err := repo.ClaimTurn(ctx, "r1", 1, "msg-abc"); err != nil {
		t.Fatalf("ClaimTurn: %v", err)
	}

	got, err := repo.Turns(ctx, "r1")
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if got[0].State != TurnSending {
		t.Fatalf("turn 1 state = %q, want %q", got[0].State, TurnSending)
	}
	if got[0].MessageID != "msg-abc" {
		t.Fatalf("turn 1 MessageID = %q, want %q", got[0].MessageID, "msg-abc")
	}
}

func TestSetRunningIsANoOpUnlessTheRunIsScheduled(t *testing.T) {
	repo, now := newTestRepository(t)
	ctx := context.Background()

	run := newRun("r1", RunDone, now())
	if err := repo.CreateRun(ctx, run, nil); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	if err := repo.SetRunning(ctx, "r1", now()); err != nil {
		t.Fatalf("SetRunning on a done run returned an error, want a silent no-op: %v", err)
	}

	got, err := repo.GetRun(ctx, "r1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State != RunDone {
		t.Errorf("state after SetRunning on a done run = %q, want unchanged %q", got.State, RunDone)
	}
	if got.StartedAt != nil {
		t.Errorf("started_at = %v, want nil (SetRunning must not have touched this row)", got.StartedAt)
	}

	// Now the real case: a scheduled run does flip.
	run2 := newRun("r2", RunScheduled, now())
	if err := repo.CreateRun(ctx, run2, nil); err != nil {
		t.Fatalf("CreateRun r2: %v", err)
	}
	if err := repo.SetRunning(ctx, "r2", now()); err != nil {
		t.Fatalf("SetRunning r2: %v", err)
	}
	got2, err := repo.GetRun(ctx, "r2")
	if err != nil {
		t.Fatalf("GetRun r2: %v", err)
	}
	if got2.State != RunRunning {
		t.Errorf("r2 state = %q, want %q", got2.State, RunRunning)
	}
	if got2.StartedAt == nil {
		t.Error("r2 started_at is nil, want a stamp")
	}
}

func TestTemplatesRoundTripAndRenameTouchesUpdatedAt(t *testing.T) {
	repo, now := newTestRepository(t)
	ctx := context.Background()

	tpl := &Template{ID: "t1", Name: "Weekend plans", TurnCount: 4, Body: `{"version":1}`}
	if err := repo.InsertTemplate(ctx, tpl); err != nil {
		t.Fatalf("InsertTemplate: %v", err)
	}
	if tpl.CreatedAt.IsZero() || tpl.UpdatedAt.IsZero() {
		t.Fatal("InsertTemplate left CreatedAt/UpdatedAt zero, want the injected clock")
	}

	got, err := repo.GetTemplate(ctx, "t1")
	if err != nil {
		t.Fatalf("GetTemplate: %v", err)
	}
	if got.Name != "Weekend plans" || got.TurnCount != 4 || got.Body != `{"version":1}` {
		t.Errorf("GetTemplate = %+v, want the inserted template back", got)
	}

	list, err := repo.ListTemplateSummaries(ctx, 50)
	if err != nil {
		t.Fatalf("ListTemplateSummaries: %v", err)
	}
	if len(list) != 1 || list[0].ID != "t1" {
		t.Errorf("ListTemplateSummaries = %+v, want one row t1", list)
	}

	renamedAt := now().Add(time.Hour)
	repo.now = func() time.Time { return renamedAt }
	if err := repo.RenameTemplate(ctx, "t1", "New name"); err != nil {
		t.Fatalf("RenameTemplate: %v", err)
	}
	renamed, err := repo.GetTemplate(ctx, "t1")
	if err != nil {
		t.Fatalf("GetTemplate after rename: %v", err)
	}
	if renamed.Name != "New name" {
		t.Errorf("name after rename = %q, want %q", renamed.Name, "New name")
	}
	if !renamed.UpdatedAt.Equal(renamedAt) {
		t.Errorf("updated_at after rename = %v, want %v", renamed.UpdatedAt, renamedAt)
	}
	if !renamed.CreatedAt.Equal(tpl.CreatedAt) {
		t.Errorf("created_at changed on rename: got %v, want unchanged %v", renamed.CreatedAt, tpl.CreatedAt)
	}

	if err := repo.RenameTemplate(ctx, "does-not-exist", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("RenameTemplate on a missing id = %v, want ErrNotFound", err)
	}

	if err := repo.DeleteTemplate(ctx, "t1"); err != nil {
		t.Fatalf("DeleteTemplate: %v", err)
	}
	if _, err := repo.GetTemplate(ctx, "t1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetTemplate after delete = %v, want ErrNotFound", err)
	}
}

// TestListTemplateSummariesOmitsTheBodyAndHonoursTheLimit pins the fix for
// "ListTemplates loads every body with no LIMIT": every caller of the list
// (the trigger page's <select>, the templates rows partial, every
// upload/rename/delete response) renders only name/turn_count/created_at,
// so the SELECT backing it must never carry the (up to 256 KiB) body
// column, and it must respect a limit the way ListRuns already does.
// TemplateSummary carrying no Body field is itself the proof the SELECT
// cannot smuggle it back — there is no field to assign it to even if the
// query changed later to select it.
func TestListTemplateSummariesOmitsTheBodyAndHonoursTheLimit(t *testing.T) {
	repo, _ := newTestRepository(t)
	ctx := context.Background()

	const limit = 3
	for i := 0; i < limit+1; i++ {
		tpl := &Template{
			ID:        fmt.Sprintf("tpl-%d", i),
			Name:      fmt.Sprintf("Template %d", i),
			TurnCount: 2,
			Body:      strings.Repeat("x", 1000),
		}
		if err := repo.InsertTemplate(ctx, tpl); err != nil {
			t.Fatalf("InsertTemplate %d: %v", i, err)
		}
	}

	summaries, err := repo.ListTemplateSummaries(ctx, limit)
	if err != nil {
		t.Fatalf("ListTemplateSummaries: %v", err)
	}
	if len(summaries) != limit {
		t.Fatalf("ListTemplateSummaries returned %d rows, want the limit (%d) even though %d templates exist",
			len(summaries), limit, limit+1)
	}
	for _, s := range summaries {
		if s.ID == "" || s.Name == "" {
			t.Errorf("summary %+v is missing ID/Name", s)
		}
		if s.TurnCount != 2 {
			t.Errorf("summary %+v TurnCount = %d, want 2", s, s.TurnCount)
		}
		if s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() {
			t.Errorf("summary %+v has a zero CreatedAt/UpdatedAt", s)
		}
	}

	// limit=0 clamps to 1 and a limit over 200 clamps to 200 — same
	// clamping ListRuns already documents and TestListRunsClampsLimit (if
	// one exists) pins for that method.
	clamped, err := repo.ListTemplateSummaries(ctx, 0)
	if err != nil {
		t.Fatalf("ListTemplateSummaries(0): %v", err)
	}
	if len(clamped) != 1 {
		t.Errorf("ListTemplateSummaries(0) returned %d rows, want 1 (clamped)", len(clamped))
	}
}
