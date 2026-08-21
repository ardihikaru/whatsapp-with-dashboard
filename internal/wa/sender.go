package wa

import (
	"context"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
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
	to    types.JID
	text  string
	done  chan SendResult // buffered(1): worker never blocks delivering the result
}

// Enqueue admits a message to the per-session queue (non-blocking). The
// returned channel yields the terminal result; callers may abandon it (202
// path) — the worker persists the outcome regardless.
func (s *ManagedSession) Enqueue(msgID string, to types.JID, text string) (<-chan SendResult, error) {
	s.mu.Lock()
	q := s.sendQ
	connected := s.state == StateConnected && s.client != nil
	s.mu.Unlock()
	if !connected || q == nil {
		return nil, ErrNotConnected
	}
	job := sendJob{msgID: msgID, to: to, text: text, done: make(chan SendResult, 1)}
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

// trySend performs one SendMessage attempt with a hard 30s deadline.
func (s *ManagedSession) trySend(ctx context.Context, job sendJob) SendResult {
	s.mu.Lock()
	cli := s.client
	connected := s.state == StateConnected
	s.mu.Unlock()
	if cli == nil || !connected {
		return SendResult{Err: ErrNotConnected}
	}
	sctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	resp, err := cli.SendMessage(sctx, job.to, &waE2E.Message{
		Conversation: proto.String(job.text), // plain text, \n preserved — MVP message shape
	})
	if err != nil {
		return SendResult{Err: err}
	}
	return SendResult{WAMessageID: string(resp.ID)}
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
	} else {
		_ = s.mgr.msgs.MarkSent(ctx, job.msgID, res.WAMessageID, res.Attempts)
	}
	job.done <- res // buffered(1); never blocks even if the HTTP caller left (202 path)
}
