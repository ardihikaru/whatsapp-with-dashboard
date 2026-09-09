package wa

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
)

// TestDispatchInsertsTheAuditRowBeforeEnqueueingAndReturnsTheTerminalResult
// is Dispatch's happy path: a connected session with its sender started
// gets the audit row inserted (id + category, proving the row exists BEFORE
// the send is even attempted), then the terminal SendResult flows back out
// once the fake backend answers.
func TestDispatchInsertsTheAuditRowBeforeEnqueueingAndReturnsTheTerminalResult(t *testing.T) {
	fake := &fakeBackend{name: "fake", sendTextID: "wamid.dispatch"}
	msgs := newStubMsgs()
	mgr := NewManager(context.Background(), fake, newStubSessions(), msgs, nil,
		Config{SendRate: 100, SendBurst: 5, ReconcilePar: 2})
	s := testSession(mgr, StateConnected)
	s.mu.Lock()
	s.startSenderLocked()
	s.mu.Unlock()
	defer s.stopSenderLocked()

	msg := &message.Message{ID: NewID(), SessionID: s.ID, ToPhone: "628120001",
		Body: "hi", Category: message.CategorySelfConvo}

	result, err := mgr.Dispatch(context.Background(), s, msg, ToJID("628120001"), nil, 5*time.Second)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if result.MsgID != msg.ID {
		t.Fatalf("MsgID = %q, want %q", result.MsgID, msg.ID)
	}
	if result.Result == nil {
		t.Fatal("Result = nil, want the terminal SendResult")
	}
	if result.Result.WAMessageID != "wamid.dispatch" {
		t.Fatalf("WAMessageID = %q, want %q", result.Result.WAMessageID, "wamid.dispatch")
	}

	msgs.mu.Lock()
	defer msgs.mu.Unlock()
	if len(msgs.insertedIDs) != 1 || msgs.insertedIDs[0] != msg.ID {
		t.Fatalf("insertedIDs = %v, want exactly [%q]", msgs.insertedIDs, msg.ID)
	}
	if msgs.insertedCategories[msg.ID] != message.CategorySelfConvo {
		t.Fatalf("insertedCategories[%q] = %q, want %q", msg.ID, msgs.insertedCategories[msg.ID], message.CategorySelfConvo)
	}
	if waID := msgs.sent[msg.ID]; waID != "wamid.dispatch" {
		t.Fatalf("MarkSent recorded waID = %q, want %q", waID, "wamid.dispatch")
	}
}

// TestDispatchMarksTheRowFailedWhenTheSessionIsNotConnected proves the
// enqueue-failure branch: the audit row still gets inserted (so the caller
// has something to look up), Enqueue is never given the chance to run
// (StateNew rejects it at admission), MarkFailed lands with the same
// "session not connected" reason string dispatchSend used and attempts 0
// (this fails before any send attempt is ever made), and the returned error
// is the ErrNotConnected sentinel so a caller can errors.Is on it.
func TestDispatchMarksTheRowFailedWhenTheSessionIsNotConnected(t *testing.T) {
	msgs := newStubMsgs()
	mgr := newTestManager(newStubSessions(), msgs)
	s := testSession(mgr, StateNew) // no sender started, not connected

	msg := &message.Message{ID: NewID(), SessionID: s.ID, ToPhone: "628120001", Body: "hi"}

	result, err := mgr.Dispatch(context.Background(), s, msg, ToJID("628120001"), nil, 5*time.Second)
	if !errors.Is(err, ErrNotConnected) {
		t.Fatalf("err = %v, want ErrNotConnected", err)
	}
	if result.MsgID != msg.ID {
		t.Fatalf("MsgID = %q, want %q (the row was inserted before enqueue failed)", result.MsgID, msg.ID)
	}
	if result.Result != nil {
		t.Fatalf("Result = %+v, want nil", result.Result)
	}

	msgs.mu.Lock()
	defer msgs.mu.Unlock()
	if reason := msgs.failed[msg.ID]; reason != "session not connected" {
		t.Fatalf("MarkFailed reason = %q, want %q", reason, "session not connected")
	}
	if attempts := msgs.failedAttempts[msg.ID]; attempts != 0 {
		t.Fatalf("MarkFailed attempts = %d, want 0", attempts)
	}
	if _, sent := msgs.sent[msg.ID]; sent {
		t.Fatal("MarkSent was recorded, want no send ever attempted")
	}
}

// newDispatchBlockingSession wires a fakeBackend whose SendText blocks until
// release is closed (or the send's own ctx ends), letting dispatch_test.go's
// timing tests control exactly when the sender's terminal result becomes
// available.
func newDispatchBlockingSession(t *testing.T, release chan struct{}) (*Manager, *ManagedSession, *stubMsgs) {
	t.Helper()
	backend := &fakeBackend{name: "blocking", sendTextFn: func(ctx context.Context, _ JID, _ string) (string, error) {
		select {
		case <-release:
			return "wamid.blocked", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}}
	msgs := newStubMsgs()
	mgr := NewManager(context.Background(), backend, newStubSessions(), msgs, nil,
		Config{SendRate: 100, SendBurst: 5, ReconcilePar: 2})
	s := testSession(mgr, StateConnected)
	s.mu.Lock()
	s.startSenderLocked()
	s.mu.Unlock()
	t.Cleanup(s.stopSenderLocked)
	return mgr, s, msgs
}

// TestDispatchWithNoWaitLimitBlocksUntilTheSenderFinishes proves wait == 0
// blocks (no timer branch at all — the plan's "wait == 0 blocks" contract
// for the self-convo runner, which has no 15s HTTP-style cap): Dispatch must
// not return before the backend's SendText is released, and must return the
// terminal result once it is.
func TestDispatchWithNoWaitLimitBlocksUntilTheSenderFinishes(t *testing.T) {
	release := make(chan struct{})
	mgr, s, _ := newDispatchBlockingSession(t, release)

	const delay = 50 * time.Millisecond
	go func() {
		time.Sleep(delay)
		close(release)
	}()

	msg := &message.Message{ID: NewID(), SessionID: s.ID, ToPhone: "628120001", Body: "hi"}
	start := time.Now()
	result, err := mgr.Dispatch(context.Background(), s, msg, ToJID("628120001"), nil, 0)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if elapsed < delay {
		t.Fatalf("Dispatch returned after %v, want at least %v (it must block for the release)", elapsed, delay)
	}
	if result.Result == nil || result.Result.WAMessageID != "wamid.blocked" {
		t.Fatalf("Result = %+v, want the terminal wamid.blocked result", result.Result)
	}
}

// TestDispatchReportsStillQueuedWhenTheWaitLimitElapses proves the
// wait-elapsed branch: Result == nil, err == nil (this is NOT the enqueue
// failure or ctx-cancellation branch — see Dispatch's doc comment on
// telling the three apart). The backend is released afterward so its
// goroutine can finish and the sender's own MarkSent/MarkFailed call does
// not leak past the test.
func TestDispatchReportsStillQueuedWhenTheWaitLimitElapses(t *testing.T) {
	release := make(chan struct{})
	mgr, s, msgs := newDispatchBlockingSession(t, release)

	msg := &message.Message{ID: NewID(), SessionID: s.ID, ToPhone: "628120001", Body: "hi"}
	result, err := mgr.Dispatch(context.Background(), s, msg, ToJID("628120001"), nil, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if result.Result != nil {
		t.Fatalf("Result = %+v, want nil (wait elapsed with no terminal result yet)", result.Result)
	}
	if result.MsgID != msg.ID {
		t.Fatalf("MsgID = %q, want %q", result.MsgID, msg.ID)
	}

	close(release) // let the blocked send finish so the sender goroutine can exit
	deadline := time.After(2 * time.Second)
	for {
		msgs.mu.Lock()
		_, done := msgs.sent[msg.ID]
		msgs.mu.Unlock()
		if done {
			break
		}
		select {
		case <-deadline:
			t.Fatal("sender never finished after release; goroutine leaked")
		case <-time.After(time.Millisecond):
		}
	}
}

// TestDispatchReturnsWhenTheCallerContextEndsBeforeTheSend proves the
// ctx-ended branch (wait == 0, so the only way out besides the terminal
// result is the caller's own ctx): Result == nil and the returned error
// reports the ctx's cause, distinct from ErrQueueFull/ErrNotConnected — see
// Dispatch's doc comment for why that distinction is what lets a caller
// (internal/handler/send.go's dispatchSend) tell "still queued, wait
// elapsed" apart from "still queued, caller went away" apart from a real
// enqueue failure.
func TestDispatchReturnsWhenTheCallerContextEndsBeforeTheSend(t *testing.T) {
	release := make(chan struct{})
	mgr, s, _ := newDispatchBlockingSession(t, release)
	t.Cleanup(func() { close(release) }) // unblock the sender so it can exit

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	msg := &message.Message{ID: NewID(), SessionID: s.ID, ToPhone: "628120001", Body: "hi"}
	result, err := mgr.Dispatch(ctx, s, msg, ToJID("628120001"), nil, 0)
	if result.Result != nil {
		t.Fatalf("Result = %+v, want nil", result.Result)
	}
	if err == nil {
		t.Fatal("err = nil, want the ctx's cause")
	}
	if errors.Is(err, ErrQueueFull) || errors.Is(err, ErrNotConnected) {
		t.Fatalf("err = %v, want the ctx cause, not an enqueue sentinel", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled (via context.Cause)", err)
	}
}
