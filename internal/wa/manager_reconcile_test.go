package wa

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/waxum"
)

type reconcileRig struct {
	b   *waxumBackend
	mgr *Manager
	s   *ManagedSession
	t0  time.Time
}

func newReconcileRig(t *testing.T, state State) *reconcileRig {
	t.Helper()
	b := &waxumBackend{}
	mgr := newWaxumTestManager(t, b)
	mgr.cfg.ReconcileGrace = 5 * time.Minute
	s := testSession(mgr, state)
	s.jid = pairedTestJID()
	s.active = true
	registerSession(mgr, s)
	return &reconcileRig{b: b, mgr: mgr, s: s, t0: time.Now()}
}

func (r *reconcileRig) pollOK(st waxum.Status, at time.Time) {
	r.b.sessionState(r.s.ID).obs.recordOK(&st, at)
}

func countEvents(m *Manager, parts ...string) int {
	n := 0
	for _, ev := range m.RecentEvents(0) {
		match := true
		for _, p := range parts {
			if !strings.Contains(ev.Note, p) {
				match = false
			}
		}
		if match {
			n++
		}
	}
	return n
}

// TestTheReconcilerFlagsALoggedOutSessionThatWaxumHoldsLoggedInOnlyAfterTheGrace
// is the 2026-09-30 detector: WSM logged_out vs waxum is_logged_in. Inside the
// grace period nothing is reported (adoption/hysteresis get their chance);
// past it the report names the session, one warn event is written, and a
// second pass does not write another.
func TestTheReconcilerFlagsALoggedOutSessionThatWaxumHoldsLoggedInOnlyAfterTheGrace(t *testing.T) {
	r := newReconcileRig(t, StateLoggedOut)
	r.pollOK(waxum.Status{Status: "logged_in", IsLoggedIn: true, SocketAlive: true}, r.t0)

	r.mgr.reconcileOnce(r.t0)
	if rep := r.mgr.Reconcile(); !rep.OK || len(rep.Mismatches) != 0 {
		t.Fatalf("inside grace: report = %+v, want OK with no mismatches", rep)
	}

	r.mgr.reconcileOnce(r.t0.Add(6 * time.Minute))
	rep := r.mgr.Reconcile()
	if rep.OK || len(rep.Mismatches) != 1 {
		t.Fatalf("past grace: report = %+v, want exactly one mismatch", rep)
	}
	mm := rep.Mismatches[0]
	if mm.SessionID != r.s.ID || mm.Kind != MismatchLoggedOutVsLoggedIn || mm.WSM != "logged_out" {
		t.Errorf("mismatch = %+v", mm)
	}
	if got := countEvents(r.mgr, r.s.ID, "logged_out", "reconcile"); got != 1 {
		t.Fatalf("warn events naming the session = %d, want 1; events=%v", got, r.mgr.RecentEvents(0))
	}
	r.mgr.reconcileOnce(r.t0.Add(7 * time.Minute))
	if got := countEvents(r.mgr, r.s.ID, "reconcile"); got != 1 {
		t.Errorf("a standing mismatch re-announced: %d events, want 1", got)
	}

	// Resolution is reported and the report goes green again.
	r.s.setState(StateConnected)
	r.mgr.reconcileOnce(r.t0.Add(8 * time.Minute))
	if rep := r.mgr.Reconcile(); !rep.OK {
		t.Errorf("after the sessions agree: %+v, want OK", rep)
	}
	if got := countEvents(r.mgr, "cleared"); got != 1 {
		t.Errorf("cleared events = %d, want 1", got)
	}
}

func TestTheReconcilerFlagsConnectedAndDisconnectedDisagreements(t *testing.T) {
	r := newReconcileRig(t, StateConnected)
	r.pollOK(waxum.Status{Status: "connecting", IsLoggedIn: true, SocketAlive: false}, r.t0)
	r.mgr.reconcileOnce(r.t0)
	r.mgr.reconcileOnce(r.t0.Add(6 * time.Minute))
	rep := r.mgr.Reconcile()
	if len(rep.Mismatches) != 1 || rep.Mismatches[0].Kind != MismatchConnectedVsDown {
		t.Fatalf("connected-vs-down: %+v", rep)
	}

	r2 := newReconcileRig(t, StateDisconnected)
	r2.pollOK(waxum.Status{Status: "connected", IsLoggedIn: true, SocketAlive: true}, r2.t0)
	r2.mgr.reconcileOnce(r2.t0)
	r2.mgr.reconcileOnce(r2.t0.Add(6 * time.Minute))
	rep = r2.mgr.Reconcile()
	if len(rep.Mismatches) != 1 || rep.Mismatches[0].Kind != MismatchDisconnectedVsUp {
		t.Fatalf("disconnected-vs-up: %+v", rep)
	}
}

// A never-paired session sitting at waiting_for_qr is a pairing, never a mismatch.
func TestTheReconcilerIgnoresAnUnpairedSession(t *testing.T) {
	r := newReconcileRig(t, StatePairing)
	r.s.jid = types.EmptyJID
	r.pollOK(waxum.Status{Status: "waiting_for_qr"}, r.t0)
	r.mgr.reconcileOnce(r.t0)
	r.mgr.reconcileOnce(r.t0.Add(time.Hour))
	if rep := r.mgr.Reconcile(); !rep.OK {
		t.Fatalf("report = %+v, want OK", rep)
	}
}

// TestTheReconcilerSurfacesAWaxumThatKeepsAnswering429 is the persistent
// 429 case (Retry-After 231683 after suspend): nothing else reports it.
func TestTheReconcilerSurfacesAWaxumThatKeepsAnswering429(t *testing.T) {
	r := newReconcileRig(t, StateConnected)
	obs := &r.b.sessionState(r.s.ID).obs
	obs.recordFailure(waxum.ErrRateLimited, r.t0)

	r.mgr.reconcileOnce(r.t0.Add(time.Minute))
	if rep := r.mgr.Reconcile(); !rep.OK {
		t.Fatalf("a one-minute blip must not alert: %+v", rep)
	}
	obs.recordFailure(waxum.ErrRateLimited, r.t0.Add(2*time.Minute)) // same run: Since stays at t0
	r.mgr.reconcileOnce(r.t0.Add(10 * time.Minute))
	rep := r.mgr.Reconcile()
	if rep.OK || rep.BackendProblem == nil || !rep.BackendProblem.RateLimited || rep.BackendProblem.Sessions != 1 {
		t.Fatalf("report = %+v, want a rate-limited backend problem over 1 session", rep)
	}
	if !rep.BackendProblem.Since.Equal(r.t0) {
		t.Errorf("outage Since = %v, want the first failure %v", rep.BackendProblem.Since, r.t0)
	}
	if got := countEvents(r.mgr, "429"); got != 1 {
		t.Errorf("429 warn events = %d, want 1", got)
	}
	// A successful poll ends the run.
	r.pollOK(waxum.Status{Status: "connected", IsLoggedIn: true, SocketAlive: true}, r.t0.Add(11*time.Minute))
	r.mgr.reconcileOnce(r.t0.Add(12 * time.Minute))
	if rep := r.mgr.Reconcile(); !rep.OK || rep.BackendProblem != nil {
		t.Errorf("after recovery: %+v", rep)
	}
}

func TestTheReconcilerFlagsASessionWaxumDoesNotKnow(t *testing.T) {
	r := newReconcileRig(t, StateConnected)
	r.b.sessionState(r.s.ID).obs.recordFailure(waxum.ErrNotFound, r.t0)
	r.mgr.reconcileOnce(r.t0)
	r.mgr.reconcileOnce(r.t0.Add(6 * time.Minute))
	rep := r.mgr.Reconcile()
	if len(rep.Mismatches) != 1 || rep.Mismatches[0].Kind != MismatchUnknownToWaxum || rep.BackendProblem != nil {
		t.Fatalf("report = %+v, want one unknown_to_waxum and no backend problem", rep)
	}
}

// statusPump must feed the reconciler from its OWN polls (no extra requests):
// a permanently 429ing waxum shows up as a rate-limited failing observation,
// and a reconcile pass adds no waxum calls at all.
func TestStatusPollsFeedTheReconcilerAndAReconcilePassMakesNoRequests(t *testing.T) {
	client, calls := newWaxumFakeServer(t, waxumFakeOpts{alwaysRateLimited: true})
	b := &waxumBackend{client: client, pollInterval: 2 * time.Millisecond}
	mgr := newWaxumTestManager(t, b)
	s := testSession(mgr, StateConnected)
	s.jid = pairedTestJID()
	registerSession(mgr, s)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	b.ensureStatusPump(ctx, s)

	waitForCondition(t, "a failing observation", func() bool {
		for _, o := range b.Observations() {
			if o.SessionID == s.ID && !o.FailingSince.IsZero() && o.RateLimited {
				return true
			}
		}
		return false
	})
	cancel()
	waitForCondition(t, "pump exit", func() bool { return !b.sessionState(s.ID).pumping.Load() })
	before := calls.status.Load()
	mgr.reconcileOnce(time.Now().Add(time.Hour))
	if after := calls.status.Load(); after != before {
		t.Fatalf("reconcileOnce made %d status requests, want 0", after-before)
	}
}

func TestTheReconcileLoopRunsOnItsOwnWhenConfigured(t *testing.T) {
	b := &waxumBackend{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mgr := NewManager(ctx, b, newStubSessions(), newStubMsgs(), nil,
		Config{SendRate: 1, SendBurst: 3, ReconcilePar: 2, ReconcileInterval: 2 * time.Millisecond, ReconcileGrace: time.Millisecond})
	if !mgr.Reconcile().Enabled {
		t.Fatal("Enabled = false with an interval configured")
	}
	waitForCondition(t, "a pass has run", func() bool { return !mgr.Reconcile().CheckedAt.IsZero() })

	off := newWaxumTestManager(t, &waxumBackend{})
	if off.Reconcile().Enabled {
		t.Fatal("Enabled = true with no interval configured")
	}
}

// The terminal verdict must leave its evidence behind (the incident had none).
func TestCommittingLoggedOutRecordsWhatWaxumSaid(t *testing.T) {
	b := &waxumBackend{}
	mgr := newWaxumTestManager(t, b)
	s := testSession(mgr, StateConnected)
	s.jid = pairedTestJID()
	for i := 0; i < loggedOutHysteresisPolls; i++ {
		b.applyStatus(s, &waxum.Status{Status: "waiting_for_qr", Pair: waxum.Pair{Attempts: 7}})
	}
	if countEvents(mgr, "declaring logged_out", `status="waiting_for_qr"`, "is_logged_in=false", "reachability=null", "pair.attempts=7") != 1 {
		t.Fatalf("no evidence event; events=%v", mgr.RecentEvents(0))
	}
}

// countingObserver wraps the real backend and counts consultations.
type countingObserver struct {
	*waxumBackend
	calls int
}

func (c *countingObserver) Observations() []BackendObservation {
	c.calls++
	return c.waxumBackend.Observations()
}

// TestManualReconcileIsCoalescedByTheCooldown: a click inside the cooldown
// recomputes nothing (the backend is not consulted again) and says so; after
// the cooldown it runs again. The clock is a parameter, not a sleep.
func TestManualReconcileIsCoalescedByTheCooldown(t *testing.T) {
	b := &countingObserver{waxumBackend: &waxumBackend{}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mgr := NewManager(ctx, b, newStubSessions(), newStubMsgs(), nil,
		Config{SendRate: 1, SendBurst: 3, ReconcilePar: 2, ReconcileInterval: time.Hour})
	t0 := time.Now()

	if _, ran := mgr.ReconcileNow(t0); !ran || b.calls != 1 {
		t.Fatalf("first click: ran=%v calls=%d, want ran with 1 consultation", ran, b.calls)
	}
	rep, ran := mgr.ReconcileNow(t0.Add(manualReconcileCooldown - time.Second))
	if ran || b.calls != 1 {
		t.Fatalf("click inside cooldown: ran=%v calls=%d, want coalesced (1 consultation)", ran, b.calls)
	}
	if !rep.CheckedAt.Equal(t0) {
		t.Errorf("coalesced answer CheckedAt = %v, want the earlier pass %v", rep.CheckedAt, t0)
	}
	if _, ran := mgr.ReconcileNow(t0.Add(manualReconcileCooldown + time.Second)); !ran || b.calls != 2 {
		t.Fatalf("click after cooldown: ran=%v calls=%d, want a second consultation", ran, b.calls)
	}
}

// Before the first pass there is no evidence of any disagreement, so a fresh
// boot must report OK (not a red "disagree" banner / health ok:false).
func TestAFreshManagerReportsReconcileOKBeforeAnyPassHasRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mgr := NewManager(ctx, &waxumBackend{}, newStubSessions(), newStubMsgs(), nil,
		Config{SendRate: 1, SendBurst: 3, ReconcilePar: 2, ReconcileInterval: time.Hour, ReconcileGrace: time.Minute})
	rep := mgr.Reconcile()
	if !rep.Enabled || !rep.OK || !rep.CheckedAt.IsZero() {
		t.Fatalf("fresh report = %+v, want Enabled && OK && CheckedAt zero", rep)
	}
}
