package waxum

// MediaKind selects which of waxum's four media-message endpoints
// SendMedia hits, and doubles as the JSON body key its payload nests under
// (see SendMedia's doc comment). There is deliberately no "ring" kind — see
// TestNoRingCallMethodExists.
type MediaKind string

const (
	MediaImage    MediaKind = "image"
	MediaVideo    MediaKind = "video"
	MediaAudio    MediaKind = "audio"
	MediaDocument MediaKind = "document"
)

// DefaultTTSVoice is used by CallTTS when the caller passes an empty voice.
const DefaultTTSVoice = "id-ID-ArdiNeural"

// Status is GET /sessions/{id}/status, decoded verbatim. Status.Status is
// one of: disconnected|connecting|waiting_for_qr|waiting_for_pair_code|
// connected|logged_in (waxum's own enum spelling, snake_case).
//
// Reachability is a *string, not a plain string, deliberately: waxum
// documents this field as null "when there is no live client in this
// process at all" for the session, which is the decisive signal
// distinguishing an ordinary, recoverable disconnect (a live client object
// exists and reports SOME reachability value, e.g. "reconnecting") from the
// orphaned-runtime wedge internal/wa/backend_waxum.go's
// isOrphanedClientWedge doc comment describes (no client object exists for this session, in this waxum
// process, at all). encoding/json leaves a plain string field untouched —
// not reset to "" — when it decodes a JSON null, so a bare `string` could
// never tell "absent" apart from a waxum build that happens to send "" for
// some other reason; a live-response fixture round-tripped in
// client_test.go pins this. See internal/wa/backend_waxum.go's
// isOrphanedClientWedge, the one reader of this field.
type Status struct {
	Status       string  `json:"status"`
	IsLoggedIn   bool    `json:"is_logged_in"`
	SocketAlive  bool    `json:"socket_alive"`
	Paused       bool    `json:"paused"`
	PhoneNumber  string  `json:"phone_number"`
	PushName     string  `json:"push_name"`
	Reachability *string `json:"reachability"`
	Pair         Pair    `json:"pair"`
}

// Pair is Status.Pair, waxum's pairing-attempt sub-object:
// {"last_qr_at":...,"last_pair_code_at":...,"pair_code_expires_at":...,
// "last_error":...,"attempts":...}. Only the two fields
// internal/wa/backend_waxum.go's pairPump actually reads are modeled here;
// the timestamp fields are left undecoded (this package's house style of
// modeling only what a caller needs — see groupsResponse's doc comment for
// the same posture elsewhere in this file).
//
// LastError is a plain string, not a *string like Status.Reachability
// above: unlike that field, a caller here only ever asks "is this
// non-empty", never "was this field present at all", so there is no
// "absent" case worth distinguishing from "" — encoding/json leaves
// LastError at its zero value "" for either a JSON null or a missing key,
// and Status is freshly unmarshaled on every poll, so a stale value from an
// earlier decode can never leak through.
//
// waxum does NOT clear last_error when a later attempt succeeds or when a
// fresh attempt starts — it is documented (and observed live, 2026-09-13)
// to persist the PREVIOUS attempt's failure text until the next one
// overwrites it. pairPump therefore treats a poll reporting LastError as
// fatal only alongside independent evidence there is no live client for
// this attempt at all (SocketAlive false, Reachability nil — the same
// fingerprint internal/wa/backend_waxum.go's isOrphanedClientWedge already
// uses for a different incident) and only once that has been observed on
// consecutive polls, not a single one — see pairErrorHysteresisPolls.
type Pair struct {
	LastError string `json:"last_error"`
	Attempts  int    `json:"attempts"`
}

// QRResult is GET /sessions/{id}/qr. QRCodes holds RAW payload strings
// ("2@…"), not PNGs — rendering them into a scannable code is the caller's
// job.
type QRResult struct {
	QRCodes        []string `json:"qr_codes"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	Status         string   `json:"status"`
}

// PairCodeResult is POST /sessions/{id}/pair.
type PairCodeResult struct {
	Code           string `json:"code"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// MessageResult is the response shape shared by SendText and SendMedia:
// {"message_id":…,"timestamp":…,"to":…}.
type MessageResult struct {
	MessageID string `json:"message_id"`
	Timestamp int64  `json:"timestamp"`
	To        string `json:"to"`
}

// Group is JoinedGroups' mapped, caller-facing shape. It is deliberately NOT
// the raw waxum wire shape — see Groups' doc comment for why Name comes from
// "subject" and Count comes from len(participants), not from a "name" or
// "participant_count" field neither of which waxum sends.
type Group struct {
	JID              string
	Name             string
	ParticipantCount int
	AddressingMode   string
}

// groupsResponse is the raw wire shape of GET /sessions/{id}/groups.
// Unexported: Groups() maps it to []Group before returning, so a caller can
// never accidentally read the (nonexistent) "name" field or treat
// "participants" as anything but "the array whose length is the count".
type groupsResponse struct {
	Groups []struct {
		JID            string           `json:"jid"`
		Subject        string           `json:"subject"`
		Participants   []rawParticipant `json:"participants"`
		AddressingMode string           `json:"addressing_mode"`
	} `json:"groups"`
	Total int `json:"total"`
}

// rawParticipant is intentionally shapeless (an empty struct decodes any
// JSON object without caring what's inside it) — Groups only ever needs
// len(Participants), never a participant's own fields.
type rawParticipant struct{}

// CallResult is the response shape shared by CallTTS and CallPlay:
// {"call_id":…,"to":…,"recording_url":…}.
type CallResult struct {
	CallID       string `json:"call_id"`
	To           string `json:"to"`
	RecordingURL string `json:"recording_url"`
}
