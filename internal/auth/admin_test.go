package auth

import (
	"errors"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// bcrypt.MinCost, not DefaultCost: at the default every row of this table costs
// ~80ms and the file becomes the slowest test in the repo for no added coverage.
func testHash(t *testing.T, pw string) []byte {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestAdminCredentialVerify(t *testing.T) {
	cred := AdminCredential{Username: "admin", PasswordHash: testHash(t, "correct horse")}

	tests := []struct {
		name     string
		cred     AdminCredential
		user     string
		password string
		wantErr  bool
	}{
		{"right user and password", cred, "admin", "correct horse", false},
		{"surrounding whitespace is trimmed", cred, "  admin  ", "correct horse", false},
		{"right user, wrong password", cred, "admin", "wrong", true},
		{"wrong user, right password", cred, "root", "correct horse", true},
		{"both wrong", cred, "root", "wrong", true},
		{"both empty", cred, "", "", true},
		// The load-bearing case: a Deps assembled without a credential must be a
		// closed door, not an open one.
		{"zero value rejects everything", AdminCredential{}, "", "", true},
		{"zero value rejects a plausible guess", AdminCredential{}, "admin", "admin", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cred.Verify(tc.user, tc.password)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidCredentials) {
					t.Fatalf("Verify() = %v, want ErrInvalidCredentials", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Verify() = %v, want nil", err)
			}
		})
	}
}

func TestThrottleAllowsBurstThenWaits(t *testing.T) {
	// 5 attempts up front, then one more every 10s.
	th := NewThrottle(0.1, 5)
	now := time.Unix(0, 0)

	for i := range 5 {
		if wait := th.Take("10.0.0.1", now); wait != 0 {
			t.Fatalf("attempt %d was throttled (wait %v); the burst must pass", i+1, wait)
		}
	}
	wait := th.Take("10.0.0.1", now)
	if wait <= 0 {
		t.Fatal("the 6th attempt in the burst window was allowed; the throttle is not throttling")
	}

	// A different IP has its own bucket — one noisy client must not lock out the
	// operator.
	if w := th.Take("10.0.0.2", now); w != 0 {
		t.Fatalf("a second IP was throttled (wait %v); buckets must be per key", w)
	}

	// Refill: after the reported wait, one more attempt is available.
	if w := th.Take("10.0.0.1", now.Add(wait)); w != 0 {
		t.Fatalf("still throttled %v after waiting the reported %v", w, wait)
	}
}

func TestThrottleResetClearsTheBucket(t *testing.T) {
	th := NewThrottle(0.1, 2)
	now := time.Unix(0, 0)
	th.Take("10.0.0.1", now)
	th.Take("10.0.0.1", now)
	if th.Take("10.0.0.1", now) == 0 {
		t.Fatal("expected the bucket to be empty")
	}
	// A fumbled password must not follow the operator around after they get it
	// right.
	th.Reset("10.0.0.1")
	if w := th.Take("10.0.0.1", now); w != 0 {
		t.Fatalf("Take after Reset was throttled (wait %v)", w)
	}
}

// A nil Throttle is the "not configured" case every existing handler test hits.
func TestNilThrottleNeverThrottles(t *testing.T) {
	var th *Throttle
	for range 100 {
		if w := th.Take("10.0.0.1", time.Unix(0, 0)); w != 0 {
			t.Fatalf("nil Throttle throttled (wait %v)", w)
		}
	}
	th.Reset("10.0.0.1") // must not panic
}
