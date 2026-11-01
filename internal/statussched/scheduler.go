// Package statussched plans and fires scheduled WhatsApp status posts
// (internal/domain/statusschedule). One component, one loop: each Tick first
// PLANS (draws today's random instant for every active schedule that owes a
// post today), then FIRES whatever is due.
//
// Rules that are deliberate, not accidents:
//
//   - Past times are skipped, never back-filled. A draw is taken from the
//     part of today's window that is still in the future; if the window is
//     over, today is skipped. A restart at 09:00 does not post a status
//     "for 07:30".
//   - No auto-retry. A failed post is recorded and shown, never retried: a
//     status that actually reached WhatsApp and then errored on the way back
//     would be duplicated to every contact, and it cannot be unsent. The
//     fire is CLAIMED (next_fire_at cleared) before the post, so even a
//     crash mid-post cannot re-send.
//   - At most one post per local day per schedule (last_fired_at guard), so
//     edit / pause / resume cannot cause a second post the same day.
//   - Clock and RNG are injected; tests never sleep.
package statussched

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	sched "github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/statusschedule"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/schedclock"
)

// DefaultPollInterval is how often the loop wakes. A scheduled time is
// therefore hit within this interval, which is plenty for a status post.
const DefaultPollInterval = 30 * time.Second

// MaxLateness is how far past its planned instant a post may still go out.
// Beyond it (the app was down or stuck) the post is skipped, not
// back-filled: "07:30" must not appear at 11:00 after a restart.
const MaxLateness = 10 * time.Minute

// postTimeout bounds one Gateway.Post. It runs detached from the loop's
// context so Stop lets an in-flight post finish instead of cutting a status
// off half-way (which could leave it delivered but unrecorded).
const postTimeout = 60 * time.Second

// MaxRecipients mirrors the handler's per-request cap (api_status.go's
// maxStatusRecipients); an "all contacts" schedule that outgrows it fails
// loudly instead of silently posting to a truncated audience.
const MaxRecipients = 1000

// Post is one resolved status post, ready for the Gateway.
type Post struct {
	Schedule   sched.Schedule
	Recipients []string
}

// Gateway is the scheduler's only route to WhatsApp, contacts and object
// storage — an interface so tests never need a live session.
type Gateway interface {
	// Connected reports whether the session can post right now; reason is
	// operator-facing when it cannot.
	Connected(sessionID string) (ok bool, reason string)
	// ContactJIDs lists the JID of every contact, resolved NOW.
	ContactJIDs(ctx context.Context) ([]string, error)
	// Post publishes the status (fetching/uploading media as needed) and
	// writes the wa_messages audit row, returning the WhatsApp message id.
	Post(ctx context.Context, p Post) (waMessageID string, err error)
}

// Options configures a Scheduler. Zero values pick production defaults.
type Options struct {
	Now          func() time.Time
	Rand         *rand.Rand
	PollInterval time.Duration
	Logger       *slog.Logger
}

// Scheduler is the planner+runner.
type Scheduler struct {
	repo *sched.Repository
	gw   Gateway
	now  func() time.Time
	rng  *rand.Rand
	poll time.Duration
	log  *slog.Logger

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New builds a Scheduler.
func New(repo *sched.Repository, gw Gateway, opt Options) *Scheduler {
	s := &Scheduler{repo: repo, gw: gw, now: opt.Now, rng: opt.Rand, poll: opt.PollInterval, log: opt.Logger}
	if s.now == nil {
		s.now = func() time.Time { return time.Now().UTC() }
	}
	if s.rng == nil {
		s.rng = rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0x57a7_5c4e))
	}
	if s.poll <= 0 {
		s.poll = DefaultPollInterval
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	return s
}

// Tick runs one plan pass and one fire pass.
func (s *Scheduler) Tick(ctx context.Context) error {
	now := s.now()
	active, err := s.repo.ListActive(ctx)
	if err != nil {
		return fmt.Errorf("list active: %w", err)
	}
	for _, sc := range active {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.plan(ctx, sc, now); err != nil {
			s.log.Error("statussched: plan", "schedule", sc.ID, "err", err)
		}
	}
	due, err := s.repo.Due(ctx, now)
	if err != nil {
		return fmt.Errorf("list due: %w", err)
	}
	for _, sc := range due {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.fire(ctx, sc, now)
	}
	return nil
}

// plan decides what, if anything, sc owes today (in sc's own timezone).
func (s *Scheduler) plan(ctx context.Context, sc sched.Schedule, now time.Time) error {
	loc, err := time.LoadLocation(sc.Timezone)
	if err != nil {
		return s.repo.SetState(ctx, sc.ID, sched.StateStalled, fmt.Sprintf("unknown timezone %q", sc.Timezone))
	}
	today := schedclock.LocalDate(now, loc)
	if sc.EndDate != "" && today > sc.EndDate {
		return s.repo.SetState(ctx, sc.ID, sched.StateFinished, "")
	}
	if today < sc.StartDate || sc.LastPlannedDate == today {
		return nil
	}
	if !sc.LastFiredAt.IsZero() && schedclock.LocalDate(sc.LastFiredAt, loc) == today {
		return nil // already posted today (edit/resume must not post twice)
	}
	if !schedclock.FiresOn(sc.Weekdays, now.In(loc)) {
		return nil
	}
	start, err1 := schedclock.ParseClock(sc.WindowStart)
	end, err2 := schedclock.ParseClock(sc.WindowEnd)
	if err1 != nil || err2 != nil {
		return s.repo.SetState(ctx, sc.ID, sched.StateStalled, "invalid time window")
	}
	lo, hi := schedclock.Bounds(now, loc, start, end)
	if now.After(lo) {
		lo = now // never plan into the past
	}
	if !now.Before(hi) {
		// Window already over today: skipped, not back-filled.
		return s.repo.MarkPlanned(ctx, sc.ID, today, time.Time{})
	}
	at := schedclock.Draw(s.rng, lo, hi).UTC()
	s.log.Info("statussched: planned", "schedule", sc.ID, "at", at.Format(time.RFC3339))
	return s.repo.MarkPlanned(ctx, sc.ID, today, at)
}

// fire posts sc once. It never retries.
func (s *Scheduler) fire(ctx context.Context, sc sched.Schedule, now time.Time) {
	if err := s.repo.ClaimFire(ctx, sc.ID, sc.NextFireAt); err != nil {
		if !errors.Is(err, sched.ErrNotClaimable) {
			s.log.Error("statussched: claim", "schedule", sc.ID, "err", err)
		}
		return
	}
	finish := context.WithoutCancel(ctx)
	record := func(result, errText string) {
		if err := s.repo.RecordResult(finish, sc.ID, now, result, errText); err != nil {
			s.log.Error("statussched: record result", "schedule", sc.ID, "err", err)
		}
	}

	if late := now.Sub(sc.NextFireAt); late > MaxLateness {
		record(sched.ResultSkipped, fmt.Sprintf("skipped: missed its slot by %s (not back-filled)", late.Round(time.Minute)))
		return
	}
	if ok, reason := s.gw.Connected(sc.SessionID); !ok {
		if reason == "" {
			reason = "session not connected"
		}
		record(sched.ResultSkipped, "skipped: "+reason)
		return
	}
	recipients, err := s.resolveRecipients(ctx, sc)
	if err != nil {
		record(sched.ResultFailed, err.Error())
		return
	}
	postCtx, cancel := context.WithTimeout(finish, postTimeout)
	defer cancel()
	waID, err := s.gw.Post(postCtx, Post{Schedule: sc, Recipients: recipients})
	if err != nil {
		s.log.Warn("statussched: post failed (not retried)", "schedule", sc.ID, "err", err)
		record(sched.ResultFailed, err.Error())
		return
	}
	s.log.Info("statussched: posted", "schedule", sc.ID, "wa_message_id", waID, "recipients", len(recipients))
	record(sched.ResultSent, "")
}

func (s *Scheduler) resolveRecipients(ctx context.Context, sc sched.Schedule) ([]string, error) {
	var out []string
	switch sc.RecipientsMode {
	case sched.ModeAll:
		jids, err := s.gw.ContactJIDs(ctx)
		if err != nil {
			return nil, fmt.Errorf("could not load contacts: %w", err)
		}
		out = jids
	default:
		if err := json.Unmarshal([]byte(sc.RecipientsJSON), &out); err != nil {
			return nil, fmt.Errorf("stored recipient list is unreadable: %w", err)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no recipients: the contact list is empty")
	}
	if len(out) > MaxRecipients {
		return nil, fmt.Errorf("too many recipients: %d (max %d per post)", len(out), MaxRecipients)
	}
	return out, nil
}

// Start spawns the loop. It runs on a context detached from ctx's
// cancellation so only Stop ends it (same shape as the other runners).
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return
	}
	child, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.cancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			if err := s.Tick(child); err != nil && child.Err() == nil {
				s.log.Error("statussched: tick failed", "err", err)
			}
			select {
			case <-child.Done():
				return
			case <-time.After(s.poll):
			}
		}
	}()
}

// Stop ends the loop and waits for any in-flight post to finish.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	s.wg.Wait()
}
