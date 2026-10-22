package handler

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
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

// newSelfConvoTestServer wires a full handler.New(Deps{...}) server around a
// SQLite-backed convo.Repository and (when enabled) a real, un-started
// *selfconvo.Runner — mirrors newSessionAdminTestServer's shape
// (session_admin_pages_test.go) but for Self Convo's own Deps fields. The
// runner is deliberately never Start'd: every test below calls Trigger/
// Cancel directly through the handler, never the background loop, so
// nothing here needs to sleep or poll.
func newSelfConvoTestServer(t *testing.T, enabled bool) (http.Handler, *auth.Store, *wa.Manager, *convo.Repository) {
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
	convoRepo := convo.NewRepository(pool)
	mgr := wa.NewManager(context.Background(), nil, wasession.NewRepository(pool),
		msgRepo, nil, wa.Config{SendRate: 100, SendBurst: 5, ReconcilePar: 2})

	var runner *selfconvo.Runner
	if enabled {
		runner = selfconvo.New(convoRepo, selfconvo.NewManagerGateway(mgr, msgRepo), selfconvo.Options{})
	}

	srv := New(Deps{
		Renderer: rend, Sessions: sessions, Middleware: auth.NewMiddleware(sessions, apiClients, ""),
		Manager: mgr, Messages: msgRepo, MessageStats: msgRepo, APIClients: apiClients,
		Convo: convoRepo, SelfConvo: runner, SelfConvoEnabled: enabled,
		SessionTTL: time.Hour, AssetsFS: wsm.FS,
	})
	return srv, sessions, mgr, convoRepo
}

// buildMultipartTemplate assembles a multipart/form-data body carrying an
// optional "name" field and a "file" part — the shape both
// PostSelfConvoTemplateUpload and PostSelfConvoTrigger's upload branch
// read. fileBytes == nil omits the file part.
func buildMultipartTemplate(t *testing.T, fields map[string]string, fileBytes []byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if fileBytes != nil {
		fw, err := mw.CreateFormFile("file", "script.json")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(fileBytes); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return body, mw.FormDataContentType()
}

func authedMultipart(t *testing.T, srv http.Handler, sessions *auth.Store, method, path, contentType string, body *bytes.Buffer) *httptest.ResponseRecorder {
	t.Helper()
	sess, err := sessions.Create("admin")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", contentType)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: sess.Token})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func authedPost(t *testing.T, srv http.Handler, sessions *auth.Store, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	sess, err := sessions.Create("admin")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: sess.Token})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// TestSelfConvoPageRendersTheDisabledNoticeByDefault pins that a
// deployment which never set WSM_SELF_CONVO_ENABLED still renders the
// trigger page (200), explaining itself rather than 404ing or 500ing.
func TestSelfConvoPageRendersTheDisabledNoticeByDefault(t *testing.T) {
	srv, sessions, _, _ := newSelfConvoTestServer(t, false)
	res := authedGet(t, srv, sessions, "/self-convo")
	if res.Code != http.StatusOK {
		t.Fatalf("GET /self-convo = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "Self Convo is disabled on this deployment") {
		t.Errorf("the page does not carry the disabled notice; body:\n%s", res.Body.String())
	}
}

// TestTriggeringASelfConvoIsRefusedInThePartialWhenTheFeatureIsDisabled
// pins selfConvoGuardUI: a POST /self-convo/runs against a disabled
// deployment answers 200 (htmx only swaps 2xx) with the disabled message
// inside the rows partial.
func TestTriggeringASelfConvoIsRefusedInThePartialWhenTheFeatureIsDisabled(t *testing.T) {
	srv, sessions, _, _ := newSelfConvoTestServer(t, false)
	body, ct := buildMultipartTemplate(t, map[string]string{"session_id": "a"}, nil)
	res := authedMultipart(t, srv, sessions, http.MethodPost, "/self-convo/runs", ct, body)
	if res.Code != http.StatusOK {
		t.Fatalf("POST /self-convo/runs (disabled) = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), selfConvoDisabledMessage) {
		t.Errorf("the rows partial does not carry the disabled message; body:\n%s", res.Body.String())
	}
}

// TestTriggeringNamesTheSessionThatIsNotConnected exercises the runner's
// real ErrNotEnoughParticipants path through the handler: two sessions
// created via mgr.Create are state "new" (never activated), so the
// gateway reports them not connected and Trigger refuses the run.
func TestTriggeringNamesTheSessionThatIsNotConnected(t *testing.T) {
	srv, sessions, mgr, repo := newSelfConvoTestServer(t, true)
	ctx := context.Background()
	a, err := mgr.Create(ctx, "Alpha", "+62 812-0000-0001")
	if err != nil {
		t.Fatal(err)
	}
	b, err := mgr.Create(ctx, "Beta", "+62 812-0000-0002")
	if err != nil {
		t.Fatal(err)
	}
	tmpl := convo.Template{ID: "tmpl-1", Name: "Example", TurnCount: 2, Body: string(selfconvo.ExampleJSON)}
	if err := repo.InsertTemplate(ctx, &tmpl); err != nil {
		t.Fatal(err)
	}

	// session_id is a repeated checkbox field, which a map[string]string
	// cannot represent — buildMultipartTemplateMulti takes ordered pairs
	// instead.
	body, ct := buildMultipartTemplateMulti(t, [][2]string{
		{"session_id", a.ID}, {"session_id", b.ID}, {"template_id", tmpl.ID},
	})

	res := authedMultipart(t, srv, sessions, http.MethodPost, "/self-convo/runs", ct, body)
	if res.Code != http.StatusOK {
		t.Fatalf("POST /self-convo/runs = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "fewer than two of the ticked sessions are connected") {
		t.Errorf("the rows partial does not name the disconnected pair; body:\n%s", res.Body.String())
	}
}

// TestTriggeringWithTheSameSessionIdTickedTwiceIsRefusedAsNotEnoughSessions
// pins the handler-level half of the duplicate-ticked-id fix (the other
// half is selfconvo.PickPair's own dedupe, expand_test.go): submitting
// session_id=[a.ID, a.ID] must be treated as ONE ticked session, not two —
// this is the ONLY way to observe the handler's own dedupe (rather than
// selfconvo.PickPair's) from outside, since a single duplicated id never
// reaches Trigger at all once deduped, and dedupe collapses the length
// check itself: it answers "tick at least two connected sessions" directly,
// never selfconvo.ErrNotEnoughParticipants' own wording (which is what an
// undeduped submission — reaching Trigger with two IDENTICAL entries —
// would produce instead, since the disconnected session would still fail
// PickPair's own connectivity filter).
func TestTriggeringWithTheSameSessionIdTickedTwiceIsRefusedAsNotEnoughSessions(t *testing.T) {
	srv, sessions, mgr, repo := newSelfConvoTestServer(t, true)
	ctx := context.Background()
	a, err := mgr.Create(ctx, "Alpha", "+62 812-0000-0001")
	if err != nil {
		t.Fatal(err)
	}
	tmpl := convo.Template{ID: "tmpl-1", Name: "Example", TurnCount: 2, Body: string(selfconvo.ExampleJSON)}
	if err := repo.InsertTemplate(ctx, &tmpl); err != nil {
		t.Fatal(err)
	}

	body, ct := buildMultipartTemplateMulti(t, [][2]string{
		{"session_id", a.ID}, {"session_id", a.ID}, {"template_id", tmpl.ID},
	})

	res := authedMultipart(t, srv, sessions, http.MethodPost, "/self-convo/runs", ct, body)
	if res.Code != http.StatusOK {
		t.Fatalf("POST /self-convo/runs = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "tick at least two connected sessions") {
		t.Errorf("a session id ticked twice was not treated as one session; body:\n%s", res.Body.String())
	}
	if strings.Contains(res.Body.String(), "fewer than two of the ticked sessions are connected") {
		t.Errorf("the request reached selfconvo.Trigger instead of being refused by the handler's own dedupe; body:\n%s", res.Body.String())
	}
}

// buildMultipartTemplateMulti is buildMultipartTemplate's variant for
// repeated form keys (session_id checkboxes), which a map[string]string
// cannot represent.
func buildMultipartTemplateMulti(t *testing.T, fields [][2]string) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	for _, kv := range fields {
		if err := mw.WriteField(kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return body, mw.FormDataContentType()
}

// TestTheExampleTemplateDownloadCarriesAttachmentHeadersAndValidates pins
// GET /self-convo/templates/example's headers and pins that the shipped
// bytes still pass Validate.
func TestTheExampleTemplateDownloadCarriesAttachmentHeadersAndValidates(t *testing.T) {
	srv, sessions, _, _ := newSelfConvoTestServer(t, false)
	res := authedGet(t, srv, sessions, "/self-convo/templates/example")
	if res.Code != http.StatusOK {
		t.Fatalf("GET .../example = %d, want 200", res.Code)
	}
	if ct := res.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	cd := res.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") || !strings.Contains(cd, selfconvo.ExampleFilename) {
		t.Errorf("Content-Disposition = %q, want attachment carrying %q", cd, selfconvo.ExampleFilename)
	}
	if _, err := selfconvo.Validate(res.Body.Bytes()); err != nil {
		t.Errorf("the downloaded example does not validate: %v", err)
	}
}

// TestTheExampleRouteIsNotShadowedByTheTemplateIdRoute pins the
// registration-order trap named in routes_ui.go's comment: "example" must
// never be swallowed by GET /self-convo/templates/{id} (which would answer
// 404, since no template is named "example").
func TestTheExampleRouteIsNotShadowedByTheTemplateIdRoute(t *testing.T) {
	srv, sessions, _, _ := newSelfConvoTestServer(t, false)
	res := authedGet(t, srv, sessions, "/self-convo/templates/example")
	if res.Code != http.StatusOK {
		t.Fatalf("GET .../templates/example = %d, want 200 (would be 404 if {id} shadowed it)", res.Code)
	}
	if !bytes.Equal(res.Body.Bytes(), selfconvo.ExampleJSON) {
		t.Errorf("GET .../templates/example did not serve selfconvo.ExampleJSON verbatim")
	}
}

// TestUploadingATemplateSavesARowThatTheTemplateRowsPartialLists exercises
// the whole upload → list round trip through the real handlers.
func TestUploadingATemplateSavesARowThatTheTemplateRowsPartialLists(t *testing.T) {
	srv, sessions, _, _ := newSelfConvoTestServer(t, false)
	body, ct := buildMultipartTemplate(t, nil, selfconvo.ExampleJSON)
	res := authedMultipart(t, srv, sessions, http.MethodPost, "/self-convo/templates", ct, body)
	if res.Code != http.StatusOK {
		t.Fatalf("POST /self-convo/templates = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}

	rows := authedGet(t, srv, sessions, "/self-convo/templates/rows")
	if rows.Code != http.StatusOK {
		t.Fatalf("GET /self-convo/templates/rows = %d, want 200", rows.Code)
	}
	script, err := selfconvo.Validate(selfconvo.ExampleJSON)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rows.Body.String(), script.Name) {
		t.Errorf("the template rows partial does not list %q; body:\n%s", script.Name, rows.Body.String())
	}
}

// TestTheTemplateRenameFormCarriesTheNoJSFallback pins that the inline
// rename form (convo_template_rows.html) follows the house pattern every
// other hx-post form in this app uses (e.g. self_convo/templates.html's
// upload form): action + method="post" alongside hx-post, so the form still
// works with JavaScript/htmx disabled instead of silently doing nothing on
// submit.
func TestTheTemplateRenameFormCarriesTheNoJSFallback(t *testing.T) {
	srv, sessions, _, repo := newSelfConvoTestServer(t, false)
	ctx := context.Background()
	tmpl := convo.Template{ID: "tmpl-rename-fallback", Name: "Original", TurnCount: 2, Body: string(selfconvo.ExampleJSON)}
	if err := repo.InsertTemplate(ctx, &tmpl); err != nil {
		t.Fatal(err)
	}

	rows := authedGet(t, srv, sessions, "/self-convo/templates/rows")
	if rows.Code != http.StatusOK {
		t.Fatalf("GET /self-convo/templates/rows = %d, want 200", rows.Code)
	}
	body := rows.Body.String()
	if !strings.Contains(body, `method="post"`) {
		t.Errorf("the rename form has no method=\"post\" no-JS fallback; body:\n%s", body)
	}
	if !strings.Contains(body, `action="/self-convo/templates/tmpl-rename-fallback/rename"`) {
		t.Errorf("the rename form has no action=\".../tmpl-rename-fallback/rename\" no-JS fallback; body:\n%s", body)
	}
}

// TestUploadingAMalformedTemplateRendersTheValidationErrorInThePartial
// pins that a script failing Validate is reported inline, at 200, rather
// than 4xx-ing (htmx only swaps 2xx).
func TestUploadingAMalformedTemplateRendersTheValidationErrorInThePartial(t *testing.T) {
	srv, sessions, _, _ := newSelfConvoTestServer(t, false)
	body, ct := buildMultipartTemplate(t, nil, []byte(`{"version":1,"turns":[]}`))
	res := authedMultipart(t, srv, sessions, http.MethodPost, "/self-convo/templates", ct, body)
	if res.Code != http.StatusOK {
		t.Fatalf("POST /self-convo/templates (malformed) = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "at least 2 turns") {
		t.Errorf("the rows partial does not carry Validate's error; body:\n%s", res.Body.String())
	}
}

// TestRenamingAndDeletingATemplateUpdateTheRows exercises both mutations
// through the real handlers against one uploaded row.
func TestRenamingAndDeletingATemplateUpdateTheRows(t *testing.T) {
	srv, sessions, _, repo := newSelfConvoTestServer(t, false)
	ctx := context.Background()
	tmpl := convo.Template{ID: "tmpl-1", Name: "Original", TurnCount: 2, Body: string(selfconvo.ExampleJSON)}
	if err := repo.InsertTemplate(ctx, &tmpl); err != nil {
		t.Fatal(err)
	}

	renameForm := url.Values{"name": {"Renamed"}}
	res := authedPost(t, srv, sessions, "/self-convo/templates/tmpl-1/rename", renameForm)
	if res.Code != http.StatusOK {
		t.Fatalf("rename = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "Renamed") {
		t.Errorf("rows do not show the new name; body:\n%s", res.Body.String())
	}

	res = authedPost(t, srv, sessions, "/self-convo/templates/tmpl-1/delete", url.Values{})
	if res.Code != http.StatusOK {
		t.Fatalf("delete = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}
	if strings.Contains(res.Body.String(), "Renamed") {
		t.Errorf("rows still show the deleted template; body:\n%s", res.Body.String())
	}
	if _, err := repo.GetTemplate(ctx, "tmpl-1"); err == nil {
		t.Errorf("GetTemplate still finds the deleted row")
	}
}

// TestTheTemplateDownloadServesTheStoredBodyAsAnAttachment pins the
// per-template download path and its slugged filename.
func TestTheTemplateDownloadServesTheStoredBodyAsAnAttachment(t *testing.T) {
	srv, sessions, _, repo := newSelfConvoTestServer(t, false)
	ctx := context.Background()
	tmpl := convo.Template{ID: "tmpl-1", Name: "Weekend Plans!", TurnCount: 2, Body: string(selfconvo.ExampleJSON)}
	if err := repo.InsertTemplate(ctx, &tmpl); err != nil {
		t.Fatal(err)
	}

	res := authedGet(t, srv, sessions, "/self-convo/templates/tmpl-1/download")
	if res.Code != http.StatusOK {
		t.Fatalf("download = %d, want 200", res.Code)
	}
	if !bytes.Equal(res.Body.Bytes(), selfconvo.ExampleJSON) {
		t.Errorf("download did not serve the stored body verbatim")
	}
	cd := res.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") || !strings.Contains(cd, "weekend-plans.json") {
		t.Errorf("Content-Disposition = %q, want attachment carrying a weekend-plans.json slug", cd)
	}
}

// TestTheTemplateViewPageRendersTheNameTurnCountAndPrettyJSON is the
// regression test for GET /self-convo/templates/{id}
// (GetSelfConvoTemplateView) never having been hit by any test — the exact
// "unknown page with a green suite" hazard SKILL.md names. Uploads the
// shipped example, GETs its view page, and checks the name, the turn
// count, and a pretty-printed line from the body (proving prettyJSON ran,
// not just that the raw bytes leaked through).
func TestTheTemplateViewPageRendersTheNameTurnCountAndPrettyJSON(t *testing.T) {
	srv, sessions, _, repo := newSelfConvoTestServer(t, false)
	ctx := context.Background()
	script, err := selfconvo.Validate(selfconvo.ExampleJSON)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := convo.Template{ID: "tmpl-view-1", Name: script.Name, TurnCount: len(script.Turns), Body: string(selfconvo.ExampleJSON)}
	if err := repo.InsertTemplate(ctx, &tmpl); err != nil {
		t.Fatal(err)
	}

	res := authedGet(t, srv, sessions, "/self-convo/templates/tmpl-view-1")
	if res.Code != http.StatusOK {
		t.Fatalf("GET /self-convo/templates/tmpl-view-1 = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	if !strings.Contains(body, tmpl.Name) {
		t.Errorf("the view page does not carry the template name %q; body:\n%s", tmpl.Name, body)
	}
	if want := fmt.Sprintf("%d", tmpl.TurnCount); !strings.Contains(body, want) {
		t.Errorf("the view page does not carry the turn count %q; body:\n%s", want, body)
	}
	// html/template auto-escapes the quotes inside <pre>{{.PrettyJSON}}</pre>,
	// so the rendered marker is HTML-entity-escaped, not literal JSON.
	if !strings.Contains(body, `&#34;speaker&#34;: &#34;a&#34;`) {
		t.Errorf("the view page does not carry a pretty-printed (indented) line from the template body; body:\n%s", body)
	}
}

// TestTheTemplateViewPageAnswers404ForAnUnknownId is
// TestTheTemplateViewPageRendersTheNameTurnCountAndPrettyJSON's negative
// sibling.
func TestTheTemplateViewPageAnswers404ForAnUnknownId(t *testing.T) {
	srv, sessions, _, _ := newSelfConvoTestServer(t, false)
	res := authedGet(t, srv, sessions, "/self-convo/templates/does-not-exist")
	if res.Code != http.StatusNotFound {
		t.Fatalf("GET /self-convo/templates/does-not-exist = %d, want 404; body:\n%s", res.Code, res.Body.String())
	}
}

// TestTheRunRowsPartialShowsProgressPairAndNextFireTime is a
// template-only test (sessions_test.go:151-181's pattern): render
// convo_run_rows directly against literal data, no server, no database.
func TestTheRunRowsPartialShowsProgressPairAndNextFireTime(t *testing.T) {
	webFS, err := fs.Sub(wsm.FS, "web")
	if err != nil {
		t.Fatal(err)
	}
	rend, err := renderer.New(webFS, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	next := time.Date(2026, 9, 10, 3, 4, 5, 0, time.UTC)
	// A FIXED +7h zone (WIB's actual offset, no DST ever), not time.UTC and
	// not time.Local — a loc argument this test controls directly proves
	// toConvoRunView actually converts rather than merely formatting
	// whatever zone NextDueAt happened to arrive in.
	loc := time.FixedZone("WIB", 7*3600)
	data := convoRunRowsData{
		Enabled: true,
		Runs: []convoRunView{
			toConvoRunView(convo.Run{
				ID: "run-running", TemplateName: "Weekend Plans", State: convo.RunRunning,
				SessionAName: "Alpha", SessionBName: "Beta", TurnCount: 20, TurnsDone: 7,
				NextDueAt: &next,
			}, loc),
			toConvoRunView(convo.Run{
				ID: "run-done", TemplateName: "Weekend Plans", State: convo.RunDone,
				SessionAName: "Alpha", SessionBName: "Beta", TurnCount: 4, TurnsDone: 4,
			}, loc),
		},
	}
	res := httptest.NewRecorder()
	rend.RenderPartial(res, "convo_run_rows", data)
	body := res.Body.String()

	for _, want := range []string{"7/20", "Alpha → Beta", "2026-09-10 10:04:05", "badge-running", "badge-done"} {
		if !strings.Contains(body, want) {
			t.Errorf("rows partial is missing %q; body:\n%s", want, body)
		}
	}
	if strings.Count(body, ">Cancel</button>") != 1 {
		t.Errorf("expected exactly one Cancel button (the running run only); body:\n%s", body)
	}
}

// TestCancelFromTheRowsPartialMarksTheRunCancelled inserts a scheduled run
// directly via the repository, cancels it through the handler, and checks
// both the persisted state and the rendered badge.
func TestCancelFromTheRowsPartialMarksTheRunCancelled(t *testing.T) {
	srv, sessions, _, repo := newSelfConvoTestServer(t, true)
	ctx := context.Background()
	run := convo.Run{
		ID: "run-1", TemplateID: "tmpl-1", TemplateName: "Weekend Plans",
		SessionAID: "s-a", SessionBID: "s-b", SessionAName: "Alpha", SessionBName: "Beta",
		PhoneA: "628000001", PhoneB: "628000002", State: convo.RunScheduled, TurnCount: 2,
	}
	turns := []convo.Turn{
		{Seq: 1, Speaker: "a", SessionID: "s-a", ToPhone: "628000002", Body: "hi", State: convo.TurnPending},
		{Seq: 2, Speaker: "b", SessionID: "s-b", ToPhone: "628000001", Body: "hey", State: convo.TurnPending},
	}
	if err := repo.CreateRun(ctx, &run, turns); err != nil {
		t.Fatal(err)
	}

	res := authedPost(t, srv, sessions, "/self-convo/runs/run-1/cancel", url.Values{})
	if res.Code != http.StatusOK {
		t.Fatalf("cancel = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "badge-cancelled") {
		t.Errorf("rows do not show the cancelled badge; body:\n%s", res.Body.String())
	}
	got, err := repo.GetRun(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != convo.RunCancelled {
		t.Errorf("run state = %q, want %q", got.State, convo.RunCancelled)
	}
}

// TestStartAtIsInterpretedInTheBrowsersOffsetNotUTC pins parseStartAt's
// timezone arithmetic in isolation.
func TestStartAtIsInterpretedInTheBrowsersOffsetNotUTC(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	got, err := parseStartAt("2026-09-10T09:00", "-420", now)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 10, 2, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("parseStartAt(-420) = %v, want %v", got, want)
	}

	got, err = parseStartAt("", "-420", now)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(now) {
		t.Errorf("parseStartAt(empty) = %v, want now (%v)", got, now)
	}

	got, err = parseStartAt("2026-09-10T09:00", "not-a-number", now)
	if err != nil {
		t.Fatal(err)
	}
	want = time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("parseStartAt(garbage offset) = %v, want %v (treated as UTC)", got, want)
	}
}

// TestTheTriggerFormCarriesBothHxEncodingAndEnctype is
// TestTheMediaFormCarriesBothHxEncodingAndEnctype's (media_send_ui_test.go)
// sibling for the trigger form's file upload.
func TestTheTriggerFormCarriesBothHxEncodingAndEnctype(t *testing.T) {
	rend := sendTestRenderer(t)
	data := selfConvoPageData{Enabled: true, Connected: []wa.Snapshot{{ID: "sess-1", Name: "Gateway 1", Phone: "628000"}}}
	rec := httptest.NewRecorder()
	rend.RenderPage(rec, "self_convo", data)
	body := rec.Body.String()

	if !strings.Contains(body, `hx-post="/self-convo/runs"`) {
		t.Fatalf("the trigger form does not hx-post to /self-convo/runs; body:\n%s", body)
	}
	if !strings.Contains(body, `hx-encoding="multipart/form-data"`) {
		t.Error(`the trigger form is missing hx-encoding="multipart/form-data"`)
	}
	if !strings.Contains(body, `enctype="multipart/form-data"`) {
		t.Error(`the trigger form is missing enctype="multipart/form-data"`)
	}
	if !strings.Contains(body, `action="/self-convo/runs"`) {
		t.Error(`the trigger form is missing its no-JS action="/self-convo/runs" fallback`)
	}
}

// TestSelfConvoTemplateManagementWorksWithTheFeatureDisabled pins the
// doc comment on self_convo_templates_ui.go: upload/rename/delete are NOT
// gated by SelfConvoEnabled.
func TestSelfConvoTemplateManagementWorksWithTheFeatureDisabled(t *testing.T) {
	srv, sessions, _, _ := newSelfConvoTestServer(t, false)
	body, ct := buildMultipartTemplate(t, nil, selfconvo.ExampleJSON)
	res := authedMultipart(t, srv, sessions, http.MethodPost, "/self-convo/templates", ct, body)
	if res.Code != http.StatusOK {
		t.Fatalf("upload with the feature disabled = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}
	if strings.Contains(res.Body.String(), selfConvoDisabledMessage) {
		t.Errorf("template upload was refused by the feature flag; body:\n%s", res.Body.String())
	}
}

// fakeUploadTriggerGateway is a minimal selfconvo.Gateway double, the same
// shape internal/selfconvo/runner_test.go's own fakeGateway serves but
// reimplemented here: this package cannot fabricate a connected wa.Snapshot
// (wa.Snapshot.Connected is computed from an unexported field — see this
// file's own newSelfConvoTestServer doc comment and
// docs/brief-self-convo.md), so TestUploadAndTriggerStoresOneTemplateAndOneRun
// needs a *selfconvo.Runner wired over a gateway it controls, not the real
// NewManagerGateway, to reach a successful Trigger at all.
type fakeUploadTriggerGateway struct {
	participants map[string]selfconvo.Participant
}

func (g *fakeUploadTriggerGateway) Participant(id string) (selfconvo.Participant, bool) {
	p, ok := g.participants[id]
	return p, ok
}

func (g *fakeUploadTriggerGateway) Send(ctx context.Context, req selfconvo.SendRequest) (selfconvo.SendReceipt, error) {
	return selfconvo.SendReceipt{MessageID: req.MessageID}, nil
}

func (g *fakeUploadTriggerGateway) MessageStatus(ctx context.Context, messageID string) (status, reason string, found bool, err error) {
	return selfconvo.MessageStatusSent, "", true, nil
}

// Healthy mirrors Participant's own view: a session is healthy here exactly
// when this fake knows it and its Participant is Connected. That is the same
// signal the production gateway reads (see selfconvo.Gateway.Healthy's doc
// comment), so a test that flips a participant's Connected flag exercises
// the planner's stall path without this double inventing a second notion of
// health the real one does not have.
func (g *fakeUploadTriggerGateway) Healthy(ids []string) (sessionID, reason string, ok bool) {
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

// newSelfConvoTestServerWithFakeGateway is newSelfConvoTestServer(t, true),
// except its Runner is wired over gw rather than a real
// selfconvo.NewManagerGateway — see fakeUploadTriggerGateway's doc comment
// for why.
func newSelfConvoTestServerWithFakeGateway(t *testing.T, gw selfconvo.Gateway) (http.Handler, *auth.Store, *convo.Repository) {
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
	convoRepo := convo.NewRepository(pool)
	mgr := wa.NewManager(context.Background(), nil, wasession.NewRepository(pool),
		msgRepo, nil, wa.Config{SendRate: 100, SendBurst: 5, ReconcilePar: 2})
	runner := selfconvo.New(convoRepo, gw, selfconvo.Options{})

	srv := New(Deps{
		Renderer: rend, Sessions: sessions, Middleware: auth.NewMiddleware(sessions, apiClients, ""),
		Manager: mgr, Messages: msgRepo, MessageStats: msgRepo, APIClients: apiClients,
		Convo: convoRepo, SelfConvo: runner, SelfConvoEnabled: true,
		SessionTTL: time.Hour, AssetsFS: wsm.FS,
	})
	return srv, sessions, convoRepo
}

// TestUploadAndTriggerStoresOneTemplateAndOneRun exercises the real
// upload-and-trigger request the trigger form makes when an operator
// attaches a script instead of picking a saved one: one POST
// /self-convo/runs carrying both a "file" part and two ticked session ids.
// It must produce exactly one template row (the upload) and exactly one run
// row (the trigger) — this is the regression net for the fix that stops
// PostSelfConvoTrigger's upload branch from validating the same bytes three
// times (docs/brief-self-convo.md's review follow-ups): a bug that saved a
// second template row, or that failed to reuse the one just saved and had
// Trigger fetch/insert another, would show up here as row counts other than
// 1 and 1.
func TestUploadAndTriggerStoresOneTemplateAndOneRun(t *testing.T) {
	gw := &fakeUploadTriggerGateway{participants: map[string]selfconvo.Participant{
		"sess-a": {ID: "sess-a", Name: "Alpha", Phone: "628110000001", Connected: true},
		"sess-b": {ID: "sess-b", Name: "Beta", Phone: "628110000002", Connected: true},
	}}
	srv, sessions, repo := newSelfConvoTestServerWithFakeGateway(t, gw)
	ctx := context.Background()

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	for _, sid := range []string{"sess-a", "sess-b"} {
		if err := mw.WriteField("session_id", sid); err != nil {
			t.Fatal(err)
		}
	}
	fw, err := mw.CreateFormFile("file", "script.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(selfconvo.ExampleJSON); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	res := authedMultipart(t, srv, sessions, http.MethodPost, "/self-convo/runs", mw.FormDataContentType(), body)
	if res.Code != http.StatusOK {
		t.Fatalf("POST /self-convo/runs (upload+trigger) = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}

	templates, err := repo.ListTemplateSummaries(ctx, 50)
	if err != nil {
		t.Fatalf("ListTemplateSummaries: %v", err)
	}
	if len(templates) != 1 {
		t.Fatalf("templates = %d, want exactly 1", len(templates))
	}

	runs, err := repo.ListRuns(ctx, 50)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want exactly 1; body:\n%s", len(runs), res.Body.String())
	}
	if runs[0].TemplateID != templates[0].ID {
		t.Errorf("run.TemplateID = %q, want the uploaded template's id %q", runs[0].TemplateID, templates[0].ID)
	}
}
