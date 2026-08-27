package wa

import (
	"context"
	"strconv"
	"time"
)

const (
	sendQueueCap = 100
	sendTimeout  = 30 * time.Second
	maxAttempts  = 3
)

type SendResult struct {
	WAMessageID string
	Attempts    int
	Err         error
}

type sendJob struct {
	msgID string // wa_messages.id (row already inserted, status=queued)
	to    JID
	text  string
	// media, when non-nil, makes this a media job: trySend calls
	// Backend.SendMedia instead of Backend.SendText (see trySend). The
	// descriptor inside was already uploaded by the HTTP handler BEFORE
	// enqueue — this queue and its retry loop never upload, only send. See
	// EnqueueMedia's doc comment for why.
	media *OutboundMedia
	done  chan SendResult // buffered(1): worker never blocks delivering the result
}

// Enqueue admits a plain-text message to the per-session queue
// (non-blocking). The returned channel yields the terminal result; callers
// may abandon it (202 path) — the worker persists the outcome regardless.
//
// Kept at its own exact signature (transport-agnostic hot path, its own
// tests) rather than growing an optional-media parameter — see EnqueueMedia
// for the media sibling and enqueue for the admission check shared by both.
func (s *ManagedSession) Enqueue(msgID string, to JID, text string) (<-chan SendResult, error) {
	return s.enqueue(sendJob{msgID: msgID, to: to, text: text, done: make(chan SendResult, 1)})
}

// EnqueueMedia is Enqueue's media sibling: m must already carry a descriptor
// from a completed MediaStore.UploadMedia call — this method and the queue
// behind it never upload, only send (see sendJob.media's doc comment and
// backend.go's OutboundMedia doc comment for the full "upload does not ride
// the queue" reasoning). Shares Enqueue's exact admission check via enqueue.
func (s *ManagedSession) EnqueueMedia(msgID string, to JID, m OutboundMedia) (<-chan SendResult, error) {
	return s.enqueue(sendJob{msgID: msgID, to: to, media: &m, done: make(chan SendResult, 1)})
}

// enqueue is Enqueue/EnqueueMedia's shared admission check, factored out so
// the rule lives in exactly one place: s.state alone gates admission, NOT
// s.client != nil. It used to also require a whatsmeow client, but that
// check is backend-specific — s.client stays nil forever under the waxum
// backend (backend_waxum.go's header comment), which would make every
// session using it permanently un-sendable. Both backends only ever move a
// session INTO StateConnected once their own transport is actually up
// (backend_meow.go's onPaired/handleEvent, backend_waxum.go's
// finishPairingSuccess/applyStatus), so state alone is already the
// backend-agnostic signal this check needs. See
// TestEnqueueAdmitsAConnectedSessionWithNoWhatsmeowClientAttached.
func (s *ManagedSession) enqueue(job sendJob) (<-chan SendResult, error) {
	s.mu.Lock()
	q := s.sendQ
	connected := s.state == StateConnected
	s.mu.Unlock()
	if !connected || q == nil {
		return nil, ErrNotConnected
	}
	select {
	case q <- job:
		return job.done, nil
	default:
		return nil, ErrQueueFull // bounded: never let a burst OOM the process
	}
}

// startSenderLocked / stopSenderLocked are the lifecycle hooks (docs/04 §4);
// both are called under s.mu.
func (s *ManagedSession) startSenderLocked() {
	if s.sendQ != nil {
		return
	}
	q := make(chan sendJob, sendQueueCap)
	s.sendQ = q
	ctx := s.ctx
	if ctx == nil {
		ctx = s.mgr.rootCtx
	}
	s.mgr.go1(func() { s.senderLoop(ctx, q) })
}

func (s *ManagedSession) stopSenderLocked() {
	if s.sendQ != nil {
		close(s.sendQ) // loop drains remaining jobs as failed and exits
		s.sendQ = nil
	}
}

// senderLoop is the single worker per session: token bucket pacing, ≤3
// attempts with backoff+jitter on transient errors, terminal persistence.
func (s *ManagedSession) senderLoop(ctx context.Context, q chan sendJob) {
	b := newBucket(s.mgr.cfg.SendRate, s.mgr.cfg.SendBurst, time.Now())
	for job := range q { // exits when stopSenderLocked closes q
		if ctx.Err() != nil { // drain-fail: session tearing down
			s.finishJob(job, SendResult{Err: context.Cause(ctx)})
			continue
		}
		if !s.waitToken(ctx, b) {
			s.finishJob(job, SendResult{Err: context.Cause(ctx)})
			continue
		}
		var res SendResult
		for attempt := 0; attempt < maxAttempts; attempt++ {
			res = s.trySend(ctx, job)
			res.Attempts = attempt + 1
			if res.Err == nil || !isRetryable(res.Err) || ctx.Err() != nil {
				break
			}
			select {
			case <-time.After(backoffDelay(attempt) + jitter()):
			case <-ctx.Done():
			}
		}
		s.finishJob(job, res)
	}
}

// waitToken blocks until the bucket grants a token; false means ctx ended.
func (s *ManagedSession) waitToken(ctx context.Context, b *bucket) bool {
	for {
		wait := b.take(time.Now())
		if wait <= 0 {
			return true
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return false
		}
	}
}

// trySend performs one send attempt with a hard 30s deadline. The transport
// call itself (whatsmeow's cli.SendMessage, or waxum's HTTP POST) lives in
// the backend — this stays the transport-agnostic half: the connected
// admission check and the timeout are the same regardless of which backend
// answers. Like Enqueue, this reads s.state only, not s.client — see
// Enqueue's doc comment for why a whatsmeow-only field cannot gate a
// transport-agnostic check.
//
// job.media == nil selects Backend.SendText; non-nil selects
// Backend.SendMedia with the already-uploaded descriptor. Deliberately the
// ONLY branch point between the two job kinds — every attempt (senderLoop's
// retry loop calls this up to maxAttempts times) re-sends the SAME
// descriptor without re-uploading, since the upload already happened once,
// in the HTTP handler, before this job ever reached the queue. See
// TestAMediaSendIsRetriedWithoutReUploading.
func (s *ManagedSession) trySend(ctx context.Context, job sendJob) SendResult {
	s.mu.Lock()
	connected := s.state == StateConnected
	s.mu.Unlock()
	if !connected {
		return SendResult{Err: ErrNotConnected}
	}
	sctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	var waID string
	var err error
	if job.media == nil {
		waID, err = s.mgr.backend.SendText(sctx, s, job.to, job.text)
	} else {
		waID, err = s.mgr.backend.SendMedia(sctx, s, job.to, *job.media)
	}
	if err != nil {
		return SendResult{Err: err}
	}
	return SendResult{WAMessageID: waID}
}

// finishJob persists the terminal outcome (via persistCtx: must land during
// teardown) and hands the result to a caller that may already be gone.
func (s *ManagedSession) finishJob(job sendJob, res SendResult) {
	ctx := s.mgr.persistCtx
	if res.Attempts == 0 {
		res.Attempts = 1
	}
	if res.Err != nil {
		_ = s.mgr.msgs.MarkFailed(ctx, job.msgID, res.Err.Error(), res.Attempts)
		// wa_messages (msgs.MarkFailed above) is the durable audit log for
		// this send specifically — see message.Repository. This ring entry
		// is the separate, high-frequency diagnostic trail (system log
		// page, ring.go) an operator scans for "what's been going wrong
		// lately" without a per-message query; see
		// TestASendFailureIsRecordedInBothEventRings.
		s.ringLevel("warn", "send failed after "+strconv.Itoa(res.Attempts)+" attempt(s): "+res.Err.Error())
	} else {
		_ = s.mgr.msgs.MarkSent(ctx, job.msgID, res.WAMessageID, res.Attempts)
	}
	job.done <- res // buffered(1); never blocks even if the HTTP caller left (202 path)
}
