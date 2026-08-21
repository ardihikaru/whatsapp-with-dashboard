package wa

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
)

// TestBucketBurstThenRate drives the pure token bucket with a fake clock:
// burst of 3 goes immediately, the 4th waits exactly 1/rate, and a token
// regenerates after that wait.
func TestBucketBurstThenRate(t *testing.T) {
	t0 := time.Unix(1000, 0)
	b := newBucket(1, 3, t0)
	for i := 0; i < 3; i++ {
		if wait := b.take(t0); wait != 0 {
			t.Fatalf("burst take %d: wait = %v, want 0", i, wait)
		}
	}
	wait := b.take(t0)
	if wait != time.Second {
		t.Fatalf("4th take: wait = %v, want 1s", wait)
	}
	if wait := b.take(t0.Add(time.Second)); wait != 0 {
		t.Fatalf("take after refill: wait = %v, want 0", wait)
	}
	// Steady state: sends space out at 1/rate.
	if wait := b.take(t0.Add(time.Second)); wait != time.Second {
		t.Fatalf("steady-state take: wait = %v, want 1s", wait)
	}
}

func TestBucketNeverExceedsBurst(t *testing.T) {
	t0 := time.Unix(1000, 0)
	b := newBucket(1, 3, t0)
	// A long idle period must not accumulate more than burst tokens.
	late := t0.Add(time.Hour)
	for i := 0; i < 3; i++ {
		if wait := b.take(late); wait != 0 {
			t.Fatalf("post-idle take %d: wait = %v, want 0", i, wait)
		}
	}
	if wait := b.take(late); wait == 0 {
		t.Fatal("4th post-idle take granted; burst cap not enforced")
	}
}

func TestBackoffSchedule(t *testing.T) {
	want := map[int]time.Duration{0: time.Second, 1: 2 * time.Second, 2: 4 * time.Second, 10: 30 * time.Second}
	for attempt, d := range want {
		if got := backoffDelay(attempt); got != d {
			t.Errorf("backoffDelay(%d) = %v, want %v", attempt, got, d)
		}
	}
}

func TestIsRetryable(t *testing.T) {
	retryable := []error{context.DeadlineExceeded, context.Canceled,
		ErrNotConnected, whatsmeow.ErrNotConnected}
	for _, err := range retryable {
		if !isRetryable(err) {
			t.Errorf("isRetryable(%v) = false, want true", err)
		}
	}
	permanent := []error{whatsmeow.ErrNotLoggedIn, errors.New("server said no")}
	for _, err := range permanent {
		if isRetryable(err) {
			t.Errorf("isRetryable(%v) = true, want false", err)
		}
	}
}
