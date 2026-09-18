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
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/knowngroup"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/wasession"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/renderer"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

func newKnownGroupsTestServer(t *testing.T) (http.Handler, *auth.Store, *wa.Manager, *knowngroup.Repository) {
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
	repo := knowngroup.NewRepository(pool)
	mgr := wa.NewManager(context.Background(), nil, wasession.NewRepository(pool),
		msgRepo, nil, wa.Config{SendRate: 100, SendBurst: 5, ReconcilePar: 2})

	srv := New(Deps{
		Renderer: rend, Sessions: sessions, Middleware: auth.NewMiddleware(sessions, apiClients, ""),
		Manager: mgr, Messages: msgRepo, MessageStats: msgRepo, APIClients: apiClients,
		KnownGroups: repo, SessionTTL: time.Hour, AssetsFS: wsm.FS,
	})
	return srv, sessions, mgr, repo
}

// TestKnownGroupCreateThenListThenDeleteRoundTrips is the explicit
// known-groups CRUD-round-trip-for-the-page test the plan names.
func TestKnownGroupCreateThenListThenDeleteRoundTrips(t *testing.T) {
	srv, sessions, mgr, repo := newKnownGroupsTestServer(t)
	ctx := context.Background()
	sender, err := mgr.Create(ctx, "Sender", "+62 812-0000-0001")
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"session_id": {sender.ID},
		"group_jid":  {"120363426585371342@g.us"},
		"name":       {"Clino Team"},
	}
	res := authedPost(t, srv, sessions, "/groups", form)
	if res.Code != http.StatusOK {
		t.Fatalf("POST /groups = %d, want 200 (htmx partial); body:\n%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "Clino Team") {
		t.Errorf("rows partial does not show the newly-created group; body:\n%s", res.Body.String())
	}

	groups, err := repo.ListBySession(ctx, sender.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("ListBySession = %d, want exactly 1", len(groups))
	}
	if groups[0].GroupJID != "120363426585371342@g.us" {
		t.Errorf("GroupJID = %q, want the canonical JID", groups[0].GroupJID)
	}

	rows := authedGet(t, srv, sessions, "/groups/rows")
	if rows.Code != http.StatusOK || !strings.Contains(rows.Body.String(), "Clino Team") {
		t.Fatalf("GET /groups/rows does not list the group; body:\n%s", rows.Body.String())
	}

	del := authedPost(t, srv, sessions, "/groups/"+groups[0].ID+"/delete", url.Values{})
	if del.Code != http.StatusOK {
		t.Fatalf("POST /groups/{id}/delete = %d, want 200; body:\n%s", del.Code, del.Body.String())
	}
	if strings.Contains(del.Body.String(), "Clino Team") {
		t.Errorf("rows partial still shows the deleted group; body:\n%s", del.Body.String())
	}
	remaining, err := repo.ListBySession(ctx, sender.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Errorf("ListBySession after delete = %d, want 0", len(remaining))
	}
}

// TestKnownGroupCreateRejectsAnInvalidJID pins the handler-layer validation
// (wa.ParseGroupJID) rather than only the domain repository's own
// constraints.
func TestKnownGroupCreateRejectsAnInvalidJID(t *testing.T) {
	srv, sessions, mgr, repo := newKnownGroupsTestServer(t)
	ctx := context.Background()
	sender, _ := mgr.Create(ctx, "Sender", "+62 812-0000-0001")

	form := url.Values{
		"session_id": {sender.ID},
		"group_jid":  {"not-a-group-jid"},
		"name":       {"Bad Group"},
	}
	res := authedPost(t, srv, sessions, "/groups", form)
	if res.Code != http.StatusOK {
		t.Fatalf("POST /groups = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "not a valid WhatsApp group JID") {
		t.Errorf("body does not carry the invalid-JID rejection; body:\n%s", res.Body.String())
	}
	groups, err := repo.ListBySession(ctx, sender.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 0 {
		t.Errorf("ListBySession = %d, want 0 — a rejected submission must insert nothing", len(groups))
	}
}
