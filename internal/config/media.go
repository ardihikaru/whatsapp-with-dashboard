package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Defaults for the media-send and calls surface (Phase 6a). Every one of
// these resolves regardless of WSM_WA_BACKEND — unlike WSM_WAXUM_URL/TOKEN,
// none of them is conditionally required, so scripts/e2e-env.sh (which
// exports none of them) still boots cleanly. See loadMedia's doc comment.
const (
	// DefaultMediaMaxBytes bounds one uploaded file — the request cap
	// http.MaxBytesReader enforces before ParseMultipartForm ever runs.
	DefaultMediaMaxBytes int64 = 16 * 1024 * 1024 // 16 MiB
	// DefaultMediaUploadTimeout is the media handler's OWN context for the
	// backend upload call (Manager.MediaStore().UploadMedia) — separate
	// from send.go's sendWaitLimit, which only starts once the upload has
	// already finished and the job is enqueued.
	DefaultMediaUploadTimeout = 60 * time.Second
	// DefaultMediaUploadConcurrency caps how many uploads run at once — the
	// memory ceiling sendQueueCap does NOT provide, since an upload happens
	// in the HTTP handler before a job is ever enqueued (see
	// wa.EnqueueMedia's doc comment).
	DefaultMediaUploadConcurrency = 4
	// DefaultMediaArchive turns the best-effort MinIO archive step on by
	// default; WSM_MEDIA_ARCHIVE=false skips it entirely (waxum still holds
	// its own copy after upload either way).
	DefaultMediaArchive = true
	// DefaultCallsEnabled is FALSE — see CLAUDE.md's calls hazard note and
	// api_calls.go's callsDisabledMessage for why shipping the capability
	// disarmed is the honest default: WhatsApp's terms are hostile to
	// automated outbound calling, and the paired device identity has no
	// second copy anywhere to fall back on if a call gets it banned.
	DefaultCallsEnabled = false
)

// loadMedia resolves the WSM_MEDIA_*/WSM_CALLS_ENABLED knobs. Follows
// loadMinio's shape exactly (each optional var resolves to its default,
// then is overridden if set and non-empty) — see minio_test.go's mirror,
// media_test.go, for the same table-driven test style.
func loadMedia(cfg *Config) error {
	cfg.MediaMaxBytes = DefaultMediaMaxBytes
	if raw := os.Getenv("WSM_MEDIA_MAX_BYTES"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n <= 0 {
			return fmt.Errorf("invalid WSM_MEDIA_MAX_BYTES %q: must be a positive integer", raw)
		}
		cfg.MediaMaxBytes = n
	}

	cfg.MediaUploadTimeout = DefaultMediaUploadTimeout
	if raw := os.Getenv("WSM_MEDIA_UPLOAD_TIMEOUT"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return fmt.Errorf("invalid WSM_MEDIA_UPLOAD_TIMEOUT %q: %v", raw, err)
		}
		cfg.MediaUploadTimeout = d
	}

	cfg.MediaUploadConcurrency = DefaultMediaUploadConcurrency
	if raw := os.Getenv("WSM_MEDIA_UPLOAD_CONCURRENCY"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return fmt.Errorf("invalid WSM_MEDIA_UPLOAD_CONCURRENCY %q: must be a positive integer", raw)
		}
		cfg.MediaUploadConcurrency = n
	}

	cfg.MediaArchive = DefaultMediaArchive
	if raw := os.Getenv("WSM_MEDIA_ARCHIVE"); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("invalid WSM_MEDIA_ARCHIVE %q: %v", raw, err)
		}
		cfg.MediaArchive = b
	}

	cfg.CallsEnabled = DefaultCallsEnabled
	if raw := os.Getenv("WSM_CALLS_ENABLED"); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("invalid WSM_CALLS_ENABLED %q: %v", raw, err)
		}
		cfg.CallsEnabled = b
	}
	return nil
}
