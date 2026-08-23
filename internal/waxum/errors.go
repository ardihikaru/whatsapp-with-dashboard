package waxum

import (
	"errors"
	"fmt"
)

// Typed errors this package maps well-known HTTP statuses to. Anything else
// non-2xx becomes an *APIError instead.
//
// These stay waxum-flavoured on purpose: internal/wa's waxumBackend
// (internal/wa/backend_waxum.go's mapErr) is the layer that knows what
// ErrNotFound etc. mean in terms of
// wa.ErrNotFound/wa.ErrNotConnected/wa.ErrQueueFull. Doing that translation
// here would make this package depend on internal/wa's vocabulary for no
// reason a plain HTTP client needs.
var (
	ErrNotFound    = errors.New("waxum: not found")
	ErrConflict    = errors.New("waxum: conflict")
	ErrRateLimited = errors.New("waxum: rate limited")
)

// APIError is returned for any non-2xx response that isn't one of the typed
// sentinels above. StatusCode and Body are kept so a caller (or a log line)
// can see exactly what waxum said, since "unexpected status" alone is not
// actionable.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("waxum: unexpected status %d: %s", e.StatusCode, e.Body)
}

// errorFromStatus maps an HTTP status (already known to be non-2xx) to one
// of the sentinels, or wraps it in *APIError.
func errorFromStatus(statusCode int, body []byte) error {
	switch statusCode {
	case 404:
		return ErrNotFound
	case 409:
		return ErrConflict
	case 429:
		return ErrRateLimited
	default:
		return &APIError{StatusCode: statusCode, Body: string(body)}
	}
}
