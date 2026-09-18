// runner.go: advancing already-planned gb.Turn rows through their state
// machine. Tick's job is deliberately small compared to
// internal/selfconvo/runner.go's own Tick: every turn's wording was already
// resolved at PLANNING time (planner.go/wording.go), so Tick never calls the
// AI client — it only reads body back and dispatches it.
package groupbroadcast

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	gb "github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/groupbroadcast"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/id"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// defaultRunnerPollInterval mirrors internal/selfconvo/runner.go's own
// defaultPollInterval.
const defaultRunnerPollInterval = 5 * time.Second

// defaultRunnerMinSleep floors Tick's returned sleep.
const defaultRunnerMinSleep = 1 * time.Second

// RunnerOptions configures a Runner.
type RunnerOptions struct {
	Now          func() time.Time
	PollInterval time.Duration
	MinSleep     time.Duration
	Logger       *slog.Logger
}

// Runner advances every group-broadcast turn through pending -> sending ->
// (sent | failed | skipped).
type Runner struct {
	repo     *gb.Repository
	gw       Gateway
	now      func() time.Time
	poll     time.Duration
	minSleep time.Duration
	log      *slog.Logger

	errTicks int // consecutive erroring ticks, same backoff shape as selfconvo's Runner

	poke chan struct{}

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New builds a Runner over repo/gw.
func New(repo *gb.Repository, gw Gateway, opt RunnerOptions) *Runner {
	now := opt.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	poll := opt.PollInterval
	if poll <= 0 {
		poll = defaultRunnerPollInterval
	}
	minSleep := opt.MinSleep
	if minSleep <= 0 {
		minSleep = defaultRunnerMinSleep
	}
	log := opt.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Runner{repo: repo, gw: gw, now: now, poll: poll, minSleep: minSleep, log: log, poke: make(chan struct{}, 1)}
}

// Tick advances every currently-due pending turn exactly once. It never
// sleeps; the caller (Start's loop, or a test) decides what to do with the
// returned sleep duration.
func (r *Runner) Tick(ctx context.Context) (time.Duration, error) {
	now := r.now()
	turns, err := r.repo.DueTurns(ctx, now, 20)
	if err != nil {
		return r.poll, err
	}
	hadErr := false
	for _, t := range turns {
		if ctx.Err() != nil {
			break
		}
		if r.processTurn(ctx, t) {
			hadErr = true
		}
	}
	if ctx.Err() != nil {
		return r.poll, ctx.Err()
	}
	return r.settle(r.poll, hadErr), nil
}

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

// processTurn re-verifies turn's own session is currently connected
// IMMEDIATELY before sending — confirmed decision #8: wording is generated
// hours ahead of time, so a session that was healthy at planning time can
// have dropped by its scheduled slot. A sender turn failing this check (or
// the send itself) fails the WHOLE occurrence and skips every remaining
// pending turn (there is nothing left to reply to); a receiver turn failing
// either is marked alone (skipped for a health failure, failed for a wire
// failure) — the sender and every other turn proceed untouched. Returns
// true only when a REPO call failed (Tick's own backoff accounting), never
// for a business-logic outcome like a disconnected session.
func (r *Runner) processTurn(ctx context.Context, turn gb.Turn) bool {
	if _, reason, ok := r.gw.Healthy([]string{turn.SessionID}); !ok {
		return r.concludeUnhealthyTurn(ctx, turn, reason)
	}

	occ, err := r.repo.GetOccurrence(ctx, turn.OccurrenceID)
	if err != nil {
		r.log.Error("groupbroadcast: runner: get occurrence", "turn", turn.ID, "err", err)
		return true
	}
	groupJID, err := wa.ParseGroupJID(occ.GroupJID)
	if err != nil {
		r.log.Error("groupbroadcast: runner: bad group jid", "turn", turn.ID, "group_jid", occ.GroupJID, "err", err)
		return r.concludeFailedTurn(ctx, turn, err.Error())
	}

	msgID := id.New()
	if err := r.repo.ClaimTurn(ctx, turn.ID, msgID); err != nil {
		if errors.Is(err, gb.ErrTurnNotPending) {
			return false
		}
		r.log.Error("groupbroadcast: runner: claim turn", "turn", turn.ID, "err", err)
		return true
	}

	_, sendErr := r.gw.Send(ctx, SendRequest{
		SessionID: turn.SessionID, ToGroupJID: groupJID, Text: turn.Body,
		OccurrenceID: turn.OccurrenceID, MessageID: msgID,
	})
	sentAt := r.now()
	finishCtx := context.WithoutCancel(ctx)

	if sendErr != nil {
		if err := r.repo.MarkTurnFailed(finishCtx, turn.ID, sendErr.Error()); err != nil {
			r.log.Error("groupbroadcast: runner: mark turn failed", "turn", turn.ID, "err", err)
		}
		return r.afterTurnConcluded(finishCtx, turn)
	}

	if err := r.repo.MarkTurnSent(finishCtx, turn.ID, sentAt); err != nil {
		r.log.Error("groupbroadcast: runner: mark turn sent", "turn", turn.ID, "err", err)
	}
	return r.afterTurnConcluded(finishCtx, turn)
}

// concludeUnhealthyTurn marks turn skipped (its own re-check failed) and
// applies the same sender-fails-the-occurrence / receiver-fails-alone rule
// as concludeFailedTurn.
func (r *Runner) concludeUnhealthyTurn(ctx context.Context, turn gb.Turn, reason string) bool {
	if err := r.repo.MarkTurnSkipped(ctx, turn.ID, reason); err != nil {
		r.log.Error("groupbroadcast: runner: mark turn skipped", "turn", turn.ID, "err", err)
	}
	return r.afterTurnConcluded(ctx, turn)
}

// concludeFailedTurn marks turn failed for reason and applies the same
// sender/receiver rule.
func (r *Runner) concludeFailedTurn(ctx context.Context, turn gb.Turn, reason string) bool {
	if err := r.repo.MarkTurnFailed(ctx, turn.ID, reason); err != nil {
		r.log.Error("groupbroadcast: runner: mark turn failed", "turn", turn.ID, "err", err)
	}
	return r.afterTurnConcluded(ctx, turn)
}

// afterTurnConcluded is the shared tail of every turn-processing path: a
// SENDER turn that did not end up "sent" fails the whole occurrence
// (skipping every other pending turn — there is nothing left to reply to
// without a delivered opener); either way it then finishes the occurrence
// if nothing is left pending. Returns true if any repo call here failed.
func (r *Runner) afterTurnConcluded(ctx context.Context, turn gb.Turn) bool {
	hadErr := false
	if turn.Role == gb.RoleSender {
		fresh, err := r.repo.TurnsForOccurrence(ctx, turn.OccurrenceID)
		if err != nil {
			r.log.Error("groupbroadcast: runner: get turns for occurrence", "occurrence", turn.OccurrenceID, "err", err)
			return true
		}
		var senderOK bool
		for _, t := range fresh {
			if t.ID == turn.ID {
				senderOK = t.State == gb.TurnSent
			}
		}
		if !senderOK {
			if r.skipRemainingPending(ctx, turn.OccurrenceID) {
				hadErr = true
			}
		}
	}
	if r.maybeFinishOccurrence(ctx, turn.OccurrenceID) {
		hadErr = true
	}
	return hadErr
}

// skipRemainingPending marks every still-pending turn of occurrenceID
// skipped — called when the sender's own turn did not end up sent, so
// there is nothing left for any receiver to reply to.
func (r *Runner) skipRemainingPending(ctx context.Context, occurrenceID string) bool {
	pending, err := r.repo.PendingTurnsForOccurrence(ctx, occurrenceID)
	if err != nil {
		r.log.Error("groupbroadcast: runner: pending turns for occurrence", "occurrence", occurrenceID, "err", err)
		return true
	}
	hadErr := false
	for _, t := range pending {
		if err := r.repo.MarkTurnSkipped(ctx, t.ID, "sender turn did not send; occurrence cancelled"); err != nil {
			r.log.Error("groupbroadcast: runner: mark turn skipped", "turn", t.ID, "err", err)
			hadErr = true
		}
	}
	return hadErr
}

// maybeFinishOccurrence concludes occurrenceID once nothing is left
// pending: failed if its sender turn did not end up sent, done otherwise
// (a skipped/failed RECEIVER turn alone never fails the occurrence — see
// this package's own doc comment on the deliberate deviation from
// selfconvo's all-or-nothing rule).
func (r *Runner) maybeFinishOccurrence(ctx context.Context, occurrenceID string) bool {
	pending, err := r.repo.PendingTurnsForOccurrence(ctx, occurrenceID)
	if err != nil {
		r.log.Error("groupbroadcast: runner: pending turns for occurrence", "occurrence", occurrenceID, "err", err)
		return true
	}
	if len(pending) > 0 {
		return false
	}
	turns, err := r.repo.TurnsForOccurrence(ctx, occurrenceID)
	if err != nil {
		r.log.Error("groupbroadcast: runner: turns for occurrence", "occurrence", occurrenceID, "err", err)
		return true
	}
	state := gb.OccurrenceDone
	for _, t := range turns {
		if t.Role == gb.RoleSender && t.State != gb.TurnSent {
			state = gb.OccurrenceFailed
		}
	}
	if err := r.repo.FinishOccurrenceIfDone(ctx, occurrenceID, state, r.now()); err != nil {
		r.log.Error("groupbroadcast: runner: finish occurrence", "occurrence", occurrenceID, "err", err)
		return true
	}
	return false
}

// Start spawns Tick's background loop — same poll-plus-poke shape as
// internal/selfconvo/runner.go's Start, including the same
// context.WithCancel(context.WithoutCancel(ctx)) derivation so only a
// direct Stop() call ends the loop.
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
	for {
		sleep, err := r.Tick(ctx)
		if err != nil && ctx.Err() == nil {
			r.log.Error("groupbroadcast: runner: tick failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(sleep):
		case <-r.poke:
		}
	}
}

// Stop cancels the loop's own ctx and waits for it to exit.
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

// Poke wakes the loop's next Tick early.
func (r *Runner) Poke() {
	select {
	case r.poke <- struct{}{}:
	default:
	}
}
