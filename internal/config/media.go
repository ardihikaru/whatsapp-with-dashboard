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
	// DefaultMediaMaxBytes bounds one uploaded file overall — the request
	// cap http.MaxBytesReader enforces before ParseMultipartForm ever
	// runs, and the ceiling no per-kind limit below may exceed. 100 MiB,
	// matching waxum's own /media/upload route cap AND the value this
	// deployment's .env already carries (WSM_MEDIA_MAX_BYTES=104857600) —
	// raised from a stale 16 MiB after a 1.6 MB video send failed with
	// "content too large" at the edge (see CLAUDE.md/the media-limits
	// phase brief). A lower default here would silently clamp
	// DefaultMediaMaxBytesDocument (100 MB) down to whatever this is, on
	// every fresh install that never sets WSM_MEDIA_MAX_BYTES.
	DefaultMediaMaxBytes int64 = 100 * 1024 * 1024 // 100 MiB
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

	// DefaultMediaMaxBytesImage/Video/Audio/Document are WhatsApp's own
	// PUBLISHED per-kind media limits (business.whatsapp.com's media
	// specs) — labelled published-not-measured because they have not been
	// verified against waxum or a real WhatsApp send from this gateway,
	// only copied from WhatsApp's documentation. A single
	// DefaultMediaMaxBytes was wrong for all four kinds at once: it either
	// under-restricts a document (which WhatsApp allows far larger) or
	// over-permits a video past what WhatsApp will actually accept.
	DefaultMediaMaxBytesImage    int64 = 5 * 1024 * 1024   // 5 MB
	DefaultMediaMaxBytesVideo    int64 = 16 * 1024 * 1024  // 16 MB
	DefaultMediaMaxBytesAudio    int64 = 16 * 1024 * 1024  // 16 MB
	DefaultMediaMaxBytesDocument int64 = 100 * 1024 * 1024 // 100 MB
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

	kinds := []struct {
		env   string
		field *int64
		def   int64
	}{
		{"WSM_MEDIA_MAX_BYTES_IMAGE", &cfg.MediaMaxBytesImage, DefaultMediaMaxBytesImage},
		{"WSM_MEDIA_MAX_BYTES_VIDEO", &cfg.MediaMaxBytesVideo, DefaultMediaMaxBytesVideo},
		{"WSM_MEDIA_MAX_BYTES_AUDIO", &cfg.MediaMaxBytesAudio, DefaultMediaMaxBytesAudio},
		{"WSM_MEDIA_MAX_BYTES_DOCUMENT", &cfg.MediaMaxBytesDocument, DefaultMediaMaxBytesDocument},
	}
	for _, k := range kinds {
		value := k.def
		explicit := false
		if raw := os.Getenv(k.env); raw != "" {
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || n <= 0 {
				return fmt.Errorf("invalid %s %q: must be a positive integer", k.env, raw)
			}
			value = n
			explicit = true
		}
		// No kind may exceed the overall request ceiling (MediaMaxBytes):
		// an operator who EXPLICITLY asks for more than that ceiling has
		// almost certainly forgotten to raise WSM_MEDIA_MAX_BYTES too, so
		// this fails fast and says so, following loadMedia's existing
		// "fail fast on anything malformed" rule rather than silently
		// honouring a limit the request-level cap would reject anyway. A
		// DEFAULT that ends up above a deliberately-lowered
		// WSM_MEDIA_MAX_BYTES is not a mistake in the same way — it is
		// just clamped down to the ceiling with no error.
		if value > cfg.MediaMaxBytes {
			if explicit {
				return fmt.Errorf("invalid %s %d: exceeds the WSM_MEDIA_MAX_BYTES ceiling of %d", k.env, value, cfg.MediaMaxBytes)
			}
			value = cfg.MediaMaxBytes
		}
		*k.field = value
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
