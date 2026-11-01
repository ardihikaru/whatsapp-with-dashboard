package handler

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	sched "github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/statusschedule"
)

// newAIScheduleServer is the schedule harness with the AI client wired
// (available) or not.
func newAIScheduleServer(t *testing.T, available bool) *scheduleHarness {
	t.Helper()
	return newStatusScheduleTestServerWith(t, nil, &callLog{}, func(d *Deps) { d.StatusAIAvailable = available })
}

func (h *scheduleHarness) aiForm(mut func(url.Values)) url.Values {
	f := h.validForm()
	f.Set("ai_enabled", "1")
	f.Set("ai_topic", "  semangat pagi untuk pelanggan  ")
	f.Set("text", "")
	f["recipients"] = []string{"628110000001@s.whatsapp.net"}
	if mut != nil {
		mut(f)
	}
	return f
}

func (h *scheduleHarness) noScheduleSaved(t *testing.T) {
	t.Helper()
	if list, _ := h.repo.List(context.Background(), 10); len(list) != 0 {
		t.Errorf("%d schedule(s) saved from a submission that should have been rejected", len(list))
	}
}

func TestEnablingAIRequiresATopicAndNoSharedLimitIsExceeded(t *testing.T) {
	h := newAIScheduleServer(t, true)
	cases := []struct {
		name, want string
		mut        func(url.Values)
	}{
		{"no topic", "topic is required", func(v url.Values) { v.Set("ai_topic", "") }},
		{"blank topic", "topic is required", func(v url.Values) { v.Set("ai_topic", "   \n ") }},
		{"topic too long", "topic is over 500 characters", func(v url.Values) { v.Set("ai_topic", strings.Repeat("é", 501)) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := postFormAsAdmin(t, h.srv, h.sessions, "/status/schedules", h.aiForm(c.mut))
			if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), c.want) {
				t.Fatalf("POST = %d, want a 200 re-render containing %q; body:\n%s", res.Code, c.want, res.Body.String())
			}
		})
	}
	h.noScheduleSaved(t)
}

func TestEnablingAIIsRejectedServerSideWhenNoAIClientIsConfigured(t *testing.T) {
	h := newAIScheduleServer(t, false)
	res := postFormAsAdmin(t, h.srv, h.sessions, "/status/schedules", h.aiForm(nil))
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "AI writing is not configured") {
		t.Fatalf("POST = %d, want a 200 re-render explaining AI is not configured; body:\n%s", res.Code, res.Body.String())
	}
	h.noScheduleSaved(t)
}

func TestAnAIScheduleNeedsNoStaticTextAndStoresTheTrimmedTopic(t *testing.T) {
	h := newAIScheduleServer(t, true)
	res := postFormAsAdmin(t, h.srv, h.sessions, "/status/schedules", h.aiForm(nil))
	if res.Code != http.StatusSeeOther {
		t.Fatalf("POST = %d, want 303; body:\n%s", res.Code, res.Body.String())
	}
	sc := h.onlySchedule(t)
	if !sc.AIEnabled || sc.AITopic != "semangat pagi untuk pelanggan" || sc.Text != "" {
		t.Errorf("stored: ai=%v topic=%q text=%q", sc.AIEnabled, sc.AITopic, sc.Text)
	}
}

func TestAnAIScheduleKeepsAnOptionalStaticTextAsTheFallback(t *testing.T) {
	h := newAIScheduleServer(t, true)
	res := postFormAsAdmin(t, h.srv, h.sessions, "/status/schedules", h.aiForm(func(v url.Values) { v.Set("text", "Selamat pagi") }))
	if res.Code != http.StatusSeeOther {
		t.Fatalf("POST = %d", res.Code)
	}
	if sc := h.onlySchedule(t); !sc.AIEnabled || sc.Text != "Selamat pagi" {
		t.Errorf("stored: %+v", sc)
	}
}

func TestSwitchingAIOffRestoresTheRequiredTextRule(t *testing.T) {
	h := newAIScheduleServer(t, true)
	form := h.aiForm(nil)
	form.Del("ai_enabled") // the box is unticked; the topic field still arrives
	res := postFormAsAdmin(t, h.srv, h.sessions, "/status/schedules", form)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "status text is required") {
		t.Fatalf("AI off + no text = %d, want the usual text-required rejection; body:\n%s", res.Code, res.Body.String())
	}
	h.noScheduleSaved(t)
}

func TestAnImageScheduleWithAIAndNoCaptionIsSaved(t *testing.T) {
	h := newStatusScheduleTestServerWith(t, &fakeObjectStore{log: &callLog{}}, &callLog{}, func(d *Deps) { d.StatusAIAvailable = true })
	var fields []struct{ key, value string }
	for _, f := range scheduleFields(h, "image") {
		if f.key != "caption" {
			fields = append(fields, f)
		}
	}
	fields = append(fields, struct{ key, value string }{"ai_enabled", "1"}, struct{ key, value string }{"ai_topic", "promo"})
	res := postMultipartAsAdmin(t, h.srv, h.sessions, "/status/schedules", fields, "file_image", "a.jpg", jpegMagic)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("POST = %d; body:\n%s", res.Code, res.Body.String())
	}
	if sc := h.onlySchedule(t); !sc.AIEnabled || sc.Caption != "" || sc.AITopic != "promo" {
		t.Errorf("stored: %+v", sc)
	}
}

func TestEditRoundTripsTheAISettingsAndSwitchingOffClearsTheTopic(t *testing.T) {
	h := newAIScheduleServer(t, true)
	if res := postFormAsAdmin(t, h.srv, h.sessions, "/status/schedules", h.aiForm(nil)); res.Code != http.StatusSeeOther {
		t.Fatalf("create = %d", res.Code)
	}
	sc := h.onlySchedule(t)

	body := authedGet(t, h.srv, h.sessions, "/status/schedules/"+sc.ID+"/edit").Body.String()
	box := regexp.MustCompile(`<input type="checkbox" name="ai_enabled"[^>]*>`).FindString(body)
	if !strings.Contains(box, " checked") || strings.Contains(box, "disabled") {
		t.Errorf("edit form's AI switch should be checked and enabled: %q", box)
	}
	if !strings.Contains(body, ">semangat pagi untuk pelanggan</textarea>") {
		t.Error("edit form did not round-trip the topic")
	}
	if !strings.Contains(body, "AI-written") {
		t.Error("edit form's summary line does not say AI-written")
	}
	if !strings.Contains(body, ">29 / 500<") {
		t.Errorf("topic counter not rendered with the stored length (29 / 500)")
	}

	off := h.validForm() // no ai_enabled, has text
	off["recipients"] = []string{"628110000001@s.whatsapp.net"}
	if res := postFormAsAdmin(t, h.srv, h.sessions, "/status/schedules/"+sc.ID, off); res.Code != http.StatusSeeOther {
		t.Fatalf("update = %d", res.Code)
	}
	got := h.onlySchedule(t)
	if got.AIEnabled || got.AITopic != "" || got.Text != "Good morning" {
		t.Errorf("after switching AI off: %+v", got)
	}
}

func TestEditingAnAIScheduleClearsTheStalePlannedWording(t *testing.T) {
	h := newAIScheduleServer(t, true)
	if res := postFormAsAdmin(t, h.srv, h.sessions, "/status/schedules", h.aiForm(nil)); res.Code != http.StatusSeeOther {
		t.Fatalf("create = %d", res.Code)
	}
	sc := h.onlySchedule(t)
	at := time.Date(2026, 11, 3, 1, 0, 0, 0, time.UTC)
	if err := h.repo.MarkPlannedWithText(context.Background(), sc.ID, "2026-11-03", at, "old topic text", sched.TextSourceAI, "old topic text"); err != nil {
		t.Fatal(err)
	}
	f := h.aiForm(func(v url.Values) { v.Set("ai_topic", "a different topic") })
	if res := postFormAsAdmin(t, h.srv, h.sessions, "/status/schedules/"+sc.ID, f); res.Code != http.StatusSeeOther {
		t.Fatalf("update = %d", res.Code)
	}
	if got := h.onlySchedule(t); got.NextText != "" || got.AITopic != "a different topic" {
		t.Errorf("a planned text for the OLD topic survived the edit: %+v", got)
	}
}

func TestTheFormDisablesAndExplainsTheAISwitchWhenAIIsUnavailable(t *testing.T) {
	for _, available := range []bool{false, true} {
		h := newAIScheduleServer(t, available)
		body := authedGet(t, h.srv, h.sessions, "/status/schedules/new").Body.String()
		box := regexp.MustCompile(`<input type="checkbox" name="ai_enabled"[^>]*>`).FindString(body)
		if box == "" {
			t.Fatalf("available=%v: the form has no ai_enabled checkbox", available)
		}
		if got := strings.Contains(box, "disabled"); got == available {
			t.Errorf("available=%v: switch disabled=%v: %q", available, got, box)
		}
		if got := strings.Contains(body, "data-ss-ai-unavailable"); got == available {
			t.Errorf("available=%v: unavailable explanation shown=%v", available, got)
		}
	}
}

func TestTheFormMarkupWiresTheSwitchTopicAndCounter(t *testing.T) {
	h := newAIScheduleServer(t, true)
	body := authedGet(t, h.srv, h.sessions, "/status/schedules/new").Body.String()
	for _, want := range []string{
		`id="ss-ai"`, `aria-controls="ss-ai-topic-panel"`, `id="ss-ai-topic-panel"`, `name="ai_topic"`,
		`maxlength="500"`, `data-ss-ai-count`, "Write each post with AI", "ss-lbl-ai",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("form lacks %q", want)
		}
	}
	// The switch must precede the radios: status-schedule.css reaches the
	// topic field and fallback labels with `#ss-ai:checked ~ …`.
	if strings.Index(body, `id="ss-ai"`) > strings.Index(body, `id="ss-kind-text"`) {
		t.Error("the AI checkbox comes after the kind radios; the sibling selectors in status-schedule.css would not reach")
	}
}

func TestTheSummarySentenceSaysAIWrittenOnlyWhenAIIsOn(t *testing.T) {
	on := statusScheduleSummary([]string{"1"}, "07:00", "10:00", "UTC", true)
	off := statusScheduleSummary([]string{"1"}, "07:00", "10:00", "UTC", false)
	if !strings.HasSuffix(on, " · AI-written") || strings.Contains(off, "AI-written") {
		t.Errorf("on=%q off=%q", on, off)
	}
}

func TestTheListRowShowsTheTruncatedPlannedTextWithItsSource(t *testing.T) {
	h := newAIScheduleServer(t, true)
	long := strings.Repeat("kopi ", 40) + "TAILMARKER"
	sc := &sched.Schedule{
		ID: "r1", Name: "Row", SessionID: h.session.ID, Kind: sched.KindText, Text: "x", Weekdays: "1",
		WindowStart: "07:00", WindowEnd: "10:00", Timezone: "UTC", StartDate: "2026-11-01",
		RecipientsMode: sched.ModeAll, State: sched.StateActive, AIEnabled: true, AITopic: "t",
		NextFireAt: time.Now().Add(time.Hour).UTC().Truncate(time.Second), NextText: long, NextTextSource: sched.TextSourceAI,
	}
	if err := h.repo.Insert(context.Background(), sc); err != nil {
		t.Fatal(err)
	}
	body := authedGet(t, h.srv, h.sessions, "/status/schedules/rows").Body.String()
	m := regexp.MustCompile(`(?s)<small class="muted ss-next-text"[^>]*>(.*?)</small>`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no planned-text line in the row:\n%s", body)
	}
	if !strings.Contains(m[1], `data-source="AI"`) || !strings.Contains(m[1], "…") || strings.Contains(m[1], "TAILMARKER") {
		t.Errorf("planned-text cell = %q, want an AI badge and a truncated text ending in an ellipsis", m[1])
	}

	// A static plan is labelled static; no pending slot shows no text at all.
	_ = h.repo.MarkPlannedWithText(context.Background(), "r1", "2026-11-02", sc.NextFireAt, "fallback text", sched.TextSourceStatic, "")
	body = authedGet(t, h.srv, h.sessions, "/status/schedules/rows").Body.String()
	if !strings.Contains(body, `data-source="static"`) {
		t.Error("a static-fallback plan is not labelled static")
	}
	_ = h.repo.MarkPlanned(context.Background(), "r1", "2026-11-02", time.Time{})
	body = authedGet(t, h.srv, h.sessions, "/status/schedules/rows").Body.String()
	if strings.Contains(body, "ss-next-text") {
		t.Error("a row with no pending slot still shows planned text")
	}
}
