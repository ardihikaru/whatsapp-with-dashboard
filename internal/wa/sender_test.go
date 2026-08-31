package wa

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func testSession(mgr *Manager, state State) *ManagedSession {
	return &ManagedSession{ID: NewID(), Name: "t", Phone: "628120000",
		mgr: mgr, state: state, events: NewRing(ringSize)}
}

func TestEnqueueNotConnected(t *testing.T) {
	s := testSession(newTestManager(newStubSessions(), newStubMsgs()), StateNew)
	if _, err := s.Enqueue("m1", ToJID("628120001"), "hi"); err != ErrNotConnected {
		t.Fatalf("err = %v, want ErrNotConnected", err)
	}
}

// TestEnqueueAdmitsAConnectedSessionWithNoWhatsmeowClientAttached is the
// regression test for the waxum backend's send path: s.client (a
// *whatsmeow.Client) is never set by backend_waxum.go, so admission must
// gate on s.state alone. Before this phase, Enqueue also required
// s.client != nil, which made every session using the waxum backend
// permanently un-sendable regardless of its real connection state.
func TestEnqueueAdmitsAConnectedSessionWithNoWhatsmeowClientAttached(t *testing.T) {
	s := testSession(newTestManager(newStubSessions(), newStubMsgs()), StateConnected)
	s.sendQ = make(chan sendJob, 1) // s.client left nil — no whatsmeow client
	if _, err := s.Enqueue("m1", ToJID("628120001"), "hi"); err != nil {
		t.Fatalf("Enqueue with no whatsmeow client: err = %v, want nil", err)
	}
}

// TestTrySendRoutesThroughTheBackendWithoutRequiringAWhatsmeowClient is
// trySend's half of the same regression: the send attempt itself must not
// require s.client either, since the waxum backend's SendText never reads
// it — the connected check is s.state, and the transport call goes through
// Backend.SendText regardless of which backend answers.
func TestTrySendRoutesThroughTheBackendWithoutRequiringAWhatsmeowClient(t *testing.T) {
	fake := &fakeBackend{name: "fake-for-test", sendTextID: "wamid.fake"}
	mgr := NewManager(context.Background(), fake, newStubSessions(), newStubMsgs(), nil,
		Config{SendRate: 1, SendBurst: 3, ReconcilePar: 2})
	s := testSession(mgr, StateConnected) // s.client left nil

	res := s.trySend(context.Background(), sendJob{msgID: "m1", to: ToJID("628120001"), text: "hi"})
	if res.Err != nil {
		t.Fatalf("trySend with no whatsmeow client: err = %v, want nil", res.Err)
	}
	if res.WAMessageID != "wamid.fake" {
		t.Fatalf("WAMessageID = %q, want %q (proves the call reached Backend.SendText)", res.WAMessageID, "wamid.fake")
	}
}

// TestEnqueueQueueFullAdmission fills the bounded queue without a running
// worker: admission is non-blocking and the overflow caller gets ErrQueueFull.
func TestEnqueueQueueFullAdmission(t *testing.T) {
	s := testSession(newTestManager(newStubSessions(), newStubMsgs()), StateConnected)
	s.sendQ = make(chan sendJob, 2)
	for i := 0; i < 2; i++ {
		if _, err := s.Enqueue("m", ToJID("628120001"), "hi"); err != nil {
			t.Fatalf("admit %d: %v", i, err)
		}
	}
	if _, err := s.Enqueue("m", ToJID("628120001"), "hi"); err != ErrQueueFull {
		t.Fatalf("overflow err = %v, want ErrQueueFull", err)
	}
}

// TestSenderDrainOnClose: with the session ctx already canceled (delete /
// shutdown path), the worker fails every remaining job fast, persists the
// failures, and still delivers on the buffered done channels.
func TestSenderDrainOnClose(t *testing.T) {
	msgs := newStubMsgs()
	s := testSession(newTestManager(newStubSessions(), msgs), StateConnected)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	q := make(chan sendJob, 10)
	jobs := make([]sendJob, 3)
	for i := range jobs {
		jobs[i] = sendJob{msgID: NewID(), to: ToJID("628120001"), text: "hi",
			done: make(chan SendResult, 1)}
		q <- jobs[i]
	}
	close(q)
	s.senderLoop(ctx, q) // runs to completion: drain-fail every job

	if msgs.failedCount() != 3 {
		t.Fatalf("failed rows = %d, want 3", msgs.failedCount())
	}
	for i, job := range jobs {
		select {
		case res := <-job.done:
			if res.Err == nil {
				t.Fatalf("job %d: err = nil, want context error", i)
			}
		default:
			t.Fatalf("job %d: no result delivered", i)
		}
	}
}

// TestASendFailureIsRecordedInBothEventRings is the "send failures" item on
// the list of places reconnect.go/manager.go's app-wide diagnostic ring is
// fed from (see ManagedSession.ringLevel's doc comment, backend_meow.go):
// finishJob (sender.go) had no ring call at all before this — a repeated
// send failure was visible only via wa_messages (the durable per-message
// audit log, a different thing — see Manager.RecentEvents' doc comment) and
// slog, never in the operator-facing event log this session already keeps
// for everything else (pairing, state transitions, reconnects). This proves
// a failed send reaches BOTH the session's own ring (Snapshot.LastEvents)
// and the manager-wide one (Manager.RecentEvents) at "warn" level.
func TestASendFailureIsRecordedInBothEventRings(t *testing.T) {
	msgs := newStubMsgs()
	mgr := newTestManager(newStubSessions(), msgs)
	s := testSession(mgr, StateConnected)

	job := sendJob{msgID: NewID(), to: ToJID("628120001"), text: "hi", done: make(chan SendResult, 1)}
	s.finishJob(job, SendResult{Err: errors.New("boom"), Attempts: 2})

	events := s.Snapshot().LastEvents
	if len(events) == 0 {
		t.Fatal("send failure recorded no event in the session's own ring")
	}
	last := events[len(events)-1]
	if last.Level != "warn" || !strings.Contains(last.Note, "boom") {
		t.Fatalf("session ring's last event = %+v, want Level=warn and Note containing the send error", last)
	}

	sysEvents := mgr.RecentEvents(1)
	if len(sysEvents) != 1 {
		t.Fatal("send failure did not reach the manager-wide diagnostic ring (Manager.RecentEvents)")
	}
	if sysEvents[0].Level != "warn" || !strings.Contains(sysEvents[0].Note, "boom") {
		t.Fatalf("manager-wide event = %+v, want Level=warn and Note containing the send error", sysEvents[0])
	}
	if sysEvents[0].SessionID != s.ID {
		t.Fatalf("manager-wide event SessionID = %q, want %q", sysEvents[0].SessionID, s.ID)
	}
}
