package handler

import (
	"net/http"
	"strings"
	"time"

	sa "github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/sessionalert"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/schedclock"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// settingsAlertsPageData feeds /settings/alerts — the logged-out alert and
// daily non-connected report configuration (internal/sessionalert). Form
// carries the operator's just-submitted (or freshly loaded) values;
// Sessions/SelfReminderName let the page show the current self-reminder
// session as the default sender.
type settingsAlertsPageData struct {
	PageBase
	Form             sa.Settings
	Sessions         []wa.Snapshot
	SelfReminderName string
	Saved            bool
	TestSent         bool
	Error            string
}

// sessionAlertSettings loads the current settings, or DefaultSettings()
// when h.SessionAlertSettings is nil — the same "nil repo renders a usable
// default, never a 500" convention knownGroups/contacts already follow.
func (h *handlers) sessionAlertSettings(r *http.Request) sa.Settings {
	if h.SessionAlertSettings == nil {
		return sa.DefaultSettings()
	}
	s, err := h.SessionAlertSettings.Get(r.Context())
	if err != nil {
		return sa.DefaultSettings()
	}
	return s
}

func (h *handlers) selfReminderSessionName() string {
	for _, snap := range h.Manager.Snapshots() {
		if snap.SelfReminder {
			return snap.Name
		}
	}
	return ""
}

// GetSettingsAlerts renders the settings page.
func (h *handlers) GetSettingsAlerts(w http.ResponseWriter, r *http.Request) {
	data := settingsAlertsPageData{
		PageBase:         h.newPageBase(r, "Session Alerts", "/settings/alerts"),
		Form:             h.sessionAlertSettings(r),
		Sessions:         h.Manager.Snapshots(),
		SelfReminderName: h.selfReminderSessionName(),
	}
	h.Renderer.RenderPage(w, "settings_alerts", data)
}

// parseAlertSettingsForm reads and validates the form into a sa.Settings,
// returning the first validation error (if any) so the caller can
// re-render inline with every typed value kept.
func parseAlertSettingsForm(r *http.Request) (sa.Settings, string) {
	s := sa.Settings{
		LogoutAlertEnabled: parseFormBool(r.FormValue("logout_alert_enabled")),
		DailyReportEnabled: parseFormBool(r.FormValue("daily_report_enabled")),
		DailyTime:          strings.TrimSpace(r.FormValue("daily_time")),
		Timezone:           strings.TrimSpace(r.FormValue("timezone")),
		// Field names "group_jid"/"name" (not "target_group_jid"/
		// "target_group_name") deliberately match what GET /groups/picker's
		// own JS (known-groups.js) fills on a pick — see known_group_picker.html
		// — so this page can reuse that partial unmodified.
		TargetGroupJID:  strings.TrimSpace(r.FormValue("group_jid")),
		TargetGroupName: strings.TrimSpace(r.FormValue("name")),
		SenderSessionID: strings.TrimSpace(r.FormValue("sender_session_id")),
	}
	if s.DailyTime == "" {
		s.DailyTime = "08:00"
	}
	if s.Timezone == "" {
		s.Timezone = "Asia/Jakarta"
	}
	if _, err := schedclock.ParseClock(s.DailyTime); err != nil {
		return s, "daily_time must be a valid HH:MM time"
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil {
		return s, "timezone is not a recognised IANA zone name"
	}
	if s.TargetGroupJID != "" {
		jid, err := wa.ParseGroupJID(s.TargetGroupJID)
		if err != nil {
			return s, "target group jid is not valid: " + err.Error()
		}
		s.TargetGroupJID = jid.String()
	} else if s.LogoutAlertEnabled || s.DailyReportEnabled {
		return s, "a target group is required to enable either alert"
	}
	return s, ""
}

// PostSettingsAlerts validates and saves the settings form.
func (h *handlers) PostSettingsAlerts(w http.ResponseWriter, r *http.Request) {
	form, errMsg := parseAlertSettingsForm(r)
	data := settingsAlertsPageData{
		PageBase:         h.newPageBase(r, "Session Alerts", "/settings/alerts"),
		Form:             form,
		Sessions:         h.Manager.Snapshots(),
		SelfReminderName: h.selfReminderSessionName(),
	}
	if errMsg != "" {
		data.Error = errMsg
		h.Renderer.RenderPage(w, "settings_alerts", data)
		return
	}
	if h.SessionAlertSettings == nil {
		data.Error = "alert settings storage is unavailable"
		h.Renderer.RenderPage(w, "settings_alerts", data)
		return
	}
	if err := h.SessionAlertSettings.Save(r.Context(), form); err != nil {
		data.Error = "could not save settings"
		h.Renderer.RenderPage(w, "settings_alerts", data)
		return
	}
	data.Saved = true
	h.Renderer.RenderPage(w, "settings_alerts", data)
}

// PostSettingsAlertsTest is the "Send test message" button — it drives
// the SAME sender-selection path a real alert would take
// (sessionalert.Service.SendTest), so a successful click is real evidence
// the configured group/sender actually works, not just that the form
// saved.
func (h *handlers) PostSettingsAlertsTest(w http.ResponseWriter, r *http.Request) {
	data := settingsAlertsPageData{
		PageBase:         h.newPageBase(r, "Session Alerts", "/settings/alerts"),
		Form:             h.sessionAlertSettings(r),
		Sessions:         h.Manager.Snapshots(),
		SelfReminderName: h.selfReminderSessionName(),
	}
	if h.SessionAlert == nil {
		data.Error = "the alert service is not running on this deployment"
		h.Renderer.RenderPage(w, "settings_alerts", data)
		return
	}
	if err := h.SessionAlert.SendTest(r.Context()); err != nil {
		data.Error = "test message failed: " + err.Error()
		h.Renderer.RenderPage(w, "settings_alerts", data)
		return
	}
	data.TestSent = true
	h.Renderer.RenderPage(w, "settings_alerts", data)
}
