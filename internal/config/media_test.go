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

func TestLoadMediaDefaultsEveryPerKindLimitWhenNoneAreSet(t *testing.T) {
	cfg := &Config{}
	if err := loadMedia(cfg); err != nil {
		t.Fatalf("loadMedia: %v", err)
	}
	if cfg.MediaMaxBytesImage != DefaultMediaMaxBytesImage {
		t.Errorf("MediaMaxBytesImage = %d, want default %d", cfg.MediaMaxBytesImage, DefaultMediaMaxBytesImage)
	}
	if cfg.MediaMaxBytesVideo != DefaultMediaMaxBytesVideo {
		t.Errorf("MediaMaxBytesVideo = %d, want default %d", cfg.MediaMaxBytesVideo, DefaultMediaMaxBytesVideo)
	}
	if cfg.MediaMaxBytesAudio != DefaultMediaMaxBytesAudio {
		t.Errorf("MediaMaxBytesAudio = %d, want default %d", cfg.MediaMaxBytesAudio, DefaultMediaMaxBytesAudio)
	}
	if cfg.MediaMaxBytesDocument != DefaultMediaMaxBytesDocument {
		t.Errorf("MediaMaxBytesDocument = %d, want default %d", cfg.MediaMaxBytesDocument, DefaultMediaMaxBytesDocument)
	}
}

func TestLoadMediaOverridesEachPerKindLimitIndependently(t *testing.T) {
	t.Setenv("WSM_MEDIA_MAX_BYTES_IMAGE", "1048576")
	t.Setenv("WSM_MEDIA_MAX_BYTES_VIDEO", "2097152")
	t.Setenv("WSM_MEDIA_MAX_BYTES_AUDIO", "3145728")
	t.Setenv("WSM_MEDIA_MAX_BYTES_DOCUMENT", "4194304")

	cfg := &Config{}
	if err := loadMedia(cfg); err != nil {
		t.Fatalf("loadMedia: %v", err)
	}
	if cfg.MediaMaxBytesImage != 1048576 {
		t.Errorf("MediaMaxBytesImage = %d, want 1048576", cfg.MediaMaxBytesImage)
	}
	if cfg.MediaMaxBytesVideo != 2097152 {
		t.Errorf("MediaMaxBytesVideo = %d, want 2097152", cfg.MediaMaxBytesVideo)
	}
	if cfg.MediaMaxBytesAudio != 3145728 {
		t.Errorf("MediaMaxBytesAudio = %d, want 3145728", cfg.MediaMaxBytesAudio)
	}
	if cfg.MediaMaxBytesDocument != 4194304 {
		t.Errorf("MediaMaxBytesDocument = %d, want 4194304", cfg.MediaMaxBytesDocument)
	}
}

// TestLoadMediaRejectsAPerKindLimitAboveTheGlobalCeiling pins the
// fail-fast half of loadMedia's clamp rule: an EXPLICIT per-kind override
// above WSM_MEDIA_MAX_BYTES is a configuration mistake (the request-level
// cap would reject it regardless of what the per-kind limit says), not
// something to honour silently.
func TestLoadMediaRejectsAPerKindLimitAboveTheGlobalCeiling(t *testing.T) {
	for _, env := range []string{
		"WSM_MEDIA_MAX_BYTES_IMAGE",
		"WSM_MEDIA_MAX_BYTES_VIDEO",
		"WSM_MEDIA_MAX_BYTES_AUDIO",
		"WSM_MEDIA_MAX_BYTES_DOCUMENT",
	} {
		t.Run(env, func(t *testing.T) {
			t.Setenv("WSM_MEDIA_MAX_BYTES", "1024")
			t.Setenv(env, "2048")
			err := loadMedia(&Config{})
			if err == nil {
				t.Fatalf("loadMedia with %s=2048 above WSM_MEDIA_MAX_BYTES=1024: got nil error, want a failure", env)
			}
		})
	}
}

// TestLoadMediaClampsADefaultPerKindLimitDownToALoweredGlobalCeiling pins
// the OTHER half: a default (never explicitly requested) that would land
// above a deliberately-lowered WSM_MEDIA_MAX_BYTES is silently clamped, not
// an error — only an EXPLICIT ask above the ceiling fails.
func TestLoadMediaClampsADefaultPerKindLimitDownToALoweredGlobalCeiling(t *testing.T) {
	t.Setenv("WSM_MEDIA_MAX_BYTES", "1024") // below every per-kind default
	cfg := &Config{}
	if err := loadMedia(cfg); err != nil {
		t.Fatalf("loadMedia: %v", err)
	}
	if cfg.MediaMaxBytesImage != 1024 {
		t.Errorf("MediaMaxBytesImage = %d, want clamped to 1024", cfg.MediaMaxBytesImage)
	}
	if cfg.MediaMaxBytesVideo != 1024 {
		t.Errorf("MediaMaxBytesVideo = %d, want clamped to 1024", cfg.MediaMaxBytesVideo)
	}
	if cfg.MediaMaxBytesAudio != 1024 {
		t.Errorf("MediaMaxBytesAudio = %d, want clamped to 1024", cfg.MediaMaxBytesAudio)
	}
	if cfg.MediaMaxBytesDocument != 1024 {
		t.Errorf("MediaMaxBytesDocument = %d, want clamped to 1024", cfg.MediaMaxBytesDocument)
	}
}

func TestLoadMediaRejectsAnInvalidPerKindLimit(t *testing.T) {
	for _, env := range []string{
		"WSM_MEDIA_MAX_BYTES_IMAGE",
		"WSM_MEDIA_MAX_BYTES_VIDEO",
		"WSM_MEDIA_MAX_BYTES_AUDIO",
		"WSM_MEDIA_MAX_BYTES_DOCUMENT",
	} {
		for _, raw := range []string{"not-a-number", "0", "-5"} {
			t.Run(env+"/"+raw, func(t *testing.T) {
				t.Setenv(env, raw)
				if err := loadMedia(&Config{}); err == nil {
					t.Fatalf("loadMedia with %s=%q: got nil error, want a failure", env, raw)
				}
			})
		}
	}
}
