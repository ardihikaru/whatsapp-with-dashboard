package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Defaults for the inbound-message pump (internal/wa/inbound.go, Stage 2 of
// GET /api/v1/inbound — see docs/brief-inbound-api.md). Every one of these
// resolves regardless of WSM_WA_BACKEND, matching loadMedia's own doc
// comment on why: none of them is conditionally required, so
// scripts/e2e-env.sh (which exports none of them) still boots cleanly. The
// pump itself is a no-op on the whatsmeow backend either way (only
// waxumBackend implements the MessageHistory capability it polls through),
// but the config resolves the same regardless of backend.
const (
	// DefaultInboundEnabled is TRUE — unlike DefaultCallsEnabled, reading a
	// customer's own reply carries none of the automated-outbound-calling
	// hazard that justifies calls shipping disarmed; the whole point of
	// this feature is a consumer that is currently getting a 404 on every
	// poll (see the brief).
	DefaultInboundEnabled = true
	// DefaultInboundPollInterval mirrors DefaultWaxumPollInterval's order
	// of magnitude (60s) rather than inventing its own cadence: both are
	// per-session polls against the same rate-limited waxum instance (see
	// CLAUDE.md's waxum rate-limit hazard note), and a faster default here
	// would add a second poller competing with statusPump for the same
	// small per-IP token bucket for no benefit an operator asked for.
	DefaultInboundPollInterval = 60 * time.Second
	// DefaultInboundStampWindow is 24h, matching the brief's own reasoning
	// verbatim: chosen against the question being asked (a 4-hour deadline
	// in the consumer product) and against a customer messaging out of the
	// blue months later being silently attributed to an old order. See
	// internal/domain/inbound.Repository.LatestOutboundRefs' doc comment
	// for the window's other half (notAfter, anchored on the arrival's own
	// timestamp rather than time.Now()).
	DefaultInboundStampWindow = 24 * time.Hour
)

// loadInbound resolves WSM_INBOUND_ENABLED / WSM_INBOUND_POLL_INTERVAL /
// WSM_INBOUND_STAMP_WINDOW. Always resolves to a default, and — like
// loadMedia's fields — never a boot failure: an operator who never sets any
// of these three still gets a working pump at sensible defaults.
func loadInbound(cfg *Config) error {
	cfg.InboundEnabled = DefaultInboundEnabled
	if raw := os.Getenv("WSM_INBOUND_ENABLED"); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("invalid WSM_INBOUND_ENABLED %q: %v", raw, err)
		}
		cfg.InboundEnabled = b
	}

	cfg.InboundPollInterval = DefaultInboundPollInterval
	if raw := os.Getenv("WSM_INBOUND_POLL_INTERVAL"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return fmt.Errorf("invalid WSM_INBOUND_POLL_INTERVAL %q: must be a positive duration", raw)
		}
		cfg.InboundPollInterval = d
	}

	cfg.InboundStampWindow = DefaultInboundStampWindow
	if raw := os.Getenv("WSM_INBOUND_STAMP_WINDOW"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return fmt.Errorf("invalid WSM_INBOUND_STAMP_WINDOW %q: must be a positive duration", raw)
		}
		cfg.InboundStampWindow = d
	}
	return nil
}
