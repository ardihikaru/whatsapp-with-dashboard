package handler

import (
	"context"
	"io/fs"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wsm "github.com/ardihikaru/whatsapp-with-dashboard"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/auth"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/apiclient"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/convo"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/wasession"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/renderer"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/selfconvo"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// newScheduleTestServer wires a full handler.New(Deps{...}) server with a
// real *wa.Manager (so PostSelfConvoScheduleCreate/Update can resolve
// sender/receiver names via h.Manager.Get) and a *selfconvo.Planner built
// over gw (never Start'd — every test below calls Pause/Resume/Stop_
// directly through the handler, the same "no background loop, no sleeping"
// convention newSelfConvoTestServerWithFakeGateway follows for Runner).
func newScheduleTestServer(t *testing.T, gw selfconvo.Gateway, enabled bool) (http.Handler, *auth.Store, *wa.Manager, *convo.Repository, *selfconvo.Planner) {
	t.Helper()
	pool, err := db.Open(filepath.Join(t.TempDir(), "test.db"), db.SQLiteDialect)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	if err := db.EnsureSchema(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	webFS, err := fs.Sub(wsm.FS, "web")
	if err != nil {
		t.Fatal(err)
	}
	rend, err := renderer.New(webFS, "")
	if err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewStore(time.Hour)
	t.Cleanup(sessions.Close)
	apiClients := apiclient.NewRepository(pool)
	msgRepo := message.NewRepository(pool)
	convoRepo := convo.NewRepository(pool)
	mgr := wa.NewManager(context.Background(), nil, wasession.NewRepository(pool),
		msgRepo, nil, wa.Config{SendRate: 100, SendBurst: 5, ReconcilePar: 2})
	runner := selfconvo.New(convoRepo, gw, selfconvo.Options{})
	planner := selfconvo.NewPlanner(convoRepo, runner, selfconvo.PlannerOptions{})

	srv := New(Deps{
		Renderer: rend, Sessions: sessions, Middleware: auth.NewMiddleware(sessions, apiClients, ""),
		Manager: mgr, Messages: msgRepo, MessageStats: msgRepo, APIClients: apiClients,
		Convo: convoRepo, SelfConvo: runner, SelfConvoEnabled: enabled, Planner: planner,
		SessionTTL: time.Hour, AssetsFS: wsm.FS,
	})
	return srv, sessions, mgr, convoRepo, planner
}

// insertOpenerTemplate gives topic exactly one opener-stage template — the
// minimum PickOpener (chain.go)/validateScheduleForm both need to accept
// topic at all.
func insertOpenerTemplate(t *testing.T, repo *convo.Repository, id, topic string) {
	t.Helper()
	tpl := &convo.Template{ID: id, Name: id, Topic: topic, Stage: convo.StageOpener, TurnCount: 1, Body: "{}"}
	if err := repo.InsertTemplate(context.Background(), tpl); err != nil {
		t.Fatalf("InsertTemplate %s: %v", id, err)
	}
}

// baseScheduleForm is a form that passes every validation rule as-is —
// every rejection test below starts from a COPY of this and breaks exactly
// one field, so a test failure always points at the rule its name claims.
func baseScheduleForm(senderID string, receiverIDs []string, topic string) url.Values {
	v := url.Values{
		"name":          {"Weekend chat"},
		"sender_id":     {senderID},
		"topic":         {topic},
		"chain_enabled": {"on"},
		"recurrence":    {"daily"},
		"sets_per_day":  {"1"},
		"window_start":  {"09:00"},
		"window_end":    {"17:00"},
		"timezone":      {"Asia/Jakarta"},
		"start_date":    {"2026-09-10"},
	}
	for _, id := range receiverIDs {
		v.Add("receiver_id", id)
	}
	return v
}

// TestSelfConvoSchedulesPageRendersTheDisabledNoticeByDefault pins that the
// list page still renders (200) with the flag off — reviewing/pausing/
// stopping existing schedules must stay possible.
func TestSelfConvoSchedulesPageRendersTheDisabledNoticeByDefault(t *testing.T) {
	srv, sessions, _, _, _ := newScheduleTestServer(t, &fakeUploadTriggerGateway{}, false)
	res := authedGet(t, srv, sessions, "/self-convo/schedules")
	if res.Code != http.StatusOK {
		t.Fatalf("GET /self-convo/schedules = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "Convo scheduling is disabled on this deployment") {
		t.Errorf("the page does not carry the disabled notice; body:\n%s", res.Body.String())
	}
}

// TestSelfConvoScheduleNewPageRenders200RegardlessOfTheFlag pins the
// "form still renders, only saving is refused" rule (trigger.html's own
// convention) for the create form specifically.
func TestSelfConvoScheduleNewPageRenders200RegardlessOfTheFlag(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		srv, sessions, _, _, _ := newScheduleTestServer(t, &fakeUploadTriggerGateway{}, enabled)
		res := authedGet(t, srv, sessions, "/self-convo/schedules/new")
		if res.Code != http.StatusOK {
			t.Fatalf("enabled=%v: GET /self-convo/schedules/new = %d, want 200; body:\n%s", enabled, res.Code, res.Body.String())
		}
	}
}

// TestScheduleViewAndEditPagesRender200RegardlessOfTheFlag pins the "GET
// pages always 200, only mutations are refused" rule for the two remaining
// GET routes (list/new are covered separately above).
func TestScheduleViewAndEditPagesRender200RegardlessOfTheFlag(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		srv, sessions, _, repo, _ := newScheduleTestServer(t, &fakeUploadTriggerGateway{}, enabled)
		ctx := context.Background()
		sc := &convo.Schedule{
			ID: "sch-1", Name: "Weekend chat", SenderSessionID: "sess-sender", SenderName: "Alpha", SenderPhone: "628000001",
			Topic: "futsal", Recurrence: convo.RecurrenceDaily, SetsPerDay: 1,
			WindowStart: "09:00", WindowEnd: "17:00", Timezone: "UTC", StartDate: "2026-09-01", State: convo.ScheduleActive,
		}
		if err := repo.InsertSchedule(ctx, sc, nil); err != nil {
			t.Fatal(err)
		}

		if res := authedGet(t, srv, sessions, "/self-convo/schedules/sch-1"); res.Code != http.StatusOK {
			t.Fatalf("enabled=%v: GET .../sch-1 = %d, want 200; body:\n%s", enabled, res.Code, res.Body.String())
		}
		if res := authedGet(t, srv, sessions, "/self-convo/schedules/sch-1/edit"); res.Code != http.StatusOK {
			t.Fatalf("enabled=%v: GET .../sch-1/edit = %d, want 200; body:\n%s", enabled, res.Code, res.Body.String())
		}
		if !enabled {
			res := authedGet(t, srv, sessions, "/self-convo/schedules/sch-1")
			if !strings.Contains(res.Body.String(), "Convo scheduling is disabled on this deployment") {
				t.Errorf("disabled schedule view does not carry the disabled notice; body:\n%s", res.Body.String())
			}
		}
	}
}

// TestCreatingAScheduleInsertsItsReceiversAndRedirects exercises the whole
// create path through the real handler: it must insert one schedule row,
// insert its receivers, and redirect (not re-render) on success — the same
// "redirect the way the existing POST handlers do" shape api_clients.go's
// PostAPIClientEdit follows.
func TestCreatingAScheduleInsertsItsReceiversAndRedirects(t *testing.T) {
	srv, sessions, mgr, repo, _ := newScheduleTestServer(t, &fakeUploadTriggerGateway{}, true)
	ctx := context.Background()
	sender, err := mgr.Create(ctx, "Alpha", "+62 812-0000-0001")
	if err != nil {
		t.Fatal(err)
	}
	recv1, err := mgr.Create(ctx, "Beta", "+62 812-0000-0002")
	if err != nil {
		t.Fatal(err)
	}
	recv2, err := mgr.Create(ctx, "Gamma", "+62 812-0000-0003")
	if err != nil {
		t.Fatal(err)
	}
	insertOpenerTemplate(t, repo, "tpl-futsal-1", "futsal")

	form := baseScheduleForm(sender.ID, []string{recv1.ID, recv2.ID}, "futsal")
	res := authedPost(t, srv, sessions, "/self-convo/schedules", form)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("POST /self-convo/schedules = %d, want %d; body:\n%s", res.Code, http.StatusSeeOther, res.Body.String())
	}
	if loc := res.Header().Get("Location"); !strings.HasSuffix(loc, "/self-convo/schedules") {
		t.Errorf("Location = %q, want the schedules list", loc)
	}

	scheds, err := repo.ListSchedules(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(scheds) != 1 {
		t.Fatalf("ListSchedules = %d, want exactly 1", len(scheds))
	}
	if scheds[0].SenderSessionID != sender.ID || scheds[0].Topic != "futsal" {
		t.Errorf("schedule = %+v, want sender=%s topic=futsal", scheds[0], sender.ID)
	}
	recs, err := repo.ScheduleReceivers(ctx, scheds[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("ScheduleReceivers = %d, want exactly 2", len(recs))
	}
}

// TestABlankEndDatePersistsAsNullAndRendersAsNoEndDate pins the
// unlimited/non-stop case end to end: the form's end_date left blank must
// come back as EndDate == "" (NULL in storage — schedule_repository.go's
// nullIfEmpty) and the rows partial/detail page must show the "no end
// date" marker, not an empty cell.
func TestABlankEndDatePersistsAsNullAndRendersAsNoEndDate(t *testing.T) {
	srv, sessions, mgr, repo, _ := newScheduleTestServer(t, &fakeUploadTriggerGateway{}, true)
	ctx := context.Background()
	sender, _ := mgr.Create(ctx, "Alpha", "+62 812-0000-0001")
	recv, _ := mgr.Create(ctx, "Beta", "+62 812-0000-0002")
	insertOpenerTemplate(t, repo, "tpl-futsal-1", "futsal")

	form := baseScheduleForm(sender.ID, []string{recv.ID}, "futsal")
	// end_date deliberately absent/blank.
	res := authedPost(t, srv, sessions, "/self-convo/schedules", form)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("create = %d, want %d; body:\n%s", res.Code, http.StatusSeeOther, res.Body.String())
	}

	scheds, err := repo.ListSchedules(ctx, 50)
	if err != nil || len(scheds) != 1 {
		t.Fatalf("ListSchedules = %+v, %v", scheds, err)
	}
	if scheds[0].EndDate != "" {
		t.Errorf("EndDate = %q, want empty (NULL)", scheds[0].EndDate)
	}

	rows := authedGet(t, srv, sessions, "/self-convo/schedules/rows")
	if !strings.Contains(rows.Body.String(), "badge-nonstop") || !strings.Contains(rows.Body.String(), "no end date") {
		t.Errorf("rows partial does not render the no-end-date marker; body:\n%s", rows.Body.String())
	}

	view := authedGet(t, srv, sessions, "/self-convo/schedules/"+scheds[0].ID)
	if view.Code != http.StatusOK {
		t.Fatalf("GET schedule view = %d, want 200; body:\n%s", view.Code, view.Body.String())
	}
	if !strings.Contains(view.Body.String(), "badge-nonstop") {
		t.Errorf("schedule view does not render the no-end-date marker; body:\n%s", view.Body.String())
	}
}

// scheduleValidationCase is one row of the rejection table below: name the
// rule, break exactly one field of an otherwise-valid form, and name the
// message the rejection must carry.
type scheduleValidationCase struct {
	name     string
	mutate   func(v url.Values, sender, recv string)
	wantText string
}

func TestScheduleCreateValidationRejectsEachRuleAndInsertsNothing(t *testing.T) {
	cases := []scheduleValidationCase{
		{
			name:     "empty name",
			mutate:   func(v url.Values, sender, recv string) { v.Set("name", "") },
			wantText: "name is required",
		},
		{
			name:     "name over 120 characters",
			mutate:   func(v url.Values, sender, recv string) { v.Set("name", strings.Repeat("x", 121)) },
			wantText: "name is over 120 characters",
		},
		{
			name:     "unknown sender session",
			mutate:   func(v url.Values, sender, recv string) { v.Set("sender_id", "does-not-exist") },
			wantText: "unknown sender session",
		},
		{
			name:     "no receivers ticked",
			mutate:   func(v url.Values, sender, recv string) { v.Del("receiver_id") },
			wantText: "tick at least one receiver",
		},
		{
			name:     "sender in receivers",
			mutate:   func(v url.Values, sender, recv string) { v.Add("receiver_id", sender) },
			wantText: "the sender cannot also be a receiver",
		},
		{
			name:     "unknown receiver session",
			mutate:   func(v url.Values, sender, recv string) { v.Add("receiver_id", "does-not-exist") },
			wantText: "unknown receiver session",
		},
		{
			name:     "topic with no opener templates",
			mutate:   func(v url.Values, sender, recv string) { v.Set("topic", "no-such-topic") },
			wantText: "no opener templates for topic",
		},
		{
			name:     "window end not after window start",
			mutate:   func(v url.Values, sender, recv string) { v.Set("window_end", "08:00") },
			wantText: "window end must be after window start",
		},
		{
			name:     "sets per day out of range (0)",
			mutate:   func(v url.Values, sender, recv string) { v.Set("sets_per_day", "0") },
			wantText: "sets per day must be between 1 and 10",
		},
		{
			name:     "sets per day out of range (11)",
			mutate:   func(v url.Values, sender, recv string) { v.Set("sets_per_day", "11") },
			wantText: "sets per day must be between 1 and 10",
		},
		{
			name:     "weekly with no weekday ticked",
			mutate:   func(v url.Values, sender, recv string) { v.Set("recurrence", "weekly") },
			wantText: "tick at least one weekday for a weekly schedule",
		},
		{
			name: "interval with interval_days < 1",
			mutate: func(v url.Values, sender, recv string) {
				v.Set("recurrence", "interval")
				v.Set("interval_days", "0")
			},
			wantText: "interval must be at least 1 day",
		},
		{
			name:     "end date before start date",
			mutate:   func(v url.Values, sender, recv string) { v.Set("end_date", "2026-01-01") },
			wantText: "end date must not be before start date",
		},
		{
			name:     "unknown timezone",
			mutate:   func(v url.Values, sender, recv string) { v.Set("timezone", "Not/AZone") },
			wantText: "unknown timezone",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, sessions, mgr, repo, _ := newScheduleTestServer(t, &fakeUploadTriggerGateway{}, true)
			ctx := context.Background()
			sender, _ := mgr.Create(ctx, "Alpha", "+62 812-0000-0001")
			recv, _ := mgr.Create(ctx, "Beta", "+62 812-0000-0002")
			insertOpenerTemplate(t, repo, "tpl-futsal-1", "futsal")

			form := baseScheduleForm(sender.ID, []string{recv.ID}, "futsal")
			tc.mutate(form, sender.ID, recv.ID)

			res := authedPost(t, srv, sessions, "/self-convo/schedules", form)
			if res.Code != http.StatusOK {
				t.Fatalf("POST /self-convo/schedules = %d, want 200 (re-rendered form); body:\n%s", res.Code, res.Body.String())
			}
			if !strings.Contains(res.Body.String(), tc.wantText) {
				t.Errorf("body does not carry %q; body:\n%s", tc.wantText, res.Body.String())
			}
			scheds, err := repo.ListSchedules(ctx, 50)
			if err != nil {
				t.Fatal(err)
			}
			if len(scheds) != 0 {
				t.Errorf("ListSchedules = %d, want 0 — a rejected submission must insert nothing", len(scheds))
			}
		})
	}
}

// TestPauseThenResumeRoundTripsAnActiveSchedulesState pins the happy path:
// pause moves active -> paused, resume (with every participant healthy)
// moves it straight back to active.
func TestPauseThenResumeRoundTripsAnActiveSchedulesState(t *testing.T) {
	gw := &fakeUploadTriggerGateway{participants: map[string]selfconvo.Participant{
		"sess-sender": {ID: "sess-sender", Name: "Alpha", Phone: "628000001", Connected: true},
		"sess-recv":   {ID: "sess-recv", Name: "Beta", Phone: "628000002", Connected: true},
	}}
	srv, sessions, _, repo, _ := newScheduleTestServer(t, gw, true)
	ctx := context.Background()
	sc := &convo.Schedule{
		ID: "sch-1", Name: "Weekend chat", SenderSessionID: "sess-sender", SenderName: "Alpha", SenderPhone: "628000001",
		Topic: "futsal", Recurrence: convo.RecurrenceDaily, SetsPerDay: 1,
		WindowStart: "09:00", WindowEnd: "17:00", Timezone: "UTC", StartDate: "2026-09-01", State: convo.ScheduleActive,
	}
	recs := []convo.ScheduleReceiver{{SessionID: "sess-recv", SessionName: "Beta", Phone: "628000002", Position: 0}}
	if err := repo.InsertSchedule(ctx, sc, recs); err != nil {
		t.Fatal(err)
	}

	res := authedPost(t, srv, sessions, "/self-convo/schedules/sch-1/pause", url.Values{})
	if res.Code != http.StatusOK {
		t.Fatalf("pause = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "badge-paused") {
		t.Errorf("rows do not show the paused badge; body:\n%s", res.Body.String())
	}
	got, err := repo.GetSchedule(ctx, "sch-1")
	if err != nil || got.State != convo.SchedulePaused {
		t.Fatalf("GetSchedule after pause = %+v, %v, want state=paused", got, err)
	}

	res = authedPost(t, srv, sessions, "/self-convo/schedules/sch-1/resume", url.Values{})
	if res.Code != http.StatusOK {
		t.Fatalf("resume = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "badge-active") {
		t.Errorf("rows do not show the active badge after resume; body:\n%s", res.Body.String())
	}
	got, err = repo.GetSchedule(ctx, "sch-1")
	if err != nil || got.State != convo.ScheduleActive {
		t.Fatalf("GetSchedule after resume = %+v, %v, want state=active", got, err)
	}
}

// TestResumingAStalledScheduleWithUnhealthySessionsRendersTheOriginalStallReason
// pins the phase brief's explicit requirement: a refused resume must show
// the reason the schedule ALREADY stalled with, not a freshly recomputed
// (and possibly different) one.
func TestResumingAStalledScheduleWithUnhealthySessionsRendersTheOriginalStallReason(t *testing.T) {
	// Neither session is known to this gateway at all, so Healthy reports
	// them unhealthy — Planner.Resume must surface sched.PausedReason
	// verbatim, not this gateway's own "unknown session" wording.
	gw := &fakeUploadTriggerGateway{participants: map[string]selfconvo.Participant{}}
	srv, sessions, _, repo, _ := newScheduleTestServer(t, gw, true)
	ctx := context.Background()
	sc := &convo.Schedule{
		ID: "sch-1", Name: "Weekend chat", SenderSessionID: "sess-sender", SenderName: "Alpha", SenderPhone: "628000001",
		Topic: "futsal", Recurrence: convo.RecurrenceDaily, SetsPerDay: 1,
		WindowStart: "09:00", WindowEnd: "17:00", Timezone: "UTC", StartDate: "2026-09-01",
		State: convo.ScheduleStalled, PausedReason: "session sess-sender: socket wedged, restart waxum",
	}
	recs := []convo.ScheduleReceiver{{SessionID: "sess-recv", SessionName: "Beta", Phone: "628000002", Position: 0}}
	if err := repo.InsertSchedule(ctx, sc, recs); err != nil {
		t.Fatal(err)
	}

	res := authedPost(t, srv, sessions, "/self-convo/schedules/sch-1/resume", url.Values{})
	if res.Code != http.StatusOK {
		t.Fatalf("resume = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "socket wedged, restart waxum") {
		t.Errorf("resume refusal does not render the ORIGINAL stall reason; body:\n%s", res.Body.String())
	}
	got, err := repo.GetSchedule(ctx, "sch-1")
	if err != nil || got.State != convo.ScheduleStalled {
		t.Fatalf("GetSchedule after refused resume = %+v, %v, want state still stalled", got, err)
	}
}

// TestStoppingAnAlreadyStoppedScheduleIsRefusedAndSaysSo pins Stop_'s
// terminal-state guard surfacing through the handler.
func TestStoppingAnAlreadyStoppedScheduleIsRefusedAndSaysSo(t *testing.T) {
	srv, sessions, _, repo, _ := newScheduleTestServer(t, &fakeUploadTriggerGateway{}, true)
	ctx := context.Background()
	sc := &convo.Schedule{
		ID: "sch-1", Name: "Weekend chat", SenderSessionID: "sess-sender", SenderName: "Alpha", SenderPhone: "628000001",
		Topic: "futsal", Recurrence: convo.RecurrenceDaily, SetsPerDay: 1,
		WindowStart: "09:00", WindowEnd: "17:00", Timezone: "UTC", StartDate: "2026-09-01", State: convo.ScheduleStopped,
	}
	if err := repo.InsertSchedule(ctx, sc, nil); err != nil {
		t.Fatal(err)
	}

	res := authedPost(t, srv, sessions, "/self-convo/schedules/sch-1/stop", url.Values{})
	if res.Code != http.StatusOK {
		t.Fatalf("stop = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "already finished or stopped") {
		t.Errorf("stop of an already-stopped schedule does not say so; body:\n%s", res.Body.String())
	}
}

// TestScheduleActionsAreRefusedWhenSelfConvoIsDisabled pins
// schedulePlannerGuard: with the flag off, Deps.Planner is nil (the exact
// production condition — cmd/app/selfconvo.go's startSelfConvo), and the
// rows partial must explain why rather than panic on a nil Planner.
func TestScheduleActionsAreRefusedWhenSelfConvoIsDisabled(t *testing.T) {
	srv, sessions, _, repo, _ := newScheduleTestServer(t, &fakeUploadTriggerGateway{}, false)
	ctx := context.Background()
	sc := &convo.Schedule{
		ID: "sch-1", Name: "Weekend chat", SenderSessionID: "sess-sender", SenderName: "Alpha", SenderPhone: "628000001",
		Topic: "futsal", Recurrence: convo.RecurrenceDaily, SetsPerDay: 1,
		WindowStart: "09:00", WindowEnd: "17:00", Timezone: "UTC", StartDate: "2026-09-01", State: convo.ScheduleActive,
	}
	if err := repo.InsertSchedule(ctx, sc, nil); err != nil {
		t.Fatal(err)
	}

	res := authedPost(t, srv, sessions, "/self-convo/schedules/sch-1/pause", url.Values{})
	if res.Code != http.StatusOK {
		t.Fatalf("pause (disabled) = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), scheduleDisabledMessage) {
		t.Errorf("pause (disabled) does not carry the disabled message; body:\n%s", res.Body.String())
	}
	got, err := repo.GetSchedule(ctx, "sch-1")
	if err != nil || got.State != convo.ScheduleActive {
		t.Fatalf("GetSchedule after refused pause = %+v, %v, want state unchanged (active)", got, err)
	}
}

// TestDeletingAScheduleRemovesItRegardlessOfTheFlag pins that delete is
// NOT gated by SelfConvoEnabled — the same "removal can only reduce
// automated sending" reasoning template deletion follows.
func TestDeletingAScheduleRemovesItRegardlessOfTheFlag(t *testing.T) {
	srv, sessions, _, repo, _ := newScheduleTestServer(t, &fakeUploadTriggerGateway{}, false)
	ctx := context.Background()
	sc := &convo.Schedule{
		ID: "sch-1", Name: "Weekend chat", SenderSessionID: "sess-sender", SenderName: "Alpha", SenderPhone: "628000001",
		Topic: "futsal", Recurrence: convo.RecurrenceDaily, SetsPerDay: 1,
		WindowStart: "09:00", WindowEnd: "17:00", Timezone: "UTC", StartDate: "2026-09-01", State: convo.ScheduleActive,
	}
	if err := repo.InsertSchedule(ctx, sc, nil); err != nil {
		t.Fatal(err)
	}

	res := authedPost(t, srv, sessions, "/self-convo/schedules/sch-1/delete", url.Values{})
	if res.Code != http.StatusOK {
		t.Fatalf("delete = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}
	if _, err := repo.GetSchedule(ctx, "sch-1"); err == nil {
		t.Error("GetSchedule still finds the deleted schedule")
	}
}

// TestTheTemplatesPageTopicFilterNarrowsTheRowsPartial pins the topic
// filter's narrowing behaviour end to end through the real handler.
func TestTheTemplatesPageTopicFilterNarrowsTheRowsPartial(t *testing.T) {
	srv, sessions, _, repo, _ := newScheduleTestServer(t, &fakeUploadTriggerGateway{}, true)
	ctx := context.Background()
	if err := repo.InsertTemplate(ctx, &convo.Template{ID: "t-futsal", Name: "Futsal Opener", Topic: "futsal", Stage: convo.StageOpener, TurnCount: 1, Body: "{}"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertTemplate(ctx, &convo.Template{ID: "t-arisan", Name: "Arisan Opener", Topic: "arisan", Stage: convo.StageOpener, TurnCount: 1, Body: "{}"}); err != nil {
		t.Fatal(err)
	}

	all := authedGet(t, srv, sessions, "/self-convo/templates/rows")
	if !strings.Contains(all.Body.String(), "Futsal Opener") || !strings.Contains(all.Body.String(), "Arisan Opener") {
		t.Fatalf("unfiltered rows must list both templates; body:\n%s", all.Body.String())
	}

	filtered := authedGet(t, srv, sessions, "/self-convo/templates/rows?topic=futsal")
	if !strings.Contains(filtered.Body.String(), "Futsal Opener") {
		t.Errorf("filtered rows are missing Futsal Opener; body:\n%s", filtered.Body.String())
	}
	if strings.Contains(filtered.Body.String(), "Arisan Opener") {
		t.Errorf("filtered rows still show Arisan Opener; body:\n%s", filtered.Body.String())
	}
}

// TestTheTemplateViewPageListsOutgoingLinksWithWeightAndGap pins the
// template correlation graph's display: target name, weight, and a
// readable gap window.
func TestTheTemplateViewPageListsOutgoingLinksWithWeightAndGap(t *testing.T) {
	srv, sessions, _, repo, _ := newScheduleTestServer(t, &fakeUploadTriggerGateway{}, true)
	ctx := context.Background()
	from := &convo.Template{ID: "tpl-from", Name: "Opener", Topic: "futsal", Stage: convo.StageOpener, TurnCount: 1, Body: "{}"}
	to := &convo.Template{ID: "tpl-to", Name: "Middle Chat", Topic: "futsal", Stage: convo.StageMiddle, TurnCount: 1, Body: "{}"}
	if err := repo.InsertTemplate(ctx, from); err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertTemplate(ctx, to); err != nil {
		t.Fatal(err)
	}
	links := []convo.TemplateLink{{ToTemplateID: "tpl-to", Weight: 7, MinGapSeconds: 3600, MaxGapSeconds: 21600}}
	if err := repo.ReplaceTemplateLinks(ctx, "tpl-from", links); err != nil {
		t.Fatal(err)
	}

	res := authedGet(t, srv, sessions, "/self-convo/templates/tpl-from")
	if res.Code != http.StatusOK {
		t.Fatalf("GET template view = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	for _, want := range []string{"Middle Chat", ">7<", "1–6 hours"} {
		if !strings.Contains(body, want) {
			t.Errorf("template view is missing %q; body:\n%s", want, body)
		}
	}
}
