package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadAIClientDefaultsTimeoutRegardlessOfGroupBroadcast(t *testing.T) {
	cfg := &Config{}
	if err := loadAIClient(cfg); err != nil {
		t.Fatalf("loadAIClient: %v", err)
	}
	if cfg.CodexCLITimeout != 20*time.Second {
		t.Errorf("CodexCLITimeout = %s, want 20s by default", cfg.CodexCLITimeout)
	}
}

// TestCodexCLIURLAndCredentialsAreRequiredOnlyWhenGroupBroadcastIsEnabled
// mirrors TestWaxumURLAndTokenAreRequiredOnlyWhenTheWaxumBackendIsSelected's
// conditional-requirement shape.
func TestCodexCLIURLAndCredentialsAreRequiredOnlyWhenGroupBroadcastIsEnabled(t *testing.T) {
	t.Run("group broadcast disabled: no URL/credentials needed", func(t *testing.T) {
		cfg := &Config{GroupBroadcastEnabled: false}
		if err := loadAIClient(cfg); err != nil {
			t.Fatalf("loadAIClient: %v", err)
		}
		if cfg.CodexCLIURL != "" || cfg.CodexCLILoginID != "" || cfg.CodexCLISecretKey != "" {
			t.Error("loadAIClient populated CodexCLI URL/credentials even though group broadcast is disabled")
		}
	})

	t.Run("group broadcast enabled with no URL fails, naming the variable", func(t *testing.T) {
		cfg := &Config{GroupBroadcastEnabled: true}
		err := loadAIClient(cfg)
		if err == nil {
			t.Fatal("loadAIClient with group broadcast enabled and no WSM_CODEX_CLI_URL: got nil error, want a failure")
		}
		if !strings.Contains(err.Error(), "WSM_CODEX_CLI_URL") {
			t.Fatalf("error = %q, want it to name WSM_CODEX_CLI_URL", err.Error())
		}
	})

	t.Run("group broadcast enabled with URL but no login id fails, naming the variable", func(t *testing.T) {
		t.Setenv("WSM_CODEX_CLI_URL", "http://127.0.0.1:8090")
		cfg := &Config{GroupBroadcastEnabled: true}
		err := loadAIClient(cfg)
		if err == nil {
			t.Fatal("loadAIClient with no WSM_CODEX_CLI_LOGIN_ID: got nil error, want a failure")
		}
		if !strings.Contains(err.Error(), "WSM_CODEX_CLI_LOGIN_ID") {
			t.Fatalf("error = %q, want it to name WSM_CODEX_CLI_LOGIN_ID", err.Error())
		}
	})

	t.Run("group broadcast enabled with URL and login id but no secret fails, naming the variable", func(t *testing.T) {
		t.Setenv("WSM_CODEX_CLI_URL", "http://127.0.0.1:8090")
		t.Setenv("WSM_CODEX_CLI_LOGIN_ID", "login")
		cfg := &Config{GroupBroadcastEnabled: true}
		err := loadAIClient(cfg)
		if err == nil {
			t.Fatal("loadAIClient with no WSM_CODEX_CLI_SECRET_KEY: got nil error, want a failure")
		}
		if !strings.Contains(err.Error(), "WSM_CODEX_CLI_SECRET_KEY") {
			t.Fatalf("error = %q, want it to name WSM_CODEX_CLI_SECRET_KEY", err.Error())
		}
	})

	t.Run("boots clean once every required variable is present", func(t *testing.T) {
		t.Setenv("WSM_CODEX_CLI_URL", "http://127.0.0.1:8090")
		t.Setenv("WSM_CODEX_CLI_LOGIN_ID", "login")
		t.Setenv("WSM_CODEX_CLI_SECRET_KEY", "secret")
		cfg := &Config{GroupBroadcastEnabled: true}
		if err := loadAIClient(cfg); err != nil {
			t.Fatalf("loadAIClient: %v", err)
		}
		if cfg.CodexCLIURL != "http://127.0.0.1:8090" || cfg.CodexCLILoginID != "login" || cfg.CodexCLISecretKey != "secret" {
			t.Errorf("loadAIClient populated %+v, want the env values back", cfg)
		}
	})
}

func TestParseCodexCLIURLRejectsMalformedInput(t *testing.T) {
	cases := []string{"", "not a url", "ftp://host", "http://"}
	for _, raw := range cases {
		if _, err := parseCodexCLIURL(raw); err == nil {
			t.Errorf("parseCodexCLIURL(%q) returned no error, want a failure", raw)
		}
	}
}

func TestParseCodexCLIURLKeepsTheScheme(t *testing.T) {
	got, err := parseCodexCLIURL("http://127.0.0.1:8090/")
	if err != nil {
		t.Fatalf("parseCodexCLIURL: %v", err)
	}
	if got != "http://127.0.0.1:8090" {
		t.Errorf("parseCodexCLIURL = %q, want the scheme kept and trailing slash trimmed", got)
	}
}
