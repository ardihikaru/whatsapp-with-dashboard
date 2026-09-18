// planner.go: turning a recurring gb.Schedule into one concrete occurrence
// (the sender's opener turn plus every healthy receiver's reply turn), once
// per matching day. Mirrors internal/selfconvo/planner.go's shape (separate
// background loop from Runner, poll-plus-poke) but NOT its all-or-nothing
// health gate or its sets_per_day multi-draw — see this file's own
// doc comments at each point of deliberate divergence, and the phase plan's
// Confirmed decisions section for the reasoning behind each one.
package groupbroadcast

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/aiclient"
	gb "github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/groupbroadcast"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/id"
)

// defaultPlannerPollInterval mirrors selfconvo's own planner poll — a
// schedule's cadence is measured in days, so there is no pacing reason to
// poll faster than a few minutes.
const defaultPlannerPollInterval = 5 * time.Minute

// defaultMinSessionGap is PlannerOptions.MinSessionGap's fallback — see
// config.DefaultGroupBroadcastMinSessionGap's own doc comment for the
// account-safety reasoning (25s, the confirmed decision #9 default).
const defaultMinSessionGap = 25 * time.Second

// defaultAIDeadline is PlannerOptions.AIDeadline's fallback.
const defaultAIDeadline = 90 * time.Second

// maxGapRedraws bounds how many times drawWithGap redraws a candidate
// before falling back to a forward nudge — see that function's own doc
// comment.
const maxGapRedraws = 20

// dateLayout is the YYYY-MM-DD shape every date column in this schema uses
// — mirrors internal/selfconvo/planner.go's own dateLayout for the same
// lexical-comparison reason (migrations/0008_convo_scheduling.sql's
// header).
const dateLayout = "2006-01-02"

// PlannerOptions configures a Planner. Every field is optional.
type PlannerOptions struct {
	Now          func() time.Time
	Rand         *rand.Rand
	PollInterval time.Duration
	// MinSessionGap is the anti-burst floor between any two sends from the
	// same session on the same date, across every group-broadcast
	// schedule. Defaults to defaultMinSessionGap when <= 0.
	MinSessionGap time.Duration
	// AIDeadline bounds one aiclient.GenerateMessage call during planning.
	// Defaults to defaultAIDeadline when <= 0.
	AIDeadline time.Duration
	// AI is the wording generation client. nil means the planner runs in
	// fallback-only mode (every turn's wording comes from the local
	// template bank) without erroring — see cmd/app/groupbroadcast.go.
	AI     *aiclient.Client
	Logger *slog.Logger
}

// Planner materialises gb.Schedule rows into gb.Occurrence rows (with their
// turns) on a recurring cadence.
type Planner struct {
	repo          *gb.Repository
	gw            Gateway
	ai            *aiclient.Client
	now           func() time.Time
	rng           *rand.Rand
	poll          time.Duration
	minSessionGap time.Duration
	aiDeadline    time.Duration
	log           *slog.Logger

	// rngMu guards every draw from the shared rng field — mirrors
	// internal/selfconvo/runner.go's own rngMu doc comment: PlanDay itself
	// is called sequentially by one loop, but a future concurrent caller
	// (or a test invoking PlanDay from more than one goroutine) must not
	// race on rng directly.
	rngMu sync.Mutex

	poke chan struct{}

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewPlanner builds a Planner over repo/gw.
func NewPlanner(repo *gb.Repository, gw Gateway, opt PlannerOptions) *Planner {
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
	minGap := opt.MinSessionGap
	if minGap <= 0 {
		minGap = defaultMinSessionGap
	}
	aiDeadline := opt.AIDeadline
	if aiDeadline <= 0 {
		aiDeadline = defaultAIDeadline
	}
	log := opt.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Planner{
		repo: repo, gw: gw, ai: opt.AI, now: now, rng: rng, poll: poll,
		minSessionGap: minGap, aiDeadline: aiDeadline, log: log,
		poke: make(chan struct{}, 1),
	}
}

// PlanDay materialises today's occurrence for every schedule
// gb.Repository.PlannableSchedules returns, in order, and returns the
// number of occurrences actually created.
//
// Per schedule:
//  1. Resolve its timezone. Unloadable -> stall THAT schedule, move on.
//  2. Decide whether today (in the schedule's own timezone) matches its
//     recurrence — same daily/weekly/interval rules as
//     internal/selfconvo/planner.go's scheduleFiresOn.
//  3. Idempotency: CountOccurrencesForDate > 0 means today was already
//     planned (normally by an earlier MarkPlanned in this same pass; this
//     branch covers the crash-mid-day case). Mark planned and move on.
//  4. Health check the SENDER ONLY (gw.Healthy([senderID])). Unhealthy ->
//     stall the whole schedule, plan nothing — there is nothing to reply
//     to without an opener. THIS IS THE DELIBERATE DEVIATION from
//     internal/selfconvo's all-or-nothing gate (which also checks every
//     receiver up front): each receiver is instead checked INDIVIDUALLY in
//     step 6 below, and an unhealthy one is skipped alone.
//  5. Draw the sender's opener time uniformly inside
//     [SenderWindowStart, SenderWindowEnd] (single draw). A time already in
//     the past for today is skipped (not fired immediately) — plan nothing
//     for this schedule today, mark planned, move on.
//  6. For each receiver: health-check it ALONE. Unhealthy -> skip it (log
//     Warn naming the session), no turn created for it, but the sender and
//     every OTHER healthy receiver still proceed and the schedule stays
//     active.
//  7. Draw each surviving receiver's reply time via disjoint, non-colliding
//     buckets across [ReceiverWindowStart, ReceiverWindowEnd].
//  8. For every drawn time (sender + each receiver), enforce the minimum
//     same-session gap against every other planned send for that session on
//     that date, across every schedule (drawWithGap).
//  9. Compute is_late per receiver (isLate against LateCutoff, using the
//     FINAL post-gap-adjustment time).
//  10. Generate wording for every turn (generateWording, bounded by
//     p.aiDeadline).
//  11. Persist the occurrence and its turns in one transaction
//     (CreateOccurrence). MarkPlanned. Transition to finished if EndDate is
//     today.
func (p *Planner) PlanDay(ctx context.Context, now time.Time) (int, error) {
	today := now.UTC().Format(dateLayout)
	scheds, err := p.repo.PlannableSchedules(ctx, today, 0)
	if err != nil {
		return 0, err
	}
	created := 0
	for _, sched := range scheds {
		ok, err := p.planSchedule(ctx, sched, today, now)
		if err != nil {
			p.log.Error("groupbroadcast: planner: plan schedule", "schedule", sched.ID, "err", err)
			continue
		}
		if ok {
			created++
		}
	}
	return created, nil
}

func (p *Planner) planSchedule(ctx context.Context, sched gb.Schedule, today string, now time.Time) (bool, error) {
	loc, err := time.LoadLocation(sched.Timezone)
	if err != nil {
		reason := fmt.Sprintf("unloadable timezone %q: %v", sched.Timezone, err)
		if serr := p.repo.SetScheduleState(ctx, sched.ID, gb.ScheduleStalled, reason, now); serr != nil &&
			!errors.Is(serr, gb.ErrScheduleFinished) {
			return false, serr
		}
		p.log.Warn("groupbroadcast: planner: stalled schedule for an unloadable timezone",
			"schedule", sched.ID, "timezone", sched.Timezone, "err", err)
		return false, nil
	}

	localNow := now.In(loc)
	if !scheduleFiresOn(sched, localNow) {
		return false, nil
	}

	alreadyPlanned, err := p.repo.CountOccurrencesForDate(ctx, sched.ID, today)
	if err != nil {
		return false, err
	}
	if alreadyPlanned > 0 {
		if err := p.repo.MarkPlanned(ctx, sched.ID, today, now); err != nil {
			return false, err
		}
		return false, p.finishIfEnded(ctx, sched, today, now)
	}

	// Step 4: sender-only health gate.
	if sessionID, reason, ok := p.gw.Healthy([]string{sched.SenderSessionID}); !ok {
		stallReason := fmt.Sprintf("sender session %s: %s", sessionID, reason)
		if err := p.repo.SetScheduleState(ctx, sched.ID, gb.ScheduleStalled, stallReason, now); err != nil &&
			!errors.Is(err, gb.ErrScheduleFinished) {
			return false, err
		}
		p.log.Warn("groupbroadcast: planner: stalled schedule (sender health)", "schedule", sched.ID, "reason", stallReason)
		return false, nil
	}

	senderWin, err := parseWindow(sched.SenderWindowStart, sched.SenderWindowEnd)
	if err != nil {
		return false, fmt.Errorf("groupbroadcast: planner: schedule %s: %w", sched.ID, err)
	}
	receiverWin, err := parseWindow(sched.ReceiverWindowStart, sched.ReceiverWindowEnd)
	if err != nil {
		return false, fmt.Errorf("groupbroadcast: planner: schedule %s: %w", sched.ID, err)
	}
	lateCutoff, err := parseClock(sched.LateCutoff)
	if err != nil {
		return false, fmt.Errorf("groupbroadcast: planner: schedule %s: bad late_cutoff: %w", sched.ID, err)
	}

	y, m, d := localNow.Date()
	senderStart := localWindowStart(y, m, d, senderWin.start, loc)
	senderEnd := localWindowStart(y, m, d, senderWin.end, loc)
	receiverStart := localWindowStart(y, m, d, receiverWin.start, loc)
	receiverEnd := localWindowStart(y, m, d, receiverWin.end, loc)

	p.rngMu.Lock()
	senderAt := drawUniform(p.rng, senderStart, senderEnd).UTC()
	p.rngMu.Unlock()

	if senderAt.Before(now) {
		// Step 5's own past-time skip — see PlanDay's doc comment. A
		// planner that started late (or is catching up after downtime)
		// must never fire a burst of back-dated occurrences.
		p.log.Debug("groupbroadcast: planner: skipping a past sender start time",
			"schedule", sched.ID, "sender_at", senderAt)
		if err := p.repo.MarkPlanned(ctx, sched.ID, today, now); err != nil {
			return false, err
		}
		return false, p.finishIfEnded(ctx, sched, today, now)
	}

	senderAt, err = p.drawWithGap(ctx, sched.SenderSessionID, today, senderStart.UTC(), senderEnd.UTC(),
		func() time.Time { return drawUniform(p.rng, senderStart, senderEnd).UTC() }, senderAt)
	if err != nil {
		return false, err
	}

	receivers, err := p.repo.ScheduleReceivers(ctx, sched.ID)
	if err != nil {
		return false, err
	}

	// Step 6: per-receiver health gate — the deliberate deviation from
	// selfconvo's all-or-nothing check (see PlanDay's own doc comment).
	var healthy []gb.ScheduleReceiver
	for _, rec := range receivers {
		if _, reason, ok := p.gw.Healthy([]string{rec.SessionID}); !ok {
			p.log.Warn("groupbroadcast: planner: skipping an unhealthy receiver for this occurrence",
				"schedule", sched.ID, "session", rec.SessionID, "reason", reason)
			continue
		}
		healthy = append(healthy, rec)
	}

	receiverTimes := drawReceiverTimes(p.rng, &p.rngMu, len(healthy), receiverStart, receiverEnd)
	for i := range healthy {
		at, err := p.drawWithGap(ctx, healthy[i].SessionID, today, receiverStart.UTC(), receiverEnd.UTC(),
			func() time.Time { return drawReceiverTimes(p.rng, &p.rngMu, 1, receiverStart, receiverEnd)[0] },
			receiverTimes[i].UTC())
		if err != nil {
			return false, err
		}
		receiverTimes[i] = at
	}

	occID := id.New()
	turns := make([]gb.Turn, 0, 1+len(healthy))

	senderLang := drawLanguage(p.rng, sched.LangIDWeight)
	senderWord := p.generateTurnWording(ctx, wordingInput{
		Role: gb.RoleSender, Language: senderLang, GroupName: sched.GroupName, SenderName: sched.SenderName,
	}, sched.ID)
	turns = append(turns, gb.Turn{
		ID: id.New(), OccurrenceID: occID, Role: gb.RoleSender,
		SessionID: sched.SenderSessionID, SessionName: sched.SenderName,
		Language: senderLang, Body: senderWord.Body, Source: senderWord.Source,
		DueAt: senderAt, State: gb.TurnPending,
	})

	for i, rec := range healthy {
		late := isLate(receiverTimes[i], loc, lateCutoff)
		reason := ""
		if late {
			reason = drawLateReason(p.rng)
		}
		lang := drawLanguage(p.rng, sched.LangIDWeight)
		word := p.generateTurnWording(ctx, wordingInput{
			Role: gb.RoleReceiver, Language: lang, IsLate: late, LateReason: reason,
			GroupName: sched.GroupName, SenderName: sched.SenderName,
		}, sched.ID)
		turns = append(turns, gb.Turn{
			ID: id.New(), OccurrenceID: occID, Role: gb.RoleReceiver,
			SessionID: rec.SessionID, SessionName: rec.SessionName,
			Language: lang, IsLate: late, LateReason: reason,
			Body: word.Body, Source: word.Source, DueAt: receiverTimes[i], State: gb.TurnPending,
		})
	}

	occ := &gb.Occurrence{
		ID: occID, ScheduleID: sched.ID, OccurrenceDate: today, GroupJID: sched.GroupJID, State: gb.OccurrenceScheduled,
	}
	if err := p.repo.CreateOccurrence(ctx, occ, turns); err != nil {
		return false, err
	}

	if err := p.repo.MarkPlanned(ctx, sched.ID, today, now); err != nil {
		return true, err
	}
	if err := p.finishIfEnded(ctx, sched, today, now); err != nil {
		return true, err
	}
	return true, nil
}

// generateTurnWording bounds the AI call at p.aiDeadline and falls back
// (logged at Warn) on any failure — the planner is the one place that owns
// the fallback decision (see wording.go's generateWording doc comment).
func (p *Planner) generateTurnWording(ctx context.Context, in wordingInput, scheduleID string) wordingResult {
	callCtx, cancel := context.WithTimeout(ctx, p.aiDeadline)
	defer cancel()
	p.rngMu.Lock()
	seed := p.rng.Uint64()
	p.rngMu.Unlock()
	rng := rand.New(rand.NewPCG(seed, seed+1))
	result := generateWording(callCtx, p.ai, in, rng)
	if result.Source == gb.SourceFallback {
		p.log.Warn("groupbroadcast: planner: AI wording unavailable, used local fallback",
			"schedule", scheduleID, "role", in.Role)
	}
	return result
}

// finishIfEnded transitions sched to finished when its EndDate is today.
func (p *Planner) finishIfEnded(ctx context.Context, sched gb.Schedule, today string, now time.Time) error {
	if sched.EndDate == "" || sched.EndDate != today {
		return nil
	}
	if err := p.repo.SetScheduleState(ctx, sched.ID, gb.ScheduleFinished, "", now); err != nil &&
		!errors.Is(err, gb.ErrScheduleFinished) {
		return err
	}
	return nil
}

// drawWithGap enforces the minimum same-session gap (p.minSessionGap): it
// fetches every OTHER planned due_at for sessionID on date (across every
// group-broadcast schedule) and, if candidate violates the gap against any
// of them, redraws via draw() up to maxGapRedraws times. If still violating
// after that many attempts, it falls back to nudging candidate forward past
// the nearest conflict, clamped to windowEnd — logged at Warn, the same
// "too narrow, fall back and log" philosophy
// internal/selfconvo/planner.go's drawStartTimes already uses for
// MinSetSpacing.
func (p *Planner) drawWithGap(ctx context.Context, sessionID, date string, windowStart, windowEnd time.Time,
	draw func() time.Time, candidate time.Time) (time.Time, error) {
	existing, err := p.repo.SessionTurnTimesForDate(ctx, sessionID, date, "")
	if err != nil {
		return time.Time{}, err
	}
	for i := 0; i < maxGapRedraws && violatesGap(candidate, existing, p.minSessionGap); i++ {
		candidate = draw()
	}
	if violatesGap(candidate, existing, p.minSessionGap) {
		nudged := nudgeAwayFromGap(candidate, existing, p.minSessionGap, windowEnd)
		p.log.Warn("groupbroadcast: planner: window too narrow for the minimum session gap; nudged forward",
			"session", sessionID, "candidate", candidate, "nudged", nudged, "min_gap", p.minSessionGap)
		candidate = nudged
	}
	return candidate, nil
}

func violatesGap(candidate time.Time, existing []time.Time, gap time.Duration) bool {
	for _, e := range existing {
		d := candidate.Sub(e)
		if d < 0 {
			d = -d
		}
		if d < gap {
			return true
		}
	}
	return false
}

// nudgeAwayFromGap repeatedly pushes candidate to gap past whichever
// existing time it is still too close to, until nothing conflicts or
// windowEnd is reached (in which case it is clamped there even if a
// conflict remains — an operator's own too-narrow window configuration,
// not a fault this function can fully resolve).
func nudgeAwayFromGap(candidate time.Time, existing []time.Time, gap time.Duration, windowEnd time.Time) time.Time {
	nudged := candidate
	for changed := true; changed; {
		changed = false
		for _, e := range existing {
			d := nudged.Sub(e)
			if d < 0 {
				d = -d
			}
			if d < gap {
				pushed := e.Add(gap)
				if pushed.After(nudged) {
					nudged = pushed
					changed = true
				}
			}
		}
	}
	if nudged.After(windowEnd) {
		nudged = windowEnd
	}
	return nudged
}

// drawLanguage draws "id" with probability idWeightPercent/100, else "jv" —
// confirmed decision #6 (default 70/30, an internal constant carried per
// schedule as LangIDWeight, never a form field beyond that one number).
func drawLanguage(rng *rand.Rand, idWeightPercent int) string {
	if idWeightPercent <= 0 {
		return gb.LangJV
	}
	if idWeightPercent >= 100 {
		return gb.LangID
	}
	if rng.Float64()*100 < float64(idWeightPercent) {
		return gb.LangID
	}
	return gb.LangJV
}

// drawUniform draws a uniform random instant inside [start, end).
func drawUniform(rng *rand.Rand, start, end time.Time) time.Time {
	span := end.Sub(start)
	if span <= 0 {
		return start
	}
	offset := time.Duration(rng.Int64N(int64(span) + 1))
	return start.Add(offset)
}

// drawReceiverTimes draws n receiver reply times, each inside its OWN
// disjoint bucket of [start, end) — bucket i is
// [start+i*bucket, start+(i+1)*bucket) — mirroring
// internal/selfconvo/runner.go's jitterReceiverStart exactly: this
// guarantees every receiver's own base offset is DISTINCT by construction,
// never merely unlikely to collide. mu guards the shared rng the same way
// Planner.rngMu does elsewhere in this file.
func drawReceiverTimes(rng *rand.Rand, mu *sync.Mutex, n int, start, end time.Time) []time.Time {
	if n <= 0 {
		return nil
	}
	window := end.Sub(start)
	bucket := window / time.Duration(n)
	if bucket <= 0 {
		bucket = time.Nanosecond
	}
	out := make([]time.Time, n)
	for i := 0; i < n; i++ {
		lo := start.Add(bucket * time.Duration(i))
		hi := lo.Add(bucket)
		if hi.After(end) {
			hi = end
		}
		mu.Lock()
		out[i] = drawUniform(rng, lo, hi)
		mu.Unlock()
	}
	return out
}

// clockTime is a parsed "HH:MM".
type clockTime struct{ hour, min int }

func parseClock(s string) (clockTime, error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return clockTime{}, fmt.Errorf("%q is not HH:MM", s)
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 || h > 23 {
		return clockTime{}, fmt.Errorf("%q is not a valid hour", s)
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return clockTime{}, fmt.Errorf("%q is not a valid minute", s)
	}
	return clockTime{hour: h, min: m}, nil
}

type window struct{ start, end clockTime }

func parseWindow(startRaw, endRaw string) (window, error) {
	start, err := parseClock(startRaw)
	if err != nil {
		return window{}, fmt.Errorf("bad window start: %w", err)
	}
	end, err := parseClock(endRaw)
	if err != nil {
		return window{}, fmt.Errorf("bad window end: %w", err)
	}
	if !(end.hour > start.hour || (end.hour == start.hour && end.min > start.min)) {
		return window{}, fmt.Errorf("window end %q is not after window start %q", endRaw, startRaw)
	}
	return window{start: start, end: end}, nil
}

func localWindowStart(y int, m time.Month, d int, c clockTime, loc *time.Location) time.Time {
	return time.Date(y, m, d, c.hour, c.min, 0, 0, loc)
}

// scheduleFiresOn mirrors internal/selfconvo/planner.go's own
// scheduleFiresOn exactly, over gb.Schedule's fields instead of
// convo.Schedule's.
func scheduleFiresOn(sched gb.Schedule, localNow time.Time) bool {
	switch sched.Recurrence {
	case gb.RecurrenceDaily:
		return true
	case gb.RecurrenceWeekly:
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
	case gb.RecurrenceInterval:
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

func isoWeekday(t time.Time) int {
	wd := int(t.Weekday())
	if wd == 0 {
		return 7
	}
	return wd
}

func daysBetween(a, b time.Time) int {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	au := time.Date(ay, am, ad, 0, 0, 0, 0, time.UTC)
	bu := time.Date(by, bm, bd, 0, 0, 0, 0, time.UTC)
	return int(bu.Sub(au).Hours() / 24)
}

// receiverIDs extracts SessionID from every receiver.
func receiverIDs(receivers []gb.ScheduleReceiver) []string {
	out := make([]string, len(receivers))
	for i, r := range receivers {
		out[i] = r.SessionID
	}
	return out
}

// Resume returns a paused or stalled schedule to active — mirrors
// internal/selfconvo/planner.go's Resume exactly, checking only the SENDER
// for the re-health-check (a stalled group-broadcast schedule only ever
// stalled because of the sender — see planSchedule's own doc comment), and
// never back-filling LastPlannedDate.
func (p *Planner) Resume(ctx context.Context, scheduleID string) error {
	sched, err := p.repo.GetSchedule(ctx, scheduleID)
	if err != nil {
		return err
	}
	if gb.IsScheduleTerminal(sched.State) {
		return gb.ErrScheduleFinished
	}
	if sched.State == gb.ScheduleStalled {
		if _, _, ok := p.gw.Healthy([]string{sched.SenderSessionID}); !ok {
			return fmt.Errorf("groupbroadcast: schedule %s is still unhealthy: %s", scheduleID, sched.PausedReason)
		}
	}
	return p.repo.SetScheduleState(ctx, scheduleID, gb.ScheduleActive, "", p.now())
}

// Pause moves scheduleID to paused (operator-initiated).
func (p *Planner) Pause(ctx context.Context, scheduleID string) error {
	return p.repo.SetScheduleState(ctx, scheduleID, gb.SchedulePaused, "", p.now())
}

// Stop_ moves scheduleID to the terminal `stopped` state — named with a
// trailing underscore for the same reason
// internal/selfconvo/planner.go's Stop_ is.
func (p *Planner) Stop_(ctx context.Context, scheduleID string) error {
	return p.repo.SetScheduleState(ctx, scheduleID, gb.ScheduleStopped, "", p.now())
}

// Start spawns PlanDay's background loop — same poll-plus-poke shape as
// internal/selfconvo/planner.go's Start.
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
			p.log.Error("groupbroadcast: planner: plan day failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(p.poll):
		case <-p.poke:
		}
	}
}

// Stop cancels the loop's own ctx and waits for it to exit.
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

// Poke wakes the loop's next PlanDay early.
func (p *Planner) Poke() {
	select {
	case p.poke <- struct{}{}:
	default:
	}
}
