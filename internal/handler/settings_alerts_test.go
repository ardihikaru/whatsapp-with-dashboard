package handler

import (
	"context"
	"errors"
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
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	sa "github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/sessionalert"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/wasession"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/renderer"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/sessionalert"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// errSendFailed is a fixed sentinel so TestSendTestMessageNamesTheSenderEvenWhenTheSendItselfFails
// can assert on a predictable Send() failure.
var errSendFailed = errors.New("fake: send failed")

// fakeAlertGateway is a minimal sessionalert.Gateway double for handler
// tests — package handler cannot fabricate a connected *wa.ManagedSession
// (see api_group_code_test.go's own doc comment on exactly this
// limitation), so SendTest's own success/failure path is exercised
// against a Gateway built independently of any live Manager/backend.
type fakeAlertGateway struct {
	snaps   []sessionalert.Snapshot
	sends   []struct{ SessionID, GroupJID, Text string }
	err     error
	members map[string]bool // sessionID -> is a member of whatever group is asked about
	events  []string
}

func (g *fakeAlertGateway) Snapshots() []sessionalert.Snapshot { return g.snaps }
func (g *fakeAlertGateway) Send(_ context.Context, sessionID, groupJID, text string) error {
	if g.err != nil {
		return g.err
	}
	g.sends = append(g.sends, struct{ SessionID, GroupJID, Text string }{sessionID, groupJID, text})
	return nil
}

// Member backs chooseSender's fallback tier — see that function's doc
// comment (internal/sessionalert/sender.go). Every test in this file uses
// a self-reminder or configured sender, neither of which is ever
// membership-checked, so this only matters for tests that explicitly
// populate g.members.
func (g *fakeAlertGateway) Member(_ context.Context, sessionID, _ string) (bool, error) {
	return g.members[sessionID], nil
}

func (g *fakeAlertGateway) RecordEvent(level, note string) {
	g.events = append(g.events, level+": "+note)
}

// newSettingsAlertsTestServer wires a handler.New(Deps{...}) around a
// SQLite-backed sessionalert settings repo and a fake alert gateway —
// enough to drive GetSettingsAlerts/PostSettingsAlerts/
// PostSettingsAlertsTest end to end, same shape as newLogsTestServer.
func newSettingsAlertsTestServer(t *testing.T, gw *fakeAlertGateway) (http.Handler, *auth.Store) {
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
	mgr := wa.NewManager(context.Background(), nil, wasession.NewRepository(pool), msgRepo, nil,
		wa.Config{SendRate: 100, SendBurst: 5, ReconcilePar: 2})
	settingsRepo := sa.NewRepository(pool)

	var svc *sessionalert.Service
	if gw != nil {
		svc = sessionalert.New(settingsRepo, gw, make(chan wa.StateChange, 1), sessionalert.Options{})
	}

	h := New(Deps{
		Renderer: rend, Sessions: sessions, Middleware: auth.NewMiddleware(sessions, apiClients, ""),
		Manager: mgr, Messages: msgRepo, MessageStats: msgRepo, APIClients: apiClients,
		SessionAlertSettings: settingsRepo, SessionAlert: svc,
		SessionTTL: time.Hour, AssetsFS: wsm.FS,
	})
	return h, sessions
}

// TestTheSettingsAlertsPageLoadsDefaults proves a fresh deployment's
// GET /settings/alerts renders without error and carries its own heading.
func TestTheSettingsAlertsPageLoadsDefaults(t *testing.T) {
	srv, sessions := newSettingsAlertsTestServer(t, &fakeAlertGateway{})
	res := authedGet(t, srv, sessions, "/settings/alerts")
	if res.Code != 200 {
		t.Fatalf("GET /settings/alerts = %d: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "Session Alerts") {
		t.Errorf("settings page does not render its own heading; body:\n%s", res.Body.String())
	}
}

// TestSavingTheSettingsAlertsFormPersistsAndReloads proves POST
// /settings/alerts saves every field and a subsequent GET shows it.
func TestSavingTheSettingsAlertsFormPersistsAndReloads(t *testing.T) {
	srv, sessions := newSettingsAlertsTestServer(t, &fakeAlertGateway{})
	res := authedPostForm(t, srv, sessions, "/settings/alerts", url.Values{
		"logout_alert_enabled": {"true"}, "daily_report_enabled": {"true"},
		"daily_time": {"09:15"}, "timezone": {"Asia/Jakarta"},
		"group_jid": {"120363111111111111@g.us"}, "name": {"Ops Alerts"},
	})
	if res.Code != 200 {
		t.Fatalf("POST /settings/alerts = %d: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "Saved") {
		t.Error("save response does not say Saved")
	}

	get := authedGet(t, srv, sessions, "/settings/alerts")
	body := get.Body.String()
	if !strings.Contains(body, "09:15") || !strings.Contains(body, "120363111111111111@g.us") || !strings.Contains(body, "Ops Alerts") {
		t.Errorf("reloaded settings page does not show the saved values; body:\n%s", body)
	}
}

// TestAnInvalidDailyTimeIsRejectedWithoutSaving proves validation runs
// before any write.
func TestAnInvalidDailyTimeIsRejectedWithoutSaving(t *testing.T) {
	srv, sessions := newSettingsAlertsTestServer(t, &fakeAlertGateway{})
	res := authedPostForm(t, srv, sessions, "/settings/alerts", url.Values{
		"daily_time": {"25:99"}, "timezone": {"Asia/Jakarta"},
	})
	if res.Code != 200 {
		t.Fatalf("POST = %d", res.Code)
	}
	if !strings.Contains(res.Body.String(), "alert-error") {
		t.Error("an invalid daily_time did not render alert-error")
	}

	get := authedGet(t, srv, sessions, "/settings/alerts")
	if strings.Contains(get.Body.String(), `value="25:99"`) {
		t.Error("an invalid daily_time was persisted despite validation failing")
	}
}

// TestSendTestMessageCallsTheGateway proves the "Send test message"
// button drives sessionalert.Service.SendTest, which calls through to the
// Gateway's Send — real evidence of wiring, not just a 200 status.
func TestSendTestMessageCallsTheGateway(t *testing.T) {
	gw := &fakeAlertGateway{snaps: []sessionalert.Snapshot{{ID: "s1", Name: "Ardi laptop", Connected: true, SelfReminder: true}}}
	srv, sessions := newSettingsAlertsTestServer(t, gw)

	// Configure a target group first (SendTest refuses without one).
	saveRes := authedPostForm(t, srv, sessions, "/settings/alerts", url.Values{
		"group_jid": {"120363222222222222@g.us"}, "daily_time": {"08:00"}, "timezone": {"Asia/Jakarta"},
	})
	if saveRes.Code != 200 {
		t.Fatalf("POST /settings/alerts (setup) = %d", saveRes.Code)
	}

	res := authedPostForm(t, srv, sessions, "/settings/alerts/test", url.Values{})
	if res.Code != 200 {
		t.Fatalf("POST /settings/alerts/test = %d: %s", res.Code, res.Body.String())
	}
	if len(gw.sends) != 1 {
		t.Fatalf("gateway.Send called %d time(s), want 1", len(gw.sends))
	}
	if gw.sends[0].SessionID != "s1" || gw.sends[0].GroupJID != "120363222222222222@g.us" {
		t.Errorf("Send called with %+v, want session s1 / the configured group", gw.sends[0])
	}
	body := res.Body.String()
	if !strings.Contains(body, "Test message sent") {
		t.Error("response does not confirm the test message was sent")
	}
	// The whole point of this fix: the operator must see WHICH session
	// sent it, not just a bare "sent" — verified against the actual
	// rendered body, not inferred from the 200 status.
	if !strings.Contains(body, "Ardi laptop") {
		t.Errorf("response does not name the sending session (Ardi laptop); body:\n%s", body)
	}
}

// TestSendTestMessageNamesTheSenderEvenWhenTheSendItselfFails proves a
// chosen-but-failed send still tells the operator which session was
// tried — not just "it didn't work" — the same gap PostSettingsAlertsTest
// used to have (a bare 200 with no outcome detail).
func TestSendTestMessageNamesTheSenderEvenWhenTheSendItselfFails(t *testing.T) {
	gw := &fakeAlertGateway{
		snaps: []sessionalert.Snapshot{{ID: "s1", Name: "Ardi laptop", Connected: true, SelfReminder: true}},
		err:   errSendFailed,
	}
	srv, sessions := newSettingsAlertsTestServer(t, gw)
	authedPostForm(t, srv, sessions, "/settings/alerts", url.Values{
		"group_jid": {"120363222222222222@g.us"}, "daily_time": {"08:00"}, "timezone": {"Asia/Jakarta"},
	})

	res := authedPostForm(t, srv, sessions, "/settings/alerts/test", url.Values{})
	if res.Code != 200 {
		t.Fatalf("POST = %d", res.Code)
	}
	body := res.Body.String()
	if !strings.Contains(body, "alert-error") {
		t.Error("a failed test send did not render alert-error")
	}
	if !strings.Contains(body, "Ardi laptop") {
		t.Errorf("failure response does not name the session that was tried; body:\n%s", body)
	}
}

// TestSendTestMessageFailsClearlyWithNoConnectedSession proves a failure
// is reported to the operator, not swallowed.
func TestSendTestMessageFailsClearlyWithNoConnectedSession(t *testing.T) {
	gw := &fakeAlertGateway{} // no sessions at all
	srv, sessions := newSettingsAlertsTestServer(t, gw)
	authedPostForm(t, srv, sessions, "/settings/alerts", url.Values{
		"group_jid": {"120363222222222222@g.us"}, "daily_time": {"08:00"}, "timezone": {"Asia/Jakarta"},
	})

	res := authedPostForm(t, srv, sessions, "/settings/alerts/test", url.Values{})
	if res.Code != 200 {
		t.Fatalf("POST = %d", res.Code)
	}
	if !strings.Contains(res.Body.String(), "alert-error") {
		t.Error("a failed test send did not render alert-error")
	}
}
