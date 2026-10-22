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
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/contact"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/wasession"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/renderer"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// newContactsTestServer wires a full handler.New(Deps{...}) server around a
// SQLite-backed wa.Manager and a real contact.Repository — the same house
// pattern newKnownGroupsTestServerWithBackend (known_groups_ui_test.go)
// uses, but additionally returning the *apiclient.Repository so the same
// helper backs both the cookie-page tests here and the bearer-API tests in
// api_contacts_crud_test.go.
func newContactsTestServer(t *testing.T, backend wa.Backend) (http.Handler, *auth.Store, *wa.Manager, *contact.Repository, *apiclient.Repository) {
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
	repo := contact.NewRepository(pool)
	mgr := wa.NewManager(context.Background(), backend, wasession.NewRepository(pool),
		msgRepo, nil, wa.Config{SendRate: 100, SendBurst: 5, ReconcilePar: 2})

	srv := New(Deps{
		Renderer: rend, Sessions: sessions, Middleware: auth.NewMiddleware(sessions, apiClients, ""),
		Manager: mgr, Messages: msgRepo, MessageStats: msgRepo, APIClients: apiClients,
		Contacts: repo, SessionTTL: time.Hour, AssetsFS: wsm.FS,
	})
	return srv, sessions, mgr, repo, apiClients
}

// TestContactCreateThenListThenDeleteRoundTrips is the page's CRUD-round-
// trip test: create, see it in the rows partial AND in the repository,
// delete, see it gone from both. No backend is wired (nil), so
// SelectForRoute finds no connected session and the create's own
// auto-verify leaves the contact at "unknown" — this test is about the
// CRUD shape, not the verify outcome (see
// TestContactCreateStillSucceedsWhenVerificationHasNoConnectedSession for
// that).
func TestContactCreateThenListThenDeleteRoundTrips(t *testing.T) {
	srv, sessions, _, repo, _ := newContactsTestServer(t, nil)
	ctx := context.Background()

	form := url.Values{"name": {"Ria"}, "phone": {"+62 812-0000-0001"}, "aliases": {"Ria <3"}}
	res := authedPost(t, srv, sessions, "/contacts", form)
	if res.Code != http.StatusOK {
		t.Fatalf("POST /contacts = %d, want 200 (htmx partial); body:\n%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "Ria") {
		t.Errorf("create response does not show the newly-created contact; body:\n%s", res.Body.String())
	}

	all, err := repo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("repo.List = %d contacts, want exactly 1", len(all))
	}
	if all[0].Phone != "6281200000001" {
		t.Errorf("Phone = %q, want the canonical digits", all[0].Phone)
	}
	if all[0].Aliases != "ria <3" {
		t.Errorf("Aliases = %q, want lowercased %q", all[0].Aliases, "ria <3")
	}

	rows := authedGet(t, srv, sessions, "/contacts/rows")
	if rows.Code != http.StatusOK || !strings.Contains(rows.Body.String(), "Ria") {
		t.Fatalf("GET /contacts/rows does not list the contact; body:\n%s", rows.Body.String())
	}

	del := authedPost(t, srv, sessions, "/contacts/"+all[0].ID+"/delete", url.Values{})
	if del.Code != http.StatusOK {
		t.Fatalf("POST /contacts/{id}/delete = %d, want 200; body:\n%s", del.Code, del.Body.String())
	}
	if strings.Contains(del.Body.String(), "Ria") {
		t.Errorf("rows partial still shows the deleted contact; body:\n%s", del.Body.String())
	}
	remaining, err := repo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Errorf("repo.List after delete = %d, want 0", len(remaining))
	}
}

// TestContactCreateRejectsAnInvalidPhone pins the handler-layer validation
// (wa.NormalizePhone) rather than only the domain repository's own
// constraints — mirrors TestKnownGroupCreateRejectsAnInvalidJID's shape.
func TestContactCreateRejectsAnInvalidPhone(t *testing.T) {
	srv, sessions, _, repo, _ := newContactsTestServer(t, nil)
	res := authedPost(t, srv, sessions, "/contacts", url.Values{"name": {"Ria"}, "phone": {"not-a-number"}})
	if res.Code != http.StatusOK {
		t.Fatalf("POST /contacts = %d, want 200 (htmx error fragment); body:\n%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "alert-error") {
		t.Errorf("response does not render an error for an invalid phone; body:\n%s", res.Body.String())
	}
	all, err := repo.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Errorf("repo.List = %d, want 0 — an invalid phone must not be saved", len(all))
	}
}

// TestContactCreateRejectsADuplicatePhone exercises contact.ErrDuplicate's
// translation at the handler layer.
func TestContactCreateRejectsADuplicatePhone(t *testing.T) {
	srv, sessions, _, _, _ := newContactsTestServer(t, nil)
	first := url.Values{"name": {"Ria"}, "phone": {"+62 812-0000-0001"}}
	if res := authedPost(t, srv, sessions, "/contacts", first); res.Code != http.StatusOK {
		t.Fatalf("first POST /contacts = %d, want 200", res.Code)
	}
	second := url.Values{"name": {"Ria Duplicate"}, "phone": {"+62 812-0000-0001"}}
	res := authedPost(t, srv, sessions, "/contacts", second)
	if res.Code != http.StatusOK {
		t.Fatalf("second POST /contacts = %d, want 200 (htmx error fragment); body:\n%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "already exists") {
		t.Errorf("response does not report the duplicate; body:\n%s", res.Body.String())
	}
}

// TestContactCreateDoesNotSwapRowMarkupIntoTheErrorTarget is the regression
// test for the known-groups bug this feature's doc comment calls out by
// name (contacts_ui.go): the create form's hx-target is
// #contact-form-error, and a SUCCESSFUL create must leave that target
// holding no row markup — an id="contact-rows" <tbody> (the rows table)
// must never appear nested inside it. The rows update happens via a
// SEPARATE out-of-band element in the same response.
func TestContactCreateDoesNotSwapRowMarkupIntoTheErrorTarget(t *testing.T) {
	srv, sessions, _, _, _ := newContactsTestServer(t, nil)
	form := url.Values{"name": {"Ria"}, "phone": {"+62 812-0000-0001"}}
	res := authedPost(t, srv, sessions, "/contacts", form)
	if res.Code != http.StatusOK {
		t.Fatalf("POST /contacts = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	// The row markup (<tbody id="contact-rows">...) must appear EXACTLY
	// once, and that one occurrence must carry hx-swap-oob="true" — i.e.
	// it travels as an out-of-band swap, never as the direct content of
	// the create form's own #contact-form-error target. The known-groups
	// bug this guards against put bare <tr> row markup straight into that
	// error div on every successful create.
	if n := strings.Count(body, `id="contact-rows"`); n != 1 {
		t.Fatalf(`response contains %d occurrence(s) of id="contact-rows", want exactly 1; body:\n%s`, n, body)
	}
	if !strings.Contains(body, `id="contact-rows" hx-swap-oob="true"`) {
		t.Errorf(`the rows table does not carry hx-swap-oob="true" — it would land in the error target instead; body:\n%s`, body)
	}
}

// TestContactCreateStillSucceedsWhenVerificationHasNoConnectedSession pins
// the task's named rule: a routing/backend failure during auto-verify
// (here: no connected session at all, since this server's Manager has a
// nil backend) must still leave the contact SAVED, its status at
// contact.StatusUnknown — never contact.StatusNotRegistered — and the
// verify problem reported back in the response.
func TestContactCreateStillSucceedsWhenVerificationHasNoConnectedSession(t *testing.T) {
	srv, sessions, _, repo, _ := newContactsTestServer(t, nil)
	res := authedPost(t, srv, sessions, "/contacts", url.Values{"name": {"Ria"}, "phone": {"+62 812-0000-0001"}})
	if res.Code != http.StatusOK {
		t.Fatalf("POST /contacts = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "no connected session") {
		t.Errorf("response does not report the verify failure; body:\n%s", res.Body.String())
	}
	all, err := repo.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("repo.List = %d, want 1 — the create must still succeed", len(all))
	}
	if all[0].WAStatus != contact.StatusUnknown {
		t.Errorf("WAStatus = %q, want %q (never not_registered on a verify failure)", all[0].WAStatus, contact.StatusUnknown)
	}
}

// TestContactsNavEntryAndChromeRender pins the sidebar entry and the shared
// drawer markup, following nav_mobile_test.go's own conventions (/contacts
// is not in pagesWithChrome because that shared helper's Manager has no
// Contacts repo wired — same reasoning as session-ops/session-webhooks'
// own exclusion there).
func TestContactsNavEntryAndChromeRender(t *testing.T) {
	srv, sessions, _, _, _ := newContactsTestServer(t, nil)
	res := authedGet(t, srv, sessions, "/contacts")
	if res.Code != http.StatusOK {
		t.Fatalf("GET /contacts = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	for _, want := range []string{
		`id="app-sidebar"`, `class="nav-toggle btn-icon"`, `aria-controls="app-sidebar"`,
		`class="nav-backdrop"`, `/static/css/responsive.css`, `/static/js/nav-drawer.js`,
		">Contacts<",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /contacts is missing %q; body:\n%s", want, body)
		}
	}
}

// TestPickVerifySnapshotFallsBackToAGroupedMasterSessionWhenNothingUngroupedIsConnected
// pins the bug fixed 2026-10-17: on a host where every connected session
// is either is_master or carries a non-empty GroupCode,
// h.Manager.SelectForRoute(ctx, wa.Route{}) (verifyContact's OLD call —
// see contacts_ui.go's git history) admits none of them, even though
// several sessions are genuinely connected. This test calls
// pickVerifySnapshot directly, the function verifyContact now uses, with
// literal snapshots shaped exactly like that host's live state (two
// is_master sessions, two sessions grouped "clino", nothing ungrouped and
// non-master) and requires it to still choose one.
func TestPickVerifySnapshotFallsBackToAGroupedMasterSessionWhenNothingUngroupedIsConnected(t *testing.T) {
	snaps := []wa.Snapshot{
		{ID: "ardi-laptop", Connected: true, IsMaster: true, GroupCode: ""},
		{ID: "arie", Connected: true, IsMaster: true, GroupCode: ""},
		{ID: "clino-2", Connected: true, IsMaster: false, GroupCode: "clino"},
		{ID: "clino-3", Connected: true, IsMaster: false, GroupCode: "clino"},
	}
	got, ok := pickVerifySnapshot(snaps)
	if !ok {
		t.Fatalf("pickVerifySnapshot = (_, false), want a fallback pick from %d connected sessions", len(snaps))
	}
	if got.ID == "" {
		t.Errorf("pickVerifySnapshot returned an empty snapshot despite ok=true")
	}
}

// TestPickVerifySnapshotStillPrefersTheUngroupedNonMasterPool pins that
// this fix only WIDENS the fallback — it must not change which session
// gets picked when an ordinary ungrouped, non-master, connected session
// already exists alongside a master and a grouped session.
func TestPickVerifySnapshotStillPrefersTheUngroupedNonMasterPool(t *testing.T) {
	snaps := []wa.Snapshot{
		{ID: "master", Connected: true, IsMaster: true, GroupCode: ""},
		{ID: "grouped", Connected: true, IsMaster: false, GroupCode: "clino"},
		{ID: "ordinary", Connected: true, IsMaster: false, GroupCode: ""},
	}
	got, ok := pickVerifySnapshot(snaps)
	if !ok {
		t.Fatal("pickVerifySnapshot = (_, false), want the ordinary session")
	}
	if got.ID != "ordinary" {
		t.Errorf("pickVerifySnapshot = %q, want %q (the ungrouped non-master pool must still win)", got.ID, "ordinary")
	}
}

// TestPickVerifySnapshotReturnsFalseWhenNothingIsConnected pins the
// no-fallback floor: no connected session anywhere must still answer
// false, not a zero-value snapshot mistaken for a real pick.
func TestPickVerifySnapshotReturnsFalseWhenNothingIsConnected(t *testing.T) {
	snaps := []wa.Snapshot{
		{ID: "a", Connected: false, IsMaster: false, GroupCode: ""},
		{ID: "b", Connected: false, IsMaster: true, GroupCode: "clino"},
	}
	if _, ok := pickVerifySnapshot(snaps); ok {
		t.Error("pickVerifySnapshot = (_, true) with nothing connected, want false")
	}
}
