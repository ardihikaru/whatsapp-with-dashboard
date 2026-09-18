package config

import (
	"strings"
	"testing"
	"time"
)

// TestLoadGroupBroadcastDefaultsToDisabled pins
// DefaultGroupBroadcastEnabled = false — same "automated outbound traffic
// needs explicit opt-in" reasoning as TestLoadSelfConvoDefaultsToDisabled.
func TestLoadGroupBroadcastDefaultsToDisabled(t *testing.T) {
	cfg := &Config{}
	if err := loadGroupBroadcast(cfg); err != nil {
		t.Fatalf("loadGroupBroadcast: %v", err)
	}
	if cfg.GroupBroadcastEnabled {
		t.Error("GroupBroadcastEnabled = true, want false by default")
	}
}

func TestLoadGroupBroadcastParsesTrueAndRejectsAMalformedValue(t *testing.T) {
	t.Run("true", func(t *testing.T) {
		t.Setenv("WSM_GROUP_BROADCAST_ENABLED", "true")
		cfg := &Config{}
		if err := loadGroupBroadcast(cfg); err != nil {
			t.Fatalf("loadGroupBroadcast: %v", err)
		}
		if !cfg.GroupBroadcastEnabled {
			t.Error("GroupBroadcastEnabled = false, want true (overridden)")
		}
	})

	t.Run("garbage value", func(t *testing.T) {
		t.Setenv("WSM_GROUP_BROADCAST_ENABLED", "sure")
		cfg := &Config{}
		err := loadGroupBroadcast(cfg)
		if err == nil {
			t.Fatal("loadGroupBroadcast with WSM_GROUP_BROADCAST_ENABLED=sure: got nil error, want a failure")
		}
		if !strings.Contains(err.Error(), "WSM_GROUP_BROADCAST_ENABLED") {
			t.Fatalf("error = %q, want it to name WSM_GROUP_BROADCAST_ENABLED", err.Error())
		}
	})
}

func TestLoadGroupBroadcastDefaultsMinSessionGapTo25Seconds(t *testing.T) {
	cfg := &Config{}
	if err := loadGroupBroadcast(cfg); err != nil {
		t.Fatalf("loadGroupBroadcast: %v", err)
	}
	if cfg.GroupBroadcastMinSessionGap != 25*time.Second {
		t.Errorf("GroupBroadcastMinSessionGap = %s, want 25s by default", cfg.GroupBroadcastMinSessionGap)
	}
}

func TestLoadGroupBroadcastMinSessionGapParsesAndRejectsInvalidDurations(t *testing.T) {
	t.Run("valid override", func(t *testing.T) {
		t.Setenv("WSM_GROUP_BROADCAST_MIN_SESSION_GAP", "45s")
		cfg := &Config{}
		if err := loadGroupBroadcast(cfg); err != nil {
			t.Fatalf("loadGroupBroadcast: %v", err)
		}
		if cfg.GroupBroadcastMinSessionGap != 45*time.Second {
			t.Errorf("GroupBroadcastMinSessionGap = %s, want 45s", cfg.GroupBroadcastMinSessionGap)
		}
	})

	t.Run("zero rejected", func(t *testing.T) {
		t.Setenv("WSM_GROUP_BROADCAST_MIN_SESSION_GAP", "0s")
		cfg := &Config{}
		err := loadGroupBroadcast(cfg)
		if err == nil {
			t.Fatal("loadGroupBroadcast with a zero gap: got nil error, want a failure")
		}
	})

	t.Run("not a duration", func(t *testing.T) {
		t.Setenv("WSM_GROUP_BROADCAST_MIN_SESSION_GAP", "soon")
		cfg := &Config{}
		err := loadGroupBroadcast(cfg)
		if err == nil {
			t.Fatal("loadGroupBroadcast with an unparseable gap: got nil error, want a failure")
		}
	})
}

func TestLoadGroupBroadcastDefaultsAIDeadlineTo90Seconds(t *testing.T) {
	cfg := &Config{}
	if err := loadGroupBroadcast(cfg); err != nil {
		t.Fatalf("loadGroupBroadcast: %v", err)
	}
	if cfg.GroupBroadcastAIDeadline != 90*time.Second {
		t.Errorf("GroupBroadcastAIDeadline = %s, want 90s by default", cfg.GroupBroadcastAIDeadline)
	}
}
