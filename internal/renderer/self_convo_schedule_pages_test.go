package renderer_test

import (
	"html/template"
	"net/http/httptest"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/renderer"
)

// The three mirror types below are shaped to match handler.PageBase/
// NavSection/NavItem field-for-field — the same "shaped to match" convention
// basepath_test.go's rowSession already documents for wa.Snapshot: a base
// (chrome) page's root template ("base") walks .Nav/.PlatformLink/.Title/
// .Username/.Theme/.DarkModeEnabled via base.html + partials/sidebar.html +
// partials/header.html + partials/theme_toggle.html, and reflection only
// cares about field names/shapes, not which package declares them.
type navItemMirror struct {
	Title, Path string
	Icon        template.HTML
	Active      bool
}

type navSectionMirror struct {
	Subheader string
	Items     []navItemMirror
}

type pageBaseMirror struct {
	Title, Username, Theme string
	DarkModeEnabled        bool
	Nav                    []navSectionMirror
	PlatformLink           navItemMirror
	NeedECharts            bool
}

// TestConvoSchedulingPagesAreRegisteredAndParse is this package's own
// version of the phase brief's required check: renderer.New already proves
// every pageDefs entry's template SET parses (TestNewParsesEveryPageAndPartial,
// above) — this test additionally proves the three NEW page names Phase 6
// added are actually reachable through RenderPage (i.e. actually present as
// pageDefs keys, not merely files that happen to parse as part of some
// other page's set) and execute without error against realistic data.
func TestConvoSchedulingPagesAreRegisteredAndParse(t *testing.T) {
	r, err := renderer.New(webFS(t), "", nil)
	if err != nil {
		t.Fatalf("parse embedded templates: %v", err)
	}
	base := pageBaseMirror{Title: "t", Username: "admin", Theme: "light"}

	t.Run("self_convo_schedules", func(t *testing.T) {
		data := struct {
			pageBaseMirror
			Enabled bool
		}{pageBaseMirror: base, Enabled: true}
		rec := httptest.NewRecorder()
		r.RenderPage(rec, "self_convo_schedules", data)
		if rec.Code != 200 {
			t.Fatalf("RenderPage self_convo_schedules = %d, want 200: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("self_convo_schedule_form", func(t *testing.T) {
		type formMirror struct {
			Name, SenderID                   string
			ReceiverIDs                      []string
			Topic, RootTemplateID            string
			ChainEnabled                     bool
			Recurrence                       string
			Weekdays                         []string
			IntervalDays, SetsPerDay         string
			WindowStart, WindowEnd, Timezone string
			StartDate, EndDate               string
		}
		type sessionMirror struct{ ID, Name, Phone string }
		type topicMirror struct {
			Topic string
			Count int
		}
		type weekdayMirror struct {
			Value, Label string
			Checked      bool
		}
		data := struct {
			pageBaseMirror
			Enabled     bool
			IsEdit      bool
			ScheduleID  string
			Form        formMirror
			Error       string
			Sessions    []sessionMirror
			Topics      []topicMirror
			Timezones   []string
			Weekdays    []weekdayMirror
			ReceiverSet map[string]bool
		}{
			pageBaseMirror: base, Enabled: true,
			Form:      formMirror{Recurrence: "daily", SetsPerDay: "1", WindowStart: "09:00", WindowEnd: "17:00", Timezone: "Asia/Jakarta"},
			Sessions:  []sessionMirror{{ID: "s1", Name: "Alpha", Phone: "62800000001"}},
			Topics:    []topicMirror{{Topic: "futsal", Count: 48}},
			Timezones: []string{"Asia/Jakarta", "UTC"},
			Weekdays: []weekdayMirror{
				{Value: "1", Label: "Mon"}, {Value: "2", Label: "Tue"},
			},
			ReceiverSet: map[string]bool{},
		}
		rec := httptest.NewRecorder()
		r.RenderPage(rec, "self_convo_schedule_form", data)
		if rec.Code != 200 {
			t.Fatalf("RenderPage self_convo_schedule_form = %d, want 200: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("self_convo_schedule_view", func(t *testing.T) {
		type scheduleMirror struct {
			ID, Name, Topic                     string
			SenderName, SenderPhone             string
			RecurrenceLabel, WindowLabel        string
			SetsPerDay                          int
			StartDate, EndDate                  string
			NoEndDate, ChainEnabled             bool
			RootTemplateID, State, PausedReason string
		}
		type receiverMirror struct{ SessionName, Phone string }
		data := struct {
			pageBaseMirror
			Enabled   bool
			Schedule  scheduleMirror
			Receivers []receiverMirror
		}{
			pageBaseMirror: base, Enabled: true,
			Schedule:  scheduleMirror{ID: "sch-1", Name: "Weekend chat", Topic: "futsal", State: "active", NoEndDate: true},
			Receivers: []receiverMirror{{SessionName: "Beta", Phone: "62800000002"}},
		}
		rec := httptest.NewRecorder()
		r.RenderPage(rec, "self_convo_schedule_view", data)
		if rec.Code != 200 {
			t.Fatalf("RenderPage self_convo_schedule_view = %d, want 200: %s", rec.Code, rec.Body.String())
		}
	})
}
