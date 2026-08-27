package wa

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testOutboundMedia is a representative media job: an already-uploaded
// descriptor, as EnqueueMedia's doc comment requires — the same shape a
// Phase 6 handler would build after calling MediaStore.UploadMedia.
func testOutboundMedia() OutboundMedia {
	return OutboundMedia{
		Kind: MediaImage,
		Descriptor: MediaDescriptor{
			URL:           "https://mmg.whatsapp.net/x",
			DirectPath:    "/x",
			MediaKey:      "key-bytes",
			FileSHA256:    "sha",
			FileEncSHA256: "esha",
			FileLength:    1024,
			MediaType:     "image",
			Mimetype:      "image/jpeg",
		},
		Caption: "hello",
	}
}

// TestEnqueueMediaIsRejectedWhenTheSessionIsNotConnected mirrors
// TestEnqueueNotConnected (sender_test.go) for the media path: admission is
// s.state alone, shared with Enqueue via the unexported enqueue helper.
func TestEnqueueMediaIsRejectedWhenTheSessionIsNotConnected(t *testing.T) {
	s := testSession(newTestManager(newStubSessions(), newStubMsgs()), StateNew)
	if _, err := s.EnqueueMedia("m1", ToJID("628120001"), testOutboundMedia()); err != ErrNotConnected {
		t.Fatalf("err = %v, want ErrNotConnected", err)
	}
}

// TestEnqueueMediaIsRejectedWhenTheQueueIsFull mirrors
// TestEnqueueQueueFullAdmission for the media path.
func TestEnqueueMediaIsRejectedWhenTheQueueIsFull(t *testing.T) {
	s := testSession(newTestManager(newStubSessions(), newStubMsgs()), StateConnected)
	s.sendQ = make(chan sendJob, 1)
	if _, err := s.EnqueueMedia("m1", ToJID("628120001"), testOutboundMedia()); err != nil {
		t.Fatalf("first EnqueueMedia: %v", err)
	}
	if _, err := s.EnqueueMedia("m2", ToJID("628120001"), testOutboundMedia()); err != ErrQueueFull {
		t.Fatalf("overflow err = %v, want ErrQueueFull", err)
	}
}

// TestAMediaJobCarriesItsDescriptorToTheBackend proves trySend's media
// branch reaches Backend.SendMedia with the EXACT descriptor EnqueueMedia
// was given — not a text send, not a mutated copy.
func TestAMediaJobCarriesItsDescriptorToTheBackend(t *testing.T) {
	fake := &fakeBackend{name: "fake", sendMediaID: "wamid.media"}
	mgr := NewManager(context.Background(), fake, newStubSessions(), newStubMsgs(),
		Config{SendRate: 100, SendBurst: 5, ReconcilePar: 2})
	s := testSession(mgr, StateConnected)
	s.startSenderLocked()
	defer s.stopSenderLocked()

	m := testOutboundMedia()
	ch, err := s.EnqueueMedia("m1", ToJID("628120001"), m)
	if err != nil {
		t.Fatal(err)
	}
	res := <-ch
	if res.Err != nil {
		t.Fatalf("send err = %v, want nil", res.Err)
	}
	if res.WAMessageID != "wamid.media" {
		t.Fatalf("WAMessageID = %q, want %q", res.WAMessageID, "wamid.media")
	}

	fake.mediaMu.Lock()
	defer fake.mediaMu.Unlock()
	if fake.sendMediaCalls != 1 {
		t.Fatalf("SendMedia called %d times, want 1", fake.sendMediaCalls)
	}
	if len(fake.sentMedia) != 1 || fake.sentMedia[0] != m {
		t.Fatalf("sentMedia = %+v, want exactly the descriptor EnqueueMedia was given (%+v)", fake.sentMedia, m)
	}
}

// TestAFailedMediaSendIsMarkedFailedInWaMessagesWithItsError proves
// finishJob (sender.go) is reused unchanged for a media job: the only
// MarkSent/MarkFailed call sites in this package are still finishJob's, so a
// failed media send must land in the same place a failed text send does.
func TestAFailedMediaSendIsMarkedFailedInWaMessagesWithItsError(t *testing.T) {
	msgs := newStubMsgs()
	fake := &fakeBackend{name: "fake"} // sendMediaID left empty: SendMedia answers ErrUnsupported
	mgr := NewManager(context.Background(), fake, newStubSessions(), msgs,
		Config{SendRate: 100, SendBurst: 5, ReconcilePar: 2})
	s := testSession(mgr, StateConnected)
	s.startSenderLocked()
	defer s.stopSenderLocked()

	ch, err := s.EnqueueMedia("m1", ToJID("628120001"), testOutboundMedia())
	if err != nil {
		t.Fatal(err)
	}
	res := <-ch
	if res.Err == nil {
		t.Fatal("send err = nil, want ErrUnsupported (fake has no sendMediaID configured)")
	}

	msgs.mu.Lock()
	failedMsg, ok := msgs.failed["m1"]
	msgs.mu.Unlock()
	if !ok {
		t.Fatal("wa_messages was not marked failed for the media job (finishJob not reused?)")
	}
	if !strings.Contains(failedMsg, "not supported") {
		t.Fatalf("recorded error = %q, want it to contain the backend's ErrUnsupported message", failedMsg)
	}
}

// TestSendMediaGoesThroughTheSameTokenBucketAsText is the most important
// test in this phase: it proves media is NOT a rate-limit bypass. With a
// bucket of rate=1/s, burst=1, the second of two back-to-back media sends
// must wait for a fresh token — exactly as a second TEXT send would — rather
// than a parallel media path draining the queue at an unbounded rate. See
// sender_limits.go's bucket and sender.go's senderLoop, both of which this
// exercises completely unchanged for the media job.
func TestSendMediaGoesThroughTheSameTokenBucketAsText(t *testing.T) {
	fake := &fakeBackend{name: "fake", sendMediaID: "wamid.media"}
	mgr := NewManager(context.Background(), fake, newStubSessions(), newStubMsgs(),
		Config{SendRate: 1, SendBurst: 1, ReconcilePar: 2})
	s := testSession(mgr, StateConnected)
	s.startSenderLocked()
	defer s.stopSenderLocked()

	m := testOutboundMedia()
	start := time.Now()
	ch1, err := s.EnqueueMedia("m1", ToJID("628120001"), m)
	if err != nil {
		t.Fatal(err)
	}
	ch2, err := s.EnqueueMedia("m2", ToJID("628120001"), m)
	if err != nil {
		t.Fatal(err)
	}

	res1 := <-ch1
	res2 := <-ch2
	if res1.Err != nil || res2.Err != nil {
		t.Fatalf("send errs = %v, %v, want nil, nil", res1.Err, res2.Err)
	}
	elapsed := time.Since(start)
	// burst=1 grants the first send its token immediately; at rate=1/s the
	// second must wait ~1s for the bucket to refill. A generous 900ms floor
	// (not the full 1s) keeps this robust to scheduler jitter while still
	// failing hard if media sends bypassed the bucket (which would finish
	// in low single-digit milliseconds).
	if elapsed < 900*time.Millisecond {
		t.Fatalf("two media sends completed in %v with rate=1/s burst=1; want the second throttled by ~1s, proving media shares the same token bucket as text", elapsed)
	}
}

// TestAMediaSendIsRetriedWithoutReUploading pins the phase's central design
// decision (sender.go's trySend doc comment): the job carries a descriptor,
// so three trySend attempts must call Backend.SendMedia three times with
// that SAME descriptor — and this package's Backend interface has no
// upload method at all for trySend to call even if it wanted to (uploading
// is MediaStore.UploadMedia, a wholly separate capability interface reached
// only from the Phase 6 HTTP handler, BEFORE a job is ever enqueued). A
// fakeBackend satisfying only Backend, with no upload counter to check,
// makes that structurally true rather than merely asserted.
func TestAMediaSendIsRetriedWithoutReUploading(t *testing.T) {
	msgs := newStubMsgs()
	var calls int32
	fake := &fakeBackend{name: "fake", sendMediaID: "wamid.media"}
	fake.sendMediaFn = func(_ JID, _ OutboundMedia) error {
		if atomic.AddInt32(&calls, 1) < 3 {
			return ErrNotConnected // retryable per isRetryable (sender_limits.go)
		}
		return nil // third attempt succeeds
	}
	mgr := NewManager(context.Background(), fake, newStubSessions(), msgs,
		Config{SendRate: 100, SendBurst: 5, ReconcilePar: 2})
	s := testSession(mgr, StateConnected)
	s.startSenderLocked()
	defer s.stopSenderLocked()

	ch, err := s.EnqueueMedia("m1", ToJID("628120001"), testOutboundMedia())
	if err != nil {
		t.Fatal(err)
	}
	res := <-ch
	if res.Err != nil {
		t.Fatalf("final result err = %v, want nil (third attempt succeeds)", res.Err)
	}
	if res.Attempts != 3 {
		t.Fatalf("Attempts = %d, want 3", res.Attempts)
	}

	fake.mediaMu.Lock()
	sendMediaCalls := fake.sendMediaCalls
	sent := append([]OutboundMedia(nil), fake.sentMedia...)
	fake.mediaMu.Unlock()
	if sendMediaCalls != 3 {
		t.Fatalf("SendMedia called %d times, want 3 (one per attempt, no re-upload in between)", sendMediaCalls)
	}
	for i, m := range sent {
		if m.Descriptor.MediaKey != "key-bytes" {
			t.Fatalf("attempt %d sent a different descriptor (%+v) than the one enqueued", i, m)
		}
	}
}
