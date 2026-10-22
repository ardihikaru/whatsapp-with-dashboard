package handler

import (
	"context"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wsm "github.com/ardihikaru/whatsapp-with-dashboard"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/auth"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/apiclient"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/wasession"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/renderer"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// jakarta is the fixed +7h offset this file's tests convert against —
// FixedZone rather than time.LoadLocation("Asia/Jakarta"), so these tests
// cannot flake if the test host's own tzdata is ever unavailable (unlike
// config's own tests, this package has no reason to depend on it).
var jakarta = time.FixedZone("WIB", 7*3600)

// newTimezoneTestServer is newLogsTestServer (logs_test.go) with
// Deps.Location set — kept separate rather than adding a parameter to that
// helper, same reasoning as newHostLabelTestServer (host_chip_test.go): every
// existing caller of newLogsTestServer stays untouched.
func newTimezoneTestServer(t *testing.T, loc *time.Location) (http.Handler, *auth.Store, *message.SQLRepository) {
	t.Helper()
	pool, err := db.Open(filepath.Join(t.TempDir(), "test.db"), db.SQLiteDialect)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	ctx := context.Background()
	if err := db.EnsureSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx,
		`INSERT INTO wa_sessions (id, name, phone, created_at, updated_at)
		 VALUES ('sess-1','Gateway 1','628000','2026-07-01T00:00:00Z','2026-07-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	webFS, err := fs.Sub(wsm.FS, "web")
	if err != nil {
		t.Fatal(err)
	}
	rend, err := renderer.New(webFS, "", loc)
	if err != nil {
		t.Fatal(err)
	}
	msgRepo := message.NewRepository(pool)
	manager := wa.NewManager(ctx, nil, wasession.NewRepository(pool), msgRepo, nil,
		wa.Config{SendRate: 1, SendBurst: 3, ReconcilePar: 2})
	sessions := auth.NewStore(time.Hour)
	t.Cleanup(sessions.Close)
	apiClients := apiclient.NewRepository(pool)
	srv := New(Deps{
		Renderer: rend, Sessions: sessions,
		Middleware: auth.NewMiddleware(sessions, apiClients, ""), Manager: manager,
		Messages: msgRepo, MessageStats: msgRepo, APIClients: apiClients,
		SessionTTL: time.Hour, AssetsFS: wsm.FS,
		Location: loc,
	})
	return srv, sessions, msgRepo
}

// TestLogsRowsRenderInTheConfiguredTimezoneNotUTC is the test named in the
// task: a message stamped 2026-10-22T01:30:00Z (UTC, as everything in
// wa_messages is stored) must render as "2026-10-22 08:30" (UTC+7) on the
// /logs page when Deps.Location is Asia/Jakarta's offset — and must NOT
// still show the raw UTC "01:30".
func TestLogsRowsRenderInTheConfiguredTimezoneNotUTC(t *testing.T) {
	srv, sessions, repo := newTimezoneTestServer(t, jakarta)
	ctx := context.Background()
	stamp := time.Date(2026, 10, 22, 1, 30, 0, 0, time.UTC)
	if err := repo.Insert(ctx, &message.Message{
		ID: "m1", SessionID: "sess-1", ToPhone: "628111000",
		Body: "Halo", Status: message.StatusSent, CreatedAt: stamp,
	}); err != nil {
		t.Fatal(err)
	}

	body := authedGet(t, srv, sessions, "/logs").Body.String()
	if !strings.Contains(body, "2026-10-22 08:30") {
		t.Errorf("/logs did not render the message's CreatedAt converted to +7h; got:\n%s", body)
	}
	if strings.Contains(body, "01:30") {
		t.Errorf("/logs still shows the raw UTC time (01:30) instead of the converted one; got:\n%s", body)
	}
}

// TestOutgoingChatRowsRenderInTheConfiguredTimezoneNotUTC is the
// outgoing-chat detail table's equivalent of the /logs test above — same
// stamp, same conversion, HH:MM:SS format (toOutgoingRowViews) instead of
// HH:MM.
func TestOutgoingChatRowsRenderInTheConfiguredTimezoneNotUTC(t *testing.T) {
	srv, sessions, repo := newTimezoneTestServer(t, jakarta)
	ctx := context.Background()
	stamp := time.Date(2026, 10, 22, 1, 30, 0, 0, time.UTC)
	if err := repo.Insert(ctx, &message.Message{
		ID: "m1", SessionID: "sess-1", ToPhone: "628111000",
		Body: "Halo", Status: message.StatusSent, CreatedAt: stamp,
	}); err != nil {
		t.Fatal(err)
	}

	body := authedGet(t, srv, sessions, "/outgoing-chat").Body.String()
	if !strings.Contains(body, "2026-10-22 08:30:00") {
		t.Errorf("/outgoing-chat did not render the message's CreatedAt converted to +7h; got:\n%s", body)
	}
	if strings.Contains(body, "01:30:00") {
		t.Errorf("/outgoing-chat still shows the raw UTC time (01:30:00) instead of the converted one; got:\n%s", body)
	}
}

// TestSystemLogRowsRenderInTheConfiguredTimezoneNotUTC is a template-only
// test of toSystemLogRowViews (self_convo_ui_test.go's
// TestTheRunRowsPartialShowsProgressPairAndNextFireTime pattern) rather
// than a full HTTP round trip: wa.Manager.RecordEvent always stamps
// time.Now() with no way to pin an exact instant from this package, so the
// deterministic stamp this test needs can only be supplied directly to the
// view-model function.
func TestSystemLogRowsRenderInTheConfiguredTimezoneNotUTC(t *testing.T) {
	stamp := time.Date(2026, 10, 22, 1, 30, 0, 0, time.UTC)
	views := toSystemLogRowViews([]wa.Event{{At: stamp, Level: "info", Note: "boot reconciliation"}}, jakarta)
	if len(views) != 1 {
		t.Fatalf("toSystemLogRowViews returned %d rows, want 1", len(views))
	}
	if !strings.Contains(views[0].At, "2026-10-22 08:30:00") {
		t.Errorf("At = %q, want it to contain the +7h-converted time", views[0].At)
	}
	if strings.Contains(views[0].At, "01:30:00") {
		t.Errorf("At = %q, still shows the raw UTC time instead of the converted one", views[0].At)
	}
}
