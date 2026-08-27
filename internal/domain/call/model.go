// Package call is the persistence model for placed voice calls (the wa_calls
// table). Deliberately a SEPARATE table from wa_messages, not more columns on
// it — see migrations/0004_media_and_calls.sql's header for why. This
// package mirrors internal/domain/message's shape on purpose: same
// Repository/SQLRepository split, same nullIfEmpty convention, same
// RFC3339-string time layout.
package call

import "time"

// Statuses a call row moves through.
//   - placing    — Insert just happened, waxum has not yet confirmed the call
//   - placed     — waxum accepted the call and returned a call_id
//   - failed     — waxum rejected the call or the place request errored
//   - terminated — the call was ended (by TerminateCall or waxum reporting it over)
const (
	StatusPlacing    = "placing"
	StatusPlaced     = "placed"
	StatusFailed     = "failed"
	StatusTerminated = "terminated"
)

// Kinds a call may be: text-to-speech or a pre-recorded audio file, mirroring
// internal/wa's CallOutcome-producing methods (PlaceCallTTS / PlaceCallPlay).
const (
	KindTTS  = "tts"
	KindPlay = "play"
)

// Call is one row of wa_calls.
type Call struct {
	ID        string
	SessionID string
	ToPhone   string
	Kind      string // tts | play
	Text      string // TTS script; empty for a play call
	Voice     string // TTS voice name; empty for a play call
	AudioURL  string // pre-recorded audio source; empty for a tts call

	CallID       string // waxum's call identifier, set once placed
	RecordingURL string // set once waxum reports one
	Status       string // placing | placed | failed | terminated
	Error        string

	CreatedAt time.Time
	EndedAt   *time.Time

	// APIClientID follows wa_messages.api_client_id's rule exactly: set only
	// from the authenticated bearer principal (auth.APIPrincipalFrom), never
	// from the request body, so a caller cannot spoof attribution. Wiring the
	// handler that actually sets it is Phase 6 of the waxum plan; this column
	// and field exist now so the schema and the handler land in the same
	// shape once that phase writes to it.
	APIClientID string
}
