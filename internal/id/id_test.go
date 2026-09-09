package id

import (
	"strings"
	"testing"
)

// TestNewReturnsThirtyTwoLowercaseHexCharsAndDoesNotRepeat pins New's shape
// against wa.NewID's own TestNewIDShapeAndUniqueness
// (internal/wa/manager_test.go): 16 random bytes hex-encoded is 32 lowercase
// hex characters, and 100 draws must never collide.
func TestNewReturnsThirtyTwoLowercaseHexCharsAndDoesNotRepeat(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		got := New()
		if len(got) != 32 || strings.ToLower(got) != got {
			t.Fatalf("id %q not 32-char lowercase hex", got)
		}
		if seen[got] {
			t.Fatalf("duplicate id %q", got)
		}
		seen[got] = true
	}
}
