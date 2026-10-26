package handler

import (
	"context"
	"io/fs"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	wsm "github.com/ardihikaru/whatsapp-with-dashboard"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/auth"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/apiclient"
	gb "github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/groupbroadcast"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/wasession"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/groupbroadcast"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/renderer"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// fakeGBGateway is a minimal groupbroadcast.Gateway double — this package
// cannot fabricate a connected wa.Snapshot (same reasoning
// fakeUploadTriggerGateway's own doc comment gives for self-convo), so a
// test that needs Planner.PlanDay to actually plan needs a gateway it
// controls.
type fakeGBGateway struct {
	participants map[string]groupbroadcast.Participant
}

func (g *fakeGBGateway) Participant(id string) (groupbroadcast.Participant, bool) {
	p, ok := g.participants[id]
	return p, ok
}

func (g *fakeGBGateway) Send(ctx context.Context, req groupbroadcast.SendRequest) (groupbroadcast.SendReceipt, error) {
	return groupbroadcast.SendReceipt{MessageID: req.MessageID}, nil
}

func (g *fakeGBGateway) Healthy(ids []string) (sessionID, reason string, ok bool) {
	for _, id := range ids {
		p, found := g.participants[id]
		switch {
		case !found:
			return id, "unknown session", false
		case !p.Connected:
			return id, "session is not connected", false
		}
	}
	return "", "", true
}

// newGBScheduleTestServer wires a full handler.New(Deps{...}) server with a
// real *wa.Manager (so PostGroupBroadcastScheduleCreate/Update can resolve
// sender/receiver names via h.Manager.Get) and a *groupbroadcast.Planner
// built over gw — never Start'd, every test calls PlanDay/Pause/Resume/
// Stop_ directly.
func newGBScheduleTestServer(t *testing.T, gw groupbroadcast.Gateway, enabled bool) (http.Handler, *auth.Store, *wa.Manager, *gb.Repository, *groupbroadcast.Planner) {
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
	rend, err := renderer.New(webFS, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewStore(time.Hour)
	t.Cleanup(sessions.Close)
	apiClients := apiclient.NewRepository(pool)
	msgRepo := message.NewRepository(pool)
	repo := gb.NewRepository(pool)
	mgr := wa.NewManager(context.Background(), nil, wasession.NewRepository(pool),
		msgRepo, nil, wa.Config{SendRate: 100, SendBurst: 5, ReconcilePar: 2})
	planner := groupbroadcast.NewPlanner(repo, gw, groupbroadcast.PlannerOptions{})

	srv := New(Deps{
		Renderer: rend, Sessions: sessions, Middleware: auth.NewMiddleware(sessions, apiClients, ""),
		Manager: mgr, Messages: msgRepo, MessageStats: msgRepo, APIClients: apiClients,
		GroupBroadcast: repo, GroupBroadcastEnabled: enabled, GroupBroadcastPlanner: planner,
		SessionTTL: time.Hour, AssetsFS: wsm.FS,
	})
	return srv, sessions, mgr, repo, planner
}

func baseGBScheduleForm(senderID, groupJID string, receiverIDs []string) url.Values {
	v := url.Values{
		"name":                  {"Morning check-in"},
		"sender_id":             {senderID},
		"group_jid":             {groupJID},
		"group_name":            {"Clino Team"},
		"recurrence":            {"daily"},
		"sender_window_start":   {"07:55"},
		"sender_window_end":     {"08:16"},
		"receiver_window_start": {"08:17"},
		"receiver_window_end":   {"09:30"},
		"late_cutoff":           {"08:30"},
		"lang_id_weight":        {"70"},
		"timezone":              {"Asia/Jakarta"},
		"start_date":            {"2026-09-18"},
	}
	for _, id := range receiverIDs {
		v.Add("receiver_id", id)
	}
	return v
}

// TestCreatingAGroupBroadcastScheduleInsertsItsReceiversAndRedirects is the
// happy-path create test, mirroring
// TestCreatingAScheduleInsertsItsReceiversAndRedirects.
func TestCreatingAGroupBroadcastScheduleInsertsItsReceiversAndRedirects(t *testing.T) {
	srv, sessions, mgr, repo, _ := newGBScheduleTestServer(t, &fakeGBGateway{}, true)
	ctx := context.Background()
	sender, _ := mgr.Create(ctx, "Sender", "+62 812-0000-0001")
	recv1, _ := mgr.Create(ctx, "R1", "+62 812-0000-0002")
	recv2, _ := mgr.Create(ctx, "R2", "+62 812-0000-0003")

	form := baseGBScheduleForm(sender.ID, "120363426585371342@g.us", []string{recv1.ID, recv2.ID})
	res := authedPost(t, srv, sessions, "/group-broadcast/schedules", form)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("POST /group-broadcast/schedules = %d, want %d; body:\n%s", res.Code, http.StatusSeeOther, res.Body.String())
	}

	scheds, err := repo.ListSchedules(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(scheds) != 1 {
		t.Fatalf("ListSchedules = %d, want exactly 1", len(scheds))
	}
	if scheds[0].SenderSessionID != sender.ID || scheds[0].GroupJID != "120363426585371342@g.us" {
		t.Errorf("schedule = %+v, want sender=%s group=120363426585371342@g.us", scheds[0], sender.ID)
	}
	recs, err := repo.ScheduleReceivers(ctx, scheds[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("ScheduleReceivers = %d, want exactly 2", len(recs))
	}
}

// TestGBScheduleCreateValidationRejectsTheSenderAsItsOwnReceiverAndAnUnknownGroupJID
// is the explicit form-validation requirement the plan names.
func TestGBScheduleCreateValidationRejectsTheSenderAsItsOwnReceiverAndAnUnknownGroupJID(t *testing.T) {
	t.Run("sender as its own receiver", func(t *testing.T) {
		srv, sessions, mgr, repo, _ := newGBScheduleTestServer(t, &fakeGBGateway{}, true)
		ctx := context.Background()
		sender, _ := mgr.Create(ctx, "Sender", "+62 812-0000-0001")

		form := baseGBScheduleForm(sender.ID, "120363426585371342@g.us", []string{sender.ID})
		res := authedPost(t, srv, sessions, "/group-broadcast/schedules", form)
		if res.Code != http.StatusOK {
			t.Fatalf("POST = %d, want 200 (re-rendered form); body:\n%s", res.Code, res.Body.String())
		}
		if !strings.Contains(res.Body.String(), "the sender cannot also be a receiver") {
			t.Errorf("body does not carry the sender-as-receiver rejection; body:\n%s", res.Body.String())
		}
		scheds, _ := repo.ListSchedules(ctx, 50)
		if len(scheds) != 0 {
			t.Errorf("ListSchedules = %d, want 0 — a rejected submission must insert nothing", len(scheds))
		}
	})

	t.Run("unknown group jid", func(t *testing.T) {
		srv, sessions, mgr, repo, _ := newGBScheduleTestServer(t, &fakeGBGateway{}, true)
		ctx := context.Background()
		sender, _ := mgr.Create(ctx, "Sender", "+62 812-0000-0001")
		recv, _ := mgr.Create(ctx, "R1", "+62 812-0000-0002")

		form := baseGBScheduleForm(sender.ID, "not-a-group-jid", []string{recv.ID})
		res := authedPost(t, srv, sessions, "/group-broadcast/schedules", form)
		if res.Code != http.StatusOK {
			t.Fatalf("POST = %d, want 200 (re-rendered form); body:\n%s", res.Code, res.Body.String())
		}
		if !strings.Contains(res.Body.String(), "unknown group JID") {
			t.Errorf("body does not carry an unknown-group-JID rejection; body:\n%s", res.Body.String())
		}
		scheds, _ := repo.ListSchedules(ctx, 50)
		if len(scheds) != 0 {
			t.Errorf("ListSchedules = %d, want 0 — a rejected submission must insert nothing", len(scheds))
		}
	})
}

// ---- editable receivers (explicit plan requirement) ----

// gbEditableReceiversFixture bundles everything the three editable-receiver
// tests below share: a running server, a schedule already planned for day
// 1 with 3 receivers, and the ability to mint more connected sessions on
// demand (newReceiver) for the edit step.
type gbEditableReceiversFixture struct {
	srv         http.Handler
	sessions    *auth.Store
	mgr         *wa.Manager
	gw          *fakeGBGateway
	repo        *gb.Repository
	planner     *groupbroadcast.Planner
	senderID    string
	receiverIDs []string
	scheduleID  string
}

func newGBEditableReceiversFixture(t *testing.T) *gbEditableReceiversFixture {
	t.Helper()
	gw := &fakeGBGateway{participants: map[string]groupbroadcast.Participant{}}
	srv, sessions, mgr, repo, planner := newGBScheduleTestServer(t, gw, true)
	ctx := context.Background()

	f := &gbEditableReceiversFixture{srv: srv, sessions: sessions, mgr: mgr, gw: gw, repo: repo, planner: planner}

	sender, err := mgr.Create(ctx, "Sender", "+62 812-0000-0001")
	if err != nil {
		t.Fatalf("create sender: %v", err)
	}
	gw.participants[sender.ID] = groupbroadcast.Participant{ID: sender.ID, Name: sender.Name(), Phone: sender.Phone, Connected: true}
	f.senderID = sender.ID

	for i := 0; i < 3; i++ {
		f.receiverIDs = append(f.receiverIDs, f.newConnectedReceiver(t, "R"+strconv.Itoa(i+1)))
	}

	form := baseGBScheduleForm(f.senderID, "120363426585371342@g.us", f.receiverIDs)
	res := authedPost(t, f.srv, f.sessions, "/group-broadcast/schedules", form)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("create schedule = %d, want %d; body:\n%s", res.Code, http.StatusSeeOther, res.Body.String())
	}
	scheds, err := repo.ListSchedules(ctx, 50)
	if err != nil || len(scheds) != 1 {
		t.Fatalf("ListSchedules = %+v, %v", scheds, err)
	}
	f.scheduleID = scheds[0].ID

	day1 := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	if _, err := planner.PlanDay(ctx, day1); err != nil {
		t.Fatalf("PlanDay (day 1): %v", err)
	}
	return f
}

func (f *gbEditableReceiversFixture) newConnectedReceiver(t *testing.T, name string) string {
	t.Helper()
	s, err := f.mgr.Create(context.Background(), name, "+62 812-0000-09"+strconv.Itoa(len(f.gw.participants)))
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	f.gw.participants[s.ID] = groupbroadcast.Participant{ID: s.ID, Name: s.Name(), Phone: s.Phone, Connected: true}
	return s.ID
}

func (f *gbEditableReceiversFixture) receiverTurnCount(t *testing.T, date string) (int, []string) {
	t.Helper()
	occs, err := f.repo.OccurrencesForSchedule(context.Background(), f.scheduleID, 50)
	if err != nil {
		t.Fatalf("OccurrencesForSchedule: %v", err)
	}
	for _, occ := range occs {
		if occ.OccurrenceDate != date {
			continue
		}
		turns, err := f.repo.TurnsForOccurrence(context.Background(), occ.ID)
		if err != nil {
			t.Fatalf("TurnsForOccurrence: %v", err)
		}
		var ids []string
		for _, tn := range turns {
			if tn.Role == gb.RoleReceiver {
				ids = append(ids, tn.SessionID)
			}
		}
		return len(ids), ids
	}
	return 0, nil
}

// TestEditingAScheduleToAddAReceiverPlansItStartingTheNextDay is the
// explicit editable-receivers requirement: day 1 plans exactly the
// original 3 receivers; an update adding a 4th receiver must NOT
// retroactively touch day 1 (still 3, re-fetched AFTER the edit), but day
// 2's occurrence must carry all 4. RED against a stub update handler that
// ignores the posted receiver list (day 2 would stay at 3).
func TestEditingAScheduleToAddAReceiverPlansItStartingTheNextDay(t *testing.T) {
	f := newGBEditableReceiversFixture(t)
	ctx := context.Background()

	n1, _ := f.receiverTurnCount(t, "2026-09-18")
	if n1 != 3 {
		t.Fatalf("day 1 receiver turns = %d, want 3", n1)
	}

	fourth := f.newConnectedReceiver(t, "R4")
	form := baseGBScheduleForm(f.senderID, "120363426585371342@g.us", append(append([]string{}, f.receiverIDs...), fourth))
	res := authedPost(t, f.srv, f.sessions, "/group-broadcast/schedules/"+f.scheduleID, form)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("update schedule = %d, want %d; body:\n%s", res.Code, http.StatusSeeOther, res.Body.String())
	}

	n1After, _ := f.receiverTurnCount(t, "2026-09-18")
	if n1After != 3 {
		t.Fatalf("day 1 receiver turns AFTER the edit = %d, want unchanged at 3 (an edit must never retroactively touch an already-planned occurrence)", n1After)
	}

	day2 := time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)
	if _, err := f.planner.PlanDay(ctx, day2); err != nil {
		t.Fatalf("PlanDay (day 2): %v", err)
	}
	n2, ids2 := f.receiverTurnCount(t, "2026-09-19")
	if n2 != 4 {
		t.Fatalf("day 2 receiver turns = %d, want 4 (3 original + 1 added by the edit); ids=%v", n2, ids2)
	}
	found := false
	for _, id := range ids2 {
		if id == fourth {
			found = true
		}
	}
	if !found {
		t.Errorf("day 2 receiver turns %v do not include the newly-added receiver %q", ids2, fourth)
	}
}

// TestEditingAScheduleToRemoveAReceiverStopsPlanningItStartingTheNextDay is
// the symmetric removal test: day 2 has 2 receiver turns, none referencing
// the removed session.
func TestEditingAScheduleToRemoveAReceiverStopsPlanningItStartingTheNextDay(t *testing.T) {
	f := newGBEditableReceiversFixture(t)
	ctx := context.Background()
	removed := f.receiverIDs[0]
	remaining := f.receiverIDs[1:]

	form := baseGBScheduleForm(f.senderID, "120363426585371342@g.us", remaining)
	res := authedPost(t, f.srv, f.sessions, "/group-broadcast/schedules/"+f.scheduleID, form)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("update schedule = %d, want %d; body:\n%s", res.Code, http.StatusSeeOther, res.Body.String())
	}

	day2 := time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)
	if _, err := f.planner.PlanDay(ctx, day2); err != nil {
		t.Fatalf("PlanDay (day 2): %v", err)
	}
	n2, ids2 := f.receiverTurnCount(t, "2026-09-19")
	if n2 != 2 {
		t.Fatalf("day 2 receiver turns = %d, want 2; ids=%v", n2, ids2)
	}
	for _, id := range ids2 {
		if id == removed {
			t.Errorf("day 2 receiver turns still reference the removed session %q", removed)
		}
	}
}

// TestAnEditNeverRetroactivelyChangesAnAlreadyPlannedOccurrence is the
// third explicit test: re-fetch day 1's turns after the edit — still the
// original 3 receivers, byte-for-byte the same session ids.
func TestAnEditNeverRetroactivelyChangesAnAlreadyPlannedOccurrence(t *testing.T) {
	f := newGBEditableReceiversFixture(t)
	_, before := f.receiverTurnCount(t, "2026-09-18")

	fourth := f.newConnectedReceiver(t, "R4")
	form := baseGBScheduleForm(f.senderID, "120363426585371342@g.us", append(append([]string{}, f.receiverIDs...), fourth))
	res := authedPost(t, f.srv, f.sessions, "/group-broadcast/schedules/"+f.scheduleID, form)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("update schedule = %d, want %d; body:\n%s", res.Code, http.StatusSeeOther, res.Body.String())
	}

	_, after := f.receiverTurnCount(t, "2026-09-18")
	if len(after) != len(before) {
		t.Fatalf("day 1 receiver turn count changed from %d to %d after the edit", len(before), len(after))
	}
	beforeSet := map[string]bool{}
	for _, id := range before {
		beforeSet[id] = true
	}
	for _, id := range after {
		if !beforeSet[id] {
			t.Errorf("day 1 receiver turns now include %q, which was not there before the edit", id)
		}
	}
}
