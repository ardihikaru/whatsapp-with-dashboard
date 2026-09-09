package handler

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
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

func newLogsTestServer(t *testing.T) (http.Handler, *auth.Store, *message.SQLRepository, *apiclient.Repository) {
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
	rend, err := renderer.New(webFS, "")
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
	return srv, sessions, msgRepo, apiClients
}

func authedGet(t *testing.T, srv http.Handler, sessions *auth.Store, path string) *httptest.ResponseRecorder {
	t.Helper()
	sess, err := sessions.Create("admin")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: sess.Token})
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, req)
	return res
}

func TestLogsPageRendersRealData(t *testing.T) {
	srv, sessions, repo, _ := newLogsTestServer(t)
	ctx := context.Background()
	must := func(m *message.Message) {
		if err := repo.Insert(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	must(&message.Message{ID: "m1", SessionID: "sess-1", ToPhone: "628111000",
		Body: "Halo pesanan siap", Status: message.StatusSent, CreatedAt: time.Now().UTC(),
		ClientRef: "cl-alpha", TenantRef: "tn-kemang", Category: message.CategoryNotification})
	must(&message.Message{ID: "m2", SessionID: "sess-1", ToPhone: "628222000",
		Body: "Gagal kirim", Status: message.StatusFailed, CreatedAt: time.Now().UTC()})

	res := authedGet(t, srv, sessions, "/logs")
	if res.Code != http.StatusOK {
		t.Fatalf("GET /logs = %d: %s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	for _, want := range []string{"Log Pesanan Keluar", "628111000", "cl-alpha", "tn-kemang", "Gateway 1"} {
		if !strings.Contains(body, want) {
			t.Fatalf("/logs body missing %q", want)
		}
	}

	rows := authedGet(t, srv, sessions, "/logs/rows")
	if rows.Code != http.StatusOK {
		t.Fatalf("GET /logs/rows = %d", rows.Code)
	}
	if !strings.Contains(rows.Body.String(), "628222000") {
		t.Fatalf("/logs/rows missing the failed message row")
	}
}

// TestTheLogsFilterOffersTheSelfConvoCategory pins that the /logs category
// dropdown lists self_convo alongside the other categories, so an operator
// can filter the audit log down to a self-convo run. parseLogFilter
// (logs.go) passes the raw "category" query value straight through to
// message.Filter with no validation against logCategories — the dropdown is
// the only place that needs to know about the new category.
func TestTheLogsFilterOffersTheSelfConvoCategory(t *testing.T) {
	srv, sessions, _, _ := newLogsTestServer(t)
	res := authedGet(t, srv, sessions, "/logs")
	if res.Code != http.StatusOK {
		t.Fatalf("GET /logs = %d: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `<option value="self_convo"`) {
		t.Fatalf("/logs body does not offer a self_convo option in the category filter")
	}
}

// The log page is platform-only: an unauthenticated browser request must bounce to
// login, never render the audit log.
func TestLogsPageRequiresSession(t *testing.T) {
	srv, _, _, _ := newLogsTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/logs", nil)
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, req)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("unauth GET /logs = %d, want 303 redirect to login", res.Code)
	}
}
