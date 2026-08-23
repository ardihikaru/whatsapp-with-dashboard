package wa

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/waxum"
)

// waxumFakeCalls counts, per-endpoint, how many times the fake waxum server
// in this file has answered GET /status, GET /qr, POST /pair and POST
// /connect — enough for tests to wait for "N polls have happened", or
// assert a call count stayed exactly N (the connect-nudge idempotency
// tests), without sleeping a fixed, flaky amount.
type waxumFakeCalls struct {
	status  atomic.Int32
	qr      atomic.Int32
	pair    atomic.Int32
	connect atomic.Int32
}

// waxumFakeOpts configures newWaxumFakeServer. Zero value is a server that
// always reports a non-terminal, never-scanned pairing: exactly what the
// hard-deadline and context-cancellation tests need (nothing about it ever
// makes the pump exit on its own).
type waxumFakeOpts struct {
	createStatus int                      // HTTP status for POST /api/v1/sessions; 0 => 200
	statusFn     func(n int) waxum.Status // n is the 1-based call number
	qrFn         func(n int) []string
	// pairFn answers POST /pair. n is the 1-based call number. status==0
	// (or http.StatusOK) succeeds with code; any other status responds with
	// that code and a body shaped like waxum's real error (see
	// TestPhonePairCodeIsRequestedOnlyAfterTheConnectionIsUp's doc comment
	// for the exact live message this mirrors). Nil means every call
	// succeeds with a fixed code — not exercised by a test that never
	// pairs in phone mode.
	pairFn func(n int) (status int, code string)
}

// newWaxumFakeServer builds an httptest server covering exactly the waxum
// endpoints this phase's backend calls (CreateSession, Connect, Disconnect,
// Status, QR, PairCode — see internal/waxum/client.go), routed the same way
// waxum.Client itself builds paths. Every path this backend does not care
// about for a given test answers a bare 200, matching the client's own
// "empty body on out==nil" no-op case.
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
		w.WriteHeader(createStatus)
	})
	mux.HandleFunc("/api/v1/sessions/", func(w http.ResponseWriter, r *http.Request) {
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
		default: // /disconnect etc — not under test here
			w.WriteHeader(http.StatusOK)
		}
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return waxum.New(srv.URL, "test-token", 5*time.Second), calls
}

// newWaxumTestManager wires a Manager around backend for these tests, with
// a cancellable root context cleaned up automatically — any status/pairing
// pump a test starts via s.mgr.go1 is left to exit on its own after the
// test, rather than leaking past it indefinitely.
func newWaxumTestManager(t *testing.T, backend Backend) *Manager {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return NewManager(ctx, backend, newStubSessions(), newStubMsgs(),
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
			return waxum.Status{Status: "connected", IsLoggedIn: true, PhoneNumber: phone}
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
// against fake servers returning waxum's three typed-error statuses and
// checks the wa-package sentinel each maps to.
func TestWaxumBackendMapsClientErrorsOntoTheWAErrSentinels(t *testing.T) {
	cases := []struct {
		name string
		code int
		want error
	}{
		{"404 maps to ErrNotFound", http.StatusNotFound, ErrNotFound},
		{"409 maps to ErrNotConnected", http.StatusConflict, ErrNotConnected},
		{"429 maps to ErrQueueFull", http.StatusTooManyRequests, ErrQueueFull},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.code)
			}))
			t.Cleanup(srv.Close)
			b := &waxumBackend{client: waxum.New(srv.URL, "test-token", time.Second)}
			s := testSession(newWaxumTestManager(t, b), StateConnected)

			_, err := b.SendText(context.Background(), s, ToJID("628120001"), "hi")
			if !errors.Is(err, c.want) {
				t.Fatalf("SendText err = %v, want %v", err, c.want)
			}
		})
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
	mgr := NewManager(ctx, b, newStubSessions(), newStubMsgs(),
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
			return waxum.Status{Status: "connecting", IsLoggedIn: true, PhoneNumber: phone}
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
