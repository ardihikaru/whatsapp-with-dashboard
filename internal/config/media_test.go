package config

import (
	"testing"
	"time"
)

func TestLoadMediaDefaultsEveryVarWhenNoneAreSet(t *testing.T) {
	cfg := &Config{}
	if err := loadMedia(cfg); err != nil {
		t.Fatalf("loadMedia: %v", err)
	}
	if cfg.MediaMaxBytes != DefaultMediaMaxBytes {
		t.Errorf("MediaMaxBytes = %d, want default %d", cfg.MediaMaxBytes, DefaultMediaMaxBytes)
	}
	if cfg.MediaUploadTimeout != DefaultMediaUploadTimeout {
		t.Errorf("MediaUploadTimeout = %v, want default %v", cfg.MediaUploadTimeout, DefaultMediaUploadTimeout)
	}
	if cfg.MediaUploadConcurrency != DefaultMediaUploadConcurrency {
		t.Errorf("MediaUploadConcurrency = %d, want default %d", cfg.MediaUploadConcurrency, DefaultMediaUploadConcurrency)
	}
	if cfg.MediaArchive != DefaultMediaArchive {
		t.Errorf("MediaArchive = %v, want default %v", cfg.MediaArchive, DefaultMediaArchive)
	}
	if cfg.CallsEnabled != DefaultCallsEnabled {
		t.Errorf("CallsEnabled = %v, want default %v", cfg.CallsEnabled, DefaultCallsEnabled)
	}
	// Pinned explicitly, not just "equals the constant": a boot with no
	// WSM_CALLS_ENABLED set must never accidentally place a real WhatsApp
	// call — see CLAUDE.md's calls hazard note.
	if cfg.CallsEnabled {
		t.Fatal("CallsEnabled defaulted to true; calls must be opt-in")
	}
}

func TestLoadMediaParsesAFullValidEnvironment(t *testing.T) {
	t.Setenv("WSM_MEDIA_MAX_BYTES", "1048576")
	t.Setenv("WSM_MEDIA_UPLOAD_TIMEOUT", "10s")
	t.Setenv("WSM_MEDIA_UPLOAD_CONCURRENCY", "2")
	t.Setenv("WSM_MEDIA_ARCHIVE", "false")
	t.Setenv("WSM_CALLS_ENABLED", "true")

	cfg := &Config{}
	if err := loadMedia(cfg); err != nil {
		t.Fatalf("loadMedia: %v", err)
	}
	if cfg.MediaMaxBytes != 1048576 {
		t.Errorf("MediaMaxBytes = %d, want 1048576", cfg.MediaMaxBytes)
	}
	if cfg.MediaUploadTimeout != 10*time.Second {
		t.Errorf("MediaUploadTimeout = %v, want 10s", cfg.MediaUploadTimeout)
	}
	if cfg.MediaUploadConcurrency != 2 {
		t.Errorf("MediaUploadConcurrency = %d, want 2", cfg.MediaUploadConcurrency)
	}
	if cfg.MediaArchive {
		t.Error("MediaArchive = true, want false")
	}
	if !cfg.CallsEnabled {
		t.Error("CallsEnabled = false, want true")
	}
}

func TestLoadMediaRejectsAnInvalidMaxBytes(t *testing.T) {
	for _, raw := range []string{"not-a-number", "0", "-5"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("WSM_MEDIA_MAX_BYTES", raw)
			if err := loadMedia(&Config{}); err == nil {
				t.Fatalf("loadMedia with WSM_MEDIA_MAX_BYTES=%q: got nil error, want a failure", raw)
			}
		})
	}
}

func TestLoadMediaRejectsAnInvalidUploadTimeout(t *testing.T) {
	t.Setenv("WSM_MEDIA_UPLOAD_TIMEOUT", "not-a-duration")
	if err := loadMedia(&Config{}); err == nil {
		t.Fatal("loadMedia with an invalid WSM_MEDIA_UPLOAD_TIMEOUT: got nil error, want a failure")
	}
}

func TestLoadMediaRejectsAnInvalidUploadConcurrency(t *testing.T) {
	for _, raw := range []string{"not-a-number", "0", "-1"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("WSM_MEDIA_UPLOAD_CONCURRENCY", raw)
			if err := loadMedia(&Config{}); err == nil {
				t.Fatalf("loadMedia with WSM_MEDIA_UPLOAD_CONCURRENCY=%q: got nil error, want a failure", raw)
			}
		})
	}
}

func TestLoadMediaRejectsAnInvalidArchiveFlag(t *testing.T) {
	t.Setenv("WSM_MEDIA_ARCHIVE", "not-a-bool")
	if err := loadMedia(&Config{}); err == nil {
		t.Fatal("loadMedia with an invalid WSM_MEDIA_ARCHIVE: got nil error, want a failure")
	}
}

func TestLoadMediaRejectsAnInvalidCallsEnabledFlag(t *testing.T) {
	t.Setenv("WSM_CALLS_ENABLED", "not-a-bool")
	if err := loadMedia(&Config{}); err == nil {
		t.Fatal("loadMedia with an invalid WSM_CALLS_ENABLED: got nil error, want a failure")
	}
}
