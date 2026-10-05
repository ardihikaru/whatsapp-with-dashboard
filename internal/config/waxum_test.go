package config

import (
	"testing"
	"time"
)

func TestWABackendDefaultsToWaxumAndRejectsUnknownValues(t *testing.T) {
	t.Run("defaults to waxum when unset, and needs WSM_WAXUM_URL/TOKEN because of it", func(t *testing.T) {
		cfg := &Config{}
		err := loadWaxum(cfg)
		// The default now requires the waxum env, same as if someone had set
		// WSM_WA_BACKEND=waxum explicitly — this is the "consequence you must
		// handle" from flipping the default: an environment that sets neither
		// WSM_WAXUM_URL nor WSM_WAXUM_TOKEN now fails to boot.
		if err == nil {
			t.Fatal("loadWaxum with WSM_WA_BACKEND unset: got nil error, want a failure " +
				"naming the missing waxum env — the default backend is now waxum")
		}
		if cfg.WABackend != "waxum" {
			t.Errorf("WABackend = %q, want %q even though loadWaxum then failed on the missing URL/token",
				cfg.WABackend, "waxum")
		}
	})

	t.Run("boots clean on the default when the waxum env is present too", func(t *testing.T) {
		t.Setenv("WSM_WAXUM_URL", "http://127.0.0.1:9000")
		t.Setenv("WSM_WAXUM_TOKEN", "test-token")
		cfg := &Config{}
		if err := loadWaxum(cfg); err != nil {
			t.Fatalf("loadWaxum: %v", err)
		}
		if cfg.WABackend != "waxum" {
			t.Errorf("WABackend = %q, want %q", cfg.WABackend, "waxum")
		}
	})

	t.Run("whatsmeow remains selectable as the rollback switch", func(t *testing.T) {
		t.Setenv("WSM_WA_BACKEND", "whatsmeow")
		cfg := &Config{}
		if err := loadWaxum(cfg); err != nil {
			t.Fatalf("loadWaxum: %v", err)
		}
		if cfg.WABackend != "whatsmeow" {
			t.Errorf("WABackend = %q, want %q", cfg.WABackend, "whatsmeow")
		}
	})

	t.Run("rejects an unknown backend name", func(t *testing.T) {
		t.Setenv("WSM_WA_BACKEND", "grpc-thing")
		cfg := &Config{}
		if err := loadWaxum(cfg); err == nil {
			t.Fatal("loadWaxum with WSM_WA_BACKEND=grpc-thing: got nil error, want a failure")
		}
	})
}

func TestWaxumURLAndTokenAreRequiredOnlyWhenTheWaxumBackendIsSelected(t *testing.T) {
	t.Run("waxum backend with no URL fails, naming the variable", func(t *testing.T) {
		t.Setenv("WSM_WA_BACKEND", "waxum")
		cfg := &Config{}
		err := loadWaxum(cfg)
		if err == nil {
			t.Fatal("loadWaxum with backend=waxum and no WSM_WAXUM_URL: got nil error, want a failure")
		}
		if got := err.Error(); got != "WSM_WAXUM_URL is required when WSM_WA_BACKEND=waxum" {
			t.Errorf("error = %q, want it to name WSM_WAXUM_URL", got)
		}
	})

	t.Run("waxum backend with URL but no token fails, naming the variable", func(t *testing.T) {
		t.Setenv("WSM_WA_BACKEND", "waxum")
		t.Setenv("WSM_WAXUM_URL", "http://127.0.0.1:9000")
		cfg := &Config{}
		err := loadWaxum(cfg)
		if err == nil {
			t.Fatal("loadWaxum with backend=waxum and no WSM_WAXUM_TOKEN: got nil error, want a failure")
		}
		if got := err.Error(); got != "WSM_WAXUM_TOKEN is required when WSM_WA_BACKEND=waxum" {
			t.Errorf("error = %q, want it to name WSM_WAXUM_TOKEN", got)
		}
	})

	t.Run("whatsmeow backend boots clean with nothing waxum-related set", func(t *testing.T) {
		// Explicit now that the default flipped to waxum — this subtest is
		// about the whatsmeow branch specifically, not about what happens
		// when WSM_WA_BACKEND is left unset.
		t.Setenv("WSM_WA_BACKEND", "whatsmeow")
		cfg := &Config{}
		if err := loadWaxum(cfg); err != nil {
			t.Fatalf("loadWaxum with backend=whatsmeow and no waxum env: %v", err)
		}
		if cfg.WaxumURL != "" {
			t.Errorf("WaxumURL = %q, want empty when backend is whatsmeow", cfg.WaxumURL)
		}
		if cfg.WaxumToken != "" {
			t.Errorf("WaxumToken = %q, want empty when backend is whatsmeow", cfg.WaxumToken)
		}
	})

	t.Run("malformed WSM_WAXUM_URL is rejected even though it is only required for waxum", func(t *testing.T) {
		t.Setenv("WSM_WA_BACKEND", "waxum")
		t.Setenv("WSM_WAXUM_URL", "not-a-url")
		t.Setenv("WSM_WAXUM_TOKEN", "test-token")
		cfg := &Config{}
		if err := loadWaxum(cfg); err == nil {
			t.Fatal("loadWaxum with a malformed WSM_WAXUM_URL: got nil error, want a failure")
		}
	})
}

func TestLoadWaxumDefaultsTheIntervalAndTimeoutKnobsRegardlessOfBackend(t *testing.T) {
	// Explicit whatsmeow: the "regardless of backend" claim is that these
	// knobs resolve to a default whichever backend runs — pin the backend
	// here so the test doesn't ALSO depend on WSM_WAXUM_URL/TOKEN, which is
	// what the default (waxum) would otherwise require.
	t.Setenv("WSM_WA_BACKEND", "whatsmeow")
	cfg := &Config{}
	if err := loadWaxum(cfg); err != nil {
		t.Fatalf("loadWaxum: %v", err)
	}
	if cfg.WaxumTimeout != DefaultWaxumTimeout {
		t.Errorf("WaxumTimeout = %v, want default %v", cfg.WaxumTimeout, DefaultWaxumTimeout)
	}
	if cfg.WaxumQRPollInterval != DefaultWaxumQRPollInterval {
		t.Errorf("WaxumQRPollInterval = %v, want default %v", cfg.WaxumQRPollInterval, DefaultWaxumQRPollInterval)
	}
	if cfg.WaxumPollInterval != DefaultWaxumPollInterval {
		t.Errorf("WaxumPollInterval = %v, want default %v", cfg.WaxumPollInterval, DefaultWaxumPollInterval)
	}
	if cfg.WaxumPairTimeout != DefaultWaxumPairTimeout {
		t.Errorf("WaxumPairTimeout = %v, want default %v", cfg.WaxumPairTimeout, DefaultWaxumPairTimeout)
	}
}

func TestLoadWaxumParsesOverriddenIntervalsAndRejectsInvalidDurations(t *testing.T) {
	t.Run("overrides all four", func(t *testing.T) {
		t.Setenv("WSM_WA_BACKEND", "whatsmeow") // see comment on the defaults test above
		t.Setenv("WSM_WAXUM_TIMEOUT", "45s")
		t.Setenv("WSM_WAXUM_QR_POLL_INTERVAL", "3s")
		t.Setenv("WSM_WAXUM_POLL_INTERVAL", "20s")
		t.Setenv("WSM_WAXUM_PAIR_TIMEOUT", "5m")
		cfg := &Config{}
		if err := loadWaxum(cfg); err != nil {
			t.Fatalf("loadWaxum: %v", err)
		}
		if cfg.WaxumTimeout != 45*time.Second {
			t.Errorf("WaxumTimeout = %v, want 45s", cfg.WaxumTimeout)
		}
		if cfg.WaxumQRPollInterval != 3*time.Second {
			t.Errorf("WaxumQRPollInterval = %v, want 3s", cfg.WaxumQRPollInterval)
		}
		if cfg.WaxumPollInterval != 20*time.Second {
			t.Errorf("WaxumPollInterval = %v, want 20s", cfg.WaxumPollInterval)
		}
		if cfg.WaxumPairTimeout != 5*time.Minute {
			t.Errorf("WaxumPairTimeout = %v, want 5m", cfg.WaxumPairTimeout)
		}
	})

	for _, env := range []string{
		"WSM_WAXUM_TIMEOUT", "WSM_WAXUM_QR_POLL_INTERVAL",
		"WSM_WAXUM_POLL_INTERVAL", "WSM_WAXUM_PAIR_TIMEOUT",
	} {
		t.Run(env+" rejects a non-duration", func(t *testing.T) {
			t.Setenv(env, "not-a-duration")
			if err := loadWaxum(&Config{}); err == nil {
				t.Fatalf("loadWaxum with %s=not-a-duration: got nil error, want a failure", env)
			}
		})
	}
}

// TestLoadWaxumDefaultsEventsToEnabledAndParsesTheOverride guards
// WSM_WAXUM_EVENTS (Phase 8's escape hatch for the SSE event pump,
// internal/wa/backend_waxum_events.go): on by default, resolves regardless
// of backend (same as the interval/timeout knobs), and rejects a
// non-boolean value.
func TestLoadWaxumDefaultsEventsToEnabledAndParsesTheOverride(t *testing.T) {
	t.Run("defaults to enabled", func(t *testing.T) {
		t.Setenv("WSM_WA_BACKEND", "whatsmeow")
		cfg := &Config{}
		if err := loadWaxum(cfg); err != nil {
			t.Fatalf("loadWaxum: %v", err)
		}
		if !cfg.WaxumEventsEnabled {
			t.Error("WaxumEventsEnabled = false, want true (default)")
		}
	})

	t.Run("WSM_WAXUM_EVENTS=false disables it", func(t *testing.T) {
		t.Setenv("WSM_WA_BACKEND", "whatsmeow")
		t.Setenv("WSM_WAXUM_EVENTS", "false")
		cfg := &Config{}
		if err := loadWaxum(cfg); err != nil {
			t.Fatalf("loadWaxum: %v", err)
		}
		if cfg.WaxumEventsEnabled {
			t.Error("WaxumEventsEnabled = true, want false after WSM_WAXUM_EVENTS=false")
		}
	})

	t.Run("rejects a non-boolean value", func(t *testing.T) {
		t.Setenv("WSM_WAXUM_EVENTS", "not-a-bool")
		if err := loadWaxum(&Config{}); err == nil {
			t.Fatal("loadWaxum with WSM_WAXUM_EVENTS=not-a-bool: got nil error, want a failure")
		}
	})
}

func TestParseWaxumURLKeepsTheSchemeUnlikeParseMinioEndpoint(t *testing.T) {
	got, err := parseWaxumURL("http://127.0.0.1:9000/")
	if err != nil {
		t.Fatalf("parseWaxumURL: %v", err)
	}
	if got != "http://127.0.0.1:9000" {
		t.Errorf("parseWaxumURL = %q, want %q (trailing slash trimmed, scheme kept)", got, "http://127.0.0.1:9000")
	}
}

func TestParseWaxumURLRejectsMalformedInput(t *testing.T) {
	for _, raw := range []string{"not-a-url", "ftp://host:9000", "http://"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := parseWaxumURL(raw); err == nil {
				t.Fatalf("parseWaxumURL(%q): got nil error, want a failure", raw)
			}
		})
	}
}

func TestLoadWaxumResolvesTheReconcilerKnobs(t *testing.T) {
	t.Setenv("WSM_WA_BACKEND", "whatsmeow")
	cfg := &Config{}
	if err := loadWaxum(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.ReconcileInterval != DefaultReconcileInterval || cfg.ReconcileGrace != DefaultReconcileGrace {
		t.Fatalf("defaults = %v/%v", cfg.ReconcileInterval, cfg.ReconcileGrace)
	}
	t.Setenv("WSM_RECONCILE_INTERVAL", "0")
	t.Setenv("WSM_RECONCILE_GRACE", "90s")
	cfg = &Config{}
	if err := loadWaxum(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.ReconcileInterval != 0 || cfg.ReconcileGrace.Seconds() != 90 {
		t.Fatalf("overrides = %v/%v", cfg.ReconcileInterval, cfg.ReconcileGrace)
	}
	t.Setenv("WSM_RECONCILE_GRACE", "0s")
	if err := loadWaxum(&Config{}); err == nil {
		t.Fatal("WSM_RECONCILE_GRACE=0s accepted")
	}
}
