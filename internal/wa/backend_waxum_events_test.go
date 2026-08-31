package wa

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/waxum"
)

// writeSSEEvent writes one SSE frame (event/id/data + trailing blank line)
// and flushes it, matching internal/waxum/events_test.go's fixture shape.
func writeSSEEvent(w http.ResponseWriter, evType, id, sessionID string) {
	fmt.Fprintf(w, "event: %s\nid: %s\ndata: {\"session_id\":%q}\n\n", evType, id, sessionID)
	w.(http.Flusher).Flush()
}

// startSSEStream writes the response header/status and an initial flush —
// every eventsHandler in this file starts this way, matching
// waxumFakeOpts.eventsHandler's own default.
func startSSEStream(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()
}

// TestAnEventForAKnownSessionPokesItsStatusPumpImmediately proves the whole
// point of Phase 8: an SSE event for a session shortens statusPump's wait,
// instead of the session sitting idle until the next timer tick.
//
// pollInterval is 3s here (b.pokeMinInterval() = 3s/4 = 750ms — see that
// method's doc comment), so this test first lets 900ms pass — past the
// floor — before sending the event, then asserts the NEXT /status poll
// lands within ~200ms of it. Both numbers are far tighter than the
// phase brief's illustrative "poll interval 10s / within ~50ms" would
// require against a raw fixed clock, but they preserve the same claim:
// the poke-triggered poll arrives at least an order of magnitude sooner
// than pollInterval itself would have (3s), which is all "immediately"
// needs to mean here. Sending the event only AFTER the floor has elapsed
// is deliberate too — see pokeMinInterval's doc comment: a poke arriving
// WITHIN the floor of the previous real poll is deferred by design (that
// is what TestAnEventDoesNotShortCircuitTheLoggedOutHysteresis, below,
// exists to prove), so a meaningful "immediate reaction" test has to be
// about a session that has been quiet for a while, not one still inside
// the floor from its own initial connect-time poll.
func TestAnEventForAKnownSessionPokesItsStatusPumpImmediately(t *testing.T) {
	sidCh := make(chan string, 1)
	client, calls := newWaxumFakeServer(t, waxumFakeOpts{
		statusFn: func(int) waxum.Status {
			return waxum.Status{Status: "connected", IsLoggedIn: true, SocketAlive: true}
		},
		eventsHandler: func(w http.ResponseWriter, r *http.Request) {
			startSSEStream(w)
			select {
			case sid := <-sidCh:
				writeSSEEvent(w, "connected", "1", sid)
			case <-r.Context().Done():
				return
			}
			<-r.Context().Done()
		},
	})
	b := &waxumBackend{client: client, eventsEnabled: true, pollInterval: 3 * time.Second}
	mgr := newWaxumTestManager(t, b)

	s, err := mgr.Create(context.Background(), "gw", "+62 812-0000-5555")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Connect(context.Background(), s); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	waitForCondition(t, "the events stream dials at least once", func() bool { return calls.events.Load() >= 1 })
	waitForCondition(t, "the initial connect-time status poll happens", func() bool { return calls.status.Load() >= 1 })

	time.Sleep(900 * time.Millisecond) // past pokeMinInterval's floor (750ms) — see doc comment above
	before := calls.status.Load()
	sidCh <- s.ID

	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if calls.status.Load() > before {
			return // success: a poll fired well inside the 3s poll interval
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("no /status poll happened within ~200ms of the poked event (pollInterval is 3s, so "+
		"this can only be explained by the poke never reaching statusPump); calls.status stayed at %d",
		calls.status.Load())
}

// TestAnEventDoesNotShortCircuitTheLoggedOutHysteresis is the direct
// regression test for the hazard backend_waxum_events.go's package doc
// comment and pokeMinInterval's doc comment both describe: three
// logged_out-shaped SSE events arriving in rapid succession (within
// milliseconds of each other) must NOT collapse applyStatus's
// loggedOutHysteresisPolls evidence window (3 CONSECUTIVE qualifying real
// polls) into that same fraction of a second — the exact live incident
// quoted in loggedOutHysteresisPolls's doc comment (a 15s flap) reopened at
// 1/75th the timescale.
//
// The fake /status here ALWAYS answers a qualifying, "logged out" shaped
// response (waiting_for_qr + a paired session), so WITHOUT pokeMinInterval's
// floor, three rapid pokes would trigger three rapid real polls and commit
// StateLoggedOut within a few milliseconds of the burst. pollInterval is 2s
// here, so pokeMinInterval() = 500ms — the assertion below runs at 150ms
// after the burst, comfortably inside that floor, so AT MOST the one
// initial (pre-burst) poll can have happened; a second real poll landing
// that early would mean the floor did not hold at all, not merely that it
// was raced. (What the floor does NOT claim: that state can never reach
// StateLoggedOut afterwards — a session that is genuinely still logged out
// on every subsequent floor-paced poll SHOULD eventually be reported as
// such, just paced no faster than pokeMinInterval apart; that eventual,
// paced commit is exercised by TestAPairedSessionReportingWaitingForQRIsTreatedAsLoggedOutNotDisconnected
// already, via the timer path.)
func TestAnEventDoesNotShortCircuitTheLoggedOutHysteresis(t *testing.T) {
	sidCh := make(chan string, 1)
	client, calls := newWaxumFakeServer(t, waxumFakeOpts{
		statusFn: func(int) waxum.Status {
			return waxum.Status{Status: "waiting_for_qr", IsLoggedIn: false}
		},
		eventsHandler: func(w http.ResponseWriter, r *http.Request) {
			startSSEStream(w)
			select {
			case sid := <-sidCh:
				// Three logged_out frames back to back — no sleep between
				// them — the "rapid burst" this test exists to guard
				// against.
				writeSSEEvent(w, "logged_out", "1", sid)
				writeSSEEvent(w, "logged_out", "2", sid)
				writeSSEEvent(w, "logged_out", "3", sid)
			case <-r.Context().Done():
				return
			}
			<-r.Context().Done()
		},
	})
	b := &waxumBackend{client: client, eventsEnabled: true, pollInterval: 2 * time.Second}
	mgr := newWaxumTestManager(t, b)

	// A PAIRED session — mapWaxumStatus's point 3 (backend_waxum.go) only
	// treats waiting_for_qr as a logged-out CANDIDATE when WSM already
	// holds a jid for it; an unpaired session reaching this same status is
	// just an ordinary pairing attempt (point 4) and would never exercise
	// the hysteresis path this test is about.
	s := testSession(mgr, StateConnected)
	s.jid = paidTestJID(t)
	mgr.mu.Lock()
	mgr.sessions[s.ID] = s
	mgr.mu.Unlock()

	pumpCtx, pumpCancel := context.WithCancel(context.Background())
	t.Cleanup(pumpCancel) // otherwise this pump outlives the test's own fake server (t.Cleanup order) and spams "connection refused" into later tests' logs
	b.ensureStatusPump(pumpCtx, s)
	waitForCondition(t, "the initial status poll happens", func() bool { return calls.status.Load() >= 1 })
	afterInitialPoll := calls.status.Load()

	sidCh <- s.ID
	time.Sleep(150 * time.Millisecond) // well inside pokeMinInterval's 500ms floor

	if got := s.Snapshot().State; got == StateLoggedOut {
		t.Fatalf("state = %s only 150ms after a rapid 3-event burst (pokeMinInterval floor is 500ms), "+
			"want anything but StateLoggedOut — the hysteresis evidence window was short-circuited "+
			"(status polled %d times)", got, calls.status.Load())
	}
	if got := calls.status.Load(); got != afterInitialPoll {
		t.Fatalf("status polled %d time(s) within 150ms of the burst (pokeMinInterval floor is 500ms), "+
			"want exactly %d (no extra poll yet) — the floor did not defer the burst", got, afterInitialPoll)
	}
}

// TestTheEventPumpRedialsAfterTheStreamDrops proves eventPump reconnects
// (rather than exiting) when TailEvents returns because the stream ended,
// not because ctx was cancelled.
func TestTheEventPumpRedialsAfterTheStreamDrops(t *testing.T) {
	var attempts atomic.Int32 // local counter — waxumFakeCalls.events (below) isn't in scope until newWaxumFakeServer returns
	client, calls := newWaxumFakeServer(t, waxumFakeOpts{
		eventsHandler: func(w http.ResponseWriter, r *http.Request) {
			startSSEStream(w)
			if attempts.Add(1) == 1 {
				return // first attempt: the stream ends immediately (a "drop")
			}
			<-r.Context().Done() // every later attempt stays open
		},
	})
	b := &waxumBackend{client: client, eventsEnabled: true, pollInterval: time.Hour}
	_ = newWaxumTestManager(t, b)

	deadline := time.Now().Add(5 * time.Second) // backoff's first wait is ~1s + up to 500ms jitter
	for time.Now().Before(deadline) {
		if calls.events.Load() >= 2 {
			return // redialed
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("events stream was dialed %d time(s) within 5s, want at least 2 (a redial after the drop)", calls.events.Load())
}

// TestManagerCloseTerminatesTheEventPump is the direct regression test for
// the Close-hang hazard: the fake server holds the events stream open
// FOREVER (waxumFakeOpts' default eventsHandler), and Manager.Close must
// still return promptly — proving the event pump is wg-tracked (m.go1) and
// keyed to m.supCtx, which Close cancels before m.wg.Wait() (see
// backend_waxum_events.go's package doc comment). This is the single most
// important test in this phase: an untracked or wrongly-keyed pump here
// would hang Close indefinitely, and CLAUDE.md documents exactly what a
// SIGKILLed-mid-teardown waxum session costs (re-pairing the handset by
// QR, with no second copy of its identity anywhere).
func TestManagerCloseTerminatesTheEventPump(t *testing.T) {
	client, calls := newWaxumFakeServer(t, waxumFakeOpts{}) // default: events stream hangs open forever
	b := &waxumBackend{client: client, eventsEnabled: true, pollInterval: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := NewManager(ctx, b, newStubSessions(), newStubMsgs(), nil,
		Config{SendRate: 1, SendBurst: 3, ReconcilePar: 2})

	waitForCondition(t, "the event pump dials the stream", func() bool { return calls.events.Load() >= 1 })

	closeDone := make(chan struct{})
	go func() {
		mgr.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Manager.Close did not return promptly with the event pump's stream held open forever " +
			"(SIGKILL-on-teardown hazard — see CLAUDE.md's WhatsApp session identity note)")
	}
}

// TestEventsAppearInTheSystemRing proves handleEvent rings EVERY event —
// both for a session Manager still knows about (via s.ring, which fans
// into Manager's system-wide ring too — see ManagedSession.ringLevel's doc
// comment) and for a session id Manager no longer recognizes (via
// m.RecordEvent directly) — so neither shows up as silently dropped on the
// system log page (internal/handler's GetSystemLog, Manager.RecentEvents).
func TestEventsAppearInTheSystemRing(t *testing.T) {
	sidCh := make(chan string, 1)
	client, _ := newWaxumFakeServer(t, waxumFakeOpts{
		statusFn: func(int) waxum.Status { return waxum.Status{Status: "connected", IsLoggedIn: true, SocketAlive: true} },
		eventsHandler: func(w http.ResponseWriter, r *http.Request) {
			startSSEStream(w)
			select {
			case sid := <-sidCh:
				writeSSEEvent(w, "connected", "1", sid)
				writeSSEEvent(w, "connected", "2", "session-that-does-not-exist")
			case <-r.Context().Done():
				return
			}
			<-r.Context().Done()
		},
	})
	b := &waxumBackend{client: client, eventsEnabled: true, pollInterval: time.Hour}
	mgr := newWaxumTestManager(t, b)

	s, err := mgr.Create(context.Background(), "gw", "+62 812-0000-6666")
	if err != nil {
		t.Fatal(err)
	}
	sidCh <- s.ID

	waitForCondition(t, "the known session's own ring records the event", func() bool {
		for _, note := range ringNotes(s) {
			if strings.Contains(note, "waxum event: connected") {
				return true
			}
		}
		return false
	})

	waitForCondition(t, "the system-wide ring records the unknown session's event too", func() bool {
		for _, evt := range mgr.RecentEvents(0) {
			if strings.Contains(evt.Note, "session-that-does-not-exist") {
				return true
			}
		}
		return false
	})
}

// TestTheEventPumpIsDisabledByWSMWaxumEvents proves eventsEnabled=false
// (WSM_WAXUM_EVENTS=0) never dials the events endpoint at all, leaving
// statusPump as the sole poller exactly as it behaved before this phase.
func TestTheEventPumpIsDisabledByWSMWaxumEvents(t *testing.T) {
	client, calls := newWaxumFakeServer(t, waxumFakeOpts{})
	b := &waxumBackend{client: client, eventsEnabled: false, pollInterval: time.Hour}
	_ = newWaxumTestManager(t, b)

	time.Sleep(100 * time.Millisecond)
	if got := calls.events.Load(); got != 0 {
		t.Fatalf("events endpoint dialed %d time(s) with eventsEnabled=false, want 0", got)
	}
}

// TestAMessageEventPokesTheInboundPumpsChannelWithoutTouchingSessionState
// guards the SSE nudge inbound.go's pump relies on (handleEvent's new
// branch): a "message" event must wake m.inboundPoke, and must NOT go
// anywhere near session state — "message" is deliberately absent from
// pokeableEventKinds, so this must NOT also poke the (untracked) session's
// status pump. See this file's own package doc comment (lines 21-49) for
// why an event must never write session state, and inbound.go's
// pokeInbound doc comment for why this call is compatible with that rule
// despite living right next to pokeSession.
func TestAMessageEventPokesTheInboundPumpsChannelWithoutTouchingSessionState(t *testing.T) {
	b := &waxumBackend{pollInterval: time.Hour}
	mgr := newTestManager(newStubSessions(), newStubMsgs())

	b.handleEvent(mgr, waxum.Event{Type: "message", Data: []byte(`{"session_id":"sess-not-tracked"}`)})

	select {
	case <-mgr.inboundPoke:
	default:
		t.Fatal(`handleEvent with a "message" event did not poke the inbound pump's channel (m.inboundPoke)`)
	}
}

// paidTestJID returns a non-empty JID for tests that need a PAIRED session
// without going through a real pairing flow.
func paidTestJID(t *testing.T) (jid types.JID) {
	t.Helper()
	return types.NewJID("6281200000000", types.DefaultUserServer)
}
