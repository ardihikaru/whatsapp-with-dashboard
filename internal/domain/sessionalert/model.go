// Package sessionalert is the persistence model for the single-row
// wa_session_alert_settings table (migrations/0016_session_alerts.sql) —
// the operator's configuration for internal/sessionalert's logged-out
// alert and daily non-connected report. The running service lives in
// internal/sessionalert; this package only stores what must survive a
// restart.
package sessionalert

import "time"

// Settings is wa_session_alert_settings' single row (id always 1).
type Settings struct {
	LogoutAlertEnabled bool
	DailyReportEnabled bool
	// DailyTime is "HH:MM", local to Timezone — parsed by
	// schedclock.ParseClock, the same shape
	// wa_status_schedules.window_start/_end uses.
	DailyTime string
	Timezone  string
	// TargetGroupJID/TargetGroupName name the WhatsApp group both alert
	// kinds post to. Name is display-only, never sent to WhatsApp.
	TargetGroupJID  string
	TargetGroupName string
	// SenderSessionID is which session POSTS the alert. "" means "use
	// whichever session currently carries wa_sessions.is_self_reminder" —
	// see internal/sessionalert's sender-fallback order.
	SenderSessionID string
	// LastDailyDate is the local date (schedclock.LocalDate) the daily
	// report was last actually sent for — claimed BEFORE sending via
	// Repository.ClaimDailyDate, so a slow send or a crash mid-post cannot
	// produce two reports for one day.
	LastDailyDate string
	UpdatedAt     time.Time
}

// DefaultSettings is what Repository.Get returns when no row exists yet
// (the first read on a fresh install) — every field "off" until an
// operator opts in through /settings/alerts, matching
// 0016_session_alerts.sql's own column defaults exactly so a direct SQL
// read and this Go default never disagree.
func DefaultSettings() Settings {
	return Settings{DailyTime: "08:00", Timezone: "Asia/Jakarta"}
}
