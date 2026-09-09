// Package convo is the persistence model for Self Convo — simulated
// two-party conversations between two of WSM's own paired sessions, driven
// by a persisted state machine that a runner goroutine polls. See
// migrations/0007_self_convo.sql for the schema this package sits on.
package convo

import (
	"errors"
	"time"
)

// Run states. A run moves scheduled -> running -> (done | failed) or is
// cancelled from either of the first two states. There is no "paused".
const (
	RunScheduled = "scheduled"
	RunRunning   = "running"
	RunDone      = "done"
	RunFailed    = "failed"
	RunCancelled = "cancelled"
)

// Turn states. A turn starts pending, moves to sending immediately before
// the send is enqueued, and lands on exactly one terminal state: sent on
// success, failed on a send error, interrupted if the process restarted
// while it was mid-send, or skipped if its run finished before it was ever
// picked up.
const (
	TurnPending     = "pending"
	TurnSending     = "sending"
	TurnSent        = "sent"
	TurnFailed      = "failed"
	TurnInterrupted = "interrupted"
	TurnSkipped     = "skipped"
)

// Template is one row of wa_convo_templates — an uploaded/saved JSON script.
// Body is the raw script bytes, opaque to this package the same way
// wa_inbound_messages.body is opaque to the inbound package; only
// internal/selfconvo.Validate ever parses it. Body can be up to
// MaxTemplateBytes (256 KiB, internal/selfconvo/script.go) — GetTemplate is
// the only read meant to carry it; every list view uses TemplateSummary
// instead (see ListTemplateSummaries's doc comment).
type Template struct {
	ID        string
	Name      string
	TurnCount int
	Body      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TemplateSummary is Template without Body — what every list view actually
// renders (name, turn count, timestamps), and the only shape
// ListTemplateSummaries can produce: there is no field to assign a body
// into even if its SELECT changed later to fetch one.
type TemplateSummary struct {
	ID        string
	Name      string
	TurnCount int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Run is one row of wa_convo_runs. SessionAName/SessionBName and
// PhoneA/PhoneB are snapshots taken at trigger time (see the migration's
// header for why there is deliberately no foreign key to wa_sessions here).
type Run struct {
	ID           string
	TemplateID   string
	TemplateName string
	SessionAID   string
	SessionBID   string
	SessionAName string
	SessionBName string
	PhoneA       string
	PhoneB       string
	State        string
	TurnCount    int
	TurnsDone    int
	StartAt      time.Time
	NextDueAt    *time.Time
	StartedAt    *time.Time
	FinishedAt   *time.Time
	Error        string
	CreatedAt    time.Time
}

// Turn is one row of wa_convo_turns, keyed (RunID, Seq). MessageID, when
// set, is the wa_messages.id the send landed as.
type Turn struct {
	RunID        string
	Seq          int
	Speaker      string
	SessionID    string
	ToPhone      string
	Body         string
	DelaySeconds int
	State        string
	MessageID    string
	Error        string
	SentAt       *time.Time
}

var (
	// ErrNotFound is returned by any lookup/mutation keyed on an id that has
	// no matching row.
	ErrNotFound = errors.New("convo: not found")
	// ErrRunFinished is returned by CancelRun when the run has already
	// reached a terminal state — cancelling it again would be a no-op that
	// silently hides a caller's mistaken assumption.
	ErrRunFinished = errors.New("convo: run already finished")
	// ErrTurnNotPending is returned by ClaimTurn when the turn is no longer
	// pending — most often a concurrent CancelRun moved it to skipped
	// between the runner's NextPendingTurn read and this claim. The runner
	// treats it as "someone else already finished this run" and does not
	// send.
	ErrTurnNotPending = errors.New("convo: turn is no longer pending")
)

// IsTerminal reports whether state is one a run never leaves once reached.
func IsTerminal(state string) bool {
	switch state {
	case RunDone, RunFailed, RunCancelled:
		return true
	default:
		return false
	}
}
