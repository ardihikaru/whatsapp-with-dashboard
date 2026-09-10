// planner.go: turning a recurring convo.Schedule into concrete runs, once
// per matching day. Planner is a SEPARATE background loop from Runner (same
// poll-plus-poke shape as Start/loop/Stop there — see PlanDay's own doc
// comment) because a schedule's cadence (daily/weekly/interval, a window of
// the day, a per-sender daily cap) is a wholly different concern from
// advancing an already-created run turn by turn; Runner never needs to know
// a schedule exists beyond the fields a run it created carries
// (ScheduleID/OccurrenceDate/ChainDepth/Topic — see runner.go's
// continueChain and StartOccurrence).
package selfconvo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/convo"
)

// defaultPlannerPollInterval is PlannerOptions.PollInterval's fallback.
// Minutes, not seconds, unlike Runner's own poll: a schedule's cadence is
// measured in days, so there is no pacing reason to poll faster, and every
// PlanDay call queries every active schedule in the system.
const defaultPlannerPollInterval = 5 * time.Minute

// defaultMinSetSpacing is PlannerOptions.MinSetSpacing's fallback — see
// that field's doc comment.
const defaultMinSetSpacing = 30 * time.Minute

// defaultMaxMsgsPerDay mirrors config.DefaultSelfConvoMaxMsgsPerDay,
// deliberately duplicated here rather than imported — this package's
// Options/PlannerOptions threading already follows the "the default lives
// beside the field it defaults, cmd/app wires the configured value in"
// pattern (see Options.Paraphrase and cmd/app/selfconvo.go), the same shape
// gateway.go's MessageStatus constants use to stay free of a cross-package
// dependency it does not otherwise need.
const defaultMaxMsgsPerDay = 200

// dateLayout is the YYYY-MM-DD shape every date column in this schema uses
// (schedule_model.go's StartDate/EndDate/LastPlannedDate, Run.OccurrenceDate)
// — see migrations/0008_convo_scheduling.sql's header for why these are
// VARCHAR, not DATE.
const dateLayout = "2006-01-02"

// PlannerOptions configures a Planner. Every field is optional, following
// Runner's own Options convention exactly (see that type's doc comment).
type PlannerOptions struct {
	Now          func() time.Time
	Rand         *rand.Rand
	PollInterval time.Duration
	// MinSetSpacing is the minimum gap PlanDay enforces between two of one
	// schedule's own sets_per_day start times on the same day (step 5 of
	// this file's own PlanDay doc comment). Defaults to
	// defaultMinSetSpacing (30m) when <= 0.
	MinSetSpacing time.Duration
	Logger        *slog.Logger
	// MaxMsgsPerDay caps how many turns one sender session may already have
	// scheduled today (convo.Repository.CountSenderTurnsForDate) before
	// PlanDay stops planning FURTHER sets for that sender — wired from
	// WSM_SELF_CONVO_MAX_MSGS_PER_DAY (cmd/app/selfconvo.go). Reaching the
	// cap stalls nothing; it just stops planning and logs (see PlanDay's
	// own doc comment) — a full inbox is not a health problem the way an
	// unhealthy session is. Defaults to defaultMaxMsgsPerDay (200) when
	// <= 0.
	MaxMsgsPerDay int
}

// Planner materialises convo.Schedule rows into convo.Run rows on a
// recurring cadence — see PlanDay's own doc comment for the full
// per-schedule algorithm, and this file's own doc comment for why it is a
// separate loop from Runner.
type Planner struct {
	repo          *convo.Repository
	runner        *Runner
	now           func() time.Time
	rng           *rand.Rand
	poll          time.Duration
	minSetSpacing time.Duration
	log           *slog.Logger
	maxMsgsPerDay int

	poke chan struct{}

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewPlanner builds a Planner over repo, driving runs through r (Planner
// reaches r's own Gateway for the health gate, and calls r.StartOccurrence
// to materialise a set — see this file's own doc comment for why a
// schedule's cadence concern is kept out of Runner itself).
func NewPlanner(repo *convo.Repository, r *Runner, opt PlannerOptions) *Planner {
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
		poll = defaultPlannerPollInterval
	}
	minSpacing := opt.MinSetSpacing
	if minSpacing <= 0 {
		minSpacing = defaultMinSetSpacing
	}
	log := opt.Logger
	if log == nil {
		log = slog.Default()
	}
	maxMsgs := opt.MaxMsgsPerDay
	if maxMsgs <= 0 {
		maxMsgs = defaultMaxMsgsPerDay
	}
	return &Planner{
		repo: repo, runner: r, now: now, rng: rng, poll: poll,
		minSetSpacing: minSpacing, log: log, maxMsgsPerDay: maxMsgs,
		poke: make(chan struct{}, 1),
	}
}

// PlanDay materialises today's occurrences for every schedule
// convo.Repository.PlannableSchedules returns, in order, and returns the
// number of receiver RUNS actually started (summed across every schedule,
// not the number of schedules touched) — so a caller/log can see at a
// glance whether a pass did anything.
//
// Per schedule:
//  1. Resolve its timezone (time.LoadLocation). An unloadable zone stalls
//     THAT schedule with a reason naming the zone and moves on — one bad
//     row must never abort the whole pass.
//  2. Decide whether today (in the schedule's OWN timezone) matches its
//     recurrence: daily always; weekly if today's ISO-8601 weekday
//     (Monday=1..Sunday=7) is in the comma-separated Weekdays list; interval
//     if the number of days since StartDate is a multiple of IntervalDays.
//  3. Idempotency: CountRunsForOccurrence(schedule, today) > 0 means a set
//     was already started for today (normally by an EARLIER call in the
//     same pass having already called MarkPlanned below, in which case
//     PlannableSchedules would not have returned this row again at all —
//     this branch exists for the crash-mid-day case, where a previous
//     process created runs but died before reaching MarkPlanned). Either
//     way, just mark planned and move on rather than starting a second set.
//  4. Health check (Gateway.Healthy on the sender + every receiver). Not
//     healthy -> SetScheduleState(stalled, reason naming the session), plan
//     nothing for this schedule today.
//  5. Per-sender daily cap (CountSenderTurnsForDate vs MaxMsgsPerDay). At or
//     over the cap -> stop planning FURTHER sets for this sender, logged,
//     but NOT stalled — a full day's quota is not a health problem.
//  6. Draw sets_per_day start times uniformly inside
//     [window_start, window_end] in the schedule's own timezone, converted
//     to UTC for storage, spaced at least MinSetSpacing apart — falling
//     back to even spacing (logged) when the window is too narrow to fit
//     that spacing.
//  7. A start time already in the past FOR TODAY is skipped, not fired
//     immediately — a burst of back-dated conversations the moment the
//     planner boots is exactly the automated-looking behaviour this
//     feature must avoid.
//  8. For each remaining set: resolve the template (RootTemplateID if the
//     schedule pins one, else PickOpener(Topic)) and call
//     r.StartOccurrence.
//  9. MarkPlanned(schedule, today).
//  10. When EndDate is today, transition the schedule to finished — AFTER
//     today's own occurrence has been planned, so the last day still runs.
func (p *Planner) PlanDay(ctx context.Context, now time.Time) (int, error) {
	today := now.UTC().Format(dateLayout)
	scheds, err := p.repo.PlannableSchedules(ctx, today, 0)
	if err != nil {
		return 0, err
	}
	started := 0
	for _, sched := range scheds {
		n, err := p.planSchedule(ctx, sched, today, now)
		if err != nil {
			p.log.Error("selfconvo: planner: plan schedule", "schedule", sched.ID, "err", err)
			continue
		}
		started += n
	}
	return started, nil
}

// planSchedule is PlanDay's per-schedule body — see PlanDay's own doc
// comment for the full numbered algorithm this method implements.
func (p *Planner) planSchedule(ctx context.Context, sched convo.Schedule, today string, now time.Time) (int, error) {
	loc, err := time.LoadLocation(sched.Timezone)
	if err != nil {
		reason := fmt.Sprintf("unloadable timezone %q: %v", sched.Timezone, err)
		if serr := p.repo.SetScheduleState(ctx, sched.ID, convo.ScheduleStalled, reason, now); serr != nil &&
			!errors.Is(serr, convo.ErrScheduleFinished) {
			return 0, serr
		}
		p.log.Warn("selfconvo: planner: stalled schedule for an unloadable timezone",
			"schedule", sched.ID, "timezone", sched.Timezone, "err", err)
		return 0, nil
	}

	localNow := now.In(loc)
	if !scheduleFiresOn(sched, localNow) {
		return 0, nil
	}

	alreadyPlanned, err := p.repo.CountRunsForOccurrence(ctx, sched.ID, today)
	if err != nil {
		return 0, err
	}
	if alreadyPlanned > 0 {
		if err := p.repo.MarkPlanned(ctx, sched.ID, today, now); err != nil {
			return 0, err
		}
		return 0, p.finishIfEnded(ctx, sched, today, now)
	}

	receivers, err := p.repo.ScheduleReceivers(ctx, sched.ID)
	if err != nil {
		return 0, err
	}

	ids := make([]string, 0, len(receivers)+1)
	ids = append(ids, sched.SenderSessionID)
	for _, rec := range receivers {
		ids = append(ids, rec.SessionID)
	}
	if sessionID, reason, ok := p.runner.gw.Healthy(ids); !ok {
		stallReason := fmt.Sprintf("session %s: %s", sessionID, reason)
		if err := p.repo.SetScheduleState(ctx, sched.ID, convo.ScheduleStalled, stallReason, now); err != nil &&
			!errors.Is(err, convo.ErrScheduleFinished) {
			return 0, err
		}
		p.log.Warn("selfconvo: planner: stalled schedule (health)", "schedule", sched.ID, "reason", stallReason)
		return 0, nil
	}

	sentToday, err := p.repo.CountSenderTurnsForDate(ctx, sched.SenderSessionID, today)
	if err != nil {
		return 0, err
	}
	if sentToday >= p.maxMsgsPerDay {
		p.log.Warn("selfconvo: planner: sender daily cap reached; not planning further sets",
			"schedule", sched.ID, "sender", sched.SenderSessionID, "sent_today", sentToday, "cap", p.maxMsgsPerDay)
		return 0, nil
	}

	starts, err := p.drawStartTimes(sched, loc, localNow)
	if err != nil {
		return 0, err
	}

	started := 0
	for _, st := range starts {
		if st.Before(now) {
			// A start time already in the past today is skipped, not fired
			// — see PlanDay's own doc comment, step 7.
			p.log.Debug("selfconvo: planner: skipping a past start time",
				"schedule", sched.ID, "start_at", st)
			continue
		}
		templateID := sched.RootTemplateID
		if templateID == "" {
			templateID, err = PickOpener(ctx, p.repo, sched.Topic, p.rng)
			if err != nil {
				p.log.Error("selfconvo: planner: pick opener", "schedule", sched.ID, "topic", sched.Topic, "err", err)
				continue
			}
		}
		runs, err := p.runner.StartOccurrence(ctx, sched, receivers, templateID, today, st)
		if err != nil {
			p.log.Error("selfconvo: planner: start occurrence", "schedule", sched.ID, "err", err)
			continue
		}
		started += len(runs)
	}

	if err := p.repo.MarkPlanned(ctx, sched.ID, today, now); err != nil {
		return started, err
	}
	if err := p.finishIfEnded(ctx, sched, today, now); err != nil {
		return started, err
	}
	return started, nil
}

// finishIfEnded transitions sched to finished when its EndDate is today —
// PlanDay's step 10, called only AFTER today's own occurrence has already
// been planned (or found already planned), so the last day still runs.
func (p *Planner) finishIfEnded(ctx context.Context, sched convo.Schedule, today string, now time.Time) error {
	if sched.EndDate == "" || sched.EndDate != today {
		return nil
	}
	if err := p.repo.SetScheduleState(ctx, sched.ID, convo.ScheduleFinished, "", now); err != nil &&
		!errors.Is(err, convo.ErrScheduleFinished) {
		return err
	}
	return nil
}

// scheduleFiresOn decides PlanDay's step 2: whether sched's recurrence
// matches localNow's OWN date (already resolved to the schedule's
// timezone by the caller).
//
// Weekdays is a comma-separated list of ISO-8601 weekday numbers, Monday=1
// .. Sunday=7 — this package's own convention for the column (see
// schedule_model.go's Weekdays field: it carries no format guarantee of its
// own). A blank or unparseable entry is skipped rather than erroring the
// whole schedule, so a stray trailing comma from a form does not stall an
// otherwise valid weekly schedule.
func scheduleFiresOn(sched convo.Schedule, localNow time.Time) bool {
	switch sched.Recurrence {
	case convo.RecurrenceDaily:
		return true
	case convo.RecurrenceWeekly:
		today := isoWeekday(localNow)
		for _, part := range strings.Split(sched.Weekdays, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			n, err := strconv.Atoi(part)
			if err != nil {
				continue
			}
			if n == today {
				return true
			}
		}
		return false
	case convo.RecurrenceInterval:
		if sched.IntervalDays <= 0 {
			return false
		}
		start, err := time.ParseInLocation(dateLayout, sched.StartDate, localNow.Location())
		if err != nil {
			return false
		}
		days := daysBetween(start, localNow)
		if days < 0 {
			return false
		}
		return days%sched.IntervalDays == 0
	default:
		return false
	}
}

// isoWeekday converts t.Weekday() (Sunday=0..Saturday=6) to ISO-8601
// numbering (Monday=1..Sunday=7) — see scheduleFiresOn's doc comment for
// why this is the convention Weekdays uses.
func isoWeekday(t time.Time) int {
	wd := int(t.Weekday())
	if wd == 0 {
		return 7
	}
	return wd
}

// daysBetween counts whole calendar days from a to b, using each time's OWN
// Y/M/D components reinterpreted as UTC midnight — NOT a.Sub(b) or
// Truncate(24*time.Hour), both of which miscount across a DST transition
// when a/b carry a real (non-UTC) location, since a calendar day is not
// always 24 wall-clock hours in a timezone that observes DST.
func daysBetween(a, b time.Time) int {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	au := time.Date(ay, am, ad, 0, 0, 0, 0, time.UTC)
	bu := time.Date(by, bm, bd, 0, 0, 0, 0, time.UTC)
	return int(bu.Sub(au).Hours() / 24)
}

// clockTime is a parsed "HH:MM" (Schedule.WindowStart/WindowEnd).
type clockTime struct{ hour, min int }

func parseClock(s string) (clockTime, error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return clockTime{}, fmt.Errorf("selfconvo: planner: %q is not HH:MM", s)
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 || h > 23 {
		return clockTime{}, fmt.Errorf("selfconvo: planner: %q is not a valid hour", s)
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return clockTime{}, fmt.Errorf("selfconvo: planner: %q is not a valid minute", s)
	}
	return clockTime{hour: h, min: m}, nil
}

// drawStartTimes is PlanDay's step 6: SetsPerDay start times uniformly
// inside [WindowStart, WindowEnd] on localNow's own date, in sched's own
// timezone (loc), converted to UTC for the caller to compare/store.
//
// The spacing guarantee is built in by construction rather than checked
// and corrected after the fact: n draws are made uniformly inside a
// REDUCED window of size (window - spacing*(n-1)) — exactly the slack left
// over once every set's own minimum gap to the next is reserved — then
// sorted and given back their reserved gap by adding i*spacing to the i-th
// smallest draw. The largest possible result is therefore
// (window-required) + (n-1)*spacing == window, so it can never land past
// the window end, and every consecutive pair is spaced by AT LEAST
// MinSetSpacing (exactly, for whichever pair drew equal reduced values) by
// construction — no shift-then-clamp correction needed, which is important
// because clamping a shift on one end unevenly can silently COMPRESS a gap
// elsewhere (a real bug caught by
// TestMinSetSpacingIsRespectedAcrossADaysSets during development). When
// spacing*(SetsPerDay-1) itself exceeds the window's own span, there is no
// valid reduced window at all — the window is simply too narrow for the
// requested spacing, and this method falls back to perfectly even spacing
// across the window instead (logged, not an error — an operator's own
// too-narrow configuration, not a fault).
func (p *Planner) drawStartTimes(sched convo.Schedule, loc *time.Location, localNow time.Time) ([]time.Time, error) {
	winStart, err := parseClock(sched.WindowStart)
	if err != nil {
		return nil, fmt.Errorf("selfconvo: planner: schedule %s: bad window_start: %w", sched.ID, err)
	}
	winEnd, err := parseClock(sched.WindowEnd)
	if err != nil {
		return nil, fmt.Errorf("selfconvo: planner: schedule %s: bad window_end: %w", sched.ID, err)
	}

	y, m, d := localNow.Date()
	start := time.Date(y, m, d, winStart.hour, winStart.min, 0, 0, loc)
	end := time.Date(y, m, d, winEnd.hour, winEnd.min, 0, 0, loc)
	if !end.After(start) {
		return nil, fmt.Errorf("selfconvo: planner: schedule %s: window_end %q is not after window_start %q",
			sched.ID, sched.WindowEnd, sched.WindowStart)
	}

	n := sched.SetsPerDay
	if n <= 0 {
		n = 1
	}
	window := end.Sub(start)

	var required time.Duration
	if n > 1 {
		required = p.minSetSpacing * time.Duration(n-1)
	}
	if required > window {
		p.log.Warn("selfconvo: planner: window too narrow for min set spacing; falling back to even spacing",
			"schedule", sched.ID, "sets_per_day", n, "window", window, "min_spacing", p.minSetSpacing)
		step := window / time.Duration(n)
		out := make([]time.Time, n)
		for i := 0; i < n; i++ {
			out[i] = start.Add(step * time.Duration(i)).UTC()
		}
		return out, nil
	}

	reducedWindow := window - required
	draws := make([]time.Duration, n)
	for i := range draws {
		draws[i] = time.Duration(p.rng.Int64N(int64(reducedWindow) + 1))
	}
	sort.Slice(draws, func(i, j int) bool { return draws[i] < draws[j] })
	for i := range draws {
		draws[i] += p.minSetSpacing * time.Duration(i)
	}

	out := make([]time.Time, n)
	for i, dd := range draws {
		out[i] = start.Add(dd).UTC()
	}
	return out, nil
}

// receiverIDs extracts SessionID from every receiver — used by both
// planSchedule's health check and Resume's own re-check below.
func receiverIDs(receivers []convo.ScheduleReceiver) []string {
	out := make([]string, len(receivers))
	for i, r := range receivers {
		out[i] = r.SessionID
	}
	return out
}

// Resume returns a paused or stalled schedule to active, clearing the
// stall reason. Refuses a terminal schedule (convo.ErrScheduleFinished) and
// refuses a STALLED schedule whose sessions are STILL unhealthy, returning
// an error that carries the SAME reason the schedule already stalled
// with (sched.PausedReason) rather than a freshly recomputed one — so an
// operator sees exactly why it stalled, not a possibly-different reason
// depending on which session Healthy happens to check first this time.
//
// Resume does NOT back-fill missed days: LastPlannedDate is left exactly
// where it is, so the next matching day plans normally. "Resume" reading as
// "catch up" would produce exactly the burst of back-dated conversations
// this feature exists to avoid — the same reasoning as PlanDay's own
// past-start-time skip (step 7 of PlanDay's doc comment).
func (p *Planner) Resume(ctx context.Context, scheduleID string) error {
	sched, err := p.repo.GetSchedule(ctx, scheduleID)
	if err != nil {
		return err
	}
	if convo.IsScheduleTerminal(sched.State) {
		return convo.ErrScheduleFinished
	}
	if sched.State == convo.ScheduleStalled {
		receivers, err := p.repo.ScheduleReceivers(ctx, scheduleID)
		if err != nil {
			return err
		}
		ids := append([]string{sched.SenderSessionID}, receiverIDs(receivers)...)
		if _, _, ok := p.runner.gw.Healthy(ids); !ok {
			return fmt.Errorf("selfconvo: schedule %s is still unhealthy: %s", scheduleID, sched.PausedReason)
		}
	}
	return p.repo.SetScheduleState(ctx, scheduleID, convo.ScheduleActive, "", p.now())
}

// Pause moves scheduleID to paused (operator-initiated) — resumable via
// Resume, unlike Stop_ below.
func (p *Planner) Pause(ctx context.Context, scheduleID string) error {
	return p.repo.SetScheduleState(ctx, scheduleID, convo.SchedulePaused, "", p.now())
}

// Stop_ moves scheduleID to the terminal `stopped` state. Named with a
// trailing underscore, not Stop, because Stop (no arguments, below) is
// already this Planner's OWN background-loop stop — mirroring the
// Runner.Stop/Runner.Cancel naming split for the exact same reason.
func (p *Planner) Stop_(ctx context.Context, scheduleID string) error {
	return p.repo.SetScheduleState(ctx, scheduleID, convo.ScheduleStopped, "", p.now())
}

// Start spawns PlanDay's background loop — same poll-plus-poke shape as
// Runner.Start/loop/Stop (runner.go), including the SAME
// context.WithCancel(context.WithoutCancel(ctx)) derivation and its exact
// reasoning: only a direct Stop() call (or ctx's own values) should ever end
// this loop, not main.go's SIGTERM ctx firing early during shutdown.
// Idempotent: a second Start call while one is already running is a no-op.
func (p *Planner) Start(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel != nil {
		return
	}
	childCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	p.cancel = cancel
	p.wg.Add(1)
	go p.loop(childCtx)
}

func (p *Planner) loop(ctx context.Context) {
	defer p.wg.Done()
	for {
		if _, err := p.PlanDay(ctx, p.now()); err != nil && ctx.Err() == nil {
			p.log.Error("selfconvo: planner: plan day failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(p.poll):
		case <-p.poke:
		}
	}
}

// Stop cancels the loop's own ctx and waits for it to exit — a no-op if the
// loop isn't running. See Start's doc comment for why this is a SEPARATE
// ctx from the one Start was called with.
func (p *Planner) Stop() {
	p.mu.Lock()
	cancel := p.cancel
	p.cancel = nil
	p.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	p.wg.Wait()
}

// Poke wakes the loop's next PlanDay early — a non-blocking send (poke is
// buffered 1), mirroring Runner.Poke exactly.
func (p *Planner) Poke() {
	select {
	case p.poke <- struct{}{}:
	default:
	}
}
