package handler

import (
	"context"
	"fmt"
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
	return newKnownGroupsTestServerWithBackend(t, nil)
}

// newKnownGroupsTestServerWithBackend is newKnownGroupsTestServer with an
// injectable wa.Backend, so tests of GetKnownGroupPicker (which calls
// s.JoinedGroups, delegating to the Manager's backend) can supply a
// fakeGatewayBackend with groupsFn set. The picker CRUD tests above pass
// nil, matching the original nil-backend behaviour.
func newKnownGroupsTestServerWithBackend(t *testing.T, backend wa.Backend) (http.Handler, *auth.Store, *wa.Manager, *knowngroup.Repository) {
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
	repo := knowngroup.NewRepository(pool)
	mgr := wa.NewManager(context.Background(), backend, wasession.NewRepository(pool),
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

// TestKnownGroupPickerRendersASearchableControlNotAFlatRadioListForManyGroups
// pins the fix for the reported bug: a session with a lot of joined groups
// (one production session has 370) used to render one <label
// class="convo-participant"> radio row per group with no way to search —
// unusable at that scale. This constructs 60 fake joined groups (comfortably
// past "a flat list is fine") and asserts the picker partial renders a
// searchable <input list="..."> bound to a <datalist>, and specifically does
// NOT render 50+ of the old per-group radio rows. Reverting the template
// change makes this fail (it would find zero <datalist> elements and 60
// ".convo-participant" rows).
func TestKnownGroupPickerRendersASearchableControlNotAFlatRadioListForManyGroups(t *testing.T) {
	const groupCount = 60
	backend := &fakeGatewayBackend{
		name: "fake",
		groupsFn: func(context.Context) ([]wa.GroupInfo, error) {
			groups := make([]wa.GroupInfo, groupCount)
			for i := range groups {
				groups[i] = wa.GroupInfo{
					JID:          fmt.Sprintf("12036342658537%04d@g.us", i),
					Name:         fmt.Sprintf("Test Group %d", i),
					Participants: 10 + i,
				}
			}
			return groups, nil
		},
	}
	srv, sessions, mgr, _ := newKnownGroupsTestServerWithBackend(t, backend)
	sender, err := mgr.Create(context.Background(), "Sender", "+62 812-0000-0002")
	if err != nil {
		t.Fatal(err)
	}

	res := authedGet(t, srv, sessions, "/groups/picker?session_id="+sender.ID)
	if res.Code != http.StatusOK {
		t.Fatalf("GET /groups/picker = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}
	body := res.Body.String()

	if !strings.Contains(body, "<datalist") {
		t.Errorf("picker partial does not render a <datalist> for the searchable control; body:\n%s", body)
	}
	if !strings.Contains(body, `<input type="text" list="known-group-options"`) {
		t.Errorf("picker partial does not render the search <input> bound to the datalist; body:\n%s", body)
	}
	if radios := strings.Count(body, `class="convo-participant"`); radios != 0 {
		t.Errorf("picker partial still renders %d old-style .convo-participant radio rows, want 0 "+
			"(a flat radio list does not scale to real session sizes); body:\n%s", radios, body)
	}
	if got := strings.Count(body, "<option value=\"Test Group"); got != groupCount {
		t.Errorf("datalist has %d <option> entries, want %d (one per joined group)", got, groupCount)
	}
	if !strings.Contains(body, `"jid":"12036342658537`) {
		t.Errorf("picker partial does not carry the JID lookup JSON blob known-groups.js reads; body:\n%s", body)
	}
}

// TestKnownGroupPickerEmptyStateStillRendersWithTheNewMarkup pins the
// no-joined-groups empty state survives the flat-list-to-datalist change.
func TestKnownGroupPickerEmptyStateStillRendersWithTheNewMarkup(t *testing.T) {
	backend := &fakeGatewayBackend{
		name: "fake",
		groupsFn: func(context.Context) ([]wa.GroupInfo, error) {
			return nil, nil
		},
	}
	srv, sessions, mgr, _ := newKnownGroupsTestServerWithBackend(t, backend)
	sender, err := mgr.Create(context.Background(), "Sender", "+62 812-0000-0003")
	if err != nil {
		t.Fatal(err)
	}

	res := authedGet(t, srv, sessions, "/groups/picker?session_id="+sender.ID)
	if res.Code != http.StatusOK {
		t.Fatalf("GET /groups/picker = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "This session has no joined groups yet.") {
		t.Errorf("empty-state message missing; body:\n%s", res.Body.String())
	}
	if strings.Contains(res.Body.String(), "<datalist") {
		t.Errorf("empty-state should not render a datalist; body:\n%s", res.Body.String())
	}
}

// TestKnownGroupPickerErrorStateStillRenders pins the not-connected error
// state survives the flat-list-to-datalist change.
func TestKnownGroupPickerErrorStateStillRenders(t *testing.T) {
	backend := &fakeGatewayBackend{
		name: "fake",
		groupsFn: func(context.Context) ([]wa.GroupInfo, error) {
			return nil, wa.ErrNotConnected
		},
	}
	srv, sessions, mgr, _ := newKnownGroupsTestServerWithBackend(t, backend)
	sender, err := mgr.Create(context.Background(), "Sender", "+62 812-0000-0004")
	if err != nil {
		t.Fatal(err)
	}

	res := authedGet(t, srv, sessions, "/groups/picker?session_id="+sender.ID)
	if res.Code != http.StatusOK {
		t.Fatalf("GET /groups/picker = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "session not connected") {
		t.Errorf("not-connected error state missing; body:\n%s", res.Body.String())
	}
}
