// Package statusschedule is the persistence model for scheduled WhatsApp
// status/story posts (wa_status_schedules, migrations/0013). One Schedule is
// one fixed post that internal/statussched publishes once on each selected
// weekday at a random time inside a daily window.
package statusschedule

import (
	"errors"
	"time"
)

// Kinds of status a schedule can post.
const (
	KindText  = "text"
	KindImage = "image"
	KindVideo = "video"
)

// Recipient modes. ModeAll is resolved against the contact book at FIRE
// time, so contacts added after the schedule was saved are included.
const (
	ModeAll    = "all"
	ModeCustom = "custom"
)

// Schedule states.
const (
	StateActive   = "active"
	StatePaused   = "paused"
	StateFinished = "finished"
	StateStalled  = "stalled"
)

// Sources of Schedule.NextText.
const (
	TextSourceAI     = "ai"
	TextSourceStatic = "static"
)

// Results of the most recent fire attempt (Schedule.LastResult).
const (
	ResultSent    = "sent"
	ResultFailed  = "failed"
	ResultSkipped = "skipped"
)

var (
	// ErrNotFound is returned by any lookup/mutation keyed on an unknown id.
	ErrNotFound = errors.New("statusschedule: not found")
	// ErrNotClaimable means ClaimFire lost the race (or the plan changed).
	ErrNotClaimable = errors.New("statusschedule: fire already claimed or replanned")
)

// Schedule mirrors one wa_status_schedules row. Dates are "YYYY-MM-DD"
// strings; the *At fields are zero when unset.
type Schedule struct {
	ID        string
	Name      string
	SessionID string

	Kind    string
	Text    string
	BgARGB  uint32
	Caption string

	MediaObjectKey   string
	MediaContentType string
	MediaFilename    string

	Weekdays    string // CSV of ISO weekdays, Monday=1 .. Sunday=7
	WindowStart string // "HH:MM"
	WindowEnd   string
	Timezone    string
	StartDate   string
	EndDate     string // "" = no end date

	RecipientsMode string
	RecipientsJSON string // JSON array of JIDs; "" when RecipientsMode is ModeAll

	State           string
	PausedReason    string
	LastPlannedDate string
	NextFireAt      time.Time
	LastFiredAt     time.Time
	LastResult      string
	LastError       string
	CreatedAt       time.Time
	UpdatedAt       time.Time

	// AI wording (migrations/0014). AIEnabled: each post's text (text kind)
	// or caption (image/video) is written by AI from AITopic; Text/Caption
	// then serve only as the fallback. NextText is the exact wording planned
	// for the pending slot (NextTextSource says where it came from);
	// LastAIText feeds the next prompt so consecutive posts differ.
	AIEnabled      bool
	AITopic        string
	NextText       string
	NextTextSource string
	LastAIText     string
}
