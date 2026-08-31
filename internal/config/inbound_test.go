package config

import (
	"testing"
	"time"
)

// TestLoadInboundDefaultsToEnabledWithSixtySecondPollAndTwentyFourHourWindow
// pins the three defaults loadInbound resolves to when nothing is set —
// the ingest-side counterpart of GET /api/v1/inbound working out of the box
// (docs/brief-inbound-api.md), not requiring an operator to opt in the way
// WSM_CALLS_ENABLED does.
func TestLoadInboundDefaultsToEnabledWithSixtySecondPollAndTwentyFourHourWindow(t *testing.T) {
	cfg := &Config{}
	if err := loadInbound(cfg); err != nil {
		t.Fatalf("loadInbound: %v", err)
	}
	if !cfg.InboundEnabled {
		t.Error("InboundEnabled = false, want true by default")
	}
	if cfg.InboundPollInterval != 60*time.Second {
		t.Errorf("InboundPollInterval = %v, want 60s", cfg.InboundPollInterval)
	}
	if cfg.InboundStampWindow != 24*time.Hour {
		t.Errorf("InboundStampWindow = %v, want 24h", cfg.InboundStampWindow)
	}
}

func TestLoadInboundParsesOverridesAndRejectsMalformedValues(t *testing.T) {
	t.Run("overrides all three", func(t *testing.T) {
		t.Setenv("WSM_INBOUND_ENABLED", "false")
		t.Setenv("WSM_INBOUND_POLL_INTERVAL", "15s")
		t.Setenv("WSM_INBOUND_STAMP_WINDOW", "4h")
		cfg := &Config{}
		if err := loadInbound(cfg); err != nil {
			t.Fatalf("loadInbound: %v", err)
		}
		if cfg.InboundEnabled {
			t.Error("InboundEnabled = true, want false (overridden)")
		}
		if cfg.InboundPollInterval != 15*time.Second {
			t.Errorf("InboundPollInterval = %v, want 15s", cfg.InboundPollInterval)
		}
		if cfg.InboundStampWindow != 4*time.Hour {
			t.Errorf("InboundStampWindow = %v, want 4h", cfg.InboundStampWindow)
		}
	})

	t.Run("rejects a non-boolean WSM_INBOUND_ENABLED", func(t *testing.T) {
		t.Setenv("WSM_INBOUND_ENABLED", "sort-of")
		cfg := &Config{}
		if err := loadInbound(cfg); err == nil {
			t.Fatal("loadInbound with WSM_INBOUND_ENABLED=sort-of: got nil error, want a failure")
		}
	})

	t.Run("rejects a malformed WSM_INBOUND_POLL_INTERVAL", func(t *testing.T) {
		t.Setenv("WSM_INBOUND_POLL_INTERVAL", "not-a-duration")
		cfg := &Config{}
		if err := loadInbound(cfg); err == nil {
			t.Fatal("loadInbound with WSM_INBOUND_POLL_INTERVAL=not-a-duration: got nil error, want a failure")
		}
	})

	t.Run("rejects a zero WSM_INBOUND_POLL_INTERVAL", func(t *testing.T) {
		t.Setenv("WSM_INBOUND_POLL_INTERVAL", "0s")
		cfg := &Config{}
		if err := loadInbound(cfg); err == nil {
			t.Fatal("loadInbound with WSM_INBOUND_POLL_INTERVAL=0s: got nil error, want a failure " +
				"(a zero poll interval would spin the pump in a tight loop)")
		}
	})

	t.Run("rejects a malformed WSM_INBOUND_STAMP_WINDOW", func(t *testing.T) {
		t.Setenv("WSM_INBOUND_STAMP_WINDOW", "not-a-duration")
		cfg := &Config{}
		if err := loadInbound(cfg); err == nil {
			t.Fatal("loadInbound with WSM_INBOUND_STAMP_WINDOW=not-a-duration: got nil error, want a failure")
		}
	})
}
