package wa

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/waxum"
)

// The reconciler exists because of 2026-09-30: four sessions with intact
// credentials were committed to a terminal logged_out, waxum later said
// logged_in for three of them, WSM said connected:0, and NOTHING anywhere
// compared the two views or told anyone — it was found by a human two days
// later. This file is that comparison.
//
// It is deliberately a READER. The backend's own pollers (statusPump) already
// fetch every session's /status on their cadence; they leave their last
// result in a pollObservation and the reconciler compares those against
// WSM's in-memory state. It makes ZERO requests of its own, so it cannot
// feed waxum's tiny rate-limit burst (CLAUDE.md's rate-limit hazard), and it
// runs on its own goroutine, off every hot path.
//
// What it does NOT cover, on purpose: "sessions waxum knows that WSM does
// not". That needs waxum's list-sessions call, whose response shape has not
// been confirmed against a live waxum anywhere in this repo (no client method
// exists, and CLAUDE.md only records its status code). Inventing a decoder
// for an unverified schema would produce false orphan alarms; the check
// belongs here once the shape is captured from a real response.

// BackendObservation is one session's last-known poll outcome, as a backend
// reports it to the reconciler. Value type: nothing aliases backend state.
type BackendObservation struct {
	SessionID   string
	Polled      bool // at least one poll has ever succeeded
	Status      string
	LoggedIn    bool
	SocketAlive bool
	PolledAt    time.Time

	// FailingSince is when the CURRENT run of failed polls began (zero when
	// the last poll succeeded). RateLimited / NotFound classify the latest
	// failure (429 after the client's own retries / 404).
	FailingSince time.Time
	RateLimited  bool
	NotFound     bool
}

// observingBackend is the optional Backend extension the reconciler needs;
// only waxumBackend implements it (whatsmeow has no second process to
// disagree with).
type observingBackend interface {
	Observations() []BackendObservation
}

// sessionObserver is the optional hook StartAll uses to give a restored
// logged_out session a status poller. See waxumBackend.Observe.
type sessionObserver interface {
	Observe(s *ManagedSession)
}

// pollObservation is the per-session record statusPump writes and
// Observations reads. Mutex-guarded: two goroutines, no hot path.
type pollObservation struct {
	mu           sync.Mutex
	polled       bool
	status       string
	loggedIn     bool
	socketAlive  bool
	polledAt     time.Time
	failingSince time.Time
	rateLimited  bool
	notFound     bool
}

func (o *pollObservation) recordOK(st *waxum.Status, at time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.polled, o.status, o.loggedIn, o.socketAlive, o.polledAt = true, st.Status, st.IsLoggedIn, st.SocketAlive, at
	o.failingSince, o.rateLimited, o.notFound = time.Time{}, false, false
}

func (o *pollObservation) recordFailure(err error, at time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.failingSince.IsZero() {
		o.failingSince = at
	}
	o.rateLimited = errors.Is(err, waxum.ErrRateLimited)
	o.notFound = errors.Is(err, waxum.ErrNotFound)
}

// Observations snapshots every session this backend has polled. No I/O.
func (b *waxumBackend) Observations() []BackendObservation {
	var out []BackendObservation
	b.sessions.Range(func(k, v any) bool {
		o := &v.(*waxumSessionState).obs
		o.mu.Lock()
		out = append(out, BackendObservation{
			SessionID: k.(string), Polled: o.polled, Status: o.status,
			LoggedIn: o.loggedIn, SocketAlive: o.socketAlive, PolledAt: o.polledAt,
			FailingSince: o.failingSince, RateLimited: o.rateLimited, NotFound: o.notFound,
		})
		o.mu.Unlock()
		return true
	})
	return out
}

// Reconcile mismatch kinds.
const (
	MismatchLoggedOutVsLoggedIn = "logged_out_vs_logged_in" // WSM terminal logged_out, waxum holds credentials
	MismatchConnectedVsDown     = "connected_vs_down"       // WSM connected, waxum says otherwise
	MismatchDisconnectedVsUp    = "disconnected_vs_up"      // WSM wants it active+down, waxum says connected
	MismatchUnknownToWaxum      = "unknown_to_waxum"        // waxum answers 404 for a session WSM holds
)

// Mismatch is one session whose WSM state and waxum status have disagreed for
// longer than the grace period.
type Mismatch struct {
	SessionID string
	Name      string
	Kind      string
	WSM       string
	Waxum     string
	Since     time.Time
}

// BackendProblem is "waxum has not answered usefully for longer than the
// grace period": unreachable, timing out, or answering 429 (the 2026-09-13
// and 2026-09-30 suspend/resume rate-limiter wedge, which nothing else
// surfaces — waxum never logs a 429).
type BackendProblem struct {
	Since       time.Time
	RateLimited bool
	Sessions    int // how many sessions' polls are failing
}

// ReconcileReport is the latest reconciler verdict.
type ReconcileReport struct {
	Enabled        bool
	OK             bool
	CheckedAt      time.Time // zero until the first pass
	Mismatches     []Mismatch
	BackendProblem *BackendProblem
}

const defaultReconcileGrace = 5 * time.Minute

type reconcileState struct {
	// manualMu serialises ReconcileNow and guards lastManual: concurrent
	// clicks queue behind one another and the later ones hit the cooldown.
	manualMu   sync.Mutex
	lastManual time.Time

	mu        sync.Mutex
	report    ReconcileReport
	firstSeen map[string]time.Time // mismatch key -> when first observed
	alerted   map[string]bool      // keys already announced
}

// Reconcile returns a copy of the latest verdict. Enabled is false when no
// reconciler is running (no observing backend, or interval unset) so a
// caller can tell "all clear" from "nobody is checking".
func (m *Manager) Reconcile() ReconcileReport {
	m.recon.mu.Lock()
	defer m.recon.mu.Unlock()
	r := m.recon.report
	r.Mismatches = append([]Mismatch(nil), r.Mismatches...)
	if r.BackendProblem != nil {
		bp := *r.BackendProblem
		r.BackendProblem = &bp
	}
	return r
}

// manualReconcileCooldown is the minimum gap between operator-triggered
// passes. A pass reads data the pollers already hold, so it costs waxum
// nothing today — the cooldown is the contract that keeps it that way: if a
// pass ever grows a backend call (e.g. waxum's list-sessions, see the file
// comment), rapid clicks still cannot turn into a request burst against its
// tiny rate-limit allowance.
const manualReconcileCooldown = 10 * time.Second

// ReconcileNow runs one comparison pass on an operator's request and returns
// the fresh verdict with ran=true — unless a manual pass already ran within
// manualReconcileCooldown, in which case nothing is recomputed and the
// latest stored verdict comes back with ran=false (callers say "checked
// moments ago"). Safe to call from concurrent HTTP handlers.
func (m *Manager) ReconcileNow(now time.Time) (rep ReconcileReport, ran bool) {
	m.recon.manualMu.Lock()
	defer m.recon.manualMu.Unlock()
	if last := m.recon.lastManual; !last.IsZero() && now.Sub(last) < manualReconcileCooldown {
		return m.Reconcile(), false
	}
	m.recon.lastManual = now
	m.reconcileOnce(now)
	return m.Reconcile(), true
}

func (m *Manager) reconcileLoop(ctx context.Context, interval time.Duration) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
		m.reconcileOnce(time.Now())
	}
}

// reconcileOnce is one comparison pass; now is injected so tests drive the
// grace period without sleeping.
func (m *Manager) reconcileOnce(now time.Time) {
	ob, ok := m.backend.(observingBackend)
	if !ok {
		return
	}
	grace := m.cfg.ReconcileGrace
	if grace <= 0 {
		grace = defaultReconcileGrace
	}

	type cand struct {
		key string
		mm  *Mismatch
		bp  *BackendProblem
	}
	var cands []cand
	failing, rateLimited := 0, false
	var failingSince time.Time

	for _, o := range ob.Observations() {
		s, ok := m.Get(o.SessionID)
		if !ok {
			continue
		}
		s.mu.Lock()
		wsm, active, paired := s.state, s.active, !s.jid.IsEmpty()
		s.mu.Unlock()
		add := func(kind, waxumView string) {
			cands = append(cands, cand{key: o.SessionID + "|" + kind, mm: &Mismatch{
				SessionID: o.SessionID, Name: s.Name(), Kind: kind, WSM: string(wsm), Waxum: waxumView}})
		}
		if !o.FailingSince.IsZero() {
			if o.NotFound {
				// 404 is "waxum does not know this session", not "waxum is
				// down" — and for a never-paired session it is simply normal.
				if paired {
					add(MismatchUnknownToWaxum, "session not found (404)")
				}
				continue
			}
			failing++
			rateLimited = rateLimited || o.RateLimited
			if failingSince.IsZero() || o.FailingSince.Before(failingSince) {
				failingSince = o.FailingSince
			}
			continue
		}
		if !o.Polled || !paired {
			continue // a never-paired session reporting waiting_for_qr is a pairing, not a mismatch
		}
		up := o.LoggedIn && o.SocketAlive
		view := fmt.Sprintf("%s logged_in=%t socket_alive=%t", o.Status, o.LoggedIn, o.SocketAlive)
		switch {
		case wsm == StateLoggedOut && o.LoggedIn:
			add(MismatchLoggedOutVsLoggedIn, view)
		case wsm == StateConnected && !up:
			add(MismatchConnectedVsDown, view)
		case (wsm == StateDisconnected) && active && up:
			add(MismatchDisconnectedVsUp, view)
		}
	}
	if failing > 0 {
		cands = append(cands, cand{key: "backend", bp: &BackendProblem{
			Since: failingSince, RateLimited: rateLimited, Sessions: failing}})
	}

	rs := &m.recon
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.firstSeen == nil {
		rs.firstSeen, rs.alerted = map[string]time.Time{}, map[string]bool{}
	}
	seen := map[string]bool{}
	var mismatches []Mismatch
	var problem *BackendProblem
	for _, c := range cands {
		seen[c.key] = true
		first, had := rs.firstSeen[c.key]
		if !had {
			first = now
			if c.bp != nil {
				first = c.bp.Since // the outage began when polls started failing, not when we noticed
			}
			rs.firstSeen[c.key] = first
		}
		if now.Sub(first) < grace {
			continue
		}
		if c.mm != nil {
			c.mm.Since = first
			mismatches = append(mismatches, *c.mm)
		} else {
			c.bp.Since = first
			problem = c.bp
		}
		if !rs.alerted[c.key] {
			rs.alerted[c.key] = true
			m.announceReconcile(c.mm, c.bp, now.Sub(first))
		}
	}
	for k := range rs.firstSeen {
		if seen[k] {
			continue
		}
		if rs.alerted[k] {
			slog.Info("wa: reconcile: discrepancy cleared", "key", k)
			m.RecordEvent("info", "reconcile: discrepancy cleared: "+k)
		}
		delete(rs.firstSeen, k)
		delete(rs.alerted, k)
	}
	sort.Slice(mismatches, func(i, j int) bool {
		if mismatches[i].SessionID != mismatches[j].SessionID {
			return mismatches[i].SessionID < mismatches[j].SessionID
		}
		return mismatches[i].Kind < mismatches[j].Kind
	})
	rs.report = ReconcileReport{Enabled: true, OK: len(mismatches) == 0 && problem == nil,
		CheckedAt: now, Mismatches: mismatches, BackendProblem: problem}
}

// announceReconcile writes the one slog.Warn + system-log event per new
// discrepancy (not per pass — a standing mismatch must not spam the 500-slot
// ring). This reconciler still has no outbound notification channel of its
// own — slog + system log + /api/v1/health + the /sessions banner remain
// the whole surface for a WSM-vs-backend DISAGREEMENT. That is now a
// narrower claim than it used to be: internal/sessionalert DOES post a
// real WhatsApp-group message, but only for a session's own committed
// state transition to logged_out (ManagedSession.setState's
// publishStateChange) and a daily non-connected roundup — neither of
// which this reconciler's mismatch/BackendProblem events feed. A
// standing WSM-vs-waxum disagreement (this function's own subject) still
// reaches no operator outside the four channels above.
func (m *Manager) announceReconcile(mm *Mismatch, bp *BackendProblem, age time.Duration) {
	var note string
	if mm != nil {
		note = fmt.Sprintf("reconcile: session %s (%s) WSM=%s but waxum=%s [%s] for %s",
			mm.SessionID, mm.Name, mm.WSM, mm.Waxum, mm.Kind, age.Round(time.Second))
	} else {
		cause := "unreachable or failing"
		if bp.RateLimited {
			cause = "answering 429 Too Many Requests (rate limiter wedge — restart the waxum process, never down -v)"
		}
		note = fmt.Sprintf("reconcile: waxum %s for %s across %d session poll(s)", cause, age.Round(time.Second), bp.Sessions)
	}
	slog.Warn("wa: " + note)
	m.RecordEvent("warn", note)
}
