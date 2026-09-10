package convo

import "time"

// Schedule states (wa_convo_schedules.state). `paused` is
// operator-initiated; `stalled` is the automatic health stop raised when a
// participating session goes down, with PausedReason recording why. Both
// are resumable via SetScheduleState. `finished`/`stopped` are terminal —
// see IsScheduleTerminal.
const (
	ScheduleActive   = "active"
	SchedulePaused   = "paused"
	ScheduleStalled  = "stalled"
	ScheduleFinished = "finished"
	ScheduleStopped  = "stopped"
)

// Recurrence kinds a schedule may declare (wa_convo_schedules.recurrence).
// Weekdays is only meaningful when Recurrence is RecurrenceWeekly;
// IntervalDays is only meaningful when Recurrence is RecurrenceInterval.
const (
	RecurrenceDaily    = "daily"
	RecurrenceWeekly   = "weekly"
	RecurrenceInterval = "interval"
)

// Template stage within a topic's arc (wa_convo_templates.stage) — where in
// a conversation's shape a template belongs. The empty string is a
// template with no declared stage, which is every template that existed
// before migrations/0008_convo_scheduling.sql.
const (
	StageOpener = "opener"
	StageMiddle = "middle"
	StageCloser = "closer"
)

// Template origin (wa_convo_templates.origin) distinguishes an
// operator-uploaded script from one a later phase bulk-generates into the
// corpus, so that phase can replace every corpus row (DELETE WHERE
// origin=OriginCorpus) without touching an operator's own uploads.
const (
	OriginUpload = "upload"
	OriginCorpus = "corpus"
)

// Schedule is one row of wa_convo_schedules — a recurring plan that
// materialises runs on a cadence, rather than a single operator-triggered
// run. SenderName/SenderPhone are snapshots taken at creation time, no FK,
// same reasoning as Run's SessionAName/PhoneA (see
// migrations/0007_self_convo.sql's header). EndDate == "" means unlimited/
// non-stop; LastPlannedDate == "" means the planner has never materialised
// an occurrence for this schedule yet.
type Schedule struct {
	ID              string
	Name            string
	SenderSessionID string
	SenderName      string
	SenderPhone     string
	Topic           string
	RootTemplateID  string
	ChainEnabled    bool
	Recurrence      string
	Weekdays        string
	IntervalDays    int
	SetsPerDay      int
	WindowStart     string
	WindowEnd       string
	Timezone        string
	StartDate       string
	EndDate         string
	State           string
	PausedReason    string
	PausedAt        *time.Time
	LastPlannedDate string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// ScheduleReceiver is one row of wa_convo_schedule_receivers — one target of
// a schedule; a set fans out to one run per receiver. SessionName/Phone are
// snapshots, same reasoning as Schedule's own sender fields above.
type ScheduleReceiver struct {
	ScheduleID  string
	SessionID   string
	SessionName string
	Phone       string
	Position    int
}

// TemplateLink is one row of wa_convo_template_links — a weighted directed
// edge "after FromTemplateID, ToTemplateID is a plausible next
// conversation". MinGapSeconds/MaxGapSeconds bound the random delay before
// the next run starts.
type TemplateLink struct {
	FromTemplateID string
	ToTemplateID   string
	Weight         int
	MinGapSeconds  int
	MaxGapSeconds  int
	CreatedAt      time.Time
}

// TopicSummary is one row of the topic dropdown a schedule's create/edit
// form offers — a topic name plus how many templates (any stage) exist for
// it, so an operator can see at a glance which topics actually have a
// corpus behind them before picking one. Count is EVERY template for the
// topic (opener/middle/closer alike), which is deliberately broader than
// the opener-only pool PickOpener draws from (chain.go) — see
// TopicSummaries' own doc comment.
type TopicSummary struct {
	Topic string
	Count int
}

// IsScheduleTerminal reports whether state is one a schedule never leaves
// once reached — mirrors IsTerminal for runs.
func IsScheduleTerminal(state string) bool {
	switch state {
	case ScheduleFinished, ScheduleStopped:
		return true
	default:
		return false
	}
}
