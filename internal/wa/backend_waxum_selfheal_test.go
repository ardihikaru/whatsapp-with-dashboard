package wa

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/wasession"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/waxum"
)

// eventMentions reports whether any app-wide system-log event contains every
// one of parts.
func eventMentions(m *Manager, parts ...string) bool {
	for _, ev := range m.RecentEvents(0) {
		ok := true
		for _, p := range parts {
			if !strings.Contains(ev.Note, p) {
				ok = false
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// TestALoggedOutSessionWaxumReportsLoggedInIsAdoptedBackToConnected is the
// 2026-09-30 incident's recovery rule: logged_out is terminal only until
// waxum contradicts it. is_logged_in && socket_alive for a session WSM holds
// as logged_out must flip it to connected, persist that, re-assert the
// operator's active intent, restart the sender (so sends work, not just the
// badge) and name the discrepancy in the system log.
func TestALoggedOutSessionWaxumReportsLoggedInIsAdoptedBackToConnected(t *testing.T) {
	repo := newStubSessions()
	b := &waxumBackend{}
	mgr := newWaxumTestManager(t, b)
	mgr.repo = repo

	s := testSession(mgr, StateLoggedOut)
	s.jid = pairedTestJID()
	s.active = false

	b.applyStatus(s, &waxum.Status{Status: "logged_in", IsLoggedIn: true, SocketAlive: true})

	if got := s.Snapshot().State; got != StateConnected {
		t.Fatalf("state = %v, want %v", got, StateConnected)
	}
	if got := repo.lastStatus(s.ID); got != string(StateConnected) {
		t.Errorf("persisted last_status = %q, want %q", got, StateConnected)
	}
	s.mu.Lock()
	active, hasQ := s.active, s.sendQ != nil
	s.mu.Unlock()
	if !active {
		t.Error("session not marked active after adoption")
	}
	repo.mu.Lock()
	acts := repo.active[s.ID]
	repo.mu.Unlock()
	if len(acts) == 0 || !acts[len(acts)-1] {
		t.Errorf("SetActive history = %v, want a trailing true", acts)
	}
	if !hasQ {
		t.Error("sender loop not started: the badge would say connected while every send fails")
	}
	if !eventMentions(mgr, s.ID, "logged_out", "adopt") {
		t.Errorf("no system-log event naming session %s and the logged_out discrepancy; events=%v", s.ID, mgr.RecentEvents(0))
	}
}

// A session that was never logged_out must NOT produce an adoption event on
// an ordinary connected poll (the event is the discrepancy, not the state).
func TestAnOrdinaryConnectedPollRecordsNoAdoptionEvent(t *testing.T) {
	b := &waxumBackend{}
	mgr := newWaxumTestManager(t, b)
	s := testSession(mgr, StateDisconnected)
	s.jid = pairedTestJID()
	b.applyStatus(s, &waxum.Status{Status: "logged_in", IsLoggedIn: true, SocketAlive: true})
	if eventMentions(mgr, "adopt") {
		t.Fatalf("unexpected adoption event: %v", mgr.RecentEvents(0))
	}
}

// Hysteresis for the OTHER direction is untouched: a connected poll must not
// be needed 3 times — one is enough to leave logged_out — but a logged_out
// verdict still needs the full streak (see the existing hysteresis tests).
// What matters here is that a single contrary poll between qualifying ones
// resets the streak, so adoption cannot be followed by an instant relapse.
func TestAdoptionResetsTheLoggedOutStreakSoOneBadPollCannotRelapse(t *testing.T) {
	b := &waxumBackend{}
	mgr := newWaxumTestManager(t, b)
	s := testSession(mgr, StateConnected)
	s.jid = pairedTestJID()
	bad := &waxum.Status{Status: "waiting_for_qr"}
	for i := 0; i < loggedOutHysteresisPolls; i++ {
		b.applyStatus(s, bad)
	}
	b.applyStatus(s, &waxum.Status{Status: "logged_in", IsLoggedIn: true, SocketAlive: true})
	b.applyStatus(s, bad)
	if got := s.Snapshot().State; got == StateLoggedOut {
		t.Fatalf("one qualifying poll right after adoption re-declared logged_out")
	}
}

// TestBootStartsObservingALoggedOutSoWaxumCanContradictIt is the half that
// makes the recovery reachable after a restart: StartAll used to restore
// logged_out from the row and never connect, so no status poller existed for
// the session and nothing could ever contradict the row.
func TestBootStartsObservingALoggedOutSoWaxumCanContradictIt(t *testing.T) {
	client, _ := newWaxumFakeServer(t, waxumFakeOpts{
		statusFn: func(int) waxum.Status {
			return waxum.Status{Status: "logged_in", IsLoggedIn: true, SocketAlive: true}
		},
	})
	b := &waxumBackend{client: client, pollInterval: 2 * time.Millisecond}
	repo := newStubSessions(wasession.Session{
		ID: "lo-1", Name: "ardi laptop", Phone: "6281200000001",
		JID: pairedTestJID().String(), LastStatus: string(StateLoggedOut), Active: true,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mgr := NewManager(ctx, b, repo, newStubMsgs(), nil, Config{SendRate: 1, SendBurst: 3, ReconcilePar: 2})
	if err := mgr.StartAll(ctx); err != nil {
		t.Fatal(err)
	}
	s, _ := mgr.Get("lo-1")
	waitForCondition(t, "logged_out row adopted as connected after boot", func() bool {
		return s.Snapshot().State == StateConnected
	})
	if got := repo.lastStatus("lo-1"); got != string(StateConnected) {
		t.Errorf("persisted last_status = %q, want connected", got)
	}
}
