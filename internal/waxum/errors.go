package waxum

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Typed errors this package maps well-known HTTP statuses to. Anything else
// non-2xx becomes an *APIError instead.
//
// These stay waxum-flavoured on purpose: internal/wa's waxumBackend
// (internal/wa/backend_waxum.go's mapErr) is the layer that knows what
// ErrNotFound etc. mean in terms of
// wa.ErrNotFound/wa.ErrNotConnected/wa.ErrBackendBusy. Doing that
// translation here would make this package depend on internal/wa's
// vocabulary for no reason a plain HTTP client needs.
var (
	ErrNotFound = errors.New("waxum: not found")
	ErrConflict = errors.New("waxum: conflict")
	// ErrClientNotConnected is waxum's 503, matched by status code alone
	// (like every sentinel in this file — none of them parse the body).
	// Observed live as {"success":false,"error":{"code":503,"message":
	// "Client not connected"}} from POST /messages/text on a session whose
	// is_logged_in had just gone true but whose websocket was still
	// dialing — see internal/wa/backend_waxum.go's finishPairingSuccess
	// (point 8) for the pairing-side half of that race.
	ErrClientNotConnected = errors.New("waxum: client not connected")
	ErrRateLimited        = errors.New("waxum: rate limited")
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

// waxumErrorEnvelope is the JSON shape waxum wraps every non-2xx error
// body in: {"success":false,"error":{"code":N,"message":"..."}} — the
// exact shape observed live and quoted in ErrClientNotConnected's doc
// comment above.
type waxumErrorEnvelope struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Message extracts the human-readable reason from Body, if it parses as
// waxum's own error envelope, falling back to the raw body otherwise (e.g.
// a proxy-generated error page that never reached waxum's own handler).
// Added specifically so a rejection whose CAUSE matters to an operator —
// waxum's SSRF guard refusing a private callback URL, for one — can be
// surfaced verbatim rather than as the generic "backend rejected the
// request" ErrRejectedByBackend text alone (internal/wa's mapErr calls
// this).
func (e *APIError) Message() string {
	var env waxumErrorEnvelope
	if err := json.Unmarshal([]byte(e.Body), &env); err == nil && env.Error.Message != "" {
		return env.Error.Message
	}
	return e.Body
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
	case 503:
		return ErrClientNotConnected
	default:
		return &APIError{StatusCode: statusCode, Body: string(body)}
	}
}
