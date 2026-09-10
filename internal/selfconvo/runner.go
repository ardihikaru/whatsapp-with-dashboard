package selfconvo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/convo"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/id"
)

// defaultPollInterval is Options.PollInterval's fallback — see the plan's
// "poll + poke" scheduler design.
const defaultPollInterval = 5 * time.Second

// defaultMinSleep is Options.MinSleep's fallback — see Tick's doc comment
// for why a floor exists at all.
const defaultMinSleep = 1 * time.Second

// defaultMaxConcurrentRuns is Options.MaxConcurrentRuns's fallback — see
// Tick's doc comment for why due runs are fanned out at all.
const defaultMaxConcurrentRuns = 4

// maxStartAhead/maxStartBehind bound Trigger's start_at: too far ahead is
// refused outright (an operator error is more likely than a genuine
// week-later trigger); too far in the past is CLAMPED to now rather than
// refused, since "start now" landing a moment late (a slow form submit,
// clock skew) must not fail the trigger it was trying to make happen.
const (
	maxStartAhead  = 30 * 24 * time.Hour
	maxStartBehind = 5 * time.Minute
)

// Options configures a Runner. Every field is optional; Now/Rand/PollInterval
// default to real time.Now/an unseeded source/defaultPollInterval, and tests
// override all three to stay deterministic and sleep-free (house
// convention: inject clocks/intervals as parameters, never mock a global).
type Options struct {
	Now          func() time.Time
	Rand         *rand.Rand
	PollInterval time.Duration
	// MinSleep floors Tick's returned sleep duration and is also the base
	// unit of its error backoff (MinSleep, 2*MinSleep, 4*MinSleep, ...,
	// capped at PollInterval — see Tick's doc comment). Defaults to
	// defaultMinSleep; tests may set it lower to keep exact-duration
	// assertions cheap.
	MinSleep time.Duration
	// MaxConcurrentRuns bounds how many of a tick's due runs processRun
	// advances at once (see Tick's doc comment). Defaults to
	// defaultMaxConcurrentRuns.
	MaxConcurrentRuns int
	Logger            *slog.Logger
	// Paraphrase gates Vary's paraphrase step (see vary.go) for every
	// Trigger this Runner processes. The DEFAULT — nil, the zero value, a
	// caller that never touches this field — is ENABLED, matching Vary's
	// own default; set a *bool pointing at false (see cmd/app/selfconvo.go,
	// wired from WSM_SELF_CONVO_PARAPHRASE) to disable it deployment-wide.
	Paraphrase *bool
}

// Runner drives every self-convo run through its persisted state machine:
// Trigger expands a script into a scheduled run, Tick advances exactly one
// due turn per due run, and Start/Stop/Poke wrap Tick in the poll-plus-poke
// background loop the plan's Scheduler design section specifies. See Tick's
// doc comment for the per-turn contract.
type Runner struct {
	repo          *convo.Repository
	gw            Gateway
	now           func() time.Time
	rng           *rand.Rand
	poll          time.Duration
	minSleep      time.Duration
	maxConcurrent int
	log           *slog.Logger
	paraphrase    bool // Options.Paraphrase resolved to a concrete bool at New time

	poke chan struct{} // buffered 1; Poke never blocks its caller

	// errTicks counts CONSECUTIVE Tick calls in which processRun logged at
	// least one repo error (never touched by the loop or by tests directly
	// — Tick is the only writer/reader, and Tick is never called
	// concurrently with itself, the same "no lock needed" reasoning as
	// `poll`). Backs off the returned sleep exponentially; a clean tick
	// resets it to 0. See Tick's doc comment.
	errTicks int

	mu     sync.Mutex
	cancel context.CancelFunc // non-nil only while the loop is running (Start/Stop guard)
	wg     sync.WaitGroup
}

// New builds a Runner over repo/gw. Call Start to run the background loop;
// Trigger/Cancel/Tick all work standalone (used directly by tests, and by
// Start's own loop) without it.
func New(repo *convo.Repository, gw Gateway, opt Options) *Runner {
	now := opt.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	rng := opt.Rand
	if rng == nil {
		rng = rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), uint64(time.Now().UnixNano())+1))
	}
	poll := opt.PollInterval
	if poll <= 0 {
		poll = defaultPollInterval
	}
	minSleep := opt.MinSleep
	if minSleep <= 0 {
		minSleep = defaultMinSleep
	}
	maxConcurrent := opt.MaxConcurrentRuns
	if maxConcurrent <= 0 {
		maxConcurrent = defaultMaxConcurrentRuns
	}
	log := opt.Logger
	if log == nil {
		log = slog.Default()
	}
	paraphrase := true // Options.Paraphrase's documented default
	if opt.Paraphrase != nil {
		paraphrase = *opt.Paraphrase
	}
	return &Runner{
		repo: repo, gw: gw, now: now, rng: rng, poll: poll, minSleep: minSleep,
		maxConcurrent: maxConcurrent, log: log, paraphrase: paraphrase,
		poke: make(chan struct{}, 1),
	}
}

// ToTurns converts expanded (Expand's output) into the convo.Turn rows
// Trigger persists, all pending and stamped with runID — a small pure
// function pulled out of Trigger so a later seeder (cmd/e2eseed) can build
// the same shape without duplicating the conversion.
func ToTurns(runID string, expanded []ExpandedTurn) []convo.Turn {
	out := make([]convo.Turn, len(expanded))
	for i, et := range expanded {
		out[i] = convo.Turn{
			RunID:        runID,
			Seq:          et.Seq,
			Speaker:      et.Speaker,
			SessionID:    et.SessionID,
			ToPhone:      et.ToPhone,
			Body:         et.Body,
			DelaySeconds: et.DelaySeconds,
			State:        convo.TurnPending,
		}
	}
	return out
}

// TriggerRequest is one operator trigger: play Template between two of the
// TickedSessionIDs (PickPair chooses which two, and their role order),
// starting at StartAt (zero means "now").
//
// Template and Script are both optional and travel together: when SET, they
// let Trigger skip repo.GetTemplate and Validate entirely and use them
// as-is — the shape the trigger handler's upload-and-trigger path needs
// (self_convo_ui.go's resolveTriggerTemplate), since that path already ran
// Validate once (readTemplateUpload) and already has the parsed Script in
// hand from the very save it just made; re-fetching and re-validating the
// row it just inserted would be a third validation of bytes already checked
// twice. Template.ID need not exist in the repo — Trigger never reads it
// back — but Template.Name/ID ARE used for the run's TemplateID/TemplateName
// snapshot, so the caller must still pass the real saved id when there is
// one. When either is nil, Trigger falls back to its original path:
// GetTemplate(TemplateID) followed by Validate — the "pick a saved
// template" case, unchanged.
type TriggerRequest struct {
	TemplateID       string
	Template         *convo.Template
	Script           *Script
	TickedSessionIDs []string
	StartAt          time.Time
}

// Trigger resolves the template to play — either the pre-validated
// Template/Script the caller already has in hand (see TriggerRequest's doc
// comment), or, when neither is set, the original path: load TemplateID and
// re-validate it (a stored template that no longer validates against the
// CURRENT rules — e.g. an older, looser version — is refused by name rather
// than silently expanded). Either way it then resolves the ticked ids to
// live Participants via gw.Participant (an unknown id is skipped, not an
// error — PickPair is what decides whether the survivors are enough), picks
// a pair, expands the script, and persists a scheduled run in one
// transaction (convo.Repository.CreateRun). Returns
// ErrNotEnoughParticipants (from PickPair) when fewer than two ticked
// sessions are connected.
func (r *Runner) Trigger(ctx context.Context, req TriggerRequest) (convo.Run, error) {
	var t convo.Template
	var script Script
	if req.Template != nil && req.Script != nil {
		t = *req.Template
		script = *req.Script
	} else {
		loaded, err := r.repo.GetTemplate(ctx, req.TemplateID)
		if err != nil {
			return convo.Run{}, err
		}
		t = loaded
		parsed, err := Validate([]byte(t.Body))
		if err != nil {
			return convo.Run{}, fmt.Errorf("self-convo template %q (%s) no longer validates: %w", t.Name, t.ID, err)
		}
		script = parsed
	}
	script, err := VaryWithOptions(script, r.rng, VaryOptions{ParaphraseDisabled: !r.paraphrase})
	if err != nil {
		return convo.Run{}, err
	}

	candidates := make([]Participant, 0, len(req.TickedSessionIDs))
	for _, sid := range req.TickedSessionIDs {
		if p, ok := r.gw.Participant(sid); ok {
			candidates = append(candidates, p)
		}
	}
	pair, err := PickPair(candidates, r.rng)
	if err != nil {
		return convo.Run{}, err
	}

	turns := Expand(script, pair, r.rng)

	now := r.now()
	startAt := req.StartAt
	switch {
	case startAt.IsZero():
		startAt = now
	default:
		startAt = startAt.UTC()
		if startAt.Sub(now) > maxStartAhead {
			return convo.Run{}, fmt.Errorf("selfconvo: start_at is more than %s ahead of now", maxStartAhead)
		}
		if now.Sub(startAt) > maxStartBehind {
			// Clamp, don't refuse — see maxStartBehind's doc comment above.
			startAt = now
		}
	}

	run := convo.Run{
		ID:           id.New(),
		TemplateID:   t.ID,
		TemplateName: t.Name,
		SessionAID:   pair.A.ID,
		SessionBID:   pair.B.ID,
		SessionAName: pair.A.Name,
		SessionBName: pair.B.Name,
		PhoneA:       pair.A.Phone,
		PhoneB:       pair.B.Phone,
		State:        convo.RunScheduled,
		TurnCount:    len(turns),
		StartAt:      startAt,
		NextDueAt:    &startAt,
		CreatedAt:    now,
	}
	convoTurns := ToTurns(run.ID, turns)

	if err := r.repo.CreateRun(ctx, &run, convoTurns); err != nil {
		return convo.Run{}, err
	}
	r.Poke()
	return run, nil
}

// Cancel marks runID cancelled (skipping every pending turn) and pokes the
// loop so a waiting operator sees it stop promptly rather than after the
// next poll interval. Errors (ErrNotFound, ErrRunFinished) propagate
// unchanged from convo.Repository.CancelRun.
func (r *Runner) Cancel(ctx context.Context, runID string) error {
	if err := r.repo.CancelRun(ctx, runID, r.now()); err != nil {
		return err
	}
	r.Poke()
	return nil
}

// Tick is one scheduler pass: every non-terminal run whose next_due_at has
// arrived gets EXACTLY ONE turn advanced (never a whole backlog drained in
// one call — that is what makes the "lazy next-due" rule produce a single
// immediate send on recovery from an outage, then normal pacing, instead of
// a catch-up burst; see docs/brief-self-convo.md's Scheduler design
// section). It never sleeps; the caller (the Start loop, or a test) decides
// what to do with the returned sleep duration.
//
// Due runs are fanned out across up to Options.MaxConcurrentRuns goroutines
// (a bounded semaphore) rather than processed strictly sequentially: two due
// runs over disjoint pairs used to serialize behind each other's gw.Send
// call (bounded only by the sender's own 3x30s retry budget), silently
// stretching the pacing docs/brief-self-convo.md's `pacing.min/max` promises
// once more than one run is in flight. This is still safe for the
// "one-turn-per-run-per-tick" rule: every run in this batch came from ONE
// DueRuns query above, and Tick waits for the WHOLE batch (wg.Wait, below)
// before returning, so no run can be picked up twice within one Tick call —
// and Tick itself is never called concurrently with itself (loop calls it
// once, sequentially; see settle's doc comment for the same invariant
// applied to poll/rng). A run therefore can never be processed by two
// goroutines at once.
//
// The returned sleep is floored at Options.MinSleep and never zero. Without
// the floor, a due run whose advance fails partway (a repo write erroring —
// e.g. MySQL read-only during a failover — while GetRun/EarliestGetRun/
// EarliestDue keep succeeding) leaves next_due_at stuck in the past:
// EarliestDue keeps reporting that same stale timestamp, the naive
// `earliest - now` clamps to 0, and loop() would re-run DueRuns/EarliestDue
// at CPU speed against the shared production MySQL forever. A run that is
// due "now" still fires on the very next tick, one MinSleep later — fine at
// this feature's pacing.
//
// When any processRun call this tick logged a repo error, the sleep also
// backs off exponentially per CONSECUTIVE erroring tick — MinSleep, then
// 2x, 4x, ... capped at PollInterval — so a sustained outage quiets the log
// instead of spinning it; a tick that logs no error resets the backoff to
// its first step. Poke can still wake the loop early regardless of the
// current backoff.
func (r *Runner) Tick(ctx context.Context) (time.Duration, error) {
	now := r.now()
	runs, err := r.repo.DueRuns(ctx, now, 20)
	if err != nil {
		return r.poll, err
	}

	var (
		hadErrMu sync.Mutex
		hadErr   bool
		wg       sync.WaitGroup
	)
	sem := make(chan struct{}, r.maxConcurrent)
	for _, run := range runs {
		if ctx.Err() != nil {
			break // stop LAUNCHING further runs; already-started ones still finish below
		}
		run := run
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if r.processRun(ctx, run) {
				hadErrMu.Lock()
				hadErr = true
				hadErrMu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return r.poll, ctx.Err()
	}

	earliest, ok, err := r.repo.EarliestDue(ctx)
	if err != nil {
		return r.poll, err
	}
	sleep := r.poll
	if ok {
		sleep = earliest.Sub(r.now())
		if sleep > r.poll {
			sleep = r.poll
		}
	}
	return r.settle(sleep, hadErr), nil
}

// settle applies Tick's floor-and-backoff policy (see Tick's doc comment)
// and updates errTicks. Not safe for concurrent use — same as every other
// unlocked Runner field Tick touches (poll, rng, ...); Tick is never called
// concurrently with itself.
func (r *Runner) settle(sleep time.Duration, hadErr bool) time.Duration {
	if hadErr {
		r.errTicks++
	} else {
		r.errTicks = 0
	}
	if sleep < r.minSleep {
		sleep = r.minSleep
	}
	if r.errTicks > 0 {
		backoff := r.minSleep
		for i := 1; i < r.errTicks && backoff < r.poll; i++ {
			backoff *= 2
		}
		if backoff > r.poll {
			backoff = r.poll
		}
		if backoff > sleep {
			sleep = backoff
		}
	}
	if sleep > r.poll {
		sleep = r.poll
	}
	return sleep
}

// processRun advances run by exactly one turn (or finishes it, if there is
// nothing left to send or a participant is no longer connected). run is the
// row Tick's own DueRuns call already fetched — processRun does NOT
// re-GetRun it at the top the way it used to: DueRuns only ever returns
// non-terminal rows, and the one place a concurrent Cancel actually matters
// (a cancel landing between this run being picked up and its turn being
// claimed) is guarded at the point of the write itself — ClaimTurn's own
// `state = 'pending'` check (ErrTurnNotPending, below) is authoritative
// there, not a re-read here. A stale `run` snapshot for anything else
// (SessionAName et al.) is harmless — those fields never change after
// CreateRun. It DOES still re-read immediately before any write that could
// conflict with a concurrent
// Cancel — a cancel landing while a turn's Send is in flight lets that one
// turn complete (its terminal DB writes always land, via a ctx stripped of
// cancellation — see finishCtx below), but the run's own conclusion, once
// decided by Cancel, is never overwritten by this call afterwards
// (finishIfNotTerminal).
//
// A graceful shutdown is handled differently from both of those: ctx here
// is the loop's OWN ctx (Start derives it from context.WithoutCancel, so
// only Stop() ends it — see Start's doc comment), so ctx.Err() != nil
// during gw.Send means Stop() cut the send off, not that it genuinely
// failed on the wire (the underlying transport send may still be running
// against its own, unrelated deadline and could still succeed). Marking
// the turn failed and the run failed on THAT basis is wrong — a hard
// process crash at the same instant would leave the turn "sending" and let
// the next boot's MarkInterrupted resume the run, which is the better
// outcome and the one this case now matches: the turn is left "sending",
// the run is NOT finished, and the next boot resumes it exactly like a
// crash. A turn cut off this way is therefore never re-sent — whatever the
// in-flight send actually did lands (or doesn't) in wa_messages on its own,
// independent of this call, which is the authoritative record of what
// happened.
//
// Returns true if any repo call along the way failed and was only logged —
// Tick's caller uses that to floor/back off the next sleep (see Tick's doc
// comment); it is not itself an error return because a business-logic
// outcome (a disconnected participant, a gateway Send failure) is not a
// repo failure and must not trigger backoff on its own.
func (r *Runner) processRun(ctx context.Context, run convo.Run) bool {
	hadErr := false

	if convo.IsTerminal(run.State) {
		return false
	}

	if err := r.repo.SetRunning(ctx, run.ID, r.now()); err != nil {
		r.log.Error("selfconvo: set running", "run", run.ID, "err", err)
		hadErr = true
	}

	// NextPendingTurns(n=2): index 0 is the turn this call may claim and
	// send; index 1, when present, is consulted ONLY for its DelaySeconds
	// after the send resolves (below) — replacing what used to be a SECOND
	// NextPendingTurn round trip made purely to learn that delay. See
	// runner.go's package-level round-trip accounting in
	// docs/brief-self-convo.md and NextPendingTurns' own doc comment.
	pending, err := r.repo.NextPendingTurns(ctx, run.ID, 2)
	if err != nil {
		r.log.Error("selfconvo: next pending turns", "run", run.ID, "err", err)
		return true
	}
	if len(pending) == 0 {
		return r.finishIfNotTerminal(ctx, run.ID, convo.RunDone, "", r.now()) || hadErr
	}
	turn := pending[0]

	if pa, ok := r.gw.Participant(run.SessionAID); !ok || !pa.Connected {
		return r.finishIfNotTerminal(ctx, run.ID, convo.RunFailed,
			fmt.Sprintf("session %s (%s) is not connected", run.SessionAName, run.SessionAID), r.now()) || hadErr
	}
	if pb, ok := r.gw.Participant(run.SessionBID); !ok || !pb.Connected {
		return r.finishIfNotTerminal(ctx, run.ID, convo.RunFailed,
			fmt.Sprintf("session %s (%s) is not connected", run.SessionBName, run.SessionBID), r.now()) || hadErr
	}

	// The message id is minted and stored BEFORE Send is ever called — see
	// SendRequest.MessageID's doc comment and Recover's, below: a turn cut
	// off mid-send must already carry the wa_messages id it needs to be
	// reconciled by, not learn one only after the send returns.
	msgID := id.New()
	if err := r.repo.ClaimTurn(ctx, run.ID, turn.Seq, msgID); err != nil {
		if errors.Is(err, convo.ErrTurnNotPending) {
			// A concurrent Cancel (or another process) already moved this
			// turn off pending between our NextPendingTurns read above and
			// this claim — the run is already finishing/finished under us.
			// Not a repo failure and not this call's job to conclude the
			// run further; just stop here without sending.
			return hadErr
		}
		r.log.Error("selfconvo: claim turn", "run", run.ID, "seq", turn.Seq, "err", err)
		return true
	}

	// The receipt's MessageID is already on the turn (ClaimTurn, above,
	// minted and stored it before this call) — nothing left to record from
	// it, unlike before this refactor.
	_, sendErr := r.gw.Send(ctx, SendRequest{
		SessionID: turn.SessionID,
		ToDigits:  turn.ToPhone,
		Text:      turn.Body,
		RunID:     run.ID,
		MessageID: msgID,
	})
	sentAt := r.now()

	if sendErr != nil && ctx.Err() != nil {
		// Stop() cut this send off (see this function's doc comment) —
		// leave the turn "sending" and the run non-terminal so the next
		// boot's Recover/processRun resumes it exactly like a crash,
		// instead of recording a failure the send may not actually have
		// suffered.
		r.log.Warn("selfconvo: shutdown cut off an in-flight send; leaving the turn sending for the next boot to resume",
			"run", run.ID, "seq", turn.Seq)
		return hadErr
	}

	// Terminal writes after a send use a ctx stripped of cancellation — a
	// shutdown mid-send (Stop's cancel) must not stop the result from being
	// recorded, mirroring wa.Manager's own persistCtx precedent. (This path
	// is for a send that ended on its own — succeeded or genuinely failed —
	// while ctx was still live; the ctx.Err() case above already returned.)
	finishCtx := context.WithoutCancel(ctx)

	if sendErr != nil {
		speaker := run.SessionAName
		if turn.Speaker == "b" {
			speaker = run.SessionBName
		}
		if err := r.repo.MarkTurnFailed(finishCtx, run.ID, turn.Seq, sendErr.Error()); err != nil {
			r.log.Error("selfconvo: mark turn failed", "run", run.ID, "seq", turn.Seq, "err", err)
			hadErr = true
		}
		reason := fmt.Sprintf("turn %d from %s: %s", turn.Seq, speaker, sendErr.Error())
		return r.finishIfNotTerminal(finishCtx, run.ID, convo.RunFailed, reason, sentAt) || hadErr
	}

	// The post-send write needs only state='sent', sent_at=? — the
	// message_id column was already stamped by ClaimTurn above, so there is
	// no longer a separate SetTurnMessageID write to make (that method is
	// gone; nothing else used it).
	if err := r.repo.MarkTurnSent(finishCtx, run.ID, turn.Seq, sentAt); err != nil {
		r.log.Error("selfconvo: mark turn sent", "run", run.ID, "seq", turn.Seq, "err", err)
		return true
	}

	// Re-read turns_done/state from the DB rather than trusting the `run`
	// snapshot passed into this call: a concurrent Cancel could have
	// finished the run while the Send above was in flight (that turn was
	// still allowed to complete, per this function's doc comment), and this
	// is also where turns_done actually gets incremented, whether the run
	// is finishing right now or continuing to its next turn.
	current, err := r.repo.GetRun(finishCtx, run.ID)
	if err != nil {
		r.log.Error("selfconvo: get run before advancing", "run", run.ID, "err", err)
		return true
	}
	if convo.IsTerminal(current.State) {
		// Already cancelled (or otherwise finished) — AdvanceRun/FinishRun
		// below must not resurrect or overwrite that verdict.
		return hadErr
	}
	turnsDone := current.TurnsDone + 1

	if len(pending) < 2 {
		if err := r.repo.AdvanceRun(finishCtx, run.ID, turnsDone, sentAt); err != nil {
			r.log.Error("selfconvo: advance run (final turn)", "run", run.ID, "err", err)
			hadErr = true
		}
		return r.finishIfNotTerminal(finishCtx, run.ID, convo.RunDone, "", sentAt) || hadErr
	}

	// now + next turn's delay — NEVER previous_due + delay (the plan's
	// "lazy next-due" rule, docs/brief-self-convo.md's Scheduler design
	// section). pending[1].DelaySeconds is the PRE-send snapshot fetched
	// above — safe to use as-is even though the send happened in between,
	// because DelaySeconds is immutable once a turn is created (Expand
	// draws it once, at Trigger time) and is never touched by anything that
	// could race here, including a concurrent Cancel (which only ever
	// changes state, never delay_seconds).
	nextDue := sentAt.Add(time.Duration(pending[1].DelaySeconds) * time.Second)
	if err := r.repo.AdvanceRun(finishCtx, run.ID, turnsDone, nextDue); err != nil {
		r.log.Error("selfconvo: advance run", "run", run.ID, "err", err)
		hadErr = true
	}
	return hadErr
}

// finishIfNotTerminal re-reads runID and calls FinishRun only if it is
// still non-terminal — the guard that keeps a concurrent Cancel's verdict
// from being overwritten by a Tick that was already past the point of no
// return (see processRun's doc comment). Returns true if a repo call here
// failed (see processRun's doc comment for what its caller does with that).
func (r *Runner) finishIfNotTerminal(ctx context.Context, runID, state, reason string, at time.Time) bool {
	current, err := r.repo.GetRun(ctx, runID)
	if err != nil {
		r.log.Error("selfconvo: get run before finishing", "run", runID, "err", err)
		return true
	}
	if convo.IsTerminal(current.State) {
		return false
	}
	if err := r.repo.FinishRun(ctx, runID, state, reason, at); err != nil {
		r.log.Error("selfconvo: finish run", "run", runID, "err", err)
		return true
	}
	return false
}

// Recover is the boot-time reconciliation this package used to do inline,
// once per due run, on every single tick (the old per-run MarkInterrupted
// call inside processRun): it now runs EXACTLY ONCE, from Start, before the
// poll loop begins — see Start's own call site below.
//
// For every turn any process left in "sending" (repo.SendingTurns, unscoped
// across every run — a crash can leave more than one run's turn mid-send at
// once), Recover asks the Gateway what wa_messages actually recorded for
// that turn's message id (ClaimTurn stores it BEFORE Send is ever called —
// see SendRequest.MessageID's doc comment — so every turn found here has
// one, unless it was claimed by code older than that change):
//
//   - "sent" → MarkTurnSent. The audit row says it went out; the crash only
//     interrupted THIS process's own bookkeeping, not the actual delivery.
//   - "failed" → MarkTurnFailed, with the row's own stored reason — the send
//     genuinely failed on the wire before the crash, and reusing wa_messages'
//     reason instead of inventing a new one keeps the two records agreeing.
//   - "queued", or no row at all → interrupted (repo.MarkInterrupted — safe
//     to call at the run's granularity here because the "one turn sending
//     per run" invariant means it only ever touches the one turn Recover is
//     already looking at). This is the genuinely unknown case: nothing on
//     this side of the send can tell whether it is still in flight or was
//     lost outright, so — as before this refactor — "interrupted" is the
//     honest answer, not a guess.
//
// In every case the run ITSELF is left exactly as found — Recover does not
// conclude it done/failed/cancelled; that stays processRun's job, driven by
// the normal Tick cycle. What Recover DOES do unconditionally is set
// next_due_at to now, so the very next Tick picks the run back up
// immediately rather than waiting out whatever next_due_at a crash mid-send
// left behind (docs/brief-self-convo.md's restart-safety section, and the
// existing TestATurnFoundMidSendAfterARestartIsMarkedInterruptedAndTheRunContinues
// / TestAGracefulStopLeavesAnInFlightTurnSendingSoTheNextBootResumesTheRun
// tests, which now call Recover explicitly before Tick rather than relying
// on Tick to do it).
//
// Returns an error only for the SendingTurns query itself failing outright
// (a genuine boot-time problem worth surfacing to Start's caller's log);
// a single turn's own reconciliation failing is logged and Recover moves on
// to the next one, the same log-and-continue style processRun itself uses.
func (r *Runner) Recover(ctx context.Context) error {
	turns, err := r.repo.SendingTurns(ctx)
	if err != nil {
		return fmt.Errorf("selfconvo: recover: list sending turns: %w", err)
	}
	for _, t := range turns {
		r.recoverTurn(ctx, t)
	}
	return nil
}

// recoverTurn is Recover's per-turn body — see Recover's doc comment for
// the full policy.
func (r *Runner) recoverTurn(ctx context.Context, t convo.Turn) {
	now := r.now()
	status, reason, found, err := r.gw.MessageStatus(ctx, t.MessageID)
	if err != nil {
		r.log.Error("selfconvo: recover: message status", "run", t.RunID, "seq", t.Seq, "message", t.MessageID, "err", err)
	}

	turnsDoneDelta := 0
	switch {
	case found && status == MessageStatusSent:
		if err := r.repo.MarkTurnSent(ctx, t.RunID, t.Seq, now); err != nil {
			r.log.Error("selfconvo: recover: mark turn sent", "run", t.RunID, "seq", t.Seq, "err", err)
			return
		}
		turnsDoneDelta = 1
	case found && status == MessageStatusFailed:
		if err := r.repo.MarkTurnFailed(ctx, t.RunID, t.Seq, reason); err != nil {
			r.log.Error("selfconvo: recover: mark turn failed", "run", t.RunID, "seq", t.Seq, "err", err)
			return
		}
	default:
		// "queued", or the message row doesn't exist at all — see Recover's
		// doc comment for why this is the only case treated as genuinely
		// unknown rather than resolved one way or the other.
		if _, err := r.repo.MarkInterrupted(ctx, t.RunID); err != nil {
			r.log.Error("selfconvo: recover: mark interrupted", "run", t.RunID, "seq", t.Seq, "err", err)
			return
		}
		r.log.Warn("selfconvo: a turn was left sending by a previous process; marked interrupted",
			"run", t.RunID, "seq", t.Seq)
	}

	run, err := r.repo.GetRun(ctx, t.RunID)
	if err != nil {
		r.log.Error("selfconvo: recover: get run", "run", t.RunID, "err", err)
		return
	}
	if convo.IsTerminal(run.State) {
		return
	}
	if err := r.repo.AdvanceRun(ctx, t.RunID, run.TurnsDone+turnsDoneDelta, now); err != nil {
		r.log.Error("selfconvo: recover: advance run", "run", t.RunID, "err", err)
	}
}

// Start spawns the background loop: Tick, then sleep until the returned
// duration, an early Poke, or the loop's own ctx ending — the plan's
// poll-plus-poke design, matching internal/wa/inbound.go's
// `select { <-ctx.Done(); <-time.After(..); <-poke }` house pattern.
// Idempotent: a second Start call while one is already running is a no-op.
//
// The loop's ctx is deliberately NOT ctx itself but
// context.WithCancel(context.WithoutCancel(ctx)): ctx is cmd/app/main.go's
// SIGTERM ctx (signal.NotifyContext), which ends the INSTANT a shutdown
// signal arrives — well before main's own shutdown sequence calls Stop()
// between server.Shutdown and manager.Close (main.go's boot-order comment).
// Deriving the loop directly from that ctx used to cancel an in-flight
// turn's gw.Send immediately on SIGTERM, long before Stop ever ran, which
// made processRun treat a send that might still have succeeded on the wire
// as a hard failure — worse than a crash, which just leaves the turn
// "sending" for the next boot to resume (see processRun's doc comment).
// WithoutCancel severs that link: only a direct call to Stop() (or ctx's
// own values, which WithoutCancel preserves) can end the loop now.
//
// Start also runs Recover exactly once, before the loop's first Tick — see
// Recover's own doc comment for what it reconciles and why that used to
// happen (worse: repeatedly) inside processRun instead.
func (r *Runner) Start(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		return
	}
	childCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	r.cancel = cancel
	r.wg.Add(1)
	go r.loop(childCtx)
}

func (r *Runner) loop(ctx context.Context) {
	defer r.wg.Done()
	if err := r.Recover(ctx); err != nil {
		r.log.Error("selfconvo: recover failed", "err", err)
	}
	for {
		sleep, err := r.Tick(ctx)
		if err != nil && ctx.Err() == nil {
			r.log.Error("selfconvo: tick failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(sleep):
		case <-r.poke:
		}
	}
}

// Stop cancels the loop's own ctx (see Start's doc comment for why that is
// NOT the same ctx Start was called with) and waits for it to exit. A turn
// that was mid-send when Stop was called is left "sending" rather than
// finished (processRun's doc comment) — the cut-off gw.Send call returns
// promptly once cancel fires, so Stop still blocks only as long as that
// return takes, never as long as a whole poll interval, but the run is
// resumed at the next boot rather than concluded here. A no-op if the loop
// isn't running.
func (r *Runner) Stop() {
	r.mu.Lock()
	cancel := r.cancel
	r.cancel = nil
	r.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	r.wg.Wait()
}

// Poke wakes the loop's next Tick early — a non-blocking send (poke is
// buffered 1), so a burst of triggers/cancels never blocks its caller.
func (r *Runner) Poke() {
	select {
	case r.poke <- struct{}{}:
	default:
	}
}
