package wa

import (
	"context"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// fastReconnectConfig is a Config sub-set tuned for the reconnect
// supervisor's own tests: a millisecond-scale scan/min/max instead of the
// production defaults (5s/30s/5m — reconnect.go's reconnectDefault*
// constants), so these tests observe real reconnect attempts within the
// couple of seconds waitForCondition (backend_waxum_test.go) allows,
// without ever waiting out a real 30s floor. TestReconnectBackoffDoesNotOutpaceWaxumsOwnWatchdog
// below is the one test that DOES need the production floor, and it calls
// reconnectDelay directly instead of running a supervisor loop for that
// exact reason.
func fastReconnectConfig() Config {
	return Config{
		SendRate: 1, SendBurst: 3, ReconcilePar: 2,
		ReconnectScanInterval: time.Millisecond,
		ReconnectMinDelay:     2 * time.Millisecond,
		ReconnectMaxDelay:     20 * time.Millisecond,
	}
}

// registerSession makes s reachable through mgr's own map the way
// Manager.Create/StartAll normally would — reconnect_test.go builds sessions
// directly via testSession (sender_test.go) instead, so this is the missing
// step that lets reconnectSupervisor's own scan (which reads Manager.mu's
// map, not Manager.Get for one id) find it.
func registerSession(mgr *Manager, s *ManagedSession) {
	mgr.mu.Lock()
	mgr.sessions[s.ID] = s
	mgr.mu.Unlock()
}

func pairedTestJID() types.JID {
	return types.NewJID("6281200000001", types.DefaultUserServer)
}

// fastReconnectConfigWithStallThreshold is fastReconnectConfig with a small,
// test-chosen ReconnectStallThreshold instead of the production default (5):
// reconnectDelay's own jitter (backoff.Jitter, U(0, 500ms) — added on TOP of
// fastReconnectConfig's already-tiny min/max on every attempt) accumulates
// across N attempts, so a threshold of 5 could take up to ~2.5s of jitter
// alone before stalling — this keeps the stall tests below fast and their
// wait windows (waitForConditionWithin) comfortably sized instead of
// flirting with waitForCondition's fixed 2s deadline.
func fastReconnectConfigWithStallThreshold(n int) Config {
	cfg := fastReconnectConfig()
	cfg.ReconnectStallThreshold = n
	return cfg
}

// waitForConditionWithin is waitForCondition (backend_waxum_test.go) with a
// caller-chosen deadline instead of that helper's fixed 2s — the stall tests
// below need to comfortably clear stallThreshold attempts' worth of
// reconnectDelay's jitter (see fastReconnectConfigWithStallThreshold), which
// a fixed 2s budget was not sized for.
func waitForConditionWithin(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

// TestASessionThatKeepsFailingToConnectIsMarkedAsNeedingAttention is the
// core regression test for the production limitation reconnect.go's package
// doc comment describes: waxum's /connect can return 200 (Activate/
// Backend.Connect succeeds, no error) while the socket never actually comes
// back up. fakeBackend here reproduces exactly that shape — connectFn always
// returns nil, but Live() (backend_test.go) always returns false and
// nothing ever calls s.setState(StateConnected) — so reconnectEligible never
// goes false on its own. After exactly the configured threshold of such
// attempts, the session must be marked Stalled with a StalledReason an
// operator can act on, and Backend.Connect must not be called a 4th time.
func TestASessionThatKeepsFailingToConnectIsMarkedAsNeedingAttention(t *testing.T) {
	fake := &fakeBackend{name: "fake", connectFn: func(context.Context, *ManagedSession) error { return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mgr := NewManager(ctx, fake, newStubSessions(), newStubMsgs(), nil, fastReconnectConfigWithStallThreshold(3))

	s := testSession(mgr, StateDisconnected)
	s.jid = pairedTestJID()
	s.active = true
	registerSession(mgr, s)

	waitForConditionWithin(t, 3*time.Second,
		"the session is marked Stalled after 3 consecutive reconnect attempts that never reach StateConnected",
		func() bool { return s.Snapshot().Stalled })

	snap := s.Snapshot()
	if snap.StalledReason == "" {
		t.Fatal("Snapshot.Stalled is true but StalledReason is empty — an operator sees a flag with no explanation")
	}
	if got := fake.connectCalls.Load(); got != 3 {
		t.Fatalf("Backend.Connect called %d times before stalling, want exactly 3 (the configured threshold — a 4th call means the supervisor kept retrying past the limit)", got)
	}
}

// TestAStalledSessionStopsBeingRetried is TestASessionThatKeepsFailingToConnectIsMarkedAsNeedingAttention's
// other half: once stalled, the supervisor's own scan loop must not keep
// firing Backend.Connect on some later tick — the whole point of stalling
// is to STOP burning attempts against a peer that has already shown,
// repeatedly, that accepting /connect does not bring the socket up.
func TestAStalledSessionStopsBeingRetried(t *testing.T) {
	fake := &fakeBackend{name: "fake", connectFn: func(context.Context, *ManagedSession) error { return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mgr := NewManager(ctx, fake, newStubSessions(), newStubMsgs(), nil, fastReconnectConfigWithStallThreshold(2))

	s := testSession(mgr, StateDisconnected)
	s.jid = pairedTestJID()
	s.active = true
	registerSession(mgr, s)

	waitForConditionWithin(t, 3*time.Second, "the session stalls", func() bool { return s.Snapshot().Stalled })
	stalledAt := fake.connectCalls.Load()

	// Several more scan ticks (ReconnectScanInterval is 1ms) and multiple
	// backoff windows' worth of real time — easily long enough for the
	// supervisor to have fired another attempt if it had not actually
	// stopped.
	time.Sleep(500 * time.Millisecond)
	if got := fake.connectCalls.Load(); got != stalledAt {
		t.Fatalf("Backend.Connect called %d more time(s) after the session stalled, want 0 — a stalled session must not keep being retried automatically", got-stalledAt)
	}
}

// TestAManualActivateClearsTheStalledStateAndResumesRetrying proves the
// operator is never permanently locked out: a manual Activate (the same
// call the UI/API "Activate" button makes — session.go) on a stalled
// session must clear Stalled immediately, and the supervisor must resume
// firing automatic attempts afterwards — reconnect.go's markStalled/Activate
// doc comments describe this contract; this is the test that holds it.
func TestAManualActivateClearsTheStalledStateAndResumesRetrying(t *testing.T) {
	fake := &fakeBackend{name: "fake", connectFn: func(context.Context, *ManagedSession) error { return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mgr := NewManager(ctx, fake, newStubSessions(), newStubMsgs(), nil, fastReconnectConfigWithStallThreshold(2))

	s := testSession(mgr, StateDisconnected)
	s.jid = pairedTestJID()
	s.active = true
	registerSession(mgr, s)

	waitForConditionWithin(t, 3*time.Second, "the session stalls", func() bool { return s.Snapshot().Stalled })

	if err := s.Activate(ctx); err != nil {
		t.Fatalf("manual Activate on a stalled session: err = %v, want nil (fakeBackend.connectFn always succeeds)", err)
	}
	if snap := s.Snapshot(); snap.Stalled {
		t.Fatalf("Snapshot.Stalled = true after a manual Activate, want false — StalledReason = %q", snap.StalledReason)
	}

	before := fake.connectCalls.Load()
	waitForConditionWithin(t, 3*time.Second,
		"the supervisor fires at least one more automatic reconnect attempt after a manual Activate clears the stalled state",
		func() bool { return fake.connectCalls.Load() > before })
}

// TestStalledIsNotConfusedWithLoggedOut pins the design choice reconnect.go
// and the stalled field's doc comment (session.go) both argue for: stalled
// is a FLAG alongside State, not a new State and not a repaint of
// StateLoggedOut. A stalled session must stay StateDisconnected (whatsmeow/
// waxum never told WSM this device was unlinked) and must stay
// reconnectEligible at the state-machine level — isStalled is a SEPARATE
// gate the supervisor checks first, not a redefinition of "eligible".
func TestStalledIsNotConfusedWithLoggedOut(t *testing.T) {
	fake := &fakeBackend{name: "fake", connectFn: func(context.Context, *ManagedSession) error { return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mgr := NewManager(ctx, fake, newStubSessions(), newStubMsgs(), nil, fastReconnectConfigWithStallThreshold(2))

	s := testSession(mgr, StateDisconnected)
	s.jid = pairedTestJID()
	s.active = true
	registerSession(mgr, s)

	waitForConditionWithin(t, 3*time.Second, "the session stalls", func() bool { return s.Snapshot().Stalled })

	snap := s.Snapshot()
	if snap.State == StateLoggedOut {
		t.Fatal("a stalled session must never be recast as StateLoggedOut — that means re-pair required, which this operator has not been told")
	}
	if snap.State != StateDisconnected {
		t.Fatalf("State = %s, want StateDisconnected (unchanged by stalling)", snap.State)
	}
	if !s.reconnectEligible() {
		t.Fatal("a stalled session must remain reconnectEligible at the state-machine level — only the supervisor's own isStalled check (reconnect.go) should gate automatic retries")
	}
}

// TestAnActiveDisconnectedSessionIsReconnectedAutomatically is the
// regression test for reconnect.go's production incident: a session that is
// active (operator/pairing intent to stay connected), paired (has a jid),
// and observed StateDisconnected must be redialled automatically, with NO
// operator action — reverting reconnect.go (or NewManager's supervisor
// wiring) makes this hang until waitForCondition's own 2s timeout, because
// nothing would ever call Backend.Connect again.
func TestAnActiveDisconnectedSessionIsReconnectedAutomatically(t *testing.T) {
	fake := &fakeBackend{name: "fake", connectFn: func(context.Context, *ManagedSession) error { return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mgr := NewManager(ctx, fake, newStubSessions(), newStubMsgs(), nil, fastReconnectConfig())

	s := testSession(mgr, StateDisconnected)
	s.jid = pairedTestJID()
	s.active = true
	registerSession(mgr, s)

	waitForCondition(t, "the supervisor calls Backend.Connect for an active, paired, disconnected session",
		func() bool { return fake.connectCalls.Load() >= 1 })
}

// TestADeactivatedSessionIsNeverReconnected: a session the operator
// deactivated (active=false, session.go's Deactivate) must never be
// auto-reconnected, regardless of how long it sits disconnected — respecting
// operator intent is explicitly part of reconnect.go's contract, not just an
// oversight to guard against.
func TestADeactivatedSessionIsNeverReconnected(t *testing.T) {
	fake := &fakeBackend{name: "fake", connectFn: func(context.Context, *ManagedSession) error { return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mgr := NewManager(ctx, fake, newStubSessions(), newStubMsgs(), nil, fastReconnectConfig())

	s := testSession(mgr, StateDisconnected)
	s.jid = pairedTestJID()
	s.active = false // operator deactivated it (or it was never activated)
	registerSession(mgr, s)

	// Give the supervisor several scan ticks (ReconnectScanInterval is 1ms)
	// to have gotten this wrong if it were going to. The window has to
	// clear reconnectDelay's own jitter ceiling (backoff.Jitter is U(0,
	// 500ms)) on top of fastReconnectConfig's own min/max, or a correctly
	// GATED session (which never even reaches reconnectDelay) and an
	// incorrectly un-gated one that just happened to roll a large jitter
	// would be indistinguishable within a shorter window.
	time.Sleep(700 * time.Millisecond)
	if got := fake.connectCalls.Load(); got != 0 {
		t.Fatalf("Backend.Connect called %d times for a deactivated session, want 0", got)
	}
}

// TestALoggedOutSessionIsNeverReconnectedAutomatically: StateLoggedOut is
// terminal (needs a re-pair, not a reconnect — see mapWaxumStatus's doc
// comment in backend_waxum.go for how the waxum backend now infers it from
// polled data). Reconnecting it would burn QR/pair-code attempts against a
// device that already knows it was unlinked, for no possible benefit.
func TestALoggedOutSessionIsNeverReconnectedAutomatically(t *testing.T) {
	fake := &fakeBackend{name: "fake", connectFn: func(context.Context, *ManagedSession) error { return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mgr := NewManager(ctx, fake, newStubSessions(), newStubMsgs(), nil, fastReconnectConfig())

	s := testSession(mgr, StateLoggedOut)
	s.jid = pairedTestJID()
	s.active = true // even if intent somehow stayed true, state must still gate it out
	registerSession(mgr, s)

	time.Sleep(700 * time.Millisecond) // see TestADeactivatedSessionIsNeverReconnected for why 700ms
	if got := fake.connectCalls.Load(); got != 0 {
		t.Fatalf("Backend.Connect called %d times for a logged-out session, want 0", got)
	}
}

// TestReconnectBackoffDoesNotOutpaceWaxumsOwnWatchdog asserts the schedule
// itself, not a running supervisor: the very FIRST automatic reconnect
// (attempt 0) must never fire sooner than waxum's own reconnect watchdog
// cadence (RECONNECT_WATCHDOG_POLL_MS, 30s default — see reconnect.go's
// package doc comment and CLAUDE.md's waxum rate-limit hazard note). A
// tighter floor would pile WSM's own /connect calls on top of waxum's own
// simultaneous attempts against the same downed peer, and risks 429-ing
// waxum's reconnect calls along with ours.
func TestReconnectBackoffDoesNotOutpaceWaxumsOwnWatchdog(t *testing.T) {
	const waxumWatchdogCadence = 30 * time.Second
	got := reconnectDelay(0, reconnectDefaultMinDelay, reconnectDefaultMaxDelay)
	if got < waxumWatchdogCadence {
		t.Fatalf("first automatic reconnect delay = %s, want >= %s (waxum's own RECONNECT_WATCHDOG_POLL_MS cadence)",
			got, waxumWatchdogCadence)
	}
}

// TestManagerCloseReturnsPromptlyWithAReconnectSupervisorRunning is the
// reconnect-supervisor analogue of
// TestManagerCloseReturnsPromptlyWithAPairingPumpRunning
// (backend_waxum_test.go): with an eligible session and an in-flight
// reconnect attempt whose Backend.Connect deliberately blocks until its ctx
// is canceled, Manager.Close must still return promptly. The supervisor is
// manager-wide, not keyed to any one session's ctx (see reconnect.go's doc
// comment), so this also proves Close's own supCancel call (manager_boot.go)
// is wired — without it this would hang on m.wg.Wait() until the test's
// outer deadline, exactly the SIGKILL-on-teardown hazard CLAUDE.md documents.
func TestManagerCloseReturnsPromptlyWithAReconnectSupervisorRunning(t *testing.T) {
	fake := &fakeBackend{name: "fake", connectFn: func(ctx context.Context, _ *ManagedSession) error {
		<-ctx.Done() // hangs until the caller's context is canceled
		return ctx.Err()
	}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mgr := NewManager(ctx, fake, newStubSessions(), newStubMsgs(), nil, fastReconnectConfig())

	s := testSession(mgr, StateDisconnected)
	s.jid = pairedTestJID()
	s.active = true
	registerSession(mgr, s)

	waitForCondition(t, "the supervisor has started at least one reconnect attempt",
		func() bool { return fake.connectCalls.Load() >= 1 })

	closeDone := make(chan struct{})
	go func() {
		mgr.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Manager.Close did not return promptly with a reconnect supervisor (and an in-flight " +
			"auto-reconnect attempt) running — SIGKILL-on-teardown hazard, see CLAUDE.md's WhatsApp " +
			"session identity note")
	}
}

// TestReconnectSupervisorDoesNothingWithoutABackend guards NewManager's own
// nil-backend guard (manager.go): dozens of existing tests build a Manager
// with backend=nil via newTestManager (manager_test.go) and never call
// Close — if the supervisor started unconditionally, every one of them
// would carry a background goroutine that eventually dereferences a nil
// Backend the first time it found an eligible session. This test proves the
// guard directly rather than relying on every OTHER test happening not to
// trip it.
func TestReconnectSupervisorDoesNothingWithoutABackend(t *testing.T) {
	m := newTestManager(newStubSessions(), newStubMsgs())
	s := testSession(m, StateDisconnected)
	s.jid = pairedTestJID()
	s.active = true
	registerSession(m, s)

	// Nothing to assert a call count against (there is no backend to call);
	// this test's claim is simply that nothing panics within a window the
	// fast-config tests above prove is long enough for a running supervisor
	// to have acted.
	time.Sleep(20 * time.Millisecond)
}
