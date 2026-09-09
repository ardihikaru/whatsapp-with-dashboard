package wa

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/backoff"
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

	// eventsEnabled/eventPumpOnce/gatewayReachable/healthKnown belong to
	// the fleet-wide SSE event pump — see backend_waxum_events.go's
	// package doc comment for what each does.
	eventsEnabled    bool
	eventPumpOnce    sync.Once
	gatewayReachable atomic.Bool // last verdict from the event pump — see Reachable
	healthKnown      atomic.Bool // true once ANY verdict has been recorded
}

// waxumSessionState is this backend's own per-session bookkeeping —
// deliberately NOT stored on ManagedSession (see backend.go's design note:
// a second backend gets its own unexported state, not a slot on the shared
// struct).
type waxumSessionState struct {
	live    atomic.Bool // refreshed by statusPump; read (no I/O) by Live
	pumping atomic.Bool // true once statusPump has been started for this id

	// loggedOutStreak counts CONSECUTIVE applyStatus calls in a row whose
	// mapWaxumStatus verdict was StateLoggedOut (mapWaxumStatus's point 3).
	// applyStatus (this backend's one caller of mapWaxumStatus) only lets
	// the terminal state actually commit once this reaches
	// loggedOutHysteresisPolls — see that constant's doc comment. Kept
	// here, next to `pumping`, rather than as a local in applyStatus: it
	// must survive across separate calls (separate statusPump ticks, and
	// occasionally finishPairingSuccess's own applyStatus call — see below)
	// for the SAME session, which is exactly what this struct already
	// exists to hold (this backend's own per-session bookkeeping, kept off
	// ManagedSession — see this type's doc comment).
	//
	// Synchronization: an atomic, not s.mu or a plain int, because it is
	// NOT always written by a single goroutine. statusPump owns it for a
	// session's entire post-pairing life — one poller per id, enforced by
	// `pumping`'s CompareAndSwap guard — but finishPairingSuccess (a
	// DIFFERENT goroutine, the pairing pump) also calls applyStatus once,
	// synchronized with the FIRST statusPump start only by the "go
	// statement" happens-before edge (ensureStatusPump is called
	// immediately after, from the same goroutine). A session that is
	// re-paired after a genuine logout can still have its ORIGINAL
	// statusPump goroutine running concurrently (statusPump only exits on
	// ctx.Done, never on logout — see its doc comment), so a second
	// pairing's finishPairingSuccess->applyStatus call is not provably
	// ordered against that goroutine's next tick. `live`/`pumping` are
	// atomics for the same reason; this field follows their lead rather
	// than introducing a different, lock-based synchronization style for
	// one field in an otherwise lock-free struct. -race is unavailable in
	// this environment (CGO_ENABLED=0) to confirm the absence of a data
	// race directly; atomic.Int32's Add/Load/Store give correctness
	// (no lost updates, no torn reads) regardless.
	loggedOutStreak atomic.Int32

	// wedgeStreak counts CONSECUTIVE applyStatus calls in a row whose poll
	// matched isOrphanedClientWedge — waxum's session runtime holding no
	// live client for this session AT ALL (is_logged_in && !socket_alive
	// && Reachability nil), not merely a socket mid-redial. Mirrors
	// loggedOutStreak exactly, same reasoning: a single poll is not enough
	// evidence — the production incident this detects
	// (isOrphanedClientWedge's doc comment, which is this repo's write-up
	// of it) is a state waxum's own /status is observed twitchy around,
	// same as loggedOutHysteresisPolls's doc comment documents for the
	// logged-out edge — so applyStatus requires
	// loggedOutHysteresisPolls (reused, not re-derived — see
	// isOrphanedClientWedge's doc comment) CONSECUTIVE qualifying polls
	// before escalating to ManagedSession.markStalled. Reset to 0 by any
	// non-qualifying poll, exactly like loggedOutStreak.
	wedgeStreak atomic.Int32

	// poke lets the fleet-wide SSE event pump (backend_waxum_events.go)
	// wake THIS session's statusPump early, without ever writing session
	// state itself — see backend_waxum_events.go's package doc comment for
	// why an event stream must never become a second state authority.
	// Buffered 1: a poke that arrives while statusPump is mid-poll, or
	// hasn't drained a previously queued one yet, is dropped rather than
	// blocking — one pending poke is exactly as useful as ten, since
	// either way the very next GET /status already reconciles real state,
	// and handleEvent's send is non-blocking specifically so the
	// fleet-wide pump can never stall on a session with no running
	// statusPump (not yet Connect'ed, or already torn down). Allocated
	// alongside the rest of this struct in sessionState — see that
	// method's doc comment.
	poke chan struct{}
}

// loggedOutHysteresisPolls is how many CONSECUTIVE GET /status polls must
// each independently satisfy mapWaxumStatus's point 3 (paired session,
// !is_logged_in, status waiting_for_qr/waiting_for_pair_code) before
// applyStatus lets the StateLoggedOut verdict actually commit.
//
// Why: one poll is not enough evidence. Observed live, from this package's
// own state-transition log:
//
//	11:49:52 state transition session=59c9… from=disconnected to=logged_out
//	11:50:07 state transition session=59c9… from=logged_out   to=disconnected  (15s later)
//	11:51:52 state transition session=59c9… from=disconnected to=connected    (2m later)
//
// waxum's /status is demonstrably twitchy during recovery — this package
// already documents two other instances of its fields disagreeing with
// observable behaviour (mapWaxumStatus's doc comment: is_logged_in:true
// while status:"connecting" for 20+ seconds; qr_code events emitted while
// still receiving messages). A single qualifying poll declaring the
// terminal state let a session that was merely re-dialling get reported to
// the operator as needing a re-pair (burning a linked-device slot on an
// unnecessary QR scan) AND wiped the reconnect supervisor's attempt/backoff
// bookkeeping for it (reconnect.go's `!s.reconnectEligible()` branch
// deletes it the instant state leaves StateDisconnected) — observed live as
// the attempt counter restarting 1, 2, 1, 2, 1, 2, 3… instead of climbing
// toward reconnectDefaultStallThreshold, which can then never fire.
//
// 3 is chosen against WSM_WAXUM_POLL_INTERVAL (config.DefaultWaxumPollInterval,
// 15s): 3 consecutive polls is ~45s (2 full poll intervals of grace beyond
// the first), comfortably longer than the 15s flap actually observed above,
// while still declaring a GENUINELY logged-out session (one that does not
// intermittently report something else — see applyStatus) within about the
// same order of magnitude as before, not minutes later. Any poll that does
// not qualify resets the streak to 0 (applyStatus), so a session must show
// this evidence on 3 polls in a row, not 3 total.
//
// A named constant, not a Config field / env var: unlike QRPollInterval/
// PollInterval (WaxumConfig, tied to real request-rate/rate-limit tuning —
// see CLAUDE.md's waxum rate-limit hazard note), this is a count relative to
// whatever PollInterval already is, unlikely to need per-deployment tuning,
// and every existing test that exercises it (see
// TestRepeatedWaitingForQRPollsEventuallyDeclareTheSessionLoggedOut) wants a
// small, fixed, known value rather than one more knob to plumb through
// WaxumConfig/Config/env parsing for a case that has not yet needed it.
const loggedOutHysteresisPolls = 3

// WaxumConfig groups the timing knobs NewWaxumBackend needs. Kept as this
// package's own type (mirroring internal/config's WSM_WAXUM_* group)
// rather than importing internal/config here, matching every other backend
// constructor in this package taking plain values, not a config type from
// outside internal/wa.
type WaxumConfig struct {
	QRPollInterval time.Duration
	PollInterval   time.Duration
	PairTimeout    time.Duration
	// EventsEnabled turns on the fleet-wide SSE event pump
	// (backend_waxum_events.go, Phase 8) — WSM_WAXUM_EVENTS. false leaves
	// statusPump behaving exactly as it did before that phase: pure
	// polling, no poke ever sent.
	EventsEnabled bool
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
		eventsEnabled:  cfg.EventsEnabled,
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
// Live only reads. poke is allocated here (buffered 1) so every entry —
// whichever caller creates it first — is immediately pokeable; the event
// pump (backend_waxum_events.go) and statusPump never need to coordinate
// over which of them provisions it.
func (b *waxumBackend) sessionState(id string) *waxumSessionState {
	v, _ := b.sessions.LoadOrStore(id, &waxumSessionState{poke: make(chan struct{}, 1)})
	return v.(*waxumSessionState)
}

// pokeSession wakes id's statusPump early, if one is running — a
// non-blocking send, so a session with no live poller (or one that is
// already mid-poll) never stalls the caller. See waxumSessionState.poke's
// doc comment.
func (b *waxumBackend) pokeSession(id string) {
	select {
	case b.sessionState(id).poke <- struct{}{}:
	default:
	}
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
//
// waxum.ErrRateLimited maps to ErrBackendBusy, NOT ErrQueueFull —
// ErrQueueFull means THIS session's own send queue (sender.go) is full, a
// wholly different condition from waxum's tower_governor throttling this
// process. A live incident where a 429 was mapped to ErrQueueFull sent a
// production debugging session looking at the wrong subsystem; see
// errors.go's doc comment and CLAUDE.md's waxum rate-limit hazard note.
// Most 429s never reach here at all — internal/waxum.Client.do already
// retries them with backoff — this is what a caller sees only once those
// retries are exhausted.
//
// waxum.ErrClientNotConnected (503, "Client not connected") maps to
// ErrNotConnected, not a bespoke sentinel: it is the exact condition
// ErrNotConnected already names (see sender.go's Enqueue/trySend), it is
// transient (the socket is mid-reconnect), and isRetryable
// (sender_limits.go) already retries ErrNotConnected with backoff — a send
// that races a still-dialling socket right after a pairing success (see
// finishPairingSuccess's point 8) gets retried instead of hard-failing on
// the first attempt.
//
// A *waxum.APIError carrying 400/415/422 maps to ErrRejectedByBackend: a
// media/ops/webhook payload waxum's own validation refused (a bad
// mimetype, a malformed descriptor, an unknown webhook event name — see
// that sentinel's doc comment). Any OTHER status inside an *APIError falls
// through to the default case unchanged — genuinely unexpected statuses
// stay a bare *waxum.APIError rather than being folded into a sentinel that
// would misrepresent them.
func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, waxum.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, waxum.ErrConflict):
		return ErrNotConnected
	case errors.Is(err, waxum.ErrClientNotConnected):
		return ErrNotConnected
	case errors.Is(err, waxum.ErrRateLimited):
		return ErrBackendBusy
	default:
		var apiErr *waxum.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.StatusCode {
			case http.StatusBadRequest, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
				// apiErr.Message() extracts waxum's own human-readable
				// reason (its {"error":{"message":...}} envelope) rather
				// than dropping it: for a webhook registration rejected by
				// waxum's SSRF guard, ErrRejectedByBackend's generic text
				// alone reads as "backend said no" with no indication WHY
				// — an operator whose receiver genuinely is on a private
				// network (the single most likely cause) gets nothing to
				// go on. See ErrRejectedByBackend's own doc comment.
				if msg := apiErr.Message(); msg != "" {
					return fmt.Errorf("%w: %s", ErrRejectedByBackend, msg)
				}
				return ErrRejectedByBackend
			}
		}
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
	// Every restart's boot-reconcile (StartAll -> Activate -> Connect)
	// lands here WITHOUT ever going through StartPairing/
	// finishPairingSuccess — that only runs once, at the moment a device is
	// first paired. Before this line, THIS was the only path back to
	// StateConnected for an already-paired session, and it never started
	// the sender: s.state and Live(s) both go true (applyStatus, once
	// statusPump's next poll reports is_logged_in && socket_alive), so
	// every operator-facing signal — /sessions, /api/v1/health,
	// SelectConnected — agreed the session was usable, while s.sendQ
	// stayed nil forever and every send failed with ErrNotConnected
	// (sender.go's enqueue: `!connected || q == nil`). Live/State have no
	// way to see this: neither reads s.sendQ. startSenderLocked is
	// idempotent (early-returns once s.sendQ != nil — see
	// TestConnectingTwiceDoesNotStartASecondSenderLoop), so calling it
	// unconditionally here, on every Connect/reconnect, is safe. See
	// TestAWaxumSessionCanSendAfterConnectingWithoutPairing.
	s.startSenderLocked()
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

// Logout tears the session down LOCALLY. It is deliberately named for the
// Backend interface method it satisfies, but on this backend it does NOT do
// what meowBackend.Logout does, and the difference is operationally
// expensive: whatsmeow's version calls cli.Logout(ctx), a real WhatsApp
// unlink that removes the entry from the handset's "Linked devices" list.
// waxum has no such call to make — its API exposes connect, disconnect,
// reconnect, pair, qr and DELETE, and NOTHING that unlinks (verified
// 2026-09-02 against the live 0.12.2-uploaded-media OpenAPI document on
// wa-phkg). DeleteSession purges waxum's own storage and registry; the
// paired device stays linked on the phone, forever, invisible from here.
//
// The consequence is a slot leak. WhatsApp caps an account at a small
// number of linked companion devices, so every create-pair-delete cycle
// burns one permanently, and once the cap is reached a freshly paired
// device is logged out by WhatsApp within seconds of connecting — which
// reads as "pairing is broken on this host", not as "the account is full".
// session_rows.html's delete confirmation says so to the operator; it used
// to claim the opposite ("This unlinks the device from WhatsApp"), which is
// true only under WSM_WA_BACKEND=whatsmeow and false under the default.
//
// DeleteSession already treats 404 as success (internal/waxum.Client.
// DeleteSession's doc comment) — a session waxum never heard of, or already
// forgot, tears down cleanly. This backend's own bookkeeping for id is
// dropped too, so a
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
//  3. !is_logged_in && status is waiting_for_qr/waiting_for_pair_code &&
//     paired (WSM already has a persisted jid for this session) ->
//     StateLoggedOut: the ONE exception to point 5's "never from a poll"
//     rule, and deliberately narrow. A session that was NEVER paired
//     reaching waiting_for_qr/waiting_for_pair_code is simply a fresh
//     pairing attempt in progress (point 4 below) — nothing wrong. A
//     session that WAS paired (StartPairing/finishPairingSuccess already
//     wrote a jid) landing back at waiting_for_qr/waiting_for_pair_code
//     with is_logged_in false means waxum no longer holds credentials for
//     it: the device was unlinked on the phone side, and only a fresh QR/
//     phone-code scan (a re-pair, not a reconnect) can recover it. Treating
//     this as StateDisconnected — the old behaviour — left the operator
//     staring at a socket that no automatic reconnect (reconnect.go) could
//     ever bring back, with nothing in the UI explaining why. See
//     TestAPairedSessionReportingWaitingForQRIsTreatedAsLoggedOutNotDisconnected.
//     Deliberately NOT implemented: waxum's own pair.attempts counter as a
//     second signal, rate-of-change ("rising attempts") in particular —
//     today's decoded Status shape (internal/waxum/models.go) does not
//     carry that field at all, and adding wire parsing for a field never
//     confirmed against a real waxum response would be guessing at an
//     unverified schema rather than reading data already polled, which is
//     the whole premise of this method. is_logged_in+status, both already
//     decoded, are sufficient for the failure mode actually observed live.
//     IMPORTANT: this function returning StateLoggedOut is necessary but not
//     sufficient for the terminal state to actually commit — mapWaxumStatus
//     stays a pure, single-poll function of (st, paired) deliberately (so it
//     stays trivially table-driven-testable — see
//     TestLiveIsTrueOnlyWhenLoggedInAndSocketAlive's shape for why that
//     matters), but a single poll is not enough real-world evidence for a
//     terminal verdict (waxum's /status is demonstrably twitchy — see
//     loggedOutHysteresisPolls's doc comment for the live incident). Its one
//     caller, applyStatus, requires loggedOutHysteresisPolls CONSECUTIVE
//     calls returning StateLoggedOut before passing it on to setState;
//     anything less is reported as StateDisconnected instead — non-terminal,
//     exactly like point 2, so the reconnect supervisor stays engaged while
//     the evidence is still accumulating.
//  4. !is_logged_in && status is waiting_for_qr/waiting_for_pair_code &&
//     NOT paired -> StatePairing: mid QR/phone-code flow, no credential
//     issued yet — the ordinary in-progress case, unaffected by point 3.
//  5. !is_logged_in, anything else (disconnected, connecting, or a status
//     value this backend does not recognize yet) -> StateDisconnected.
//     Same reasoning as (2) for a NEVER-paired or already-recovered
//     session: status polling alone cannot otherwise prove a device was
//     actually unlinked, which is exactly why point 3 above needs the
//     extra `paired` signal to narrow it instead of inferring it from
//     status/is_logged_in alone.
//
// paired is whether WSM currently holds a persisted jid for this session —
// the caller's (applyStatus's) own s.jid.IsEmpty() check, not anything
// waxum reports.
func mapWaxumStatus(st *waxum.Status, paired bool) State {
	switch {
	case st.IsLoggedIn && st.SocketAlive:
		return StateConnected
	case st.IsLoggedIn:
		return StateDisconnected
	case st.Status == "waiting_for_qr" || st.Status == "waiting_for_pair_code":
		if paired {
			return StateLoggedOut // point 3
		}
		return StatePairing // point 4
	default:
		return StateDisconnected
	}
}

// isOrphanedClientWedge reports whether st matches the fingerprint of a
// production incident root-caused 2026-09-09, INSIDE waxum itself, not in
// this repo (CLAUDE.md carries the operator-facing version; the mechanism
// is waxum's `connect_client` tail clearing the session's client slot with
// no ownership check after its `bot.run()` unwinds, so a superseded bot
// evicts the live one — fixed by `clear_client_if` in that fork): an
// operator
// Deactivate-then-Activate within about a second of each other can leave
// waxum's OWN session runtime holding no client object at all for this
// session, permanently — every subsequent /connect builds a new bot that
// the old bot's still-unwinding run loop then evicts, forever, until the
// waxum PROCESS (not this session) is restarted.
//
// waxum's /status renders that exact condition as:
//
//	is_logged_in: true   (credentials cached — mapWaxumStatus's point 2 already
//	                       reports StateDisconnected for this, correctly, since
//	                       the credential really is still good)
//	socket_alive: false  (no working socket — same as point 2 alone)
//	reachability: null   (waxum's own documented meaning: no live client
//	                       object exists for this session in this process AT
//	                       ALL — the decisive addition over point 2's plain
//	                       is_logged_in && !socket_alive, which is also true
//	                       of an ordinary, recoverable reconnect where a live
//	                       client object exists and reports SOME concrete
//	                       value here instead, e.g. "reconnecting")
//
// This must NOT change mapWaxumStatus's own verdict (StateDisconnected stays
// correct — the session really is disconnected) — it is a SEPARATE signal
// applyStatus escalates, after sustained evidence (see wedgeStreak's doc
// comment), into ManagedSession.markStalled, so the operator is told the
// true remedy (restart the waxum process) instead of being left to believe
// another Activate might help.
func isOrphanedClientWedge(st *waxum.Status) bool {
	return st.IsLoggedIn && !st.SocketAlive && st.Reachability == nil
}

// wedgeStalledReason is the operator-facing text ManagedSession.markStalled
// records once isOrphanedClientWedge has been sustained for
// loggedOutHysteresisPolls consecutive polls (applyStatus). Worded, like
// every other markStalled reason (see that method's doc comment), for an
// operator to act on directly: it names the actual remedy (restart the
// waxum PROCESS) and says plainly that Activate — the one button the
// dashboard offers — cannot fix this specific condition, unlike the generic
// reconnect.go stall reason, where retrying repeatedly is at least a
// plausible (if exhausted) strategy.
const wedgeStalledReason = "waxum reports this session logged in with a dead socket and no live client for it anywhere in its process (reachability: null) — its session runtime is orphaned, and no amount of Activate/reconnect can revive it; only restarting the waxum PROCESS (not this session) clears it. Activate still resumes automatic retrying if you want to confirm, but it will re-stall if the wedge is still there."

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

// maxStatusPollBackoff caps how far statusPump backs off between GET
// /status attempts after consecutive failures (see statusPump's doc
// comment, "resilient to repeated Status() failures"). Matches
// reconnectDefaultMaxDelay's order of magnitude deliberately — both exist
// for the same reason: a persistently unreachable waxum peer should be
// polled at a slow, bounded cadence, not fast forever.
const maxStatusPollBackoff = 5 * time.Minute

// pokeMinIntervalDivisor derives pokeMinInterval (below) from b.pollInterval
// rather than a fixed duration: 4, so that at config.DefaultWaxumPollInterval
// (60s) the floor lands at exactly 15s — the OLD DefaultWaxumPollInterval,
// i.e. the cadence loggedOutHysteresisPolls's ~45s evidence window was
// originally calibrated against (see that constant's doc comment) — without
// hardcoding a second, independent reference to that old value. Deriving it
// ALSO means a test that injects a tiny pollInterval (this package's own
// house convention — WaxumConfig's fields are all struct fields, not
// constants, for exactly this reason) gets a correspondingly tiny floor,
// instead of a fixed 15s swallowing an entire fast test.
const pokeMinIntervalDivisor = 4

// pokeMinInterval floors how close together two REAL GET /status polls may
// land when at least one of them was triggered early by an SSE poke
// (waxumSessionState.poke) rather than the timer. See
// pokeMinIntervalDivisor's doc comment for how the floor is derived.
//
// Why this needs to exist at all: applyStatus's loggedOutHysteresisPolls
// gate counts CONSECUTIVE qualifying polls, with no timing requirement of
// its own — it was calibrated (see that constant's doc comment) assuming
// consecutive polls are naturally spaced by whatever the poll cadence is,
// giving ~45s of real wall-clock evidence at the OLD 15s default before a
// terminal StateLoggedOut verdict could commit. A poke breaks that
// assumption on purpose (that's the whole point of this phase — see this
// file's package doc comment and backend_waxum_events.go's), and without a
// floor a BURST of SSE events (three logged_out frames arriving within a
// couple hundred milliseconds, exactly the scenario waxum's own lossy
// events ring makes plausible right after a reconnect) could trigger three
// consecutive real polls within that same couple hundred milliseconds —
// numerically still "3 consecutive qualifying polls", but with the
// production flap's actual timescale (a 15s flap, per that doc comment)
// compressed to a fifth of a second. That would reopen the exact incident
// loggedOutHysteresisPolls exists to prevent, just triggered by an event
// burst instead of a single poll.
//
// The FIRST poke after any idle period longer than this still reacts
// immediately (see statusPump) — only a poke arriving within
// pokeMinInterval of the PREVIOUS real poll is deferred, not dropped. See
// TestAnEventDoesNotShortCircuitTheLoggedOutHysteresis.
func (b *waxumBackend) pokeMinInterval() time.Duration {
	if b.pollInterval <= 0 {
		return 0
	}
	return b.pollInterval / pokeMinIntervalDivisor
}

// statusPump is the sole state source for this backend: it polls GET
// /status every pollInterval, refreshes the Live() atomic, and calls
// setState on a recognized transition. Phase 8 (backend_waxum_events.go)
// added a second way to wake this loop early — a poke on
// sessionState(s.ID).poke, sent by the fleet-wide SSE event pump — but that
// pump NEVER calls applyStatus itself and NEVER carries a *waxum.Status; a
// poke only shortens the wait before the NEXT real GET /status, which then
// flows through applyStatus exactly as a timer-driven tick would. This
// loop remains the one and only state machine — see
// backend_waxum_events.go's package doc comment for why that split matters.
//
// It is wg-tracked (spawned via s.mgr.go1) and exits solely on ctx.Done()
// — ctx is s.ctx, a child of Manager.rootCtx, canceled by
// Manager.Close/Delete, so this always returns promptly at teardown; no
// separate hard deadline is needed the way the pairing pump needs one (that
// one has no terminal item of its own — see pairPump).
//
// Three fixes live here:
//
//  1. pumping (the sessionState guard ensureStatusPump uses to avoid a
//     duplicate poller for the same id) is reset to false on EVERY exit
//     path via defer, not left permanently true. Before this, once a
//     statusPump goroutine returned for any reason, ensureStatusPump could
//     never start a new one for that session id again — this package would
//     go permanently blind for it, Live() frozen at whatever it last
//     observed. See TestTheStatusPumpCanBeRestartedAfterItExits.
//  2. Status() failures are no longer silently dropped: each one is logged
//     and rung (see setState's doc comment for why both), and consecutive
//     failures back the poll interval off (capped at maxStatusPollBackoff)
//     instead of hammering an already-unreachable waxum at the same fixed
//     cadence forever — "resilient... rather than silently spinning".
//     failCount resets to 0 on the next success, returning the pump to its
//     normal pollInterval cadence.
//  3. A poke (sst.poke, Phase 8) shortens the wait instead of the timer —
//     but only down to pokeMinInterval since the LAST real poll, never to
//     zero; see that constant's doc comment for why a floor is required,
//     not optional. lastPoll tracks that timestamp across iterations.
func (b *waxumBackend) statusPump(ctx context.Context, s *ManagedSession) {
	sst := b.sessionState(s.ID)
	defer sst.pumping.Store(false)

	pokeFloor := b.pokeMinInterval()
	failCount := 0
	var lastPoll time.Time
	for {
		st, err := b.client.Status(ctx, s.ID)
		lastPoll = time.Now()
		wait := b.pollInterval
		switch {
		case err == nil:
			b.applyStatus(s, st)
			failCount = 0
		default:
			failCount++
			slog.Warn("wa: waxum status poll failed", "session", s.ID, "consecutive_failures", failCount, "err", err)
			s.ringLevel("warn", fmt.Sprintf("status poll failed (%d in a row): %s", failCount, err))
			wait = backoff.DelayFrom(failCount-1, b.pollInterval, maxStatusPollBackoff) + backoff.Jitter()
		}
		select {
		case <-ctx.Done():
			return
		case <-sst.poke:
			// An SSE event pumped this session (backend_waxum_events.go's
			// handleEvent) — react sooner than `wait`, but never sooner
			// than pokeFloor since lastPoll (see pokeMinInterval's doc
			// comment). Still inside the floor: wait out the remainder
			// instead of polling immediately; ctx.Done() still takes
			// priority even during that deferral.
			if since := time.Since(lastPoll); since < pokeFloor {
				select {
				case <-ctx.Done():
					return
				case <-time.After(pokeFloor - since):
				}
			}
		case <-time.After(wait):
		}
	}
}

// applyStatus refreshes Live and the session's State from one GET /status
// response. Both derive from is_logged_in/socket_alive, not status alone —
// see mapWaxumStatus's doc comment for the full rule and why. Unlike the
// old status-string-only mapping, mapWaxumStatus is exhaustive (always
// returns a concrete State), so setState is called on every tick; setState
// itself is already a no-op when the state hasn't actually changed.
//
// paired is read fresh under s.mu on every call (not cached) — it feeds
// mapWaxumStatus's point 3 (a paired session found back at
// waiting_for_qr/waiting_for_pair_code means logged-out, not disconnected)
// and must reflect s.jid at the moment of THIS poll, not whenever
// statusPump started.
//
// Hysteresis around StateLoggedOut lives HERE, not in mapWaxumStatus, and
// not on ManagedSession: mapWaxumStatus stays a pure function of one poll
// (see its doc comment's IMPORTANT note on point 3, and
// loggedOutHysteresisPolls's doc comment for why one poll is not enough
// evidence); this is the stateful caller that turns "the pure mapping said
// StateLoggedOut" into "…and it has now said so loggedOutHysteresisPolls
// times in a row" before ever calling setState with it. The counter itself
// is sessionState(s.ID).loggedOutStreak — this backend's own per-session
// bookkeeping (waxumSessionState's doc comment), not ManagedSession,
// matching where `live`/`pumping` already live for the same reason.
//
// Deliberately NOT applied to the StateConnected->StateDisconnected edge
// (points 1/2 above): that transition already gates Enqueue (sender.go) —
// being slow to notice a dead socket there means admitting sends that will
// hit waxum's 503 "Client not connected" instead of failing fast at the
// queue. It is also not the transition the production incident this
// hysteresis fixes was ever about: is_logged_in&&!socket_alive already maps
// to the non-terminal StateDisconnected (point 2), so a socket blip already
// gets the "stay engaged, keep retrying" treatment point 2 documents,
// without needing confirmation first — nothing about that edge risks a
// wrongly-terminal verdict the way point 3 does, which is the only reason
// hysteresis is justified here at all.
func (b *waxumBackend) applyStatus(s *ManagedSession, st *waxum.Status) {
	b.markLive(s.ID, st.IsLoggedIn && st.SocketAlive)
	s.mu.Lock()
	paired := !s.jid.IsEmpty()
	s.mu.Unlock()

	sst := b.sessionState(s.ID)

	mapped := mapWaxumStatus(st, paired)
	streak := &sst.loggedOutStreak
	if mapped != StateLoggedOut {
		streak.Store(0) // any non-qualifying poll resets the evidence — loggedOutHysteresisPolls's doc comment
	} else if streak.Add(1) < loggedOutHysteresisPolls {
		// Qualifying, but not yet confirmed loggedOutHysteresisPolls times
		// in a row: report the same non-terminal state point 2 already
		// uses for "credential probably still fine, socket is not" rather
		// than committing to the terminal verdict on this poll alone.
		mapped = StateDisconnected
	}
	s.setState(mapped)

	b.applyWedgeDetection(s, st, sst)
}

// applyWedgeDetection is applyStatus's other hysteresis-gated escalation —
// see isOrphanedClientWedge and waxumSessionState.wedgeStreak's doc
// comments for what it detects and why sustained evidence, not one poll, is
// required. Unlike the StateLoggedOut hysteresis above, this never changes
// `mapped`/State — the session really is StateDisconnected either way
// (mapWaxumStatus's point 2 already reports that correctly); it only
// escalates ManagedSession's SEPARATE stalled flag (session.go), the same
// flag reconnect.go's own attempt-count stall already uses, just reached
// via a faster, more specific signal than counting failed reconnect
// attempts one at a time.
//
// Guarded by `!s.isStalled()` rather than calling markStalled on every
// sustained qualifying poll: markStalled is idempotent (it only re-sets the
// same two fields and re-logs/re-rings), but re-ringing a warning every
// poll interval for as long as the wedge persists would spam the session's
// event ring for no operator benefit. The one case that DOES need
// markStalled called again — a manual Activate (session.go) clearing
// stalled while the underlying wedge has NOT actually cleared on waxum's
// side — is exactly what this guard still allows: Activate flips
// s.stalled back to false, so the very next qualifying poll (wedgeStreak
// is already >= loggedOutHysteresisPolls and does not need to
// re-accumulate — the evidence that mattered was the sustained observation
// BEFORE Activate, not the button press) sees !s.isStalled() true again
// and re-stalls with the same reason.
func (b *waxumBackend) applyWedgeDetection(s *ManagedSession, st *waxum.Status, sst *waxumSessionState) {
	if !isOrphanedClientWedge(st) {
		sst.wedgeStreak.Store(0)
		return
	}
	if sst.wedgeStreak.Add(1) < loggedOutHysteresisPolls {
		return
	}
	if !s.isStalled() {
		s.markStalled(wedgeStalledReason)
	}
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

// rollbackPairing undoes StartPairing's local state bump (s.pairing, s.state)
// when a remote setup call failed outright. It also best-effort tells waxum
// to stop, because the two remote calls above are not atomic:
// CreateSession may well have already succeeded on the line before Connect
// failed, leaving a registry row (and, if Connect itself partially started
// a client, a live pairing attempt) on waxum's side with nothing in WSM
// pointing at it any more — the same forgotten-attempt shape
// finishPairingFailure's doc comment describes, just entered from the other
// direction. A CreateSession failure leaves nothing on waxum's side to
// stop, so the Disconnect below is a routine 404/NotConnected there and is
// swallowed the same way — see disconnectAbandonedPairing.
func (b *waxumBackend) rollbackPairing(s *ManagedSession) {
	s.mu.Lock()
	s.pairing = nil
	s.state = StateNew
	s.mu.Unlock()
	b.disconnectAbandonedPairing(s, "pairing setup failed")
}

// abandonedPairingDisconnectTimeout bounds the best-effort waxum Disconnect
// issued by disconnectAbandonedPairing. It must never hold up teardown or a
// StartPairing caller waiting on rollbackPairing, and s.mgr.persistCtx
// itself carries no deadline (context.WithoutCancel, manager.go) to bound
// it for us.
const abandonedPairingDisconnectTimeout = 5 * time.Second

// disconnectAbandonedPairing best-effort tells waxum to stop an attempt WSM
// has already given up on — see finishPairingFailure's and rollbackPairing's
// doc comments for why each calls this.
//
// It runs on s.mgr.persistCtx, not the caller's own ctx: by the time either
// caller runs, pairPump's pctx is already expired/cancelled (that IS why
// the pump returned), so a call scoped to it would fail instantly and do
// nothing — persistCtx exists precisely so teardown writes like this one
// can still land (manager.go's doc comment on the field). It also runs
// OUTSIDE s.mu: session.go documents s.mu as for short, cheap work only,
// never held across a remote call like Connect/SendMessage/Logout, and this
// is the same shape of call.
//
// This is deliberately the ONLY remote call made here — never follow it
// with a Connect. See CLAUDE.md's Deactivate-then-Activate wedge hazard: a
// Disconnect racing an in-flight Connect on waxum's side can strand the
// session's runtime with no live client anywhere in waxum's process
// (reachability: null), recoverable only by restarting the waxum process.
// Calling Disconnect and then stopping keeps this well clear of that race.
//
// A failing or 404 Disconnect does not change control flow — the caller's
// state transition to StateNew already happened and stays unconditional
// either way. waxum answers NotConnected/404 for a session with no live
// client, the ordinary outcome for an attempt that was abandoned or never
// got off the ground, so that case is swallowed exactly like
// waxumBackend.Disconnect already swallows waxum.ErrNotFound. Only a
// genuinely unexpected failure is surfaced, matching the posture
// Manager.Delete uses for a failed backend Logout (manager_crud.go): a
// slog.Warn for operational visibility plus s.ring so a test — or an
// operator reading the session's own event log — can see it without
// capturing slog output.
//
// Skipped entirely once s.mgr.closed is true — i.e. once Manager.Close has
// started. Close sets closed via CompareAndSwap as its very first action,
// strictly before it cancels any session's ctx or issues its own
// m.backend.Disconnect(m.persistCtx, s) for that same session
// (manager_boot.go), so a pairing pump that wakes from finishPairingFailure
// BECAUSE of shutdown always observes closed already true, and Close has
// already covered — or is about to cover, moments later in the very same
// loop — the disconnect this method exists to make. Without this guard,
// this call runs on persistCtx (context.WithoutCancel — deliberately
// immune to the shutdown that triggered it) and can block m.wg.Wait() for
// the full abandonedPairingDisconnectTimeout: against
// docker-compose.yml's 15s stop_grace_period that is a third of the
// teardown budget, spent for nothing, during exactly the window CLAUDE.md's
// SIGKILL-mid-write-to-`-wal` hazard warns about. A pump that instead
// expires on its own pairTimeout while the manager is still running must
// still reach this call normally — only the shutdown path is short-
// circuited. See
// TestClosingTheManagerDoesNotMakeAnAbandonedPairingWaitOnADisconnectShutdownAlreadyIssued.
func (b *waxumBackend) disconnectAbandonedPairing(s *ManagedSession, reason string) {
	if s.mgr.closed.Load() {
		return
	}
	ctx, cancel := context.WithTimeout(s.mgr.persistCtx, abandonedPairingDisconnectTimeout)
	defer cancel()
	if err := b.client.Disconnect(ctx, s.ID); err != nil && !errors.Is(err, waxum.ErrNotFound) {
		slog.Warn("waxum disconnect failed while abandoning a pairing attempt; the session may still be pairing on waxum's side",
			"session", s.ID, "reason", reason, "err", err)
		s.ring("abandon pairing: waxum disconnect failed (possibly still pairing on waxum's side): " + err.Error())
	}
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
//     signal; nothing here requires status to also say
//     "connected"/"logged_in". phone_number is corroboration, not part of
//     the success test itself — see point 6 below for why (it is not
//     always populated the instant is_logged_in flips true). See
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
//  6. waxum flips is_logged_in to true BEFORE it populates phone_number —
//     observed live: a session persisted with jid "s.whatsapp.net" (an
//     EMPTY user, produced by types.NewJID("", DefaultUserServer) on the
//     tick where is_logged_in was already true but phone_number was still
//     ""), and the real number showed up on Status moments later.
//     types.JID.IsEmpty() only checks Server, so that malformed jid slips
//     straight past manager_boot.go's boot-reconnect guard, and it is
//     exactly the shape ParseGroupJID exists to reject (group.go) — this
//     backend must never write it itself. pendingLoginTicks below bounds a
//     short wait for the real phone_number (still inside this same
//     deadline) before finishPairingSuccess falls back to s.Phone — the
//     operator-supplied number, validated by NormalizePhone at Create and
//     therefore always non-empty. See
//     TestPairingNeverPersistsAJIDWithAnEmptyUser and
//     TestPairingFallsBackToTheConfiguredPhoneWhenWaxumNeverReportsOne.
//  7. GET /qr is only called while it can still do something useful — a
//     QR-wanting status (waiting_for_qr/waiting_for_pair_code) or no code
//     seen yet — not unconditionally every tick. Before this, EVERY tick of
//     EVERY pairing session cost two requests (status + qr) on top of every
//     OTHER session's statusPump sharing the same per-peer-IP bucket; that
//     is most of what turned waxum's tower_governor default (burst ~3) into
//     the 429 storm this whole change addresses — see CLAUDE.md's waxum
//     rate-limit hazard note. See
//     TestTheQREndpointIsNotPolledWhenTheSessionIsNotPairing.
func (b *waxumBackend) pairPump(ctx context.Context, s *ManagedSession, ps *PairingState,
	pollInterval time.Duration, deadline time.Time) {

	pctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	var lastCode string
	var lastStatus string     // most recent Status.Status string (point 7's QR-polling gate)
	var qrSeen bool           // true once /qr has returned >=1 payload — proves the socket is up (point 4)
	var phoneCodeSet bool     // true once PairCode has succeeded — stop retrying (point 4)
	var connectNudged bool    // true once a post-scan reconnect nudge has been sent (point 5)
	var pendingLoginTicks int // consecutive polls seen with is_logged_in true but phone_number still empty (point 6)

	// maxPendingLoginTicks bounds how many extra polls this pump waits for
	// waxum's phone_number to catch up to is_logged_in before giving up and
	// falling back to s.Phone (point 6). It is a tick count, not a
	// duration, so it scales with whatever pollInterval the caller passes
	// (production 2-3s, tests as low as 2ms) instead of hardcoding a
	// duration that would be wrong at either extreme; the outer pctx
	// deadline is still the hard upper bound regardless.
	const maxPendingLoginTicks = 5

	for {
		if st, err := b.client.Status(pctx, s.ID); err == nil {
			lastStatus = st.Status
			if st.IsLoggedIn { // point 3 — the ONLY signal that matters for success
				switch {
				case st.PhoneNumber != "":
					b.finishPairingSuccess(s, ps, st, st.PhoneNumber)
					return
				case pendingLoginTicks >= maxPendingLoginTicks:
					b.finishPairingSuccess(s, ps, st, s.Phone) // point 6: waxum never reported one — use the operator-supplied number
					return
				default:
					pendingLoginTicks++ // point 6: keep polling; do not fabricate a jid from an empty user yet
				}
			}
			if qrSeen && !connectNudged && st.Status == "disconnected" {
				_ = b.client.Connect(pctx, s.ID) // best-effort nudge; point 5
				connectNudged = true
			}
		}
		// point 7: GET /qr costs a second request every tick it is called,
		// and this pump used to call it EVERY tick unconditionally — on
		// top of statusPump's own /status polling and every other
		// concurrently pairing/connected session sharing the same
		// per-peer-IP bucket, that is most of what drove waxum's
		// tower_governor default (burst ~3) into 429s during the incident
		// this whole change addresses (see CLAUDE.md's waxum rate-limit
		// hazard note). /qr is only useful while (a) a QR-wanting state is
		// actually in play (waiting_for_qr, or waiting_for_pair_code —
		// phone mode still needs qrSeen as its "socket is up" readiness
		// signal, point 4) or (b) this pump has never seen a payload yet
		// (lastCode == "": covers the first tick, before any Status call
		// has necessarily run, and any error tick where lastStatus was
		// never set). Once a phone code has actually been issued
		// (phoneCodeSet) there is nothing left for /qr to do.
		wantsQR := !phoneCodeSet &&
			(lastCode == "" || lastStatus == "waiting_for_qr" || lastStatus == "waiting_for_pair_code")
		if wantsQR {
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
// start the sender, hand State/Live to applyStatus, resume statusPump.
// phoneNumber is either waxum's own Status.PhoneNumber (the common case) or,
// per pairPump's point 6, s.Phone when waxum never populated one within the
// grace window — either way it MUST be non-empty before being used to build
// a jid (guarded below) because types.NewJID(phoneNumber, DefaultUserServer)
// with an empty phoneNumber builds a jid whose Server alone is non-empty, so
// types.JID.IsEmpty() (which checks only Server) would NOT catch it, and
// SetPaired would persist "s.whatsapp.net" — exactly the live bug point 6
// documents. waxum has no separate "success" event to read, is_logged_in
// alone IS the signal that pairing itself succeeded (pairPump's doc
// comment, point 3) — but see point 8 for why that is NOT the same claim
// as "the session can send right now".
//
// Point 8 — is_logged_in is not socket_alive: an earlier version of this
// method forced State to StateConnected and Live to true unconditionally
// here, on the theory that reaching is_logged_in:true means pairing is
// done. It does mean credentials are established, but NOT that waxum's
// websocket is up yet — observed live, a pairing that finished here still
// had SendText fail moments later with a 503 "Client not connected",
// because the socket was still dialing. st is the SAME Status read that
// proved is_logged_in (pairPump passes through the exact response it just
// polled, not a re-fetch), so applyStatus(s, st) — the one place
// State/Live are computed from is_logged_in+socket_alive for the
// steady-state statusPump too — gives the honest answer instead of a
// second, optimistic one that can disagree with it. A session that lands
// in StateDisconnected here is promoted to StateConnected by statusPump's
// very next tick once socket_alive catches up; Enqueue correctly refuses
// sends until then instead of admitting one waxum will 503.
func (b *waxumBackend) finishPairingSuccess(s *ManagedSession, ps *PairingState, st *waxum.Status, phoneNumber string) {
	// haveJID is false only when BOTH waxum's phone_number and the
	// operator-supplied s.Phone fallback (pairPump's point 6) came back
	// empty — should be unreachable in production, since Manager.Create
	// validates s.Phone non-empty via NormalizePhone. When it does happen,
	// s.jid is left as-is (empty, for a first pairing) rather than set to
	// the malformed types.NewJID("", DefaultUserServer) — see this
	// method's doc comment for why that shape is dangerous — so
	// manager_boot.go's boot-reconnect guard still treats the session as
	// unpaired. The device DID log in either way, so the pairing box, the
	// sender, and State/Live below are unaffected; only the persisted jid
	// is skipped.
	haveJID := phoneNumber != ""
	var jid types.JID
	if haveJID {
		jid = types.NewJID(phoneNumber, types.DefaultUserServer)
	} else {
		s.ring("paired with no phone number available from waxum or the session record — jid left unset")
	}

	s.mu.Lock()
	if haveJID {
		s.jid = jid
		s.active = true // mirrors SetPaired's active=1 below — see reconnect.go's eligibility check.
		// This is the exact intent gap the production incident (reconnect.go's
		// doc comment) exposed: without it, a session that just paired
		// successfully but never went through Activate() would sit
		// permanently ineligible for automatic reconnection the moment its
		// still-dialling socket (point 8 below) dropped.
	}
	if s.pairing != nil {
		s.pairing.finish(nil)
		s.pairing = nil
	}
	s.startSenderLocked() // safe to start before the socket is confirmed up — Enqueue's own gate is s.state, not "does a sender goroutine exist"
	sctx := s.ensureCtxLocked()
	s.mu.Unlock()

	if haveJID {
		_ = s.mgr.repo.SetPaired(s.mgr.persistCtx, s.ID, jid.String())
	}
	b.applyStatus(s, st) // point 8: State/Live come from is_logged_in+socket_alive, not an unconditional "connected"
	b.ensureStatusPump(sctx, s)
	if haveJID {
		s.ring("paired as " + jid.String())
	}
}

// finishPairingFailure mirrors meowBackend's onPairFailed: roll back to new,
// keep the pairing box (done+err) so the status partial can show why. No
// device row exists to purge (waxum only persists credentials on its own
// side, on success) — matches meow's comment for the same reason.
//
// It also best-effort tells waxum to stop the attempt
// (disconnectAbandonedPairing), which used to be the whole missing half of
// this function: WSM's give-up was purely local, and waxum kept driving
// whatsapp-rust for the same never-scanned client forever. Every
// Event::PairingQrCode there sets status WaitingForQr and bumps
// pair.attempts (waxum fork's src/handlers/sessions.rs:1659), and
// whatsapp-rust rotates the QR payload roughly every 20s indefinitely for
// an unpaired client — so attempts climbs without bound. Neither
// reconnect_all_on_startup nor the reconnect watchdog stops it:
// WaitingForQr is skipped by the former, and the latter only rebuilds a
// stuck client, it never gives up on one. Observed live: session
// 6686fd00… sat at pair.attempts=3744, no phone_number, roughly 20 hours
// after WSM itself had already rolled the session back to new. Only an
// explicit Disconnect (or restarting the waxum process) stops the loop.
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
	s.ringLevel("warn", "pairing failed: "+cause.Error())
	b.disconnectAbandonedPairing(s, "pairing failed: "+cause.Error())
}
