// Package groupbroadcast is the persistence model for Group Broadcast
// Schedule — one WSM session ("sender") posting an opening message into a
// real WhatsApp group at a random morning time, and a configurable list of
// other sessions ("receivers") each replying into that same group at their
// own random later time. See migrations/0011_group_broadcast.sql for the
// schema this package sits on, and internal/domain/convo for the sibling
// (deliberately separate) two-party scheduling package this one borrows its
// shape from.
package groupbroadcast

import (
	"errors"
	"time"
)

// Schedule states — same enum-not-boolean convention as
// convo.Schedule.State: active | paused | stalled | finished | stopped.
// paused is operator-initiated; stalled is the automatic health stop raised
// when the SENDER session goes unhealthy (an unhealthy receiver alone does
// NOT stall the schedule — see the planner's own doc comment for why this
// differs from convo's all-or-nothing gate). Both are resumable via
// SetScheduleState; finished/stopped are terminal — see
// IsScheduleTerminal.
const (
	ScheduleActive   = "active"
	SchedulePaused   = "paused"
	ScheduleStalled  = "stalled"
	ScheduleFinished = "finished"
	ScheduleStopped  = "stopped"
)

// Recurrence kinds a schedule may declare — same three kinds as
// convo.Schedule.Recurrence.
const (
	RecurrenceDaily    = "daily"
	RecurrenceWeekly   = "weekly"
	RecurrenceInterval = "interval"
)

// Occurrence states (wa_group_broadcast_occurrences.state).
const (
	OccurrenceScheduled = "scheduled"
	OccurrenceDone      = "done"
	OccurrenceFailed    = "failed"
	OccurrenceCancelled = "cancelled"
)

// Turn roles (wa_group_broadcast_turns.role).
const (
	RoleSender   = "sender"
	RoleReceiver = "receiver"
)

// Turn states — same shape as convo.Turn's own state machine.
const (
	TurnPending = "pending"
	TurnSending = "sending"
	TurnSent    = "sent"
	TurnFailed  = "failed"
	TurnSkipped = "skipped"
)

// Turn languages (wa_group_broadcast_turns.language).
const (
	LangID = "id"
	LangJV = "jv"
)

// Turn wording sources (wa_group_broadcast_turns.source).
const (
	SourceAI       = "ai"
	SourceFallback = "fallback"
)

// Schedule is one row of wa_group_broadcast_schedules. SenderName/
// SenderPhone/GroupName are snapshots taken at save time, no FK — same
// reasoning as every other session/group reference in this schema (see
// migrations/0007_self_convo.sql's header). KnownGroupID is a reference
// only (no FK either): GroupJID is the value every send actually uses, so a
// later change/deletion of the known-group row never retroactively alters
// an already-saved schedule's destination. EndDate == "" means unlimited/
// non-stop, same as convo.Schedule.
type Schedule struct {
	ID                  string
	Name                string
	SenderSessionID     string
	SenderName          string
	SenderPhone         string
	KnownGroupID        string
	GroupJID            string
	GroupName           string
	Recurrence          string
	Weekdays            string
	IntervalDays        int
	SenderWindowStart   string
	SenderWindowEnd     string
	ReceiverWindowStart string
	ReceiverWindowEnd   string
	LateCutoff          string
	LangIDWeight        int
	Timezone            string
	StartDate           string
	EndDate             string
	State               string
	PausedReason        string
	PausedAt            *time.Time
	LastPlannedDate     string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// ScheduleReceiver is one row of wa_group_broadcast_receivers — deliberately
// the same shape as convo.ScheduleReceiver so the existing
// "delete+reinsert the whole set on update" edit pattern transfers
// unchanged.
type ScheduleReceiver struct {
	ScheduleID  string
	SessionID   string
	SessionName string
	Phone       string
	Position    int
}

// Occurrence is one row of wa_group_broadcast_occurrences — the planner's
// per-(schedule, date) idempotency unit.
type Occurrence struct {
	ID             string
	ScheduleID     string
	OccurrenceDate string
	GroupJID       string
	State          string
	CreatedAt      time.Time
	FinishedAt     *time.Time
}

// Turn is one row of wa_group_broadcast_turns — one actual message: the
// sender's opener OR one receiver's reply. Body/Language/IsLate/LateReason/
// Source are all pre-generated at PLANNING time (see the planner's own doc
// comment for why) and never touched again before the runner dispatches
// them as-is.
type Turn struct {
	ID           string
	OccurrenceID string
	Role         string
	SessionID    string
	SessionName  string
	Language     string
	IsLate       bool
	LateReason   string
	Body         string
	Source       string
	DueAt        time.Time
	State        string
	MessageID    string
	Error        string
	SentAt       *time.Time
	CreatedAt    time.Time
}

var (
	// ErrNotFound is returned by any lookup/mutation keyed on an id that has
	// no matching row.
	ErrNotFound = errors.New("groupbroadcast: not found")
	// ErrScheduleFinished is returned by SetScheduleState when the schedule
	// has already reached a terminal state (finished/stopped) — mirrors
	// convo.ErrScheduleFinished.
	ErrScheduleFinished = errors.New("groupbroadcast: schedule already finished")
	// ErrTurnNotPending is returned by ClaimTurn when the turn is no longer
	// pending — mirrors convo.ErrTurnNotPending.
	ErrTurnNotPending = errors.New("groupbroadcast: turn is no longer pending")
)

// IsScheduleTerminal reports whether state is one a schedule never leaves
// once reached — mirrors convo.IsScheduleTerminal.
func IsScheduleTerminal(state string) bool {
	switch state {
	case ScheduleFinished, ScheduleStopped:
		return true
	default:
		return false
	}
}

// IsOccurrenceTerminal reports whether state is one an occurrence never
// leaves once reached.
func IsOccurrenceTerminal(state string) bool {
	switch state {
	case OccurrenceDone, OccurrenceFailed, OccurrenceCancelled:
		return true
	default:
		return false
	}
}
