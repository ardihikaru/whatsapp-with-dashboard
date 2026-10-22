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

// newHostLabelTestServer is newLogsTestServer (logs_test.go) with HostLabel
// wired onto Deps, the field this file's tests exercise. Kept separate
// rather than adding a parameter to newLogsTestServer so every existing
// caller of that helper stays untouched.
func newHostLabelTestServer(t *testing.T, hostLabel string) (http.Handler, *auth.Store) {
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
		HostLabel: hostLabel,
	})
	return srv, sessions
}

// TestTheHeaderRendersTheHostLabelChipWhenConfigured is the test that fails
// if newPageBase stops copying HostLabel onto PageBase, or header.html drops
// the {{if .HostLabel}} branch: an operator on the mini-PC or the laptop
// must see which one served the page.
func TestTheHeaderRendersTheHostLabelChipWhenConfigured(t *testing.T) {
	srv, sessions := newHostLabelTestServer(t, "MINIPC")
	body := authedGet(t, srv, sessions, "/dashboard").Body.String()

	if !strings.Contains(body, `class="badge badge-host-label"`) {
		t.Errorf("dashboard body is missing the host-label chip element; got:\n%s", body)
	}
	if !strings.Contains(body, "MINIPC") {
		t.Error("dashboard body does not contain the configured host label \"MINIPC\"")
	}
}

// TestTheHeaderRendersNoChipElementAtAllWhenHostLabelIsEmpty is the other
// half: an operator who never set WSM_HOST_LABEL must see nothing new in the
// header, not an empty chip. Matches platformLink's "" hides the whole
// element" convention this feature copies.
func TestTheHeaderRendersNoChipElementAtAllWhenHostLabelIsEmpty(t *testing.T) {
	srv, sessions := newHostLabelTestServer(t, "")
	body := authedGet(t, srv, sessions, "/dashboard").Body.String()

	if strings.Contains(body, "badge-host-label") {
		t.Errorf("dashboard body renders a host-label chip even though HostLabel "+
			"is empty; got:\n%s", body)
	}
}
