package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// DefaultCodexCLITimeout is the per-request HTTP timeout for
// internal/aiclient.Client, wired regardless of whether group broadcast is
// enabled (harmless to read either way, same convention as loadWaxum's
// interval/timeout knobs).
const DefaultCodexCLITimeout = 20 * time.Second

// loadAIClient resolves WSM_CODEX_CLI_URL/LOGIN_ID/SECRET_KEY/TIMEOUT/MODEL.
// URL/LoginID/SecretKey are boot failures, but ONLY when
// cfg.GroupBroadcastEnabled is true (when it is false they are still
// populated if all three are set and valid, for AI-written status posts) — same conditional-requirement idiom as
// loadWaxum's URL/Token (required only when WABackend == "waxum"). Must run
// AFTER loadGroupBroadcast, which resolves cfg.GroupBroadcastEnabled.
//
// The real secret lives in ~/.config/ai-stack/basic-auth.json
// (login_id/secret_key keys) on the host that runs ai-codex-cli — that is
// an operator/deploy-time step to populate .env, never something this
// function reads directly (see CLAUDE.md's credential-index-files
// reasoning for every other secret in this codebase).
func loadAIClient(cfg *Config) error {
	cfg.CodexCLITimeout = DefaultCodexCLITimeout
	if raw := os.Getenv("WSM_CODEX_CLI_TIMEOUT"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return fmt.Errorf("invalid WSM_CODEX_CLI_TIMEOUT %q: %v", raw, err)
		}
		cfg.CodexCLITimeout = d
	}
	cfg.CodexCLIModel = os.Getenv("WSM_CODEX_CLI_MODEL")

	if !cfg.GroupBroadcastEnabled {
		// Not required, but not ignored: AI-written status posts
		// (internal/statussched) use the same client. Populate only when ALL
		// THREE are present and the URL is valid; anything less leaves them
		// empty, which means "AI unavailable" — it must never stop a deploy
		// that has no use for AI from booting.
		u, login, secret := os.Getenv("WSM_CODEX_CLI_URL"), os.Getenv("WSM_CODEX_CLI_LOGIN_ID"), os.Getenv("WSM_CODEX_CLI_SECRET_KEY")
		if u == "" || login == "" || secret == "" {
			return nil
		}
		normalized, err := parseCodexCLIURL(u)
		if err != nil {
			return nil
		}
		cfg.CodexCLIURL, cfg.CodexCLILoginID, cfg.CodexCLISecretKey = normalized, login, secret
		return nil
	}

	raw := os.Getenv("WSM_CODEX_CLI_URL")
	if raw == "" {
		return errors.New("WSM_CODEX_CLI_URL is required when WSM_GROUP_BROADCAST_ENABLED=true")
	}
	normalized, err := parseCodexCLIURL(raw)
	if err != nil {
		return err
	}
	cfg.CodexCLIURL = normalized

	cfg.CodexCLILoginID = os.Getenv("WSM_CODEX_CLI_LOGIN_ID")
	if cfg.CodexCLILoginID == "" {
		return errors.New("WSM_CODEX_CLI_LOGIN_ID is required when WSM_GROUP_BROADCAST_ENABLED=true")
	}
	cfg.CodexCLISecretKey = os.Getenv("WSM_CODEX_CLI_SECRET_KEY")
	if cfg.CodexCLISecretKey == "" {
		return errors.New("WSM_CODEX_CLI_SECRET_KEY is required when WSM_GROUP_BROADCAST_ENABLED=true")
	}
	return nil
}

// parseCodexCLIURL validates WSM_CODEX_CLI_URL the same way parseWaxumURL
// validates WSM_WAXUM_URL: absolute, http/https only, host required, scheme
// kept in the returned value (internal/aiclient.New wants a ready-to-use
// base URL).
func parseCodexCLIURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("WSM_CODEX_CLI_URL is not a URL: %w", err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("WSM_CODEX_CLI_URL must be absolute (scheme://host), got %q", raw)
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return "", fmt.Errorf("WSM_CODEX_CLI_URL scheme must be http or https, got %q", raw)
	}
	return strings.TrimSuffix(raw, "/"), nil
}
