package groupbroadcast

import (
	"testing"
	"time"
)

// TestIsLateBoundaryIsExclusiveOfTheCutoffItself pins the phase plan's
// explicit boundary requirement: a draw landing EXACTLY on the late cutoff
// is NOT late (.After, not .After-or-equal); one second later IS.
func TestIsLateBoundaryIsExclusiveOfTheCutoffItself(t *testing.T) {
	loc := time.UTC
	cutoff := clockTime{hour: 8, min: 30}

	exact := time.Date(2026, 9, 18, 8, 30, 0, 0, loc)
	if isLate(exact, loc, cutoff) {
		t.Error("isLate at exactly the cutoff (08:30:00) = true, want false")
	}

	oneSecondLater := exact.Add(time.Second)
	if !isLate(oneSecondLater, loc, cutoff) {
		t.Error("isLate one second after the cutoff (08:30:01) = false, want true")
	}

	oneSecondEarlier := exact.Add(-time.Second)
	if isLate(oneSecondEarlier, loc, cutoff) {
		t.Error("isLate one second before the cutoff (08:29:59) = true, want false")
	}
}

func TestDrawLateReasonAlwaysReturnsANonEmptyKnownReason(t *testing.T) {
	rng := newDeterministicRand()
	seen := map[string]bool{}
	for k := range lateReasons {
		seen[lateReasons[k]] = true
	}
	for i := 0; i < 50; i++ {
		got := drawLateReason(rng)
		if got == "" {
			t.Fatal("drawLateReason returned an empty string")
		}
		if !seen[got] {
			t.Fatalf("drawLateReason returned %q, not one of the declared lateReasons", got)
		}
	}
}
