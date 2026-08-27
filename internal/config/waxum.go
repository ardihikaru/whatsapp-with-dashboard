package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// loadWaxum resolves the WA_BACKEND selection and every WSM_WAXUM_* knob.
//
// The interval/timeout knobs always resolve (they have defaults and are
// harmless to read regardless of which backend runs); WSM_WAXUM_URL and
// WSM_WAXUM_TOKEN are boot failures, but ONLY when WSM_WA_BACKEND=waxum —
// same reasoning as loadMinio's required fields, except here the
// requirement is conditional on a second variable instead of unconditional.
func loadWaxum(cfg *Config) error {
	cfg.WABackend = envOr("WSM_WA_BACKEND", DefaultWABackend)
	switch cfg.WABackend {
	case "whatsmeow", "waxum":
		// ok
	default:
		return fmt.Errorf("invalid WSM_WA_BACKEND %q: must be \"whatsmeow\" or \"waxum\"", cfg.WABackend)
	}

	cfg.WaxumTimeout = DefaultWaxumTimeout
	if raw := os.Getenv("WSM_WAXUM_TIMEOUT"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return fmt.Errorf("invalid WSM_WAXUM_TIMEOUT %q: %v", raw, err)
		}
		cfg.WaxumTimeout = d
	}
	cfg.WaxumQRPollInterval = DefaultWaxumQRPollInterval
	if raw := os.Getenv("WSM_WAXUM_QR_POLL_INTERVAL"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return fmt.Errorf("invalid WSM_WAXUM_QR_POLL_INTERVAL %q: %v", raw, err)
		}
		cfg.WaxumQRPollInterval = d
	}
	cfg.WaxumPollInterval = DefaultWaxumPollInterval
	if raw := os.Getenv("WSM_WAXUM_POLL_INTERVAL"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return fmt.Errorf("invalid WSM_WAXUM_POLL_INTERVAL %q: %v", raw, err)
		}
		cfg.WaxumPollInterval = d
	}
	cfg.WaxumPairTimeout = DefaultWaxumPairTimeout
	if raw := os.Getenv("WSM_WAXUM_PAIR_TIMEOUT"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return fmt.Errorf("invalid WSM_WAXUM_PAIR_TIMEOUT %q: %v", raw, err)
		}
		cfg.WaxumPairTimeout = d
	}
	cfg.WaxumEventsEnabled = DefaultWaxumEventsEnabled
	if raw := os.Getenv("WSM_WAXUM_EVENTS"); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("invalid WSM_WAXUM_EVENTS %q: %v", raw, err)
		}
		cfg.WaxumEventsEnabled = b
	}

	if cfg.WABackend != "waxum" {
		return nil // URL/token only matter to whichever backend is actually selected
	}

	raw := os.Getenv("WSM_WAXUM_URL")
	if raw == "" {
		return errors.New("WSM_WAXUM_URL is required when WSM_WA_BACKEND=waxum")
	}
	normalized, err := parseWaxumURL(raw)
	if err != nil {
		return err
	}
	cfg.WaxumURL = normalized

	cfg.WaxumToken = os.Getenv("WSM_WAXUM_TOKEN")
	if cfg.WaxumToken == "" {
		return errors.New("WSM_WAXUM_TOKEN is required when WSM_WA_BACKEND=waxum")
	}
	return nil
}

// parseWaxumURL validates WSM_WAXUM_URL the same way parseMinioEndpoint
// validates WSM_MINIO_ENDPOINT (internal/config/minio.go): absolute,
// http/https only, host required. Unlike the MinIO helper this one keeps
// the scheme in the returned value, because internal/waxum.Client.New wants
// a ready-to-use base URL, not a bare host it has to reattach a scheme to.
func parseWaxumURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("WSM_WAXUM_URL is not a URL: %w", err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("WSM_WAXUM_URL must be absolute (scheme://host), got %q", raw)
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return "", fmt.Errorf("WSM_WAXUM_URL scheme must be http or https, got %q", raw)
	}
	return strings.TrimSuffix(raw, "/"), nil
}
