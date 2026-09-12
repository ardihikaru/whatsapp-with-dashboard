package wa

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/waxum"
)

// waxumFakeCalls counts, per-endpoint, how many times the fake waxum server
// in this file has answered POST /sessions, GET /status, GET /qr, POST
// /pair, POST /connect and DELETE /sessions/{id} — enough for tests to wait
// for "N polls have happened", or assert a call count stayed exactly N (the
// connect-nudge idempotency tests), without sleeping a fixed, flaky amount.
type waxumFakeCalls struct {
	create     atomic.Int32
	status     atomic.Int32
	qr         atomic.Int32
	pair       atomic.Int32
	connect    atomic.Int32
	disconnect atomic.Int32
	delete_    atomic.Int32
	// events counts how many times a client has dialed GET
	// /api/v1/events/tail — one per connection ATTEMPT, so a test asserting
	// a redial can watch this climb past 1.
	events atomic.Int32
}

// waxumFakeOpts configures newWaxumFakeServer. Zero value is a server that
// always reports a non-terminal, never-scanned pairing: exactly what the
// hard-deadline and context-cancellation tests need (nothing about it ever
// makes the pump exit on its own).
type waxumFakeOpts struct {
	createStatus int // HTTP status for POST /api/v1/sessions; 0 => 200. Ignored if createFn is set.
	// createFn, when set, determines the HTTP status for POST
	// /api/v1/sessions per call (n is 1-based) — used to simulate a
	// TRANSIENT 429 (a few calls, then success) or a permanent one,
	// unlike createStatus's single fixed value.
	createFn func(n int) int
	statusFn func(n int) waxum.Status // n is the 1-based call number
	qrFn     func(n int) []string
	// pairFn answers POST /pair. n is the 1-based call number. status==0
	// (or http.StatusOK) succeeds with code; any other status responds with
	// that code and a body shaped like waxum's real error (see
	// TestPhonePairCodeIsRequestedOnlyAfterTheConnectionIsUp's doc comment
	// for the exact live message this mirrors). Nil means every call
	// succeeds with a fixed code — not exercised by a test that never
	// pairs in phone mode.
	pairFn func(n int) (status int, code string)
	// deleteFn, when set, determines the HTTP status for DELETE
	// /api/v1/sessions/{id} per call (n is 1-based). Nil means every call
	// succeeds with 200.
	deleteFn func(n int) int
	// disconnectFn, when set, determines the HTTP status for POST
	// /api/v1/sessions/{id}/disconnect per call (n is 1-based). Nil means
	// every call succeeds with 200 — used to simulate waxum answering 404
	// (ErrNotFound, the normal "no live client to stop" outcome — see
	// waxumBackend.Disconnect's own posture) or a genuinely unexpected
	// failure.
	disconnectFn func(n int) int
	// disconnectHandler, when set, handles POST …/disconnect directly
	// instead of the calls.disconnect+disconnectFn shape above (calls.disconnect
	// is still incremented either way). Needed when a test cares about the
	// RESPONSE TIMING of a disconnect call — e.g. blocking it to prove some
	// other codepath does not end up waiting on it — not just its status
	// code, mirroring eventsHandler's "full control" escape hatch below.
	disconnectHandler http.HandlerFunc
	// alwaysRateLimited, when true, makes EVERY request under
	// /api/v1/sessions/ (status, qr, pair, connect, delete — NOT the bare
	// POST /api/v1/sessions create path) answer 429 forever, regardless of
	// any other opt above. Used to prove a pump's own DEADLINE — not the
	// client's bounded per-call retry count — is what stops a genuinely
	// permanent rate limit from hanging.
	alwaysRateLimited bool
	// eventsHandler, when set, handles GET /api/v1/events/tail directly —
	// full control over an SSE test's timing (scripted frames, flushing,
	// holding the connection open, ending the stream, answering a non-2xx
	// status) rather than trying to force one more opts.*Fn shape to cover
	// every case a stream test needs. nil registers a handler that writes
	// a 200 + text/event-stream header, flushes, and then blocks on the
	// REQUEST's own context until the client gives up — an always-open,
	// never-emitting stream, the same "nothing about it ever makes the
	// pump exit on its own" default this type's doc comment already
	// promises for every other endpoint here, extended to this one.
	eventsHandler http.HandlerFunc
}

// newWaxumFakeServer builds an httptest server covering exactly the waxum
// endpoints this phase's backend calls (CreateSession, Connect, Disconnect,
// Status, QR, PairCode, DeleteSession — see internal/waxum/client.go),
// routed the same way waxum.Client itself builds paths. Every path this
// backend does not care about for a given test answers a bare 200, matching
// the client's own "empty body on out==nil" no-op case.
//
// The Client returned is built with two overrides, both replacing a
// PRODUCTION-scale schedule with a millisecond one — see
// internal/waxum/client_test.go's newFakeServer, which does the same for
// the same reason: WithClientRateLimit(1e6, 1e6) so this package's OWN
// polling loops (2ms poll intervals are common in this file) never trip the
// client-wide throttle meant for real traffic, and WithRetryPolicy(5, …)
// so a test that simulates a 429 does not pay do()'s real backoff schedule
// (up to ~31s across 5 retries) — see TestPairingSurvivesATransientRateLimit
// and TestPairingStillGivesUpAtTheDeadlineWhenRateLimitedForever, both of
// which rely on retries actually happening within a fast test.
func newWaxumFakeServer(t *testing.T, opts waxumFakeOpts) (*waxum.Client, *waxumFakeCalls) {
	t.Helper()
	calls := &waxumFakeCalls{}
	createStatus := opts.createStatus
	if createStatus == 0 {
		createStatus = http.StatusOK
	}

	mux := http.NewServeMux()
	// CreateSession posts to the bare collection path, not a per-id
	// sub-path — ServeMux's "/api/v1/sessions/" subtree pattern below does
	// NOT match this (no trailing slash on the request), so it needs its
	// own exact registration.
	mux.HandleFunc("/api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.create.Add(1))
		status := createStatus
		if opts.createFn != nil {
			status = opts.createFn(n)
		}
		w.WriteHeader(status)
	})
	mux.HandleFunc("/api/v1/events/tail", func(w http.ResponseWriter, r *http.Request) {
		calls.events.Add(1)
		if opts.eventsHandler != nil {
			opts.eventsHandler(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done() // hang open until the client gives up — see waxumFakeOpts.eventsHandler's doc comment
	})
	mux.HandleFunc("/api/v1/sessions/", func(w http.ResponseWriter, r *http.Request) {
		if opts.alwaysRateLimited {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if r.Method == http.MethodDelete {
			n := int(calls.delete_.Add(1))
			status := http.StatusOK
			if opts.deleteFn != nil {
				status = opts.deleteFn(n)
			}
			w.WriteHeader(status)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/status"):
			n := int(calls.status.Add(1))
			st := waxum.Status{Status: "waiting_for_qr"}
			if opts.statusFn != nil {
				st = opts.statusFn(n)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(st)
		case strings.HasSuffix(r.URL.Path, "/qr"):
			n := int(calls.qr.Add(1))
			var codes []string
			if opts.qrFn != nil {
				codes = opts.qrFn(n)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(waxum.QRResult{QRCodes: codes, TimeoutSeconds: 30})
		case strings.HasSuffix(r.URL.Path, "/pair"):
			n := int(calls.pair.Add(1))
			status, code := http.StatusOK, "1234-5678"
			if opts.pairFn != nil {
				status, code = opts.pairFn(n)
			}
			if status != 0 && status != http.StatusOK {
				w.WriteHeader(status)
				// Body shape matches the live 500 quoted in backend_waxum.go's
				// StartPairing doc comment.
				_, _ = w.Write([]byte(`{"success":false,"error":{"code":500,` +
					`"message":"Internal error: pair_with_code failed: client is not connected"}}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(waxum.PairCodeResult{Code: code, TimeoutSeconds: 60})
		case strings.HasSuffix(r.URL.Path, "/connect"):
			calls.connect.Add(1)
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/disconnect"):
			n := int(calls.disconnect.Add(1))
			if opts.disconnectHandler != nil {
				opts.disconnectHandler(w, r)
				return
			}
			status := http.StatusOK
			if opts.disconnectFn != nil {
				status = opts.disconnectFn(n)
			}
			w.WriteHeader(status)
		default: // not under test here
			w.WriteHeader(http.StatusOK)
		}
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := waxum.New(srv.URL, "test-token", 5*time.Second,
		waxum.WithClientRateLimit(1e6, 1e6),
		waxum.WithRetryPolicy(5, func(int, string) time.Duration { return time.Millisecond }),
	)
	return client, calls
}

// newWaxumTestManager wires a Manager around backend for these tests, with
// a cancellable root context cleaned up automatically — any status/pairing
// pump a test starts via s.mgr.go1 is left to exit on its own after the
// test, rather than leaking past it indefinitely.
func newWaxumTestManager(t *testing.T, backend Backend) *Manager {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return NewManager(ctx, backend, newStubSessions(), newStubMsgs(), nil,
		Config{SendRate: 1, SendBurst: 3, ReconcilePar: 2})
}

// waitForCondition polls cond (cheap, no I/O) until it is true or 2s pass.
func waitForCondition(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

// pairedJIDFromHistory scans repo's full status history for id (not just
// the latest entry — stubSessions.lastStatus is the wrong tool here) for a
// SetPaired call ("paired:<jid>", stubs_test.go), returning the jid string
// and true if one was ever recorded. finishPairingSuccess calls SetPaired
// BEFORE applyStatus's own setState (which writes "connected"/"disconnected"
// right after, into the SAME history), so the "paired:" entry is not
// necessarily the last one.
func pairedJIDFromHistory(repo *stubSessions, id string) (string, bool) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	const prefix = "paired:"
	for i := len(repo.statuses[id]) - 1; i >= 0; i-- {
		if entry := repo.statuses[id][i]; strings.HasPrefix(entry, prefix) {
			return entry[len(prefix):], true
		}
	}
	return "", false
}

// TestWaxumPairingPumpOnlyBumpsTheQRGenerationWhenThePayloadChanges is
// hazard #1 from pairPump's doc comment: GET /qr returns its whole qr_codes array
// on every poll, not just on rotation. Naively calling PairingState.SetCode
// every tick would bump Gen every poll and make the browser's
// <img src="qr.png?g=N"> refetch forever even though the code on screen
// never changed.
func TestWaxumPairingPumpOnlyBumpsTheQRGenerationWhenThePayloadChanges(t *testing.T) {
	client, calls := newWaxumFakeServer(t, waxumFakeOpts{
		qrFn: func(n int) []string {
			if n <= 3 {
				return []string{"code-A"}
			}
			return []string{"code-A", "code-B"} // rotated: newest element changes
		},
	})
	b := &waxumBackend{client: client}
	mgr := newWaxumTestManager(t, b)
	s := testSession(mgr, StatePairing)
	ps := &PairingState{mode: PairModeQR}
	s.pairing = ps

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		b.pairPump(ctx, s, ps, 2*time.Millisecond, time.Now().Add(time.Minute))
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	waitForCondition(t, "3 unchanged qr polls", func() bool { return calls.qr.Load() >= 3 })
	if got := ps.Snapshot().Gen; got != 1 {
		t.Fatalf("Gen after 3 identical polls = %d, want 1 (SetCode must not fire on an unchanged payload)", got)
	}

	waitForCondition(t, "a poll past the rotation", func() bool { return calls.qr.Load() >= 5 })
	if got := ps.Snapshot().Gen; got != 2 {
		t.Fatalf("Gen after the payload changed = %d, want 2", got)
	}
}

// TestWaxumPairingPumpStopsWhenTheContextIsCancelled proves ctx.Done() alone
// ends the pump promptly, even with an enormous poll interval and an
// enormous deadline — neither of those must be what stops it.
func TestWaxumPairingPumpStopsWhenTheContextIsCancelled(t *testing.T) {
	client, _ := newWaxumFakeServer(t, waxumFakeOpts{})
	b := &waxumBackend{client: client}
	mgr := newWaxumTestManager(t, b)
	s := testSession(mgr, StatePairing)
	ps := &PairingState{mode: PairModeQR}
	s.pairing = ps

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		b.pairPump(ctx, s, ps, time.Hour, time.Now().Add(time.Hour))
		close(done)
	}()
	time.Sleep(20 * time.Millisecond) // let the pump run its first iteration
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pairPump did not return promptly after ctx was cancelled")
	}
	if snap := ps.Snapshot(); !snap.Done || snap.Err == "" {
		t.Fatalf("PairingState after cancel = %+v, want Done with a non-empty Err", snap)
	}
}

// TestWaxumPairingPumpStopsAtTheHardDeadline is hazard #2: a poller has no
// terminal item of its own, so without an enforced deadline it would poll a
// device that never scans forever. ctx here is context.Background()
// (NEVER cancelled) and the fake server never reaches a terminal status —
// only the deadline can end this pump. Removing the
// context.WithDeadline wrapping in pairPump would make this test hang
// (bounded here to 2s so a regression fails fast instead of hanging CI).
func TestWaxumPairingPumpStopsAtTheHardDeadline(t *testing.T) {
	client, _ := newWaxumFakeServer(t, waxumFakeOpts{}) // never terminal, by default
	b := &waxumBackend{client: client}
	mgr := newWaxumTestManager(t, b)
	s := testSession(mgr, StatePairing)
	ps := &PairingState{mode: PairModeQR}
	s.pairing = ps

	done := make(chan struct{})
	go func() {
		b.pairPump(context.Background(), s, ps, 2*time.Millisecond, time.Now().Add(20*time.Millisecond))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pairPump did not return at its deadline — the pump would poll a never-scanned device forever")
	}
	if snap := ps.Snapshot(); !snap.Done || snap.Err == "" {
		t.Fatalf("PairingState after deadline = %+v, want Done with a non-empty Err", snap)
	}
}

// TestWaxumPairingPumpFinishesWhenTheSessionReachesConnected is the success
// path: waxum has no distinct "success" event, so reaching status
// connected/logged_in IS the terminal signal, detected on the very first
// poll (before any wait).
func TestWaxumPairingPumpFinishesWhenTheSessionReachesConnected(t *testing.T) {
	const phone = "6281200001111"
	client, _ := newWaxumFakeServer(t, waxumFakeOpts{
		statusFn: func(int) waxum.Status {
			return waxum.Status{Status: "connected", IsLoggedIn: true, SocketAlive: true, PhoneNumber: phone} // SocketAlive: true — point 8 (backend_waxum.go) now derives State from BOTH, not is_logged_in alone
		},
	})
	b := &waxumBackend{client: client}
	mgr := newWaxumTestManager(t, b)
	s := testSession(mgr, StatePairing)
	ps := &PairingState{mode: PairModeQR}
	s.pairing = ps

	done := make(chan struct{})
	go func() {
		b.pairPump(context.Background(), s, ps, time.Hour, time.Now().Add(time.Hour))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pairPump did not finish promptly on a connected status")
	}

	wantJID := types.NewJID(phone, types.DefaultUserServer)
	if s.jid != wantJID {
		t.Fatalf("s.jid = %v, want %v", s.jid, wantJID)
	}
	if s.pairing != nil {
		t.Fatal("s.pairing was not cleared on success")
	}
	if s.state != StateConnected {
		t.Fatalf("s.state = %v, want %v", s.state, StateConnected)
	}
	if !b.Live(s) {
		t.Fatal("Live(s) = false after a successful pairing, want true")
	}
	if snap := ps.Snapshot(); !snap.Done || snap.Err != "" {
		t.Fatalf("PairingState after success = %+v, want Done with an empty Err", snap)
	}
}

// TestWaxumBackendLiveDoesNoIO points Live's client at a server that flags
// itself if hit at all, proving Live is a pure, non-blocking atomic read —
// the contract Backend.Live documents (Snapshot calls it on every 2s HTMX
// row poll and every send's load-balance pick, while HOLDING s.mu).
func TestWaxumBackendLiveDoesNoIO(t *testing.T) {
	var hit atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	b := &waxumBackend{client: waxum.New(srv.URL, "test-token", time.Second)}
	mgr := newWaxumTestManager(t, b)
	s := testSession(mgr, StateConnected)

	if b.Live(s) {
		t.Fatal("Live() on a never-seen session = true, want false")
	}
	b.markLive(s.ID, true)
	if !b.Live(s) {
		t.Fatal("Live() after markLive(id, true) = false, want true")
	}
	if hit.Load() {
		t.Fatal("Live() performed an HTTP request against the fake waxum server — it must be O(1) with no I/O")
	}
}

// TestWaxumBackendMapsClientErrorsOntoTheWAErrSentinels drives SendText
// against fake servers returning waxum's typed-error statuses and checks
// the wa-package sentinel each maps to. Every case shares a fast retry
// policy (WithRetryPolicy(1, 1ms)) — the 429/503 cases would otherwise
// retry against do()'s real backoff schedule (up to ~31s) before this test
// could observe the final sentinel; see
// TestRateLimitedBackendErrorIsNotReportedAsAFullSendQueue below for the
// dedicated regression test this table's 429 case corroborates.
func TestWaxumBackendMapsClientErrorsOntoTheWAErrSentinels(t *testing.T) {
	cases := []struct {
		name string
		code int
		want error
	}{
		{"404 maps to ErrNotFound", http.StatusNotFound, ErrNotFound},
		{"409 maps to ErrNotConnected", http.StatusConflict, ErrNotConnected},
		{"429 maps to ErrBackendBusy, NOT ErrQueueFull", http.StatusTooManyRequests, ErrBackendBusy},
		{"503 maps to ErrNotConnected (transient — socket still dialing)", http.StatusServiceUnavailable, ErrNotConnected},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.code)
			}))
			t.Cleanup(srv.Close)
			client := waxum.New(srv.URL, "test-token", time.Second,
				waxum.WithRetryPolicy(1, func(int, string) time.Duration { return time.Millisecond }))
			b := &waxumBackend{client: client}
			s := testSession(newWaxumTestManager(t, b), StateConnected)

			_, err := b.SendText(context.Background(), s, ToJID("628120001"), "hi")
			if !errors.Is(err, c.want) {
				t.Fatalf("SendText err = %v, want %v", err, c.want)
			}
		})
	}
}

// TestRateLimitedBackendErrorIsNotReportedAsAFullSendQueue is the dedicated
// regression test for the live misdiagnosis: mapErr used to translate
// waxum's 429 onto ErrQueueFull, whose "send queue full" message describes
// a completely different condition (sender.go's per-session send channel
// being full) and sent a production debugging session looking at the wrong
// subsystem. It must never be ErrQueueFull, and must be the new
// ErrBackendBusy sentinel instead.
func TestRateLimitedBackendErrorIsNotReportedAsAFullSendQueue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	client := waxum.New(srv.URL, "test-token", time.Second,
		waxum.WithRetryPolicy(1, func(int, string) time.Duration { return time.Millisecond }))
	b := &waxumBackend{client: client}
	s := testSession(newWaxumTestManager(t, b), StateConnected)

	_, err := b.SendText(context.Background(), s, ToJID("628120001"), "hi")
	if errors.Is(err, ErrQueueFull) {
		t.Fatalf("a rate-limited (429) send was reported as ErrQueueFull (%q) — "+
			"this is the exact mislabeling that cost a live production debugging session; "+
			"a backend rate limit and a full per-session send queue are unrelated conditions", err)
	}
	if !errors.Is(err, ErrBackendBusy) {
		t.Fatalf("SendText err = %v, want errors.Is(err, ErrBackendBusy)", err)
	}
}

// TestWaxumBackendTreatsAnExistingSessionAsSuccessOnConnect: CreateSession
// answers 409 ("already exists") — Connect must still succeed, matching
// whatsmeow's Connect reusing an already-attached client instead of
// erroring. Reconnect must be idempotent.
func TestWaxumBackendTreatsAnExistingSessionAsSuccessOnConnect(t *testing.T) {
	client, _ := newWaxumFakeServer(t, waxumFakeOpts{createStatus: http.StatusConflict})
	b := &waxumBackend{client: client, pollInterval: time.Hour}
	mgr := newWaxumTestManager(t, b)
	s := testSession(mgr, StateNew)

	if err := b.Connect(context.Background(), s); err != nil {
		t.Fatalf("Connect against an already-existing waxum session: err = %v, want nil", err)
	}
}

// TestManagerCloseReturnsPromptlyWithAPairingPumpRunning is the direct
// regression test for the SIGKILL/lost-identity hazard CLAUDE.md documents:
// with an active pairing pump, Manager.Close must still return promptly.
// pairTimeout is deliberately huge (1h) — if this test only passed because
// the deadline happened to be short, it would NOT be testing what actually
// matters in production (WSM_WAXUM_PAIR_TIMEOUT defaults to 180s, far
// longer than compose's 15s stop_grace_period): what makes this prompt is
// Manager.Close cancelling every session's own ctx, which the pairing pump
// must actually be listening on. Without that wiring this test would hang
// until the pairTimeout, i.e. for over an hour — bounded here to 2s so a
// regression fails fast instead of hanging CI.
func TestManagerCloseReturnsPromptlyWithAPairingPumpRunning(t *testing.T) {
	client, _ := newWaxumFakeServer(t, waxumFakeOpts{}) // never reaches a terminal status
	b := &waxumBackend{
		client:         client,
		qrPollInterval: 5 * time.Millisecond,
		pollInterval:   time.Hour,
		pairTimeout:    time.Hour,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := NewManager(ctx, b, newStubSessions(), newStubMsgs(), nil,
		Config{SendRate: 1, SendBurst: 3, ReconcilePar: 2})

	s, err := mgr.Create(context.Background(), "gw", "+62 812-0000-1111")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.StartPairing(PairModeQR); err != nil {
		t.Fatalf("StartPairing: %v", err)
	}
	time.Sleep(20 * time.Millisecond) // let the pump actually start polling

	closeDone := make(chan struct{})
	go func() {
		mgr.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Manager.Close did not return promptly with a pairing pump running " +
			"(SIGKILL-on-teardown hazard — see CLAUDE.md's WhatsApp session identity note)")
	}
}

// TestClosingTheManagerDoesNotMakeAnAbandonedPairingWaitOnADisconnectShutdownAlreadyIssued
// is the regression test for the shutdown-latency regression
// disconnectAbandonedPairing's guard exists to prevent: Manager.Close
// (manager_boot.go) cancels this session's ctx AND issues its own
// m.backend.Disconnect(m.persistCtx, s) for it, in that same loop, before
// blocking on m.wg.Wait(). Without the s.mgr.closed guard, the pairing pump
// woken by that ctx cancellation reaches finishPairingFailure and makes a
// SECOND Disconnect call of its own — on persistCtx, which Close's shutdown
// cannot interrupt — so Close winds up waiting on two serialized disconnect
// calls instead of the one it already made itself.
//
// disconnectHandler below forces that serialization deterministically (a
// shared mutex, independent of how many goroutines the test HTTP server
// itself uses) rather than relying on real concurrency to line the two
// calls up: every call to POST …/disconnect blocks for disconnectBlock.
// With the guard, Close makes exactly ONE such call (its own) and returns
// in roughly disconnectBlock; without it, Close wants both calls to finish
// (its own AND the pump's) before wg.Wait() unblocks — roughly
// 2×disconnectBlock. closePromptBound sits strictly between the two, so
// this fails against the unguarded code and passes with the guard.
func TestClosingTheManagerDoesNotMakeAnAbandonedPairingWaitOnADisconnectShutdownAlreadyIssued(t *testing.T) {
	const disconnectBlock = 400 * time.Millisecond
	const closePromptBound = 700 * time.Millisecond // > 1×disconnectBlock, < 2×disconnectBlock

	var disconnectMu sync.Mutex
	client, calls := newWaxumFakeServer(t, waxumFakeOpts{
		disconnectHandler: func(w http.ResponseWriter, r *http.Request) {
			disconnectMu.Lock()
			defer disconnectMu.Unlock()
			time.Sleep(disconnectBlock)
			w.WriteHeader(http.StatusOK)
		},
	})
	b := &waxumBackend{
		client:         client,
		qrPollInterval: 2 * time.Millisecond,
		pollInterval:   time.Hour,
		pairTimeout:    time.Hour, // never reached — only Close's own ctx cancellation ends this pump
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := NewManager(ctx, b, newStubSessions(), newStubMsgs(), nil,
		Config{SendRate: 1, SendBurst: 3, ReconcilePar: 2})

	s, err := mgr.Create(context.Background(), "gw", "+62 812-0000-1111")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.StartPairing(PairModeQR); err != nil {
		t.Fatalf("StartPairing: %v", err)
	}
	time.Sleep(20 * time.Millisecond) // let the pump actually start polling

	start := time.Now()
	closeDone := make(chan struct{})
	go func() {
		mgr.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Manager.Close did not return at all within 3s — far worse than the regression under test")
	}
	elapsed := time.Since(start)

	if elapsed > closePromptBound {
		t.Fatalf("Manager.Close took %v (want <= %v): an abandoned pairing's own waxum disconnect "+
			"is waiting behind the shutdown's disconnect instead of being skipped — "+
			"see disconnectAbandonedPairing's s.mgr.closed guard", elapsed, closePromptBound)
	}
	if got := calls.disconnect.Load(); got != 1 {
		t.Fatalf("POST …/disconnect calls = %d, want exactly 1 (Close's own) — "+
			"the pairing pump made an extra one instead of skipping it", got)
	}
}

// TestAPairingPumpReachingItsHardDeadlineTellsWaxumToDisconnectSoAnAbandonedQRAttemptDoesNotRotateCodesForever
// is the direct regression test for the resource leak diagnosed against the
// live waxum fork: finishPairingFailure used to reset ONLY WSM's own state
// (StateNew) and never told waxum to stop, so an unscanned QR kept rotating
// and pair.attempts kept climbing on waxum's side forever (observed live:
// session 6686fd00…, pair.attempts=3744, ~20h after WSM itself had already
// given up). This fails against the pre-fix code because nothing ever calls
// POST …/disconnect when pairPump gives up at its deadline.
func TestAPairingPumpReachingItsHardDeadlineTellsWaxumToDisconnectSoAnAbandonedQRAttemptDoesNotRotateCodesForever(t *testing.T) {
	client, calls := newWaxumFakeServer(t, waxumFakeOpts{}) // never terminal, never scanned — by default
	b := &waxumBackend{client: client}
	mgr := newWaxumTestManager(t, b)
	s := testSession(mgr, StatePairing)
	ps := &PairingState{mode: PairModeQR}
	s.pairing = ps

	done := make(chan struct{})
	go func() {
		b.pairPump(context.Background(), s, ps, 2*time.Millisecond, time.Now().Add(20*time.Millisecond))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pairPump did not return at its deadline")
	}

	waitForCondition(t, "waxum to receive a Disconnect for the abandoned pairing attempt", func() bool {
		return calls.disconnect.Load() >= 1
	})
	if s.state != StateNew {
		t.Fatalf("s.state = %v, want StateNew after an abandoned pairing attempt", s.state)
	}
}

// TestAConnectFailureDuringStartPairingStillDisconnectsTheSessionCreateSessionJustCreated
// is rollbackPairing's regression test: CreateSession and Connect are two
// separate remote calls, not one atomic operation, so a Connect failure
// leaves a registry row on waxum's side with nothing in WSM pointing at it
// any more unless rollbackPairing also disconnects it.
//
// CreateSession answers 409 (ErrConflict) here, not 200: since StartPairing
// stopped calling Connect after a CreateSession that actually created a row
// (see that method's doc comment — waxum auto-connects a fresh session
// itself, and an immediate explicit connect used to race its migrations),
// Connect is only reachable at all on the "session already existed" path.
// This still exercises the exact scenario the test name describes — a
// registry row exists on waxum's side (created earlier, hence the 409) and
// Connect fails — just via the code path where StartPairing still issues
// that call.
func TestAConnectFailureDuringStartPairingStillDisconnectsTheSessionCreateSessionJustCreated(t *testing.T) {
	// waxumFakeOpts has no connectFn hook to make POST …/connect fail on
	// demand, so this test builds a minimal purpose-built server instead of
	// newWaxumFakeServer — just enough of the same shape for the one call
	// sequence under test (CreateSession conflicts, Connect fails).
	var disconnects atomic.Int32
	var creates atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		creates.Add(1)
		w.WriteHeader(http.StatusConflict)
	})
	mux.HandleFunc("/api/v1/sessions/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/connect"):
			w.WriteHeader(http.StatusInternalServerError)
		case strings.HasSuffix(r.URL.Path, "/disconnect"):
			disconnects.Add(1)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	wc := waxum.New(srv.URL, "test-token", 5*time.Second,
		waxum.WithClientRateLimit(1e6, 1e6),
		waxum.WithRetryPolicy(1, func(int, string) time.Duration { return time.Millisecond }))

	b := &waxumBackend{client: wc}
	mgr := newWaxumTestManager(t, b)
	s, err := mgr.Create(context.Background(), "gw", "+62 812-0000-1111")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.StartPairing(PairModeQR); err == nil {
		t.Fatal("StartPairing: err = nil, want a Connect failure to propagate")
	}
	if creates.Load() != 1 {
		t.Fatalf("CreateSession calls = %d, want 1 (StartPairing must still have created the row before Connect failed)", creates.Load())
	}
	waitForCondition(t, "waxum to receive a Disconnect for the session CreateSession just created", func() bool {
		return disconnects.Load() >= 1
	})
	if s.state != StateNew {
		t.Fatalf("s.state = %v, want StateNew after a failed StartPairing", s.state)
	}
}

// TestStartPairingDoesNotConnectAFreshlyCreatedWaxumSession is the direct
// regression test for the race root-caused live 2026-09-13: waxum's
// POST /sessions auto-connects a brand-new session itself, and an explicit
// POST …/connect issued immediately after used to arrive while that
// auto-connect bot was still running the fresh store's migrations, racing a
// second bot onto the same sqlite file and producing
// "Internal error: migration error" with no client left afterward. Fails
// against the pre-fix StartPairing, which called Connect unconditionally
// after a successful CreateSession.
func TestStartPairingDoesNotConnectAFreshlyCreatedWaxumSession(t *testing.T) {
	client, calls := newWaxumFakeServer(t, waxumFakeOpts{createStatus: http.StatusCreated})
	b := &waxumBackend{client: client, qrPollInterval: time.Hour, pollInterval: time.Hour, pairTimeout: time.Hour}
	mgr := newWaxumTestManager(t, b)
	s, err := mgr.Create(context.Background(), "gw", "+62 812-0000-3333")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.StartPairing(PairModeQR); err != nil {
		t.Fatalf("StartPairing: %v", err)
	}
	time.Sleep(20 * time.Millisecond) // let the pairing pump run a tick or two

	if got := calls.create.Load(); got != 1 {
		t.Fatalf("POST /api/v1/sessions calls = %d, want 1", got)
	}
	if got := calls.connect.Load(); got != 0 {
		t.Fatalf("POST …/connect calls = %d, want 0 — waxum auto-connects a freshly created "+
			"session itself; an explicit connect this soon races the fresh store's migrations "+
			"(see StartPairing's doc comment for the live incident)", got)
	}
}

// TestStartPairingStillConnectsAnExistingWaxumSession guards the re-pair
// path: when CreateSession reports the session already exists
// (waxum.ErrConflict — a re-pair after logout, or a retried StartPairing),
// waxum does NOT auto-connect it, so StartPairing must still issue an
// explicit Connect exactly as before this phase.
func TestStartPairingStillConnectsAnExistingWaxumSession(t *testing.T) {
	client, calls := newWaxumFakeServer(t, waxumFakeOpts{createStatus: http.StatusConflict})
	b := &waxumBackend{client: client, qrPollInterval: time.Hour, pollInterval: time.Hour, pairTimeout: time.Hour}
	mgr := newWaxumTestManager(t, b)
	s, err := mgr.Create(context.Background(), "gw", "+62 812-0000-4444")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.StartPairing(PairModeQR); err != nil {
		t.Fatalf("StartPairing: %v", err)
	}

	waitForCondition(t, "StartPairing to issue an explicit Connect for a session waxum already knew about", func() bool {
		return calls.connect.Load() >= 1
	})
}

// TestStartPairingSetupTimesOutInsteadOfHangingForeverAndLeavesTheSessionInStateNew
// is the regression test for the 2026-09-13 incident: waxum answered every
// request with a multi-hour Retry-After after the laptop resumed from
// suspend, and StartPairing's setup calls (CreateSession here) had no
// deadline of their own — s.mgr.rootCtx is never cancelled in normal
// operation — so the session sat wedged in StatePairing for hours with
// every further pair/start answering 409. This fake server never responds
// to POST /api/v1/sessions within any time this test cares about (it
// sleeps well past b.pairingSetupTimeout before ever answering), standing
// in for a wedged backend of any shape; only b.pairingSetupTimeout
// (injected short here, the way qrPollInterval/pairTimeout already are in
// these tests) can end the call. mapErr has no case for a context
// deadline — CreateSession's error here is a plain wrapped
// context.DeadlineExceeded, not one of waxum's typed sentinels or an
// *APIError — so it falls through mapErr's default case unchanged; the
// handler (pairing.go's PostPairStart) then answers a generic 500, not the
// dedicated 503 ErrBackendBusy gets. Fails against the pre-fix
// StartPairing, which passes ctx straight through with no timeout and
// hangs until the test's own 2s watchdog fires.
func TestStartPairingSetupTimesOutInsteadOfHangingForeverAndLeavesTheSessionInStateNew(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		// Deliberately longer than pairingSetupTimeout below but short
		// enough not to hold up the test/httptest.Server.Close() itself —
		// a plain <-r.Context().Done() was tried here first and left the
		// handler goroutine (and httptest.Server.Close, which waits for
		// every connection to go idle) blocked well past this test's own
		// deadline: cancelling the CLIENT's context does not reliably
		// close the underlying connection fast enough for the SERVER side
		// to observe it in this harness, so a fixed sleep is the
		// deterministic way to simulate "never responds in time".
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	// A large retryDelay would never actually be slept for: CreateSession
	// here never gets a response at all (no status code to retry on), so
	// this is exercising the request's own ctx deadline, not do()'s 429
	// retry loop — WithClientRateLimit is still raised so the limiter
	// itself never delays this single call.
	client := waxum.New(srv.URL, "test-token", 5*time.Second, waxum.WithClientRateLimit(1e6, 1e6))

	b := &waxumBackend{client: client, pairingSetupTimeout: 30 * time.Millisecond}
	mgr := newWaxumTestManager(t, b)
	s, err := mgr.Create(context.Background(), "gw", "+62 812-0000-5555")
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- s.StartPairing(PairModeQR) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("StartPairing against a waxum that never responds: err = nil, want the setup timeout to fail it")
		}
		if errors.Is(err, ErrWrongState) || errors.Is(err, ErrBackendBusy) || errors.Is(err, ErrNotConnected) {
			t.Errorf("StartPairing err = %v, want a generic (context deadline) error — mapErr has no case for "+
				"context.DeadlineExceeded, so this must NOT be one of the typed sentinels", err)
		}
		t.Logf("StartPairing returned (as expected, mapErr has no dedicated case for this): %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("StartPairing did not return within its setup timeout — a wedged backend must not hang the pairing handler forever")
	}
	if s.state != StateNew {
		t.Fatalf("s.state = %v, want StateNew — a timed-out setup call must roll back exactly like a hard CreateSession failure", s.state)
	}
}

// TestPairingFailsFastWhenWaxumReportsAPairError is pairPump's point 8
// regression test: waxum's own pair.last_error, sustained across
// pairErrorHysteresisPolls consecutive polls with no live client for this
// attempt anywhere (socket_alive false, reachability nil) and no QR ever
// served, must end the pairing promptly with a message naming waxum's own
// text — not run out the (here, deliberately long) deadline showing
// "Waiting for WhatsApp…". Fails against the pre-fix pairPump, which never
// reads Status.Pair at all.
func TestPairingFailsFastWhenWaxumReportsAPairError(t *testing.T) {
	client, _ := newWaxumFakeServer(t, waxumFakeOpts{
		statusFn: func(int) waxum.Status {
			return waxum.Status{
				Status:      "disconnected",
				IsLoggedIn:  false,
				SocketAlive: false,
				Pair:        waxum.Pair{LastError: "Internal error: migration error"},
			}
		},
	})
	b := &waxumBackend{client: client}
	mgr := newWaxumTestManager(t, b)
	s := testSession(mgr, StatePairing)
	ps := &PairingState{mode: PairModeQR}
	s.pairing = ps

	const deadlineBudget = 30 * time.Second
	start := time.Now()
	done := make(chan struct{})
	go func() {
		b.pairPump(context.Background(), s, ps, 2*time.Millisecond, time.Now().Add(deadlineBudget))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pairPump did not fail fast on waxum's pair error — it is waiting out the deadline instead")
	}
	if elapsed := time.Since(start); elapsed >= deadlineBudget {
		t.Fatalf("pairPump took %v, want well under its %v deadline", elapsed, deadlineBudget)
	}
	snap := ps.Snapshot()
	if !snap.Done || snap.Err == "" {
		t.Fatalf("PairingState after a waxum pair error = %+v, want Done with a non-empty Err", snap)
	}
	if !strings.Contains(snap.Err, "migration error") {
		t.Fatalf("PairingState.Err = %q, want it to contain waxum's own message (%q)", snap.Err, "migration error")
	}
}

// TestAStaleWaxumPairErrorDoesNotAbortALivePairing is point 8's guard
// against the false positive Status.Pair's doc comment warns about: waxum
// does not clear pair.last_error when a later attempt succeeds, so a poll
// carrying it alongside evidence the socket IS actually alive (and a QR has
// already been served) must not abort a pairing that is otherwise
// proceeding normally.
func TestAStaleWaxumPairErrorDoesNotAbortALivePairing(t *testing.T) {
	client, calls := newWaxumFakeServer(t, waxumFakeOpts{
		statusFn: func(int) waxum.Status {
			return waxum.Status{
				Status:      "waiting_for_qr",
				IsLoggedIn:  false,
				SocketAlive: true, // a live client for THIS attempt — the error is stale, from an earlier one
				Pair:        waxum.Pair{LastError: "Internal error: migration error"},
			}
		},
		qrFn: func(int) []string { return []string{"code-A"} },
	})
	b := &waxumBackend{client: client}
	mgr := newWaxumTestManager(t, b)
	s := testSession(mgr, StatePairing)
	ps := &PairingState{mode: PairModeQR}
	s.pairing = ps

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		b.pairPump(ctx, s, ps, 2*time.Millisecond, time.Now().Add(time.Minute))
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	waitForCondition(t, "a QR code to be served", func() bool { return calls.qr.Load() >= 1 })
	waitForCondition(t, "several more status polls with the stale pair error still present", func() bool {
		return calls.status.Load() >= 10
	})
	if snap := ps.Snapshot(); snap.Done {
		t.Fatalf("PairingState = %+v, want NOT done — a stale pair.last_error with a live socket "+
			"and a served QR must not abort pairing", snap)
	}
}

// TestAFailingWaxumDisconnectDuringPairingFailureStillLeavesTheSessionInStateNew
// proves the disconnect this phase added is genuinely best-effort: even
// when waxum answers something other than success or 404 for the
// disconnect, the local state transition to StateNew — the thing an
// operator actually depends on to retry pairing — must happen regardless.
func TestAFailingWaxumDisconnectDuringPairingFailureStillLeavesTheSessionInStateNew(t *testing.T) {
	client, calls := newWaxumFakeServer(t, waxumFakeOpts{
		disconnectFn: func(int) int { return http.StatusInternalServerError },
	})
	b := &waxumBackend{client: client}
	mgr := newWaxumTestManager(t, b)
	s := testSession(mgr, StatePairing)
	ps := &PairingState{mode: PairModeQR}
	s.pairing = ps

	b.finishPairingFailure(s, ps, errors.New("boom"))

	if s.state != StateNew {
		t.Fatalf("s.state = %v, want StateNew even though waxum's Disconnect call failed — "+
			"the disconnect must be best-effort and never gate the local state transition", s.state)
	}
	waitForCondition(t, "the failing Disconnect to actually have been attempted", func() bool {
		return calls.disconnect.Load() >= 1
	})
	if snap := ps.Snapshot(); !snap.Done || snap.Err == "" {
		t.Fatalf("PairingState after failure = %+v, want Done with a non-empty Err", snap)
	}
}

// TestPairingSucceedsWhenWaxumReportsLoggedInEvenWhileStatusIsStillConnecting
// is the regression test for the live pairing failure documented in
// mapWaxumStatus's and pairPump's doc comments: waxum's own "status" field
// tracks the socket, not login, and can sit at "connecting" long after
// is_logged_in has already flipped true. Before this fix pairPump switched
// on st.Status alone, never matched "connecting", ran to its deadline, and
// finishPairingFailure rolled the session back to new with an empty jid
// even though the phone had already linked. This test fails against that
// old behaviour: with a huge deadline/poll interval, only an immediate
// success (on the very first poll) can make it return within 2s.
func TestPairingSucceedsWhenWaxumReportsLoggedInEvenWhileStatusIsStillConnecting(t *testing.T) {
	const phone = "6285132148259"
	client, _ := newWaxumFakeServer(t, waxumFakeOpts{
		statusFn: func(int) waxum.Status {
			return waxum.Status{Status: "connecting", IsLoggedIn: true, SocketAlive: true, PhoneNumber: phone} // SocketAlive: true — this test is about the STATUS TEXT lagging reality, not about the socket itself being down; see TestPairingSuccessDoesNotReportConnectedWhileTheSocketIsDown for that axis
		},
	})
	b := &waxumBackend{client: client}
	mgr := newWaxumTestManager(t, b)
	s := testSession(mgr, StatePairing)
	ps := &PairingState{mode: PairModeQR}
	s.pairing = ps

	done := make(chan struct{})
	go func() {
		b.pairPump(context.Background(), s, ps, time.Hour, time.Now().Add(time.Hour))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pairPump did not finish promptly on is_logged_in:true while status was still " +
			"\"connecting\" — status must not be required for pairing success")
	}

	wantJID := types.NewJID(phone, types.DefaultUserServer)
	if s.jid != wantJID {
		t.Fatalf("s.jid = %v, want %v", s.jid, wantJID)
	}
	if s.state != StateConnected {
		t.Fatalf("s.state = %v, want %v", s.state, StateConnected)
	}
	if snap := ps.Snapshot(); !snap.Done || snap.Err != "" {
		t.Fatalf("PairingState after success = %+v, want Done with an empty Err", snap)
	}
}

// TestPairingDoesNotSucceedWhileNotLoggedIn is the inverse of the test
// above: a "connecting" status with is_logged_in:false must NOT be treated
// as paired, however status-flavoured it looks. The pump is only ended here
// by cancelling ctx (never by a match), proving finishPairingSuccess was
// never called.
func TestPairingDoesNotSucceedWhileNotLoggedIn(t *testing.T) {
	client, calls := newWaxumFakeServer(t, waxumFakeOpts{
		statusFn: func(int) waxum.Status {
			return waxum.Status{Status: "connecting", IsLoggedIn: false}
		},
	})
	b := &waxumBackend{client: client}
	mgr := newWaxumTestManager(t, b)
	s := testSession(mgr, StatePairing)
	ps := &PairingState{mode: PairModeQR}
	s.pairing = ps

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		b.pairPump(ctx, s, ps, 2*time.Millisecond, time.Now().Add(time.Hour))
		close(done)
	}()

	waitForCondition(t, "a few status polls while not logged in", func() bool { return calls.status.Load() >= 5 })
	if s.state == StateConnected {
		t.Fatal("s.state reached StateConnected while is_logged_in stayed false")
	}
	if !s.jid.IsEmpty() {
		t.Fatalf("s.jid = %v, want empty — pairing must not be treated as finished", s.jid)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pairPump did not return promptly after ctx was cancelled")
	}
	if snap := ps.Snapshot(); !snap.Done || snap.Err == "" {
		t.Fatalf("PairingState after cancel = %+v, want Done with a non-empty Err "+
			"(pairing must end in failure, not silently succeed)", snap)
	}
}

// TestALoggedInSessionWithADeadSocketIsDisconnectedNotLoggedOut guards the
// terminal-state hazard from mapWaxumStatus's doc comment (point 2):
// is_logged_in:true with a dead socket is a transient reconnect, not a
// logout, and must map to StateDisconnected, never StateLoggedOut —
// StateLoggedOut is terminal in WSM and clears wa_sessions.active.
func TestALoggedInSessionWithADeadSocketIsDisconnectedNotLoggedOut(t *testing.T) {
	b := &waxumBackend{}
	mgr := newWaxumTestManager(t, b)
	s := testSession(mgr, StateConnected)

	b.applyStatus(s, &waxum.Status{Status: "connecting", IsLoggedIn: true, SocketAlive: false})

	if s.state != StateDisconnected {
		t.Fatalf("s.state = %v, want %v (logged in but socket dead must not be StateLoggedOut)",
			s.state, StateDisconnected)
	}
	if b.Live(s) {
		t.Fatal("Live(s) = true with socket_alive:false, want false")
	}
}

// TestLiveIsTrueOnlyWhenLoggedInAndSocketAlive exercises every quadrant of
// mapWaxumStatus's is_logged_in/socket_alive rule (its doc comment, points
// 1–2 and 4) against Live specifically — the field Enqueue gates sends on.
func TestLiveIsTrueOnlyWhenLoggedInAndSocketAlive(t *testing.T) {
	cases := []struct {
		name        string
		isLoggedIn  bool
		socketAlive bool
		wantLive    bool
		wantState   State
	}{
		{"logged in, socket alive", true, true, true, StateConnected},
		{"logged in, socket dead", true, false, false, StateDisconnected},
		{"not logged in, socket reported alive", false, true, false, StateDisconnected},
		{"not logged in, socket dead", false, false, false, StateDisconnected},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := &waxumBackend{}
			mgr := newWaxumTestManager(t, b)
			s := testSession(mgr, StateNew)

			b.applyStatus(s, &waxum.Status{Status: "connecting", IsLoggedIn: c.isLoggedIn, SocketAlive: c.socketAlive})

			if got := b.Live(s); got != c.wantLive {
				t.Errorf("Live(s) = %v, want %v", got, c.wantLive)
			}
			if s.state != c.wantState {
				t.Errorf("s.state = %v, want %v", s.state, c.wantState)
			}
		})
	}
}

// TestPhonePairCodeIsRequestedOnlyAfterTheConnectionIsUp is the regression
// test for the third live defect: waxum's POST /pair requires an
// established connection and answers
//
//	waxum: unexpected status 500: {"success":false,"error":{"code":500,
//	"message":"Internal error: pair_with_code failed: client is not connected"}}
//
// when called too early. The fake here 500s /pair until /qr has served at
// least one payload (qrServed), then succeeds — mirroring backend_meow.go's
// pairPump, which only calls PairPhone after the QR channel's first item
// proves the socket is up. pairPump must retry rather than abort pairing on
// the early 500s.
func TestPhonePairCodeIsRequestedOnlyAfterTheConnectionIsUp(t *testing.T) {
	var qrServed atomic.Bool
	client, calls := newWaxumFakeServer(t, waxumFakeOpts{
		qrFn: func(int) []string {
			qrServed.Store(true)
			return []string{"code-A"}
		},
		pairFn: func(n int) (int, string) {
			if !qrServed.Load() {
				t.Error("PairCode was called before /qr ever served a payload")
			}
			if n < 3 {
				return http.StatusInternalServerError, ""
			}
			return http.StatusOK, "AAAA-BBBB"
		},
	})
	b := &waxumBackend{client: client}
	mgr := newWaxumTestManager(t, b)
	s := testSession(mgr, StatePairing)
	s.Phone = "6281200001111"
	ps := &PairingState{mode: PairModePhone}
	s.pairing = ps

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		b.pairPump(ctx, s, ps, 2*time.Millisecond, time.Now().Add(2*time.Second))
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	waitForCondition(t, "PairCode to eventually succeed", func() bool {
		return ps.Snapshot().PhoneCode == "AAAA-BBBB"
	})
	if calls.qr.Load() == 0 {
		t.Fatal("PairCode succeeded without /qr ever having been polled")
	}
	if snap := ps.Snapshot(); snap.Done {
		t.Fatalf("PairingState = %+v, want NOT done — early /pair 500s must be retried, "+
			"not treated as a fatal pairing failure", snap)
	}
}

// TestPairingPumpNudgesConnectOnceAfterAQRScanLeavesTheSessionDisconnected
// is the regression test for the fourth live defect: a session can sit at
// status:"disconnected"/is_logged_in:false/socket_alive:false after a QR
// scan without waxum recovering it on its own — the live fix was a single
// explicit POST /connect. The nudge must fire exactly once per pairing
// attempt (pairPump's doc comment, point 5) rather than on every poll,
// which would fight waxum's own reconnect watchdog.
func TestPairingPumpNudgesConnectOnceAfterAQRScanLeavesTheSessionDisconnected(t *testing.T) {
	client, calls := newWaxumFakeServer(t, waxumFakeOpts{
		qrFn:     func(int) []string { return []string{"code-A"} },
		statusFn: func(int) waxum.Status { return waxum.Status{Status: "disconnected"} },
	})
	b := &waxumBackend{client: client}
	mgr := newWaxumTestManager(t, b)
	s := testSession(mgr, StatePairing)
	ps := &PairingState{mode: PairModeQR}
	s.pairing = ps

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		b.pairPump(ctx, s, ps, 2*time.Millisecond, time.Now().Add(time.Minute))
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	waitForCondition(t, "several status polls while stuck disconnected", func() bool {
		return calls.status.Load() >= 10
	})
	if got := calls.connect.Load(); got != 1 {
		t.Fatalf("POST /connect nudges after 10 disconnected polls = %d, want exactly 1 "+
			"(must nudge once, not spam every poll and fight waxum's own reconnect watchdog)", got)
	}
}

// TestPairingSurvivesATransientRateLimit is the direct regression test for
// the production failure this whole change addresses: waxum answering 429
// to StartPairing's CreateSession call used to abort pairing outright via
// rollbackPairing (mapped to the misleading "send queue full"). With
// internal/waxum.Client.do now retrying a 429 with backoff before StartPairing
// ever sees an error, a transient rate limit must be invisible to the
// caller — StartPairing returns nil and pairing completes normally.
func TestPairingSurvivesATransientRateLimit(t *testing.T) {
	const phone = "6281200002222"
	client, calls := newWaxumFakeServer(t, waxumFakeOpts{
		createFn: func(n int) int {
			if n <= 3 {
				return http.StatusTooManyRequests
			}
			return http.StatusOK
		},
		statusFn: func(int) waxum.Status {
			return waxum.Status{Status: "connected", IsLoggedIn: true, SocketAlive: true, PhoneNumber: phone}
		},
	})
	b := &waxumBackend{client: client, qrPollInterval: 2 * time.Millisecond, pollInterval: time.Hour, pairTimeout: 2 * time.Second}
	mgr := newWaxumTestManager(t, b)
	s, err := mgr.Create(context.Background(), "gw", "+62 812-0000-2222")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.StartPairing(PairModeQR); err != nil {
		t.Fatalf("StartPairing with a transient rate limit on CreateSession: err = %v, want nil — "+
			"a transient 429 must be retried by the client, not roll the pairing back", err)
	}

	// finishPairingSuccess clears s.pairing on success (pairPump's doc
	// comment), so PairingSnapshot's ok flips back to false once pairing
	// has actually finished — StateConnected is the durable signal to wait
	// on here, matching pairStatus's own "no pairing box + connected means
	// success" rule (internal/handler/pairing.go).
	waitForCondition(t, "pairing to finish successfully despite the transient 429s", func() bool {
		return s.Snapshot().State == StateConnected
	})
	if got := calls.create.Load(); got < 4 {
		t.Fatalf("POST /api/v1/sessions calls = %d, want >= 4 (3 rate-limited + at least 1 success)", got)
	}
	if got := calls.create.Load(); got < 4 {
		t.Fatalf("POST /api/v1/sessions calls = %d, want >= 4 (3 rate-limited + at least 1 success)", got)
	}
}

// TestPairingStillGivesUpAtTheDeadlineWhenRateLimitedForever proves a
// PERMANENT rate limit — every single call under /api/v1/sessions/ answering
// 429 forever — still cannot hang pairPump past its deadline. This is
// distinct from do()'s own bounded per-call retry count: even though each
// individual Status()/QR() call eventually gives up and returns
// ErrRateLimited on its own, the pump's outer loop must still respect
// deadline regardless of how many individual calls that takes.
func TestPairingStillGivesUpAtTheDeadlineWhenRateLimitedForever(t *testing.T) {
	client, _ := newWaxumFakeServer(t, waxumFakeOpts{alwaysRateLimited: true})
	b := &waxumBackend{client: client}
	mgr := newWaxumTestManager(t, b)
	s := testSession(mgr, StatePairing)
	ps := &PairingState{mode: PairModeQR}
	s.pairing = ps

	done := make(chan struct{})
	go func() {
		b.pairPump(context.Background(), s, ps, 2*time.Millisecond, time.Now().Add(50*time.Millisecond))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pairPump did not return at its deadline even though waxum rate-limits every call — " +
			"a permanently rate-limited backend must not hang pairing forever")
	}
	if snap := ps.Snapshot(); !snap.Done || snap.Err == "" {
		t.Fatalf("PairingState after deadline = %+v, want Done with a non-empty Err", snap)
	}
}

// TestTheQREndpointIsNotPolledWhenTheSessionIsNotPairing is the regression
// test for point 7's QR-polling gate: once a QR payload has been seen and
// the status has moved out of a QR-wanting state (waiting_for_qr/
// waiting_for_pair_code), further ticks must not call GET /qr at all —
// before this fix, every tick of every pairing session cost a QR request
// unconditionally, which was a major contributor to the 429 storm this
// change addresses (CLAUDE.md's waxum rate-limit hazard note).
func TestTheQREndpointIsNotPolledWhenTheSessionIsNotPairing(t *testing.T) {
	client, calls := newWaxumFakeServer(t, waxumFakeOpts{
		statusFn: func(n int) waxum.Status {
			if n <= 2 {
				return waxum.Status{Status: "waiting_for_qr"}
			}
			return waxum.Status{Status: "connecting"} // left the QR-wanting state; still not logged in
		},
		qrFn: func(int) []string { return []string{"code-A"} },
	})
	b := &waxumBackend{client: client}
	mgr := newWaxumTestManager(t, b)
	s := testSession(mgr, StatePairing)
	ps := &PairingState{mode: PairModeQR}
	s.pairing = ps

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		b.pairPump(ctx, s, ps, 2*time.Millisecond, time.Now().Add(time.Minute))
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	waitForCondition(t, "status to move past waiting_for_qr", func() bool { return calls.status.Load() >= 5 })
	qrCallsAtTransition := calls.qr.Load()
	if qrCallsAtTransition == 0 {
		t.Fatal("GET /qr was never polled while the session WAS waiting_for_qr — test setup is broken")
	}

	waitForCondition(t, "several more status polls after leaving the QR-wanting state", func() bool {
		return calls.status.Load() >= 20
	})
	if got := calls.qr.Load(); got != qrCallsAtTransition {
		t.Fatalf("GET /qr was polled %d more times (from %d to %d) after the session left waiting_for_qr "+
			"with a code already seen — it must not be polled once neither condition holds",
			got-qrCallsAtTransition, qrCallsAtTransition, got)
	}
}

// TestPairingNeverPersistsAJIDWithAnEmptyUser is the regression test for the
// live bug documented in pairPump's point 6 and finishPairingSuccess's
// doc comment: waxum flips is_logged_in to true BEFORE phone_number is
// populated. The fake here reproduces exactly that — is_logged_in:true with
// an empty phone_number on the very first successful poll, then the real
// number on the next. The persisted jid must be the REAL number and must
// NEVER be built from an empty user (which would render as bare
// "s.whatsapp.net", not "@s.whatsapp.net" — see types.JID.String()).
func TestPairingNeverPersistsAJIDWithAnEmptyUser(t *testing.T) {
	const realPhone = "6285132148259"
	client, _ := newWaxumFakeServer(t, waxumFakeOpts{
		statusFn: func(n int) waxum.Status {
			if n == 1 {
				return waxum.Status{Status: "connected", IsLoggedIn: true, SocketAlive: true} // phone_number still empty
			}
			return waxum.Status{Status: "connected", IsLoggedIn: true, SocketAlive: true, PhoneNumber: realPhone}
		},
	})
	b := &waxumBackend{client: client}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	repo := newStubSessions()
	mgr := NewManager(ctx, b, repo, newStubMsgs(), nil, Config{SendRate: 1, SendBurst: 3, ReconcilePar: 2})
	s := testSession(mgr, StatePairing)
	s.Phone = "6280000000000" // deliberately different from realPhone, so the assertion below can tell which source won
	ps := &PairingState{mode: PairModeQR}
	s.pairing = ps

	done := make(chan struct{})
	go func() {
		b.pairPump(context.Background(), s, ps, 2*time.Millisecond, time.Now().Add(2*time.Second))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pairPump did not finish")
	}

	wantJID := types.NewJID(realPhone, types.DefaultUserServer)
	if s.jid != wantJID {
		t.Fatalf("s.jid = %v, want %v (the real phone_number, reported one tick later)", s.jid, wantJID)
	}
	// SetPaired's "paired:<jid>" entry is not necessarily the LAST status
	// recorded — applyStatus's own setState("connected") writes again right
	// after it (both land in the same stub history) — so scan the whole
	// history for the "paired:" entry rather than trusting lastStatus.
	persistedJID, ok := pairedJIDFromHistory(repo, s.ID)
	if !ok {
		t.Fatalf("no SetPaired call recorded in history for %s", s.ID)
	}
	if persistedJID == "s.whatsapp.net" || persistedJID == "@s.whatsapp.net" {
		t.Fatalf("persisted jid = %q — an empty-user jid was written to wa_sessions, "+
			"exactly the live bug this test guards against", persistedJID)
	}
	if persistedJID != wantJID.String() {
		t.Fatalf("persisted jid = %q, want %q", persistedJID, wantJID.String())
	}
}

// TestPairingFallsBackToTheConfiguredPhoneWhenWaxumNeverReportsOne covers
// pairPump's point 6 fallback: is_logged_in stays true but phone_number
// stays empty for the whole grace window (maxPendingLoginTicks), so
// finishPairingSuccess must fall back to s.Phone — the operator-supplied,
// NormalizePhone-validated number — rather than hang forever waiting for a
// value waxum is never going to send, and never build an empty-user jid.
func TestPairingFallsBackToTheConfiguredPhoneWhenWaxumNeverReportsOne(t *testing.T) {
	const configuredPhone = "6280000009999"
	client, _ := newWaxumFakeServer(t, waxumFakeOpts{
		statusFn: func(int) waxum.Status {
			return waxum.Status{Status: "connected", IsLoggedIn: true, SocketAlive: true} // phone_number NEVER populated
		},
	})
	b := &waxumBackend{client: client}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	repo := newStubSessions()
	mgr := NewManager(ctx, b, repo, newStubMsgs(), nil, Config{SendRate: 1, SendBurst: 3, ReconcilePar: 2})
	s := testSession(mgr, StatePairing)
	s.Phone = configuredPhone
	ps := &PairingState{mode: PairModeQR}
	s.pairing = ps

	done := make(chan struct{})
	go func() {
		b.pairPump(context.Background(), s, ps, 2*time.Millisecond, time.Now().Add(2*time.Second))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pairPump did not finish — the grace window must bound the wait, not the outer 2s deadline")
	}

	wantJID := types.NewJID(configuredPhone, types.DefaultUserServer)
	if s.jid != wantJID {
		t.Fatalf("s.jid = %v, want %v (fallback to s.Phone)", s.jid, wantJID)
	}
	if snap := ps.Snapshot(); !snap.Done || snap.Err != "" {
		t.Fatalf("PairingState = %+v, want Done with no error — the device DID log in", snap)
	}
}

// TestPairingSuccessDoesNotReportConnectedWhileTheSocketIsDown is the
// regression test for the live 503: finishPairingSuccess used to force
// StateConnected/Live=true unconditionally on is_logged_in alone, so
// Enqueue admitted a send before waxum's socket was actually usable and
// SendText failed with 503 "Client not connected". is_logged_in:true with
// socket_alive:false must land the session in StateDisconnected (NOT
// StateConnected, NOT StateLoggedOut — see mapWaxumStatus) and Live() false,
// so Enqueue correctly refuses instead of admitting a doomed send.
func TestPairingSuccessDoesNotReportConnectedWhileTheSocketIsDown(t *testing.T) {
	const phone = "6281299990000"
	client, _ := newWaxumFakeServer(t, waxumFakeOpts{
		statusFn: func(int) waxum.Status {
			return waxum.Status{Status: "connecting", IsLoggedIn: true, SocketAlive: false, PhoneNumber: phone}
		},
	})
	b := &waxumBackend{client: client, pollInterval: time.Hour}
	mgr := newWaxumTestManager(t, b)
	s := testSession(mgr, StatePairing)
	ps := &PairingState{mode: PairModeQR}
	s.pairing = ps

	done := make(chan struct{})
	go func() {
		b.pairPump(context.Background(), s, ps, time.Hour, time.Now().Add(time.Hour))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pairPump did not finish promptly on the very first is_logged_in:true poll")
	}

	if got := s.Snapshot().State; got == StateConnected {
		t.Fatalf("state = %v after a pairing success with socket_alive:false, want NOT connected "+
			"(this is the exact production 503 \"Client not connected\" this test guards against)", got)
	}
	if b.Live(s) {
		t.Fatal("Live(s) = true with socket_alive:false, want false")
	}
	if _, err := s.Enqueue("m1", ToJID("628120001"), "hi"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("Enqueue on a logged-in-but-not-socket-alive session: err = %v, want ErrNotConnected "+
			"(must refuse rather than admit a send waxum will 503)", err)
	}
}

// TestTheStatusPumpPromotesToConnectedOnceTheSocketComesAlive is the other
// half of the fix above: once socket_alive catches up, the next statusPump
// tick (not a special case in finishPairingSuccess) must promote the
// session to StateConnected/Live — pairing success and the steady-state
// poller share exactly one place State/Live are computed (applyStatus), so
// this is the same code path TestPairingSuccessDoesNotReportConnectedWhileTheSocketIsDown
// exercises, one tick later.
func TestTheStatusPumpPromotesToConnectedOnceTheSocketComesAlive(t *testing.T) {
	const phone = "6281299990000"
	var socketAlive atomic.Bool
	client, _ := newWaxumFakeServer(t, waxumFakeOpts{
		statusFn: func(int) waxum.Status {
			return waxum.Status{Status: "connected", IsLoggedIn: true, SocketAlive: socketAlive.Load(), PhoneNumber: phone}
		},
	})
	b := &waxumBackend{client: client, pollInterval: 2 * time.Millisecond}
	mgr := newWaxumTestManager(t, b)
	s := testSession(mgr, StatePairing)
	ps := &PairingState{mode: PairModeQR}
	s.pairing = ps

	done := make(chan struct{})
	go func() {
		b.pairPump(context.Background(), s, ps, time.Hour, time.Now().Add(time.Hour))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pairPump did not finish")
	}
	if got := s.Snapshot().State; got == StateConnected {
		t.Fatalf("state = %v right after pairing success with socket_alive:false, want NOT connected", got)
	}

	socketAlive.Store(true)
	waitForCondition(t, "statusPump to promote the session once socket_alive is true", func() bool {
		return s.Snapshot().State == StateConnected
	})
	if !b.Live(s) {
		t.Fatal("Live(s) = false after socket_alive turned true, want true")
	}
}

// TestA503FromWaxumIsRetriedRatherThanFailingTheMessage is the send-side
// regression test: waxum's 503 "Client not connected" now maps to
// ErrNotConnected (mapErr), which isRetryable (sender_limits.go) already
// retries — a send that races a still-dialling socket right after a
// pairing success must succeed on a later attempt instead of hard-failing
// on the first.
func TestA503FromWaxumIsRetriedRatherThanFailingTheMessage(t *testing.T) {
	var textCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/sessions/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/messages/text") {
			w.WriteHeader(http.StatusOK)
			return
		}
		n := textCalls.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"success":false,"error":{"code":503,"message":"Client not connected"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(waxum.MessageResult{MessageID: "wamid-1"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := waxum.New(srv.URL, "test-token", 5*time.Second,
		waxum.WithClientRateLimit(1e6, 1e6),
		waxum.WithRetryPolicy(1, func(int, string) time.Duration { return time.Millisecond }))

	b := &waxumBackend{client: client}
	mgr := newWaxumTestManager(t, b)
	s := testSession(mgr, StateConnected)
	s.startSenderLocked()

	done, err := s.Enqueue("m1", ToJID("628120001"), "hi")
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	select {
	case res := <-done:
		if res.Err != nil {
			t.Fatalf("send result Err = %v, want nil — a transient 503 must be retried until it succeeds", res.Err)
		}
		if res.WAMessageID != "wamid-1" {
			t.Fatalf("WAMessageID = %q, want %q", res.WAMessageID, "wamid-1")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("send did not complete — a retryable 503 must not hang past maxAttempts/backoff")
	}
	if got := textCalls.Load(); got != 3 {
		t.Fatalf("POST .../messages/text was called %d times, want 3 (2 x 503 + 1 success)", got)
	}
}

// TestDeletingASessionSurfacesAFailedBackendLogoutInsteadOfSilentlyOrphaningIt
// is the regression test for the live incident where DELETE calls to waxum
// were rate-limited (429) during a delete storm, the resulting error was
// discarded (`_ = m.backend.Logout(...)`), and wa_sessions went to 0 rows
// while waxum still held the sessions — permanently orphaned, with nothing
// in the UI or logs to show it. The WSM row must still be deleted (a failed
// remote logout must not block the local delete), but the failure must be
// OBSERVABLE — via the session's own ring log, since s is still reachable
// by pointer even after Manager.Delete removes it from the session map.
func TestDeletingASessionSurfacesAFailedBackendLogoutInsteadOfSilentlyOrphaningIt(t *testing.T) {
	client, calls := newWaxumFakeServer(t, waxumFakeOpts{
		deleteFn: func(int) int { return http.StatusTooManyRequests }, // permanently rate-limited
	})
	b := &waxumBackend{client: client}
	mgr := newWaxumTestManager(t, b)
	s, err := mgr.Create(context.Background(), "gw", "+62 812-0000-3333")
	if err != nil {
		t.Fatal(err)
	}

	if err := mgr.Delete(context.Background(), s.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := mgr.Get(s.ID); ok {
		t.Fatal("session is still reachable via Manager.Get after Delete")
	}
	assertRingContains(t, s, "backend logout failed")
	if got := calls.delete_.Load(); got == 0 {
		t.Fatal("DELETE /api/v1/sessions/{id} was never called")
	}
}

// TestDeletingASessionAfterATransientRateLimitLogsOutCleanly is the paired
// positive case: a 429 that clears within do()'s own retry budget must
// result in a normal remote delete with NO orphan warning recorded.
func TestDeletingASessionAfterATransientRateLimitLogsOutCleanly(t *testing.T) {
	client, calls := newWaxumFakeServer(t, waxumFakeOpts{
		deleteFn: func(n int) int {
			if n <= 2 {
				return http.StatusTooManyRequests
			}
			return http.StatusOK
		},
	})
	b := &waxumBackend{client: client}
	mgr := newWaxumTestManager(t, b)
	s, err := mgr.Create(context.Background(), "gw", "+62 812-0000-4444")
	if err != nil {
		t.Fatal(err)
	}

	if err := mgr.Delete(context.Background(), s.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for _, evt := range s.events.Snapshot() {
		if strings.Contains(evt.Note, "backend logout failed") {
			t.Fatalf("ring recorded a logout-failure warning (%q) despite the 429 clearing within the retry budget", evt.Note)
		}
	}
	if got := calls.delete_.Load(); got != 3 {
		t.Fatalf("DELETE /api/v1/sessions/{id} was called %d times, want 3 (2 rate-limited + 1 success)", got)
	}
}

// TestTheStatusPumpCanBeRestartedAfterItExits is the regression test for
// Defect B: before this fix, waxumSessionState.pumping was never reset to
// false when statusPump returned on ctx.Done(), so ensureStatusPump's
// CompareAndSwap(false, true) guard could never succeed again for that
// session id — WSM would go permanently blind for it (Live() frozen at
// whatever it last observed), with no way back short of a process restart.
func TestTheStatusPumpCanBeRestartedAfterItExits(t *testing.T) {
	client, calls := newWaxumFakeServer(t, waxumFakeOpts{})
	b := &waxumBackend{client: client, pollInterval: 2 * time.Millisecond}
	mgr := newWaxumTestManager(t, b)
	s := testSession(mgr, StateNew)

	firstCtx, firstCancel := context.WithCancel(context.Background())
	b.ensureStatusPump(firstCtx, s)

	waitForCondition(t, "the first status pump makes at least one poll",
		func() bool { return calls.status.Load() >= 1 })

	firstCancel() // ends the first pump via ctx.Done()

	waitForCondition(t, "pumping resets to false once the first pump has exited",
		func() bool { return !b.sessionState(s.ID).pumping.Load() })

	// A second ensureStatusPump call, matching what Connect (session.go's
	// Activate, or this package's own reconnect supervisor) does on every
	// (re)connect, must actually start polling again — proving the
	// CompareAndSwap guard is usable again, not just that the flag reads
	// false.
	countAtRestart := calls.status.Load()
	secondCtx, secondCancel := context.WithCancel(context.Background())
	t.Cleanup(secondCancel)
	b.ensureStatusPump(secondCtx, s)

	waitForCondition(t, "the restarted status pump makes a NEW poll beyond the first pump's count",
		func() bool { return calls.status.Load() > countAtRestart })
}

// TestAPairedSessionReportingWaitingForQRIsTreatedAsLoggedOutNotDisconnected
// is the regression test for Defect 4 (mapWaxumStatus's doc comment, point
// 3): a session WSM already has a jid for, whose waxum status has fallen
// back to waiting_for_qr/waiting_for_pair_code with is_logged_in:false,
// means the device was unlinked on the phone side — a re-pair is required,
// not a reconnect. The old mapping produced StateDisconnected here, which
// left the operator staring at a socket the reconnect supervisor
// (reconnect.go) would spin on forever without ever being able to bring it
// back (only a re-pair can), with no UI signal explaining why.
//
// applyStatus now requires loggedOutHysteresisPolls CONSECUTIVE qualifying
// polls before committing to StateLoggedOut (see that constant's doc
// comment) — this test drives exactly that many calls to still prove the
// terminal verdict is eventually reached; the single-poll case (it must NOT
// fire early) is TestASingleWaitingForQRPollDoesNotDeclareTheSessionLoggedOut's
// job specifically, to keep this test's own regression purpose readable.
func TestAPairedSessionReportingWaitingForQRIsTreatedAsLoggedOutNotDisconnected(t *testing.T) {
	b := &waxumBackend{}
	mgr := newWaxumTestManager(t, b)

	paired := testSession(mgr, StateConnected)
	paired.jid = types.NewJID("6281200009999", types.DefaultUserServer)
	for i := 0; i < loggedOutHysteresisPolls; i++ {
		b.applyStatus(paired, &waxum.Status{Status: "waiting_for_qr", IsLoggedIn: false, SocketAlive: false})
	}
	if paired.state != StateLoggedOut {
		t.Fatalf("paired session: state = %v, want %v (device unlinked — needs a re-pair, not a reconnect)",
			paired.state, StateLoggedOut)
	}

	// Control case: a session that was NEVER paired (no jid yet) reaching
	// the exact same waxum status is an ordinary fresh pairing attempt in
	// progress, not a logout — mapWaxumStatus's point 4. Regressing this
	// would make EVERY brand-new QR pairing attempt appear logged-out
	// before it ever had a chance to succeed.
	fresh := testSession(mgr, StatePairing)
	b.applyStatus(fresh, &waxum.Status{Status: "waiting_for_qr", IsLoggedIn: false, SocketAlive: false})
	if fresh.state != StatePairing {
		t.Fatalf("never-paired session: state = %v, want %v (a fresh pairing attempt must not be treated as logged out)",
			fresh.state, StatePairing)
	}
}

// TestASingleWaitingForQRPollDoesNotDeclareTheSessionLoggedOut is the
// regression test for the production flap loggedOutHysteresisPolls's doc
// comment describes: ONE qualifying poll (paired, !is_logged_in, status
// waiting_for_qr) must not, on its own, commit the terminal StateLoggedOut
// verdict — waxum's /status is demonstrably twitchy, and a session merely
// re-dialling was seen logged_out, then un-logged_out 15s later, then fully
// connected two minutes after that, all from single-poll snapshots. Before
// applyStatus's hysteresis, this exact call sequence (see the now-updated
// TestAPairedSessionReportingWaitingForQRIsTreatedAsLoggedOutNotDisconnected,
// which used to make a single such call) reached StateLoggedOut
// immediately.
func TestASingleWaitingForQRPollDoesNotDeclareTheSessionLoggedOut(t *testing.T) {
	b := &waxumBackend{}
	mgr := newWaxumTestManager(t, b)

	s := testSession(mgr, StateConnected)
	s.jid = pairedTestJID()

	b.applyStatus(s, &waxum.Status{Status: "waiting_for_qr", IsLoggedIn: false, SocketAlive: false})

	if got := s.Snapshot().State; got != StateDisconnected {
		t.Fatalf("state after ONE qualifying poll = %v, want %v (loggedOutHysteresisPolls=%d requires more than one poll before the terminal verdict commits)",
			got, StateDisconnected, loggedOutHysteresisPolls)
	}
}

// TestRepeatedWaitingForQRPollsEventuallyDeclareTheSessionLoggedOut proves
// the hysteresis DELAYS the terminal verdict rather than preventing it: a
// genuinely unlinked device — one that keeps reporting the same qualifying
// status on every poll, with nothing healthy interleaved — must still reach
// StateLoggedOut, exactly loggedOutHysteresisPolls consecutive polls in.
func TestRepeatedWaitingForQRPollsEventuallyDeclareTheSessionLoggedOut(t *testing.T) {
	b := &waxumBackend{}
	mgr := newWaxumTestManager(t, b)

	s := testSession(mgr, StateConnected)
	s.jid = pairedTestJID()

	for i := 1; i <= loggedOutHysteresisPolls; i++ {
		b.applyStatus(s, &waxum.Status{Status: "waiting_for_qr", IsLoggedIn: false, SocketAlive: false})
		got := s.Snapshot().State
		if i < loggedOutHysteresisPolls {
			if got == StateLoggedOut {
				t.Fatalf("state after %d/%d consecutive qualifying polls = %v, want NOT logged_out yet",
					i, loggedOutHysteresisPolls, got)
			}
			continue
		}
		if got != StateLoggedOut {
			t.Fatalf("state after all %d consecutive qualifying polls = %v, want %v (a genuinely logged-out session must still reach the terminal state, only later)",
				loggedOutHysteresisPolls, got, StateLoggedOut)
		}
	}
}

// TestANonQualifyingPollResetsTheLoggedOutEvidence proves the streak is
// RESET, not merely paused, by an interleaved healthy poll — a session that
// almost reached the threshold, then genuinely recovered, then started
// flapping again must earn a whole fresh streak of loggedOutHysteresisPolls,
// not just the one or two more it would take if the earlier near-miss still
// counted.
func TestANonQualifyingPollResetsTheLoggedOutEvidence(t *testing.T) {
	if loggedOutHysteresisPolls < 2 {
		t.Fatalf("this test requires loggedOutHysteresisPolls >= 2 to exercise a near-miss streak, got %d", loggedOutHysteresisPolls)
	}
	b := &waxumBackend{}
	mgr := newWaxumTestManager(t, b)

	s := testSession(mgr, StateConnected)
	s.jid = pairedTestJID()

	// One qualifying poll short of the threshold.
	for i := 0; i < loggedOutHysteresisPolls-1; i++ {
		b.applyStatus(s, &waxum.Status{Status: "waiting_for_qr", IsLoggedIn: false, SocketAlive: false})
	}
	if got := s.Snapshot().State; got == StateLoggedOut {
		t.Fatalf("state after %d/%d qualifying polls = %v, want not yet logged_out (test setup is broken)",
			loggedOutHysteresisPolls-1, loggedOutHysteresisPolls, got)
	}

	// A genuinely healthy poll interleaved: is_logged_in AND socket_alive
	// both true. This must reset the streak, not just pause it.
	b.applyStatus(s, &waxum.Status{Status: "connected", IsLoggedIn: true, SocketAlive: true})
	if got := s.Snapshot().State; got != StateConnected {
		t.Fatalf("state after the interleaved healthy poll = %v, want %v", got, StateConnected)
	}

	// Fewer than loggedOutHysteresisPolls fresh qualifying polls must NOT
	// be enough — if the earlier near-miss streak had merely been paused
	// instead of reset to 0, this exact count would already have reached
	// the threshold before the healthy poll.
	for i := 0; i < loggedOutHysteresisPolls-1; i++ {
		b.applyStatus(s, &waxum.Status{Status: "waiting_for_qr", IsLoggedIn: false, SocketAlive: false})
	}
	if got := s.Snapshot().State; got == StateLoggedOut {
		t.Fatalf("state = %v after only %d fresh qualifying polls following the reset, want NOT logged_out yet (the interleaved healthy poll should have reset the streak to 0, not merely paused it)",
			got, loggedOutHysteresisPolls-1)
	}

	// One more qualifying poll completes a full FRESH streak and must
	// finally commit the terminal verdict.
	b.applyStatus(s, &waxum.Status{Status: "waiting_for_qr", IsLoggedIn: false, SocketAlive: false})
	if got := s.Snapshot().State; got != StateLoggedOut {
		t.Fatalf("state after a full fresh streak post-reset = %v, want %v", got, StateLoggedOut)
	}
}

// strp returns a pointer to v — waxum.Status.Reachability is a *string
// (models.go) specifically so applyStatus/isOrphanedClientWedge can tell
// "the field was JSON null" (nil) apart from "the field was a real, if
// unhealthy, value" (a non-nil pointer) — see isOrphanedClientWedge's doc
// comment. Tests below construct both shapes explicitly.
func strp(v string) *string { return &v }

// TestSustainedOrphanedClientWedgeMarksTheSessionStalledWithTheWaxumRestartReason
// is the core regression test for the 2026-09-09 production incident
// (isOrphanedClientWedge's doc
// comment): waxum's own session runtime can be left holding NO client
// object at all for a session, permanently, after a
// Deactivate-then-Activate landed within about a second of each other —
// every further /connect just gets evicted by the old bot's still-
// unwinding run loop. waxum's /status renders that exactly as
// is_logged_in:true, socket_alive:false, reachability:null. Once that
// fingerprint has been observed on loggedOutHysteresisPolls CONSECUTIVE
// polls, the session must be marked Stalled with a reason that names the
// real remedy (restart the waxum PROCESS), not the generic
// reconnect.go attempt-count reason — reverting isOrphanedClientWedge (or
// applyWedgeDetection's call from applyStatus) makes this session sit at
// Stalled=false forever, since reconnect.go's OWN attempt-count stall never
// even runs here (nothing in this test calls Backend.Connect through the
// supervisor).
func TestSustainedOrphanedClientWedgeMarksTheSessionStalledWithTheWaxumRestartReason(t *testing.T) {
	b := &waxumBackend{}
	mgr := newWaxumTestManager(t, b)

	s := testSession(mgr, StateConnected)
	s.jid = pairedTestJID()

	wedged := &waxum.Status{Status: "connecting", IsLoggedIn: true, SocketAlive: false} // Reachability left nil: the wedge fingerprint
	for i := 0; i < loggedOutHysteresisPolls; i++ {
		b.applyStatus(s, wedged)
	}

	snap := s.Snapshot()
	if !snap.Stalled {
		t.Fatalf("Stalled = false after %d consecutive orphaned-client-wedge polls, want true", loggedOutHysteresisPolls)
	}
	if snap.StalledReason != wedgeStalledReason {
		t.Fatalf("StalledReason = %q, want the waxum-restart wedge reason %q", snap.StalledReason, wedgeStalledReason)
	}
	// mapWaxumStatus's own verdict must stay untouched: the session really
	// is disconnected, not logged out — this feature must never repaint
	// that.
	if snap.State != StateDisconnected {
		t.Fatalf("State = %v, want %v (the wedge escalates Stalled, it must never change State)", snap.State, StateDisconnected)
	}
}

// TestATransientDisconnectWithAReachabilityValueDoesNotTriggerTheWedgeStall
// is the negative case isOrphanedClientWedge exists to exclude: an
// ORDINARY, recoverable disconnect also has is_logged_in && !socket_alive
// (mapWaxumStatus's point 2), but a live client object still exists and
// reports a concrete Reachability value (e.g. "reconnecting") — only a nil
// Reachability means no client object exists at all. Sending this same,
// non-nil-Reachability status on loggedOutHysteresisPolls consecutive polls
// (i.e. clearing every doubt that this is just a slow first poll) must
// never stall the session.
func TestATransientDisconnectWithAReachabilityValueDoesNotTriggerTheWedgeStall(t *testing.T) {
	b := &waxumBackend{}
	mgr := newWaxumTestManager(t, b)

	s := testSession(mgr, StateConnected)
	s.jid = pairedTestJID()

	reconnecting := &waxum.Status{Status: "connecting", IsLoggedIn: true, SocketAlive: false, Reachability: strp("reconnecting")}
	for i := 0; i < loggedOutHysteresisPolls+2; i++ {
		b.applyStatus(s, reconnecting)
	}

	if snap := s.Snapshot(); snap.Stalled {
		t.Fatalf("Stalled = true after an ordinary disconnect with a non-nil Reachability, want false — StalledReason = %q", snap.StalledReason)
	}
}

// TestAHealthySessionNeverTriggersTheWedgeStall guards the third
// isOrphanedClientWedge conjunct in isolation: is_logged_in && socket_alive
// (a fully healthy poll) must never stall the session, regardless of how
// many consecutive polls report it — socket_alive alone already rules the
// wedge out.
func TestAHealthySessionNeverTriggersTheWedgeStall(t *testing.T) {
	b := &waxumBackend{}
	mgr := newWaxumTestManager(t, b)

	s := testSession(mgr, StateConnected)
	s.jid = pairedTestJID()

	healthy := &waxum.Status{Status: "connected", IsLoggedIn: true, SocketAlive: true}
	for i := 0; i < loggedOutHysteresisPolls+2; i++ {
		b.applyStatus(s, healthy)
	}

	if snap := s.Snapshot(); snap.Stalled {
		t.Fatalf("Stalled = true for a fully healthy session, want false — StalledReason = %q", snap.StalledReason)
	}
}

// TestOneOrTwoWedgeQualifyingPollsAloneDoNotStallTheSession proves the
// hysteresis window is real for the wedge signal too, not just for
// StateLoggedOut: fewer than loggedOutHysteresisPolls consecutive
// qualifying polls must never stall the session, even though every one of
// them individually matches isOrphanedClientWedge.
func TestOneOrTwoWedgeQualifyingPollsAloneDoNotStallTheSession(t *testing.T) {
	if loggedOutHysteresisPolls < 2 {
		t.Fatalf("this test requires loggedOutHysteresisPolls >= 2 to exercise a near-miss streak, got %d", loggedOutHysteresisPolls)
	}
	b := &waxumBackend{}
	mgr := newWaxumTestManager(t, b)

	s := testSession(mgr, StateConnected)
	s.jid = pairedTestJID()

	wedged := &waxum.Status{Status: "connecting", IsLoggedIn: true, SocketAlive: false}
	for i := 1; i < loggedOutHysteresisPolls; i++ {
		b.applyStatus(s, wedged)
		if snap := s.Snapshot(); snap.Stalled {
			t.Fatalf("Stalled = true after only %d/%d consecutive qualifying polls, want false (the hysteresis window is not yet full)",
				i, loggedOutHysteresisPolls)
		}
	}
}

// TestActivateClearsTheWedgeStallButItReturnsOnTheNextPollIfTheWedgePersists
// is applyWedgeDetection's other documented contract (its own doc comment,
// and reconnect.go's markStalled/Activate doc comments this mirrors): a
// manual Activate always clears Stalled — the operator must always be able
// to retry — but for THIS specific condition retrying genuinely cannot
// help (isOrphanedClientWedge's doc comment), so if the wedge is still
// there on the very next poll the session must re-stall with the SAME
// reason instead of silently resuming an unbounded retry loop. Mirrors
// TestTheReconnectAttemptCounterSurvivesATransientLoggedOutFlap's shape: a
// fakeBackend drives the supervisor/Activate (no real network), and a
// standalone waxumBackend drives applyStatus directly against the same
// session — applyStatus never touches mgr.backend, only its own
// sessionState(s.ID) bookkeeping and s itself.
func TestActivateClearsTheWedgeStallButItReturnsOnTheNextPollIfTheWedgePersists(t *testing.T) {
	fake := &fakeBackend{name: "fake", connectFn: func(context.Context, *ManagedSession) error { return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mgr := NewManager(ctx, fake, newStubSessions(), newStubMsgs(), nil, fastReconnectConfig())

	s := testSession(mgr, StateDisconnected)
	s.jid = pairedTestJID()
	s.active = true
	registerSession(mgr, s)

	b := &waxumBackend{}
	wedged := &waxum.Status{Status: "connecting", IsLoggedIn: true, SocketAlive: false}
	for i := 0; i < loggedOutHysteresisPolls; i++ {
		b.applyStatus(s, wedged)
	}
	if snap := s.Snapshot(); !snap.Stalled || snap.StalledReason != wedgeStalledReason {
		t.Fatalf("setup: Stalled = %v, StalledReason = %q, want true / the wedge reason before exercising Activate",
			snap.Stalled, snap.StalledReason)
	}

	if err := s.Activate(ctx); err != nil {
		t.Fatalf("Activate on a wedge-stalled session: err = %v, want nil (fakeBackend.connectFn always succeeds)", err)
	}
	if snap := s.Snapshot(); snap.Stalled {
		t.Fatalf("Stalled = true right after Activate, want false — Activate must always let the operator retry")
	}

	// The wedge itself has NOT actually cleared — no waxum process was
	// restarted in this test — so the very next poll must re-stall the
	// session with the same reason, not resume looping silently.
	b.applyStatus(s, wedged)
	snap := s.Snapshot()
	if !snap.Stalled {
		t.Fatal("session did not re-stall on the next poll after Activate even though the wedge condition still persists")
	}
	if snap.StalledReason != wedgeStalledReason {
		t.Fatalf("StalledReason after re-stall = %q, want the same waxum-restart wedge reason", snap.StalledReason)
	}
}

// TestTheReconnectAttemptCounterSurvivesATransientLoggedOutFlap ties the
// applyStatus hysteresis fix back to reconnect.go's supervisor — the actual
// harm the production incident caused, not just the state-machine mapping
// in isolation. reconnect.go's scan loop deletes its attempts/nextAttempt
// bookkeeping for a session the instant s.reconnectEligible() goes false
// (the `!s.reconnectEligible()` branch, its own doc comment), and
// StateLoggedOut is deliberately excluded from eligibility (session.go's
// reconnectEligible) because it is terminal. Before this fix, ONE
// qualifying poll — reproduced here exactly as observed live, then
// "un-flapped" 15s later per loggedOutHysteresisPolls's doc comment's
// transition log — would have flipped the session to StateLoggedOut long
// enough for the supervisor's very next scan tick to see !reconnectEligible
// and wipe its attempt count, so the NEXT eligible tick started counting
// over at 1: the "1, 2, 1, 2, 1, 2, 3…" symptom observed live instead of a
// number climbing toward reconnectDefaultStallThreshold. With the fix, the
// flap never leaves StateDisconnected, so reconnectEligible never goes
// false and the count is never wiped.
func TestTheReconnectAttemptCounterSurvivesATransientLoggedOutFlap(t *testing.T) {
	fake := &fakeBackend{name: "fake", connectFn: func(context.Context, *ManagedSession) error { return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	// A high stall threshold: this test only needs a handful of attempts
	// to fire, and must not have the supervisor give up and stall mid-test.
	mgr := NewManager(ctx, fake, newStubSessions(), newStubMsgs(), nil, fastReconnectConfigWithStallThreshold(10))

	s := testSession(mgr, StateDisconnected)
	s.jid = pairedTestJID()
	s.active = true
	registerSession(mgr, s)

	waitForConditionWithin(t, 3*time.Second, "the supervisor fires at least 2 automatic reconnect attempts",
		func() bool { return fake.connectCalls.Load() >= 2 })

	// Inject the exact production flap. b is a standalone waxumBackend used
	// only to drive applyStatus directly against s — applyStatus never
	// touches mgr.backend (fake, wired above for the supervisor's own
	// Connect calls), only its own sessionState(s.ID) bookkeeping and s
	// itself, so this is safe to call against a session whose Manager was
	// built with a different Backend.
	b := &waxumBackend{}
	b.applyStatus(s, &waxum.Status{Status: "waiting_for_qr", IsLoggedIn: false, SocketAlive: false}) // one qualifying poll
	if got := s.Snapshot().State; got != StateDisconnected {
		t.Fatalf("state right after the single qualifying poll = %v, want %v (must not go terminal on one poll)", got, StateDisconnected)
	}
	b.applyStatus(s, &waxum.Status{Status: "disconnected", IsLoggedIn: false, SocketAlive: false}) // "un-flap", 15s later in the real log
	if got := s.Snapshot().State; got != StateDisconnected {
		t.Fatalf("state after the un-flap = %v, want %v", got, StateDisconnected)
	}

	waitForConditionWithin(t, 3*time.Second, "the supervisor fires at least one more attempt after the flap",
		func() bool { return fake.connectCalls.Load() >= 3 })

	// The actual bookkeeping claim: the attempt counter reconnect.go prints
	// into each ring note (its "attempt %d/%d" reason string) must never
	// have reset to 1 after the flap.
	var oneCount int
	for _, note := range ringNotes(s) {
		if strings.Contains(note, "attempt 1/") {
			oneCount++
		}
	}
	if oneCount != 1 {
		t.Fatalf("ring contains %d \"attempt 1/...\" auto-reconnect notes, want exactly 1 (a reset after the flap would add a second one); notes = %v",
			oneCount, ringNotes(s))
	}
}

// TestStatusPumpRecordsAndBacksOffOnRepeatedPollFailures covers the second
// half of statusPump's Defect B fix: Status() failures are no longer
// silently dropped (each is logged AND rung — see setState's doc comment
// for why both), and repeated failures back the poll interval off instead
// of hammering an unreachable waxum at a fixed fast cadence forever
// ("resilient... rather than silently spinning"). The fake server here
// answers /status with 503 for its first two calls (WithRetryPolicy(1, ...)
// below makes each of THOSE count as exactly one failed statusPump poll,
// not itself retried away) then 200 forever, so this proves both the ring
// records the failures AND the pump keeps going and eventually recovers.
func TestStatusPumpRecordsAndBacksOffOnRepeatedPollFailures(t *testing.T) {
	var statusCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/status") {
			w.WriteHeader(http.StatusOK)
			return
		}
		n := statusCalls.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(waxum.Status{Status: "connected", IsLoggedIn: true, SocketAlive: true})
	}))
	t.Cleanup(srv.Close)
	client := waxum.New(srv.URL, "test-token", time.Second,
		waxum.WithClientRateLimit(1e6, 1e6),
		waxum.WithRetryPolicy(1, func(int, string) time.Duration { return time.Millisecond }))

	b := &waxumBackend{client: client, pollInterval: time.Millisecond}
	mgr := newWaxumTestManager(t, b)
	s := testSession(mgr, StateNew)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	b.ensureStatusPump(ctx, s)

	waitForCondition(t, "the session eventually recovers to StateConnected despite the first two poll failures",
		func() bool { return s.Snapshot().State == StateConnected })

	assertRingContains(t, s, "status poll failed")
}

// TestAWaxumSessionCanSendAfterConnectingWithoutPairing is the regression
// test for the live production bug: startSenderLocked used to be called
// from exactly one place in this backend, finishPairingSuccess, so a
// session that reached StateConnected via Connect ALONE — every boot
// reconcile after an app restart, for a session that was already paired in
// an earlier process — had s.state == StateConnected and Live(s) == true
// (both maintained by statusPump/applyStatus) but s.sendQ == nil, and
// enqueue (sender.go) gates on BOTH: `!connected || q == nil`. Every send
// failed with ErrNotConnected while every operator-facing signal
// (/sessions, /api/v1/health, SelectConnected) agreed the session was
// usable. This test never calls StartPairing — only Connect, exactly the
// boot-reconcile path — so it fails on the unfixed backend and passes once
// Connect itself starts the sender.
func TestAWaxumSessionCanSendAfterConnectingWithoutPairing(t *testing.T) {
	client, _ := newWaxumFakeServer(t, waxumFakeOpts{
		statusFn: func(int) waxum.Status {
			return waxum.Status{Status: "connected", IsLoggedIn: true, SocketAlive: true, PhoneNumber: "6281299990000"}
		},
	})
	b := &waxumBackend{client: client, pollInterval: 2 * time.Millisecond}
	mgr := newWaxumTestManager(t, b)
	// StateNew, jid already set: mirrors a session paired in an EARLIER
	// process — manager_boot.go's StartAll->Activate->Connect path for a
	// session that survives a restart, never StartPairing again.
	s := testSession(mgr, StateNew)
	s.jid = types.NewJID("6281299990000", types.DefaultUserServer)

	if err := b.Connect(context.Background(), s); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	waitForCondition(t, "statusPump to promote the session to StateConnected",
		func() bool { return s.Snapshot().State == StateConnected })

	if _, err := s.Enqueue("m1", ToJID("628120001"), "hi"); err != nil {
		t.Fatalf("Enqueue after Connect-only (no pairing) = %v, want nil — "+
			"a connected session with no send queue is exactly the bug "+
			"where /sessions and /api/v1/health both say connected but "+
			"every send fails", err)
	}
}

// TestConnectingTwiceDoesNotStartASecondSenderLoop pins the idempotency the
// fix above relies on: startSenderLocked (sender.go) early-returns once
// s.sendQ is already non-nil, so calling it on every Connect — including a
// duplicate boot reconcile, or a manual reconnect after Disconnect — must
// never replace the channel or spawn a second senderLoop goroutine for the
// same session. Asserted the only way that's observable from outside
// sender.go: the channel value identity is unchanged across two Connects.
func TestConnectingTwiceDoesNotStartASecondSenderLoop(t *testing.T) {
	client, _ := newWaxumFakeServer(t, waxumFakeOpts{
		statusFn: func(int) waxum.Status {
			return waxum.Status{Status: "connected", IsLoggedIn: true, SocketAlive: true, PhoneNumber: "6281299990000"}
		},
	})
	b := &waxumBackend{client: client, pollInterval: 2 * time.Millisecond}
	mgr := newWaxumTestManager(t, b)
	s := testSession(mgr, StateNew)
	s.jid = types.NewJID("6281299990000", types.DefaultUserServer)

	if err := b.Connect(context.Background(), s); err != nil {
		t.Fatalf("first Connect: %v", err)
	}
	waitForCondition(t, "statusPump to promote the session to StateConnected",
		func() bool { return s.Snapshot().State == StateConnected })

	s.mu.Lock()
	firstQ := s.sendQ
	s.mu.Unlock()
	if firstQ == nil {
		t.Fatal("sendQ is nil after the first Connect")
	}

	if err := b.Connect(context.Background(), s); err != nil {
		t.Fatalf("second Connect: %v", err)
	}
	s.mu.Lock()
	secondQ := s.sendQ
	s.mu.Unlock()
	if secondQ != firstQ {
		t.Fatal("sendQ channel identity changed across a second Connect — startSenderLocked started a second sender loop instead of no-op'ing")
	}
}
