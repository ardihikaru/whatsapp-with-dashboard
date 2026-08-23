package wa

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/waxum"
)

// This file holds the waxum implementation of Backend: a REST client to a
// separate Rust process (internal/waxum) instead of an in-process library.
// Two consequences fall out of that, both load-bearing:
//
//  1. Session ids are wa_sessions.id, passed to waxum verbatim as ITS
//     session id too (see waxum.Client.CreateSession's doc comment). This is
//     bijective by construction — no lookup table, no id translation layer —
//     because this backend never generates its own id and never needs to
//     reconcile one against wa_sessions.id. A translation table would be a
//     second source of truth for something that is already 1:1.
//  2. ManagedSession.client (a *whatsmeow.Client) stays nil forever under
//     this backend — there is no in-process client to attach. All transport
//     state this backend owns lives in waxumBackend.sessions instead (the
//     design note in backend.go anticipates exactly this). See sender.go's
//     Enqueue/trySend: their admission check was changed in this phase to
//     stop reading s.client (whatsmeow-only) and read s.state instead, for
//     precisely this reason.
type waxumBackend struct {
	client *waxum.Client

	qrPollInterval time.Duration // how often the pairing pump polls GET /qr and /status
	pollInterval   time.Duration // how often the steady-state status poller polls GET /status
	pairTimeout    time.Duration // hard deadline for the pairing pump (hazard: see pairPump)

	sessions sync.Map // id (string) -> *waxumSessionState
}

// waxumSessionState is this backend's own per-session bookkeeping —
// deliberately NOT stored on ManagedSession (see backend.go's design note:
// a second backend gets its own unexported state, not a slot on the shared
// struct).
type waxumSessionState struct {
	live    atomic.Bool // refreshed by statusPump; read (no I/O) by Live
	pumping atomic.Bool // true once statusPump has been started for this id
}

// WaxumConfig groups the timing knobs NewWaxumBackend needs. Kept as this
// package's own type (mirroring internal/config's WSM_WAXUM_* group)
// rather than importing internal/config here, matching every other backend
// constructor in this package taking plain values, not a config type from
// outside internal/wa.
type WaxumConfig struct {
	QRPollInterval time.Duration
	PollInterval   time.Duration
	PairTimeout    time.Duration
}

// NewWaxumBackend builds a Backend that drives a running waxum instance
// through client. client is expected to already carry the base URL and
// bearer token (see internal/waxum.New).
func NewWaxumBackend(client *waxum.Client, cfg WaxumConfig) Backend {
	return &waxumBackend{
		client:         client,
		qrPollInterval: cfg.QRPollInterval,
		pollInterval:   cfg.PollInterval,
		pairTimeout:    cfg.PairTimeout,
	}
}

func (b *waxumBackend) Name() string { return "waxum" }

// Close releases backend-level resources. Every goroutine this backend
// spawns (statusPump, pairPump) is wg-tracked via s.mgr.go1 and keyed to a
// session's OWN context (s.ctx, a child of Manager.rootCtx) — Manager.Close
// already cancels every session's ctx and joins the waitgroup BEFORE calling
// Close (manager_boot.go), so by the time this runs every such goroutine has
// already exited. Nothing manager-wide needs stopping here. Kept as a real
// (empty) method, matching meowBackend.Close, rather than omitted.
func (b *waxumBackend) Close() {}

// sessionState returns (creating on first use) this backend's bookkeeping
// for id. Only writers (Connect/StartPairing/statusPump) create an entry;
// Live only reads.
func (b *waxumBackend) sessionState(id string) *waxumSessionState {
	v, _ := b.sessions.LoadOrStore(id, &waxumSessionState{})
	return v.(*waxumSessionState)
}

func (b *waxumBackend) markLive(id string, live bool) {
	b.sessionState(id).live.Store(live)
}

// Live is O(1) and does NO I/O: an atomic load against state kept current by
// statusPump. See Backend.Live — this is called while Snapshot holds s.mu,
// on every 2s HTMX row poll and every send's load-balance pick.
func (b *waxumBackend) Live(s *ManagedSession) bool {
	v, ok := b.sessions.Load(s.ID)
	if !ok {
		return false
	}
	return v.(*waxumSessionState).live.Load()
}

// mapErr translates waxum's typed errors onto this package's sentinels
// (Backend's contract: handlers only ever see wa.Err*, never a waxum type).
func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, waxum.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, waxum.ErrConflict):
		return ErrNotConnected
	case errors.Is(err, waxum.ErrRateLimited):
		return ErrQueueFull
	default:
		return err
	}
}

// Connect (re)establishes the session's waxum-side connection: CreateSession
// (reuse=true — waxum keeps credentials server-side keyed by id) then
// Connect. An "already exists"/"already connected" 409 from either call is
// treated as success, not an error — Activate (session.go) may call this on
// an already-connected session (e.g. a duplicate boot reconcile), and that
// MUST be idempotent, exactly like whatsmeow's Connect (backend_meow.go)
// reusing an already-attached client instead of erroring.
func (b *waxumBackend) Connect(ctx context.Context, s *ManagedSession) error {
	if err := b.client.CreateSession(ctx, s.ID, s.Name, true); err != nil && !errors.Is(err, waxum.ErrConflict) {
		return mapErr(err)
	}
	if err := b.client.Connect(ctx, s.ID); err != nil && !errors.Is(err, waxum.ErrConflict) {
		return mapErr(err)
	}
	s.mu.Lock()
	sctx := s.ensureCtxLocked()
	s.mu.Unlock()
	b.ensureStatusPump(sctx, s)
	return nil
}

// Disconnect drops the waxum-side socket but keeps credentials. The live
// flag is flipped immediately for responsiveness; statusPump will confirm
// (or correct) it on its next tick regardless.
func (b *waxumBackend) Disconnect(ctx context.Context, s *ManagedSession) error {
	err := b.client.Disconnect(ctx, s.ID)
	b.markLive(s.ID, false)
	if err != nil && !errors.Is(err, waxum.ErrNotFound) {
		return mapErr(err)
	}
	return nil
}

// Logout revokes the paired device: waxum's DeleteSession, which already
// treats 404 as success (internal/waxum.Client.DeleteSession's doc
// comment) — a session waxum never heard of, or already forgot, logs out
// cleanly. This backend's own bookkeeping for id is dropped too, so a
// deleted-then-recreated session (same wa_sessions row reused is not
// possible — ids are crypto/rand — but a long-lived process deleting many
// sessions over its lifetime) does not leak sync.Map entries forever.
func (b *waxumBackend) Logout(ctx context.Context, s *ManagedSession) error {
	err := mapErr(b.client.DeleteSession(ctx, s.ID))
	b.sessions.Delete(s.ID)
	return err
}

// SendText is trySend's transport call for this backend.
func (b *waxumBackend) SendText(ctx context.Context, s *ManagedSession, to JID, text string) (string, error) {
	res, err := b.client.SendText(ctx, s.ID, to.String(), text)
	if err != nil {
		return "", mapErr(err)
	}
	return res.MessageID, nil
}

// JoinedGroups lists the groups the paired device belongs to.
func (b *waxumBackend) JoinedGroups(ctx context.Context, s *ManagedSession) ([]GroupInfo, error) {
	groups, err := b.client.Groups(ctx, s.ID)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]GroupInfo, 0, len(groups))
	for _, g := range groups {
		out = append(out, GroupInfo{JID: g.JID, Name: g.Name, Participants: g.ParticipantCount})
	}
	return out, nil
}

// mapWaxumStatus derives this package's State from a full waxum Status
// snapshot. This is the crux of the waxum integration, so the rule is
// spelled out in full:
//
// waxum's own "status" field tracks the SOCKET lifecycle, not login. A
// session can sit at status:"connecting" for 20+ seconds AFTER WhatsApp has
// actually logged it in — observed live, on a session whose own waxum log
// line said "Connected" and "offline_sync_completed" while /status kept
// answering {"status":"connecting","is_logged_in":true,"socket_alive":false}.
// So is_logged_in, NOT status, is the authoritative signal for "this
// session has valid credentials"; status/socket_alive only refine which
// usable-or-not state that credential is currently in. In order:
//
//  1. is_logged_in && socket_alive  -> StateConnected: fully up, sends work.
//  2. is_logged_in && !socket_alive -> StateDisconnected, deliberately NOT
//     StateLoggedOut. The credential is still good; only the socket
//     dropped — whatsmeow's auto-reconnect analogue, not a logout.
//     StateLoggedOut is terminal in WSM (clears wa_sessions.active), so
//     inferring it from a transient socket blip would wrongly discard a
//     session that waxum's own reconnect watchdog is about to bring back.
//  3. !is_logged_in && status is waiting_for_qr/waiting_for_pair_code ->
//     StatePairing: mid QR/phone-code flow, no credential issued yet.
//  4. !is_logged_in, anything else (disconnected, connecting, or a status
//     value this backend does not recognize yet) -> StateDisconnected.
//     Same reasoning as (2): this backend only ever produces StateLoggedOut
//     from an explicit Logout() call (session.go), never from a poll —
//     status polling alone can never prove a device was actually unlinked.
func mapWaxumStatus(st *waxum.Status) State {
	switch {
	case st.IsLoggedIn && st.SocketAlive:
		return StateConnected
	case st.IsLoggedIn:
		return StateDisconnected
	case st.Status == "waiting_for_qr" || st.Status == "waiting_for_pair_code":
		return StatePairing
	default:
		return StateDisconnected
	}
}

// ensureStatusPump starts statusPump for s exactly once per process — a
// second Connect (reconnect after a manual disconnect, a duplicate boot
// reconcile) must not spawn a second poller for the same id.
func (b *waxumBackend) ensureStatusPump(ctx context.Context, s *ManagedSession) {
	st := b.sessionState(s.ID)
	if !st.pumping.CompareAndSwap(false, true) {
		return // already running
	}
	s.mgr.go1(func() { b.statusPump(ctx, s) })
}

// statusPump is the sole state source for this backend today: it polls
// GET /status every pollInterval, refreshes the Live() atomic, and calls
// setState on a recognized transition. A future phase may add a waxum
// webhook that reports transitions immediately — this poller is
// deliberately kept as the whole state machine (not layered under
// something webhook-shaped) so that later addition can simply take
// precedence over a tick (e.g. by also calling applyStatus) without
// restructuring this loop.
//
// It is wg-tracked (spawned via s.mgr.go1) and exits solely on ctx.Done()
// — ctx is s.ctx, a child of Manager.rootCtx, canceled by
// Manager.Close/Delete, so this always returns promptly at teardown; no
// separate hard deadline is needed the way the pairing pump needs one (that
// one has no terminal item of its own — see pairPump).
func (b *waxumBackend) statusPump(ctx context.Context, s *ManagedSession) {
	for {
		if st, err := b.client.Status(ctx, s.ID); err == nil {
			b.applyStatus(s, st)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(b.pollInterval):
		}
	}
}

// applyStatus refreshes Live and the session's State from one GET /status
// response. Both derive from is_logged_in/socket_alive, not status alone —
// see mapWaxumStatus's doc comment for the full rule and why. Unlike the
// old status-string-only mapping, this is exhaustive (mapWaxumStatus always
// returns a concrete State), so setState is called on every tick; setState
// itself is already a no-op when the state hasn't actually changed.
func (b *waxumBackend) applyStatus(s *ManagedSession, st *waxum.Status) {
	b.markLive(s.ID, st.IsLoggedIn && st.SocketAlive)
	s.setState(mapWaxumStatus(st))
}

// StartPairing provisions the waxum-side session and starts the pairing
// pump. Mirrors meowBackend.StartPairing's shape (same state guard, same
// PairingState handoff) even though the transport underneath is entirely
// different — see pairPump's doc comment for what changes and why.
//
// Unlike an earlier version of this method, phone-mode pairing does NOT
// call PairCode here. waxum's POST /pair requires an established
// connection — calling it immediately after Connect (before the socket is
// actually up) is exactly the observed live failure:
//
//	waxum: unexpected status 500: {"success":false,"error":{"code":500,
//	"message":"Internal error: pair_with_code failed: client is not connected"}}
//
// pairPump now defers PairCode until it has independent evidence the
// connection is up (a QR payload having been served — see its doc comment)
// and retries it if waxum still 500s, exactly like whatsmeow's PairPhone
// call (backend_meow.go's pairPump) waiting for the first QR channel item
// before dialing.
func (b *waxumBackend) StartPairing(ctx context.Context, s *ManagedSession, mode PairMode) error {
	s.mu.Lock()
	if s.state != StateNew && s.state != StateLoggedOut {
		s.mu.Unlock()
		return ErrWrongState
	}
	ps := &PairingState{mode: mode}
	s.pairing, s.state = ps, StatePairing
	sctx := s.ensureCtxLocked()
	s.mu.Unlock()

	if err := b.client.CreateSession(ctx, s.ID, s.Name, true); err != nil && !errors.Is(err, waxum.ErrConflict) {
		b.rollbackPairing(s)
		return mapErr(err)
	}
	if err := b.client.Connect(ctx, s.ID); err != nil && !errors.Is(err, waxum.ErrConflict) {
		b.rollbackPairing(s)
		return mapErr(err)
	}
	_ = s.mgr.repo.UpdateStatus(s.mgr.persistCtx, s.ID, string(StatePairing))
	deadline := time.Now().Add(b.pairTimeout)
	s.mgr.go1(func() { b.pairPump(sctx, s, ps, b.qrPollInterval, deadline) })
	return nil
}

func (b *waxumBackend) rollbackPairing(s *ManagedSession) {
	s.mu.Lock()
	s.pairing = nil
	s.state = StateNew
	s.mu.Unlock()
}

// pairPump polls waxum for the duration of one pairing attempt. It replaces
// whatsmeow's QR channel (backend_meow.go's pairPump), and the replacement
// is not mechanical — REST polling has none of a channel's properties:
//
//  1. GET /qr returns qr_codes, an ARRAY that grows as codes rotate, on
//     EVERY call — not a fresh item only on rotation. Calling ps.SetCode on
//     every poll would bump PairingState.gen (pairing.go) every tick, and
//     the browser's <img src="qr.png?g=N"> would refetch forever even
//     though the code on screen never changed. lastCode is compared against
//     the newest element so SetCode (and the generation bump) only fires
//     when the payload actually changes — see
//     TestWaxumPairingPumpOnlyBumpsTheQRGenerationWhenThePayloadChanges.
//  2. A channel has exactly one terminal item and then closes; a poller has
//     none — it would loop forever on a device that never scans. deadline
//     (computed by the caller as time.Now().Add(WaxumPairTimeout)) is
//     enforced via context.WithDeadline, so this ALWAYS returns: on ctx
//     being canceled (shutdown/delete — ctx is s.ctx), on deadline, or on a
//     terminal status. Skipping this is the exact SIGKILL/lost-identity
//     hazard CLAUDE.md documents for Manager.Close: an unterminated pairing
//     pump keeps Manager.wg.Wait() blocked forever, teardown exceeds
//     compose's stop_grace_period, and the process is SIGKILLed mid-write to
//     the WhatsApp session's -wal file. See
//     TestManagerCloseReturnsPromptlyWithAPairingPumpRunning and
//     TestWaxumPairingPumpStopsAtTheHardDeadline.
//  3. Success is decided by is_logged_in alone, NOT status — see
//     mapWaxumStatus's doc comment for why status is unreliable as a
//     signal. status/phone_number/push_name are only corroboration:
//     phone_number is used (once is_logged_in is true it is always
//     populated) but nothing here requires status to also say
//     "connected"/"logged_in". See
//     TestPairingSucceedsWhenWaxumReportsLoggedInEvenWhileStatusIsStillConnecting,
//     the regression test for the live failure this fixed: a real pairing
//     sat at status:"connecting" with is_logged_in:true for 20+ seconds,
//     the old status-only check never matched, and the pump ran to its
//     deadline and rolled the session back to new with an empty jid even
//     though the phone had already linked successfully.
//  4. In phone mode, PairCode is requested from HERE, not from
//     StartPairing, and only once the pump has independent evidence the
//     connection is actually up (qrSeen — the first GET /qr call that
//     returned at least one payload; waxum serves QR payloads regardless of
//     pair mode, so this is a legitimate readiness signal for phone mode
//     too, exactly like backend_meow.go's pairPump waiting for the QR
//     channel's first item before calling PairPhone). Calling PairCode
//     before that is the observed live 500 ("client is not connected").
//     A 500 does not abort pairing — phoneCodeSet stays false and the next
//     tick retries — only a successful PairCode or the pump's own
//     deadline/cancellation ends the attempts. See
//     TestPhonePairCodeIsRequestedOnlyAfterTheConnectionIsUp.
//  5. A session can reach a genuine "disconnected, not logged in" status
//     after a QR scan without waxum recovering it on its own within the
//     pairing window (observed live: status stayed
//     disconnected/is_logged_in:false/socket_alive:false until an explicit
//     POST /connect was issued, which then let the session actually reach
//     is_logged_in:true). connectNudged makes that nudge exactly ONCE per
//     pairing attempt, deliberately not on every tick — waxum runs its own
//     reconnect watchdog (RECONNECT_WATCHDOG_POLL_MS, 30s by default) and
//     repeatedly calling Connect would fight it rather than help. See
//     TestPairingPumpNudgesConnectOnceAfterAQRScanLeavesTheSessionDisconnected.
func (b *waxumBackend) pairPump(ctx context.Context, s *ManagedSession, ps *PairingState,
	pollInterval time.Duration, deadline time.Time) {

	pctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	var lastCode string
	var qrSeen bool        // true once /qr has returned >=1 payload — proves the socket is up (point 4)
	var phoneCodeSet bool  // true once PairCode has succeeded — stop retrying (point 4)
	var connectNudged bool // true once a post-scan reconnect nudge has been sent (point 5)

	for {
		if st, err := b.client.Status(pctx, s.ID); err == nil {
			if st.IsLoggedIn { // point 3 — the ONLY signal that matters for success
				b.finishPairingSuccess(s, ps, st.PhoneNumber)
				return
			}
			if qrSeen && !connectNudged && st.Status == "disconnected" {
				_ = b.client.Connect(pctx, s.ID) // best-effort nudge; point 5
				connectNudged = true
			}
		}
		if qr, err := b.client.QR(pctx, s.ID); err == nil && len(qr.QRCodes) > 0 {
			qrSeen = true
			code := qr.QRCodes[len(qr.QRCodes)-1] // newest payload — see doc comment above
			if code != lastCode {
				lastCode = code
				ttl := time.Duration(qr.TimeoutSeconds) * time.Second
				if ttl <= 0 {
					ttl = pollInterval // defensive: waxum is expected to always send a positive timeout
				}
				ps.SetCode(code, ttl)
			}
		}
		if qrSeen && !phoneCodeSet && ps.mode == PairModePhone {
			if code, err := b.client.PairCode(pctx, s.ID, s.Phone, true); err != nil {
				ps.SetPhoneCode("", err) // recorded for the status partial; retried next tick — point 4
			} else {
				ps.SetPhoneCode(code.Code, nil)
				phoneCodeSet = true
			}
		}
		select {
		case <-pctx.Done():
			b.finishPairingFailure(s, ps, pctx.Err())
			return
		case <-time.After(pollInterval):
		}
	}
}

// finishPairingSuccess mirrors meowBackend's onPaired: persist the jid,
// start the sender, flip state, mark live. phoneNumber comes from the
// terminal Status response — waxum has no separate "success" event to read,
// is_logged_in:true IS the success signal (pairPump's doc comment, point 3).
//
// It also starts statusPump if it isn't already running. Reaching
// is_logged_in:true here does not mean waxum's connection is durable from
// this point on — the pairing pump itself may have needed a reconnect nudge
// to get here at all (pairPump's doc comment, point 5), and nothing else
// would otherwise poll this session's status again until some future
// Connect() call. Without this, Live()/State would go stale forever the
// first time the socket drops after a successful pairing.
func (b *waxumBackend) finishPairingSuccess(s *ManagedSession, ps *PairingState, phoneNumber string) {
	jid := types.NewJID(phoneNumber, types.DefaultUserServer)
	s.mu.Lock()
	s.jid = jid
	if s.pairing != nil {
		s.pairing.finish(nil)
		s.pairing = nil
	}
	s.startSenderLocked()
	sctx := s.ensureCtxLocked()
	s.mu.Unlock()
	_ = s.mgr.repo.SetPaired(s.mgr.persistCtx, s.ID, jid.String())
	s.setState(StateConnected)
	b.markLive(s.ID, true)
	b.ensureStatusPump(sctx, s)
	s.ring("paired as " + jid.String())
}

// finishPairingFailure mirrors meowBackend's onPairFailed: roll back to new,
// keep the pairing box (done+err) so the status partial can show why. No
// device row exists to purge (waxum only persists credentials on its own
// side, on success) — matches meow's comment for the same reason.
func (b *waxumBackend) finishPairingFailure(s *ManagedSession, ps *PairingState, cause error) {
	if cause == nil {
		cause = errors.New("pairing aborted")
	}
	s.mu.Lock()
	if s.pairing != nil {
		s.pairing.finish(cause)
	}
	s.state = StateNew
	s.mu.Unlock()
	_ = s.mgr.repo.UpdateStatus(s.mgr.persistCtx, s.ID, string(StateNew))
	s.ring("pairing failed: " + cause.Error())
}
