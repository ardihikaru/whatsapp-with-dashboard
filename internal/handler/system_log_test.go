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

// newSystemLogTestServer is newLogsTestServer (logs_test.go) with the
// *wa.Manager also handed back — the system log tests need it directly, to
// seed events via Manager.RecordEvent, the way a real reconnect attempt or
// state transition would.
func newSystemLogTestServer(t *testing.T) (http.Handler, *auth.Store, *wa.Manager) {
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
	webFS, err := fs.Sub(wsm.FS, "web")
	if err != nil {
		t.Fatal(err)
	}
	rend, err := renderer.New(webFS, "", nil)
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
		PlatformURL: "https://clino.test/platform/",
	})
	return srv, sessions, manager
}

// TestTheSystemLogPageRendersRecentEventsNewestFirst drives GetSystemLogRows
// (the HTMX partial the page itself polls — system_log/list.html's
// hx-trigger) directly via httptest, in-package, per house style (see
// authedGet, logs_test.go). Two events recorded in order must come back
// newest-first — the same contract Manager.RecentEvents itself already
// guarantees (ring_test.go's TestTheEventRingKeepsOnlyTheMostRecentEntries);
// this is the page's own proof that it renders that order rather than
// silently reversing (or not reversing) it again on the way to HTML.
func TestTheSystemLogPageRendersRecentEventsNewestFirst(t *testing.T) {
	srv, sessions, manager := newSystemLogTestServer(t)
	manager.RecordEvent("info", "first thing that happened")
	manager.RecordEvent("warn", "second thing that happened")

	res := authedGet(t, srv, sessions, "/system-log/rows")
	if res.Code != http.StatusOK {
		t.Fatalf("GET /system-log/rows = %d: %s", res.Code, res.Body.String())
	}
	body := res.Body.String()

	firstIdx := strings.Index(body, "first thing that happened")
	secondIdx := strings.Index(body, "second thing that happened")
	if firstIdx < 0 || secondIdx < 0 {
		t.Fatalf("both events must appear in the rendered rows; body:\n%s", body)
	}
	if secondIdx > firstIdx {
		t.Fatalf("the second (later) event must render BEFORE the first (earlier) one — newest first — got them in the opposite order:\n%s", body)
	}
	if !strings.Contains(body, "warn") {
		t.Error("the warn-level event's Level is not rendered anywhere in the row")
	}
}

// TestTheSystemLogPageIsReachableAndInTheSidebar is the markup-assertion
// counterpart to nav_mobile_test.go's TestEveryChromePageRendersTheDrawerMarkup
// / TestDrawerCarriesEverySidebarDestination (which both already cover
// /system-log as of this change) — this test asserts the SAME two facts
// (200 status, sidebar destination present) from this page's own test file,
// so a future refactor of the shared drawer test does not silently stop
// proving this page is reachable.
func TestTheSystemLogPageIsReachableAndInTheSidebar(t *testing.T) {
	srv, sessions, _ := newSystemLogTestServer(t)

	res := authedGet(t, srv, sessions, "/system-log")
	if res.Code != http.StatusOK {
		t.Fatalf("GET /system-log = %d: %s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	if !strings.Contains(body, `<aside class="sidebar"`) {
		t.Fatal("/system-log did not render the shared sidebar chrome")
	}
	if !strings.Contains(body, "System Log") {
		t.Error("the sidebar has no 'System Log' destination")
	}
	if !strings.Contains(body, `href="/system-log"`) && !strings.Contains(body, `hx-get="/system-log/rows"`) {
		t.Error(`the page must link to itself in the sidebar and poll /system-log/rows for its rows`)
	}

	rows := authedGet(t, srv, sessions, "/system-log/rows")
	if rows.Code != http.StatusOK {
		t.Fatalf("GET /system-log/rows = %d: %s", rows.Code, rows.Body.String())
	}
}

// TestTheSystemLogPageSaysItIsInMemory guards the honesty requirement this
// feature was built under (see Manager.RecentEvents' and GetSystemLog's own
// doc comments): the page must say plainly that the buffer is lost on
// restart, so it is never mistaken for wa_messages' durable audit log.
func TestTheSystemLogPageSaysItIsInMemory(t *testing.T) {
	srv, sessions, _ := newSystemLogTestServer(t)
	body := authedGet(t, srv, sessions, "/system-log").Body.String()

	for _, want := range []string{"in-memory", "restart"} {
		if !strings.Contains(strings.ToLower(body), want) {
			t.Errorf("the system log page does not mention %q — an operator could mistake this in-memory ring for a durable log", want)
		}
	}
}
