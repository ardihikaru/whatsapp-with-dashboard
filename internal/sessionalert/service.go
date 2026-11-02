// Package sessionalert runs two operator-configurable alerts over
// WhatsApp, modelled on internal/statussched/scheduler.go's one-loop,
// injected-clock shape:
//
//   - A LOGOUT ALERT fires when a session transitions to logged_out
//     (internal/wa's ManagedSession.setState, fanned out on
//     Manager.StateChanges()). Plain "disconnected" never alerts — the
//     hysteresis pre-states and socket blips that produce it would make
//     this noisy (see CLAUDE.md's logged_out hysteresis incidents).
//     Changes are collected for about BatchWindow and sent as ONE
//     message (six sessions flipped within two minutes on 2026-09-30),
//     and a per-session Cooldown suppresses repeats. A session that goes
//     from logged_out back to connected gets an optional "recovered"
//     line in the next batch.
//   - A DAILY REPORT lists every session that is not connected —
//     including ones deactivated on purpose, marked as such — once per
//     local day at a configured time. The date is claimed BEFORE sending
//     (internal/domain/sessionalert.Repository.ClaimDailyDate), so a slow
//     send or a crash mid-post cannot double it, and a slot missed by
//     more than MissedSlotGrace is skipped, never back-filled (the same
//     discipline internal/statussched.MaxLateness enforces).
//
// Both alerts choose a sender in the same order: the configured
// sender_session_id, else the session flagged is_self_reminder, else any
// connected session — and never a session that is itself part of what is
// being reported (see chooseSender).
//
// Clock and the Gateway are injected; tests never sleep and never need a
// live session (see Gateway's doc comment).
package sessionalert

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	sa "github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/sessionalert"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/schedclock"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// Gateway is the service's only route to WhatsApp — an interface so tests
// never need a live session, mirroring internal/statussched.Gateway.
type Gateway interface {
	// Snapshots returns every session's current reporting-relevant fields.
	Snapshots() []Snapshot
	// Send posts text to groupJID through session sessionID, stamped with
	// message.CategorySessionAlert by the implementation.
	Send(ctx context.Context, sessionID, groupJID, text string) error
	// MembershipChecker backs chooseSender's final fallback tier — see
	// that function's doc comment. The production implementation
	// (gateway_wa.go) MUST cache this per session for a sensible TTL: it
	// is reachable only from the fallback path, but a poorly-cached
	// implementation could still feed waxum's tiny rate-limit burst
	// (CLAUDE.md) on every Tick while several sessions are logged out at
	// once.
	MembershipChecker
	// RecordEvent writes a manager-level system-log entry (no single
	// session in scope) — used when no eligible sender exists at all, so
	// that failure is visible on the system log page, not just in slog.
	RecordEvent(level, note string)
}

// Snapshot is the reporting-relevant slice of wa.Snapshot this package
// needs — its own type (not wa.Snapshot directly), the same reasoning
// statussched.Gateway's Post/ContactJIDs methods exist instead of handing
// the scheduler a live *wa.Manager: Gateway stays the one seam a test
// fakes.
type Snapshot struct {
	ID, Name, Phone string
	Connected       bool
	// Active mirrors wa.Snapshot.Active — operator intent. false means
	// deliberately deactivated, not a connection problem; the daily
	// report marks these "(deactivated)" rather than as an outage.
	Active        bool
	State         string
	StalledReason string
	SelfReminder  bool
}

// Options configures a Service. Zero values pick production defaults.
type Options struct {
	Now          func() time.Time
	PollInterval time.Duration
	Logger       *slog.Logger
	// BatchWindow/Cooldown tune the logout-alert collector (Tick's doc
	// comment). Zero picks DefaultBatchWindow/DefaultCooldown.
	BatchWindow time.Duration
	Cooldown    time.Duration
	// MissedSlotGrace bounds how late a daily report may still go out.
	// Zero picks DefaultMissedSlotGrace.
	MissedSlotGrace time.Duration
}

const (
	// DefaultPollInterval mirrors statussched.DefaultPollInterval's own
	// reasoning: frequent enough that both alerts land promptly, cheap
	// enough to poll an empty settings row forever when both are off.
	DefaultPollInterval = 30 * time.Second
	// DefaultBatchWindow is how long the logout-alert collector waits
	// before sending — see the package doc comment's 2026-09-30 incident.
	DefaultBatchWindow = 30 * time.Second
	// DefaultCooldown suppresses a repeat alert for the SAME session.
	DefaultCooldown = 30 * time.Minute
	// DefaultMissedSlotGrace mirrors statussched.MaxLateness.
	DefaultMissedSlotGrace = 10 * time.Minute
)

// pendingEntry is one collected state change awaiting the next batch
// flush — kind is derived once at collection time so flush never has to
// re-derive it from raw wa.State values.
type pendingEntry struct {
	ID, Name, Phone string
	Kind            string // "logged_out" | "recovered"
	At              time.Time
}

const (
	kindLoggedOut = "logged_out"
	kindRecovered = "recovered"
)

// Service is the alert runner.
type Service struct {
	settings sa.Repository
	gw       Gateway
	changes  <-chan wa.StateChange
	now      func() time.Time
	poll     time.Duration
	batch    time.Duration
	cooldown time.Duration
	grace    time.Duration
	log      *slog.Logger

	mu            sync.Mutex
	pending       []pendingEntry
	batchOpenedAt time.Time
	cooldownUntil map[string]time.Time

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New builds a Service. changes is normally manager.StateChanges().
func New(settings sa.Repository, gw Gateway, changes <-chan wa.StateChange, opt Options) *Service {
	s := &Service{
		settings: settings, gw: gw, changes: changes,
		now: opt.Now, poll: opt.PollInterval, batch: opt.BatchWindow,
		cooldown: opt.Cooldown, grace: opt.MissedSlotGrace, log: opt.Logger,
		cooldownUntil: map[string]time.Time{},
	}
	if s.now == nil {
		s.now = func() time.Time { return time.Now().UTC() }
	}
	if s.poll <= 0 {
		s.poll = DefaultPollInterval
	}
	if s.batch <= 0 {
		s.batch = DefaultBatchWindow
	}
	if s.cooldown <= 0 {
		s.cooldown = DefaultCooldown
	}
	if s.grace <= 0 {
		s.grace = DefaultMissedSlotGrace
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	return s
}

// Tick runs one pass: drain any pending state changes, flush the logout
// batch if its window has elapsed, and check the daily report. Errors
// from either half are logged and do not stop the other — matching
// statussched.Tick's "never let one bad schedule stop every other one"
// posture, scaled down to this package's two independent concerns.
func (s *Service) Tick(ctx context.Context) error {
	now := s.now()
	settings, err := s.settings.Get(ctx)
	if err != nil {
		return fmt.Errorf("sessionalert: load settings: %w", err)
	}
	s.drainChanges(now)
	if err := s.maybeFlushBatch(ctx, now, settings); err != nil {
		s.log.Error("sessionalert: flush logout batch", "err", err)
	}
	if err := s.maybeDailyReport(ctx, now, settings); err != nil {
		s.log.Error("sessionalert: daily report", "err", err)
	}
	return nil
}

// drainChanges empties s.changes (non-blocking) into the pending batch,
// applying the per-session cooldown — a session inside its own cooldown
// window is silently skipped, not merely delayed to the next batch.
func (s *Service) drainChanges(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		select {
		case c, ok := <-s.changes:
			if !ok {
				return
			}
			var kind string
			switch {
			case c.To == wa.StateLoggedOut:
				kind = kindLoggedOut
			case c.From == wa.StateLoggedOut && c.To == wa.StateConnected:
				kind = kindRecovered
			default:
				continue // not an edge this package alerts on
			}
			if until, suppressed := s.cooldownUntil[c.ID]; suppressed && now.Before(until) {
				continue
			}
			if len(s.pending) == 0 {
				s.batchOpenedAt = now
			}
			s.pending = append(s.pending, pendingEntry{ID: c.ID, Name: c.Name, Phone: c.Phone, Kind: kind, At: c.At})
			s.cooldownUntil[c.ID] = now.Add(s.cooldown)
		default:
			return
		}
	}
}

// maybeFlushBatch sends (or, if the feature is disabled, simply discards)
// the pending batch once BatchWindow has elapsed since it opened.
func (s *Service) maybeFlushBatch(ctx context.Context, now time.Time, settings sa.Settings) error {
	s.mu.Lock()
	if len(s.pending) == 0 || now.Sub(s.batchOpenedAt) < s.batch {
		s.mu.Unlock()
		return nil
	}
	batch := s.pending
	s.pending = nil
	s.batchOpenedAt = time.Time{}
	s.mu.Unlock()

	if !settings.LogoutAlertEnabled {
		return nil
	}
	if settings.TargetGroupJID == "" {
		s.log.Warn("sessionalert: logout alert enabled but no target group configured; dropping batch", "count", len(batch))
		return nil
	}
	excluded := map[string]bool{}
	for _, e := range batch {
		excluded[e.ID] = true
	}
	sender, ok := chooseSender(ctx, s.gw, s.gw.Snapshots(), settings.SenderSessionID, settings.TargetGroupJID, excluded)
	if !ok {
		s.noEligibleSender("logout alert", len(batch))
		return nil
	}
	return s.gw.Send(ctx, sender.ID, settings.TargetGroupJID, buildLogoutAlertText(batch))
}

// maybeDailyReport claims and sends the daily report once per local date,
// at or after the configured time, skipping (not back-filling) a slot
// missed by more than s.grace.
func (s *Service) maybeDailyReport(ctx context.Context, now time.Time, settings sa.Settings) error {
	if !settings.DailyReportEnabled {
		return nil
	}
	loc, err := time.LoadLocation(settings.Timezone)
	if err != nil {
		return fmt.Errorf("unknown timezone %q: %w", settings.Timezone, err)
	}
	clock, err := schedclock.ParseClock(settings.DailyTime)
	if err != nil {
		return fmt.Errorf("invalid daily_time %q: %w", settings.DailyTime, err)
	}
	today := schedclock.LocalDate(now, loc)
	if today == settings.LastDailyDate {
		return nil
	}
	y, m, d := now.In(loc).Date()
	slot := time.Date(y, m, d, clock.Hour, clock.Min, 0, 0, loc)
	if now.Before(slot) {
		return nil // not due yet today
	}
	claimed, err := s.settings.ClaimDailyDate(ctx, today)
	if err != nil {
		return err
	}
	if !claimed {
		return nil // another tick/process already claimed today
	}
	if late := now.Sub(slot); late > s.grace {
		s.log.Warn("sessionalert: daily report slot missed; skipping, not back-filled", "late", late.Round(time.Minute))
		return nil
	}
	return s.sendDailyReport(ctx, settings)
}

func (s *Service) sendDailyReport(ctx context.Context, settings sa.Settings) error {
	if settings.TargetGroupJID == "" {
		s.log.Warn("sessionalert: daily report due but no target group configured")
		return nil
	}
	snaps := s.gw.Snapshots()
	var notConnected []Snapshot
	for _, sn := range snaps {
		if !sn.Connected {
			notConnected = append(notConnected, sn)
		}
	}
	excluded := map[string]bool{}
	for _, sn := range notConnected {
		excluded[sn.ID] = true
	}
	sender, ok := chooseSender(ctx, s.gw, snaps, settings.SenderSessionID, settings.TargetGroupJID, excluded)
	if !ok {
		s.noEligibleSender("daily report", 0)
		return nil
	}
	return s.gw.Send(ctx, sender.ID, settings.TargetGroupJID, buildDailyReportText(notConnected, len(snaps)))
}

// noEligibleSenderMsg names the real failure mode root-caused live
// 2026-11-02 (CLAUDE.md): the self-reminder session itself had logged
// out, and the old unconditional "any connected session" fallback picked
// one that had never joined the target group — waxum answered 403
// forbidden and the alert was lost. "No connected session" alone would
// have been misleading (other sessions WERE connected); the group
// membership requirement is the actual cause an operator needs to see.
const noEligibleSenderMsg = "no connected session is a member of the target group"

// noEligibleSender logs AND records a system-log event (RecordEvent) for
// the no-eligible-sender case — unlike every other warning in this file,
// this one means the alert could not be sent AT ALL, which is worth
// surfacing on the system log page, not only in container logs.
func (s *Service) noEligibleSender(what string, batchSize int) {
	args := []any{"kind", what}
	if batchSize > 0 {
		args = append(args, "count", batchSize)
	}
	s.log.Warn("sessionalert: "+noEligibleSenderMsg, args...)
	s.gw.RecordEvent("warn", fmt.Sprintf("sessionalert: %s not sent — %s", what, noEligibleSenderMsg))
}

// SendTestResult names which session attempted a test send — surfaced by
// the /settings/alerts "Send test message" button even when the send
// itself failed, so an operator sees exactly what a real alert would have
// done, not just a bare error.
type SendTestResult struct {
	SenderID, SenderName string
}

// SendTest posts a short test message using the SAME sender-selection
// order both real alerts use, so a successful "Send test message" click
// on /settings/alerts is real evidence the configured group/sender
// actually works. The returned SendTestResult names the chosen sender
// even when the error is non-nil (a Send failure, as opposed to no
// eligible sender at all), so the caller can always say which session was
// tried.
func (s *Service) SendTest(ctx context.Context) (SendTestResult, error) {
	settings, err := s.settings.Get(ctx)
	if err != nil {
		return SendTestResult{}, err
	}
	if settings.TargetGroupJID == "" {
		return SendTestResult{}, errors.New("no target group configured")
	}
	sender, ok := chooseSender(ctx, s.gw, s.gw.Snapshots(), settings.SenderSessionID, settings.TargetGroupJID, nil)
	if !ok {
		s.noEligibleSender("test message", 0)
		return SendTestResult{}, errors.New(noEligibleSenderMsg)
	}
	result := SendTestResult{SenderID: sender.ID, SenderName: sender.Name}
	if err := s.gw.Send(ctx, sender.ID, settings.TargetGroupJID, "✅ WSM session-alert test message"); err != nil {
		return result, fmt.Errorf("sent from %s, but the send failed: %w", sender.Name, err)
	}
	return result, nil
}

// Start spawns the loop on a context detached from ctx's cancellation —
// same shape as every other runner in this codebase (statussched,
// selfconvo, groupbroadcast): only Stop ends it.
func (s *Service) Start(ctx context.Context) {
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
				s.log.Error("sessionalert: tick failed", "err", err)
			}
			select {
			case <-child.Done():
				return
			case <-time.After(s.poll):
			}
		}
	}()
}

// Stop ends the loop and waits for it to exit.
func (s *Service) Stop() {
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
