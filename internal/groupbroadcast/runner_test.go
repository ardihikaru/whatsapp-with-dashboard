package groupbroadcast

import (
	"context"
	"testing"
	"time"

	gb "github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/groupbroadcast"
)

func seedOccurrenceWithTurns(t *testing.T, repo *gb.Repository, occID string, turns []gb.Turn) gb.Occurrence {
	t.Helper()
	occ := &gb.Occurrence{ID: occID, ScheduleID: "sch-1", OccurrenceDate: "2026-09-18", GroupJID: "120363426585371342@g.us", State: gb.OccurrenceScheduled}
	if err := repo.CreateOccurrence(context.Background(), occ, turns); err != nil {
		t.Fatalf("CreateOccurrence: %v", err)
	}
	return *occ
}

// TestTickSendsADueSenderTurnAndMarksItSent is the runner's positive path.
func TestTickSendsADueSenderTurnAndMarksItSent(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 18, 7, 58, 0, 0, time.UTC))
	r, repo, gw := newTestRunner(t, clock)
	gw.seed(Participant{ID: "sess-sender", Name: "Sender", Connected: true})

	seedOccurrenceWithTurns(t, repo, "occ-1", []gb.Turn{
		{ID: "t-sender", Role: gb.RoleSender, SessionID: "sess-sender", SessionName: "Sender",
			Language: gb.LangID, Body: "pagi tim!", Source: gb.SourceFallback, DueAt: clock.Now()},
	})

	if _, err := r.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if gw.sendCount() != 1 {
		t.Fatalf("sendCount = %d, want 1", gw.sendCount())
	}

	turns, err := repo.TurnsForOccurrence(context.Background(), "occ-1")
	if err != nil {
		t.Fatalf("TurnsForOccurrence: %v", err)
	}
	if turns[0].State != gb.TurnSent {
		t.Errorf("turn state = %q, want %q", turns[0].State, gb.TurnSent)
	}

	occ, err := repo.GetOccurrence(context.Background(), "occ-1")
	if err != nil {
		t.Fatalf("GetOccurrence: %v", err)
	}
	if occ.State != gb.OccurrenceDone {
		t.Errorf("occurrence state = %q, want %q", occ.State, gb.OccurrenceDone)
	}
}

// TestATurnIsNotSentBlindlyWhenItsSessionIsNoLongerConnectedAtSendTime is
// the explicit re-check-before-dispatch test (confirmed decision #8): a
// receiver that disconnects after planning but before its send time must be
// skipped at Tick, never sent.
func TestATurnIsNotSentBlindlyWhenItsSessionIsNoLongerConnectedAtSendTime(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 18, 8, 20, 0, 0, time.UTC))
	r, repo, gw := newTestRunner(t, clock)
	gw.seed(Participant{ID: "sess-sender", Name: "Sender", Connected: true})
	gw.seed(Participant{ID: "sess-r1", Name: "R1", Connected: true})
	gw.seed(Participant{ID: "sess-r2", Name: "R2", Connected: true})

	seedOccurrenceWithTurns(t, repo, "occ-1", []gb.Turn{
		{ID: "t-sender", Role: gb.RoleSender, SessionID: "sess-sender", SessionName: "Sender",
			Language: gb.LangID, Body: "pagi tim!", Source: gb.SourceFallback,
			DueAt: clock.Now().Add(-time.Hour), State: gb.TurnSent, SentAt: ptrTime(clock.Now().Add(-time.Hour))},
		{ID: "t-r1", Role: gb.RoleReceiver, SessionID: "sess-r1", SessionName: "R1",
			Language: gb.LangID, Body: "pagi juga", Source: gb.SourceFallback, DueAt: clock.Now()},
		{ID: "t-r2", Role: gb.RoleReceiver, SessionID: "sess-r2", SessionName: "R2",
			Language: gb.LangID, Body: "pagi juga", Source: gb.SourceFallback, DueAt: clock.Now()},
	})

	// sess-r1 dropped between planning and its scheduled slot.
	gw.setUnhealthy("sess-r1", "session R1 is not connected")

	if _, err := r.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if gw.sendCount() != 1 {
		t.Fatalf("sendCount = %d, want exactly 1 (only sess-r2, sess-r1 must never be sent)", gw.sendCount())
	}
	sent := gw.sends[0]
	if sent.SessionID != "sess-r2" {
		t.Fatalf("the only Send call was for %q, want sess-r2", sent.SessionID)
	}

	turns, err := repo.TurnsForOccurrence(context.Background(), "occ-1")
	if err != nil {
		t.Fatalf("TurnsForOccurrence: %v", err)
	}
	for _, tn := range turns {
		switch tn.ID {
		case "t-r1":
			if tn.State != gb.TurnSkipped {
				t.Errorf("t-r1 state = %q, want %q (unhealthy at send time)", tn.State, gb.TurnSkipped)
			}
		case "t-r2":
			if tn.State != gb.TurnSent {
				t.Errorf("t-r2 state = %q, want %q", tn.State, gb.TurnSent)
			}
		}
	}
}

// TestASenderTurnFailingItsReCheckFailsTheWholeOccurrenceAndSkipsPendingReceivers
// is the sender-side mirror of the test above: the sender re-check failing
// must fail the whole occurrence and skip every remaining pending receiver
// turn WITHOUT ever calling Send for any of them.
func TestASenderTurnFailingItsReCheckFailsTheWholeOccurrenceAndSkipsPendingReceivers(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC))
	r, repo, gw := newTestRunner(t, clock)
	gw.seed(Participant{ID: "sess-sender", Name: "Sender", Connected: true})
	gw.seed(Participant{ID: "sess-r1", Name: "R1", Connected: true})

	seedOccurrenceWithTurns(t, repo, "occ-1", []gb.Turn{
		{ID: "t-sender", Role: gb.RoleSender, SessionID: "sess-sender", SessionName: "Sender",
			Language: gb.LangID, Body: "pagi tim!", Source: gb.SourceFallback, DueAt: clock.Now()},
		{ID: "t-r1", Role: gb.RoleReceiver, SessionID: "sess-r1", SessionName: "R1",
			Language: gb.LangID, Body: "pagi juga", Source: gb.SourceFallback, DueAt: clock.Now().Add(30 * time.Minute)},
	})

	// The sender dropped before its own scheduled slot fired.
	gw.setUnhealthy("sess-sender", "session Sender is not connected")

	if _, err := r.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if gw.sendCount() != 0 {
		t.Fatalf("sendCount = %d, want 0 (sender never sent, receiver must never be sent either)", gw.sendCount())
	}

	turns, err := repo.TurnsForOccurrence(context.Background(), "occ-1")
	if err != nil {
		t.Fatalf("TurnsForOccurrence: %v", err)
	}
	for _, tn := range turns {
		switch tn.ID {
		case "t-sender":
			if tn.State != gb.TurnSkipped {
				t.Errorf("t-sender state = %q, want %q", tn.State, gb.TurnSkipped)
			}
		case "t-r1":
			if tn.State != gb.TurnSkipped {
				t.Errorf("t-r1 state = %q, want %q (must be cancelled, not left pending or sent)", tn.State, gb.TurnSkipped)
			}
		}
	}

	occ, err := repo.GetOccurrence(context.Background(), "occ-1")
	if err != nil {
		t.Fatalf("GetOccurrence: %v", err)
	}
	if occ.State != gb.OccurrenceFailed {
		t.Errorf("occurrence state = %q, want %q", occ.State, gb.OccurrenceFailed)
	}
}

// TestASendErrorOnASenderTurnAlsoFailsTheWholeOccurrence covers the wire
// failure path (not just the health re-check): gw.Send itself returning an
// error for the sender turn must fail the occurrence and skip the pending
// receiver, without ever calling Send for it.
func TestASendErrorOnASenderTurnAlsoFailsTheWholeOccurrence(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC))
	r, repo, gw := newTestRunner(t, clock)
	gw.seed(Participant{ID: "sess-sender", Name: "Sender", Connected: true})
	gw.seed(Participant{ID: "sess-r1", Name: "R1", Connected: true})
	gw.sendErr = context.DeadlineExceeded

	seedOccurrenceWithTurns(t, repo, "occ-1", []gb.Turn{
		{ID: "t-sender", Role: gb.RoleSender, SessionID: "sess-sender", SessionName: "Sender",
			Language: gb.LangID, Body: "pagi tim!", Source: gb.SourceFallback, DueAt: clock.Now()},
		{ID: "t-r1", Role: gb.RoleReceiver, SessionID: "sess-r1", SessionName: "R1",
			Language: gb.LangID, Body: "pagi juga", Source: gb.SourceFallback, DueAt: clock.Now().Add(30 * time.Minute)},
	})

	if _, err := r.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if gw.sendCount() != 1 {
		t.Fatalf("sendCount = %d, want 1 (only the failed sender attempt, receiver never attempted)", gw.sendCount())
	}

	turns, err := repo.TurnsForOccurrence(context.Background(), "occ-1")
	if err != nil {
		t.Fatalf("TurnsForOccurrence: %v", err)
	}
	for _, tn := range turns {
		switch tn.ID {
		case "t-sender":
			if tn.State != gb.TurnFailed {
				t.Errorf("t-sender state = %q, want %q", tn.State, gb.TurnFailed)
			}
		case "t-r1":
			if tn.State != gb.TurnSkipped {
				t.Errorf("t-r1 state = %q, want %q", tn.State, gb.TurnSkipped)
			}
		}
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
