package handler

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	wsm "github.com/ardihikaru/whatsapp-with-dashboard"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/auth"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/apiclient"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/call"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/wasession"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/renderer"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// newSessionAdminTestServer wires a full handler.New(Deps{...}) server
// around a SQLite-backed wa.Manager carrying ONE created session — the same
// shape as newMediaTestServer (media_calls_test_backend_test.go) — but
// additionally returns the *auth.Store so browser-page (cookie-auth) tests
// can authenticate, which newMediaTestServer's API-only callers never
// needed. Kept as its OWN helper rather than widening
// newMediaTestServer's five-value return across its four existing call
// sites — see nav_mobile_test.go's comment on why /sessions/{id}/ops and
// /sessions/{id}/webhooks need a properly seeded manager instead of
// pagesWithChrome's shared newLogsTestServer (whose Manager has a nil
// backend and is never StartAll'd, so Manager.Get 404s).
func newSessionAdminTestServer(t *testing.T, backend wa.Backend, callsEnabled bool) (http.Handler, *auth.Store, *wa.ManagedSession, call.Repository) {
	return newSessionAdminTestServerReconcile(t, backend, callsEnabled, 2*time.Millisecond)
}

// newSessionAdminTestServerReconcile is newSessionAdminTestServer with the
// periodic reconciler's interval chosen by the caller — 0 turns the loop off,
// which a test counting backend consultations needs.
func newSessionAdminTestServerReconcile(t *testing.T, backend wa.Backend, callsEnabled bool, reconcileInterval time.Duration) (http.Handler, *auth.Store, *wa.ManagedSession, call.Repository) {
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
	callRepo := call.NewRepository(pool)
	mgr := wa.NewManager(context.Background(), backend, wasession.NewRepository(pool),
		msgRepo, nil, wa.Config{SendRate: 100, SendBurst: 5, ReconcilePar: 2,
			// Only a backend implementing wa's Observations hook ever starts the
			// reconciler; every other backend in these tests is unaffected.
			ReconcileInterval: reconcileInterval, ReconcileGrace: time.Millisecond})
	s, err := mgr.Create(context.Background(), "gw", "+62 812-0000-9999")
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Deps{
		Renderer: rend, Sessions: sessions, Middleware: auth.NewMiddleware(sessions, apiClients, ""),
		Manager: mgr, Messages: msgRepo, MessageStats: msgRepo, APIClients: apiClients, Calls: callRepo,
		CallsEnabled: callsEnabled, SessionTTL: time.Hour, AssetsFS: wsm.FS,
	})
	return srv, sessions, s, callRepo
}

// whatsmeowShapedBackend is a *fakeGatewayBackend by another name for these
// tests' readability at call sites — it implements NONE of
// SessionOps/WebhookAdmin/GatewayHealth, exactly like the real meowBackend
// (internal/wa/backend_meow_unsupported.go), which is the whole point of
// TestPagesRenderTheUnsupportedStateOnAWhatsmeowBackend below.
func whatsmeowShapedBackend() *fakeGatewayBackend {
	return &fakeGatewayBackend{name: "whatsmeow-shaped"}
}

// TestSessionOpsPageRendersTheDrawerMarkup is nav_mobile_test.go's
// TestEveryChromePageRendersTheDrawerMarkup, run over the dedicated seeded
// manager /sessions/{id}/ops needs (see newSessionAdminTestServer's doc
// comment for why it cannot join pagesWithChrome).
func TestSessionOpsPageRendersTheDrawerMarkup(t *testing.T) {
	srv, sessions, s, _ := newSessionAdminTestServer(t, whatsmeowShapedBackend(), true)
	res := authedGet(t, srv, sessions, "/sessions/"+s.ID+"/ops")
	if res.Code != http.StatusOK {
		t.Fatalf("GET /sessions/%s/ops = %d: %s", s.ID, res.Code, res.Body.String())
	}
	body := res.Body.String()
	for _, want := range []string{
		`id="app-sidebar"`, `class="nav-toggle btn-icon"`, `aria-controls="app-sidebar"`,
		`class="nav-backdrop"`, `/static/css/responsive.css`, `/static/js/nav-drawer.js`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /sessions/%s/ops is missing %s", s.ID, want)
		}
	}
}

// TestSessionWebhooksPageRendersTheDrawerMarkup is the webhooks page's
// equivalent of the test above.
func TestSessionWebhooksPageRendersTheDrawerMarkup(t *testing.T) {
	srv, sessions, s, _ := newSessionAdminTestServer(t, whatsmeowShapedBackend(), true)
	res := authedGet(t, srv, sessions, "/sessions/"+s.ID+"/webhooks")
	if res.Code != http.StatusOK {
		t.Fatalf("GET /sessions/%s/webhooks = %d: %s", s.ID, res.Code, res.Body.String())
	}
	body := res.Body.String()
	for _, want := range []string{
		`id="app-sidebar"`, `class="nav-toggle btn-icon"`, `aria-controls="app-sidebar"`,
		`class="nav-backdrop"`, `/static/css/responsive.css`, `/static/js/nav-drawer.js`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /sessions/%s/webhooks is missing %s", s.ID, want)
		}
	}
}

// TestPagesRenderTheUnsupportedStateOnAWhatsmeowBackend is the phase
// brief's §3 requirement, pinned directly: scripts/e2e-env.sh pins
// WSM_WA_BACKEND=whatsmeow, so /sessions/{id}/ops and
// /sessions/{id}/webhooks answer wa.ErrUnsupported for everything, and
// this proves both pages render 200 with an explanatory message rather
// than a 500 or a blank panel.
func TestPagesRenderTheUnsupportedStateOnAWhatsmeowBackend(t *testing.T) {
	srv, sessions, s, _ := newSessionAdminTestServer(t, whatsmeowShapedBackend(), true)

	opsRes := authedGet(t, srv, sessions, "/sessions/"+s.ID+"/ops")
	if opsRes.Code != http.StatusOK {
		t.Fatalf("GET /sessions/%s/ops = %d, want 200 even though the backend is unsupported", s.ID, opsRes.Code)
	}
	// The panel loads by hx-get (load, every 5s) — see ops.html — so the
	// unsupported message is on the PANEL route, not the shell.
	panelRes := authedGet(t, srv, sessions, "/sessions/"+s.ID+"/ops/panel")
	if panelRes.Code != http.StatusOK {
		t.Fatalf("GET /sessions/%s/ops/panel = %d, want 200", s.ID, panelRes.Code)
	}
	if !strings.Contains(panelRes.Body.String(), "not supported by this backend") {
		t.Errorf("ops panel does not explain the unsupported backend; got:\n%s", panelRes.Body.String())
	}

	webhooksRes := authedGet(t, srv, sessions, "/sessions/"+s.ID+"/webhooks")
	if webhooksRes.Code != http.StatusOK {
		t.Fatalf("GET /sessions/%s/webhooks = %d, want 200 even though the backend is unsupported", s.ID, webhooksRes.Code)
	}
	if !strings.Contains(webhooksRes.Body.String(), "not supported by this backend") {
		t.Errorf("webhooks page does not explain the unsupported backend; got:\n%s", webhooksRes.Body.String())
	}
}

// TestTheCallsPageRendersEvenWithCallingDisabled is the calls-page half of
// the §3 requirement: WSM_CALLS_ENABLED defaults to false, and the page
// must say so plainly rather than showing a form whose button lies.
func TestTheCallsPageRendersEvenWithCallingDisabled(t *testing.T) {
	srv, sessions, _, _ := newSessionAdminTestServer(t, whatsmeowShapedBackend(), false)
	res := authedGet(t, srv, sessions, "/calls")
	if res.Code != http.StatusOK {
		t.Fatalf("GET /calls = %d, want 200 even with calling disabled", res.Code)
	}
	if !strings.Contains(res.Body.String(), "disabled on this deployment") {
		t.Errorf("/calls does not explain that calling is disabled; got:\n%s", res.Body.String())
	}
}

// TestTheWebhookPageNeverRendersASecret pins api_webhooks.go's rule one
// layer up: ListWebhooks (what the rendered table is built from — the
// register response never reaches the page directly, see
// renderWebhookRows) returns a secret, and the table must show
// "secret set" — literally never the secret's own value. Same guarantee
// TestWebhookResponsesNeverIncludeTheSecret (api_webhooks_test.go) gives
// the JSON API, over ListWebhooks the same way that test does.
func TestTheWebhookPageNeverRendersASecret(t *testing.T) {
	const theSecret = "sekret-hmac-key-do-not-leak"
	backend := &fakeOpsBackend{
		fakeGatewayBackend: &fakeGatewayBackend{name: "waxum-shaped"},
		listWebhooksFn: func() ([]wa.WebhookInfo, error) {
			s := theSecret
			return []wa.WebhookInfo{{ID: "wh-1", URL: "https://example.test/hook", Events: []string{"message"}, Secret: &s, Enabled: true}}, nil
		},
	}
	srv, sessions, s, _ := newSessionAdminTestServer(t, backend, true)

	res := authedGet(t, srv, sessions, "/sessions/"+s.ID+"/webhooks/rows")
	if res.Code != http.StatusOK {
		t.Fatalf("GET /sessions/%s/webhooks/rows = %d: %s", s.ID, res.Code, res.Body.String())
	}
	body := res.Body.String()
	if strings.Contains(body, theSecret) {
		t.Fatalf("the webhook secret leaked into the rendered page:\n%s", body)
	}
	if !strings.Contains(body, "secret set") {
		t.Errorf("the webhook row does not say \"secret set\"; got:\n%s", body)
	}
}

// TestSessionOpsPauseTogglesThePanel is a behavioural check, not just a
// markup one: pausing must flip the panel's own rendered state, proving
// the button is wired to wa.SessionOps.Pause and not just decorative.
func TestSessionOpsPauseTogglesThePanel(t *testing.T) {
	paused := false
	backend := &fakeOpsBackend{
		fakeGatewayBackend: &fakeGatewayBackend{name: "waxum-shaped"},
		pauseFn:            func() (bool, error) { paused = true; return true, nil },
		isPausedFn:         func() (bool, error) { return paused, nil },
	}
	srv, sessions, s, _ := newSessionAdminTestServer(t, backend, true)

	before := authedGet(t, srv, sessions, "/sessions/"+s.ID+"/ops/panel")
	if strings.Contains(before.Body.String(), "This session will not send or receive") {
		t.Fatalf("panel reports paused before Pause was ever called:\n%s", before.Body.String())
	}

	sess, err := sessions.Create("admin")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/sessions/"+s.ID+"/ops/pause", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: sess.Token})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /sessions/%s/ops/pause = %d: %s", s.ID, rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Paused") {
		t.Errorf("panel does not reflect the paused state after Pause; got:\n%s", rec.Body.String())
	}
	if !paused {
		t.Error("wa.SessionOps.Pause was never actually called")
	}
}

// TestSessionOpsAutoReconnectTogglesThePanel is TestSessionOpsPauseTogglesThePanel's
// shape applied to the auto-reconnect toggle: this was flagged in Phase 7's
// report as having unsupported-state and drawer-markup coverage but no
// flip-it-and-verify test of its own, and Phase 8 did not add one either.
func TestSessionOpsAutoReconnectTogglesThePanel(t *testing.T) {
	enabled := false
	backend := &fakeOpsBackend{
		fakeGatewayBackend: &fakeGatewayBackend{name: "waxum-shaped"},
		autoReconnectFn: func() (wa.AutoReconnectState, error) {
			return wa.AutoReconnectState{Enabled: enabled}, nil
		},
		setAutoReconnectFn: func(e bool) (wa.AutoReconnectState, error) {
			enabled = e
			return wa.AutoReconnectState{Enabled: e}, nil
		},
	}
	srv, sessions, s, _ := newSessionAdminTestServer(t, backend, true)

	before := authedGet(t, srv, sessions, "/sessions/"+s.ID+"/ops/panel")
	if strings.Contains(before.Body.String(), "Enabled — error count:") {
		t.Fatalf("panel reports auto-reconnect enabled before SetAutoReconnect was ever called:\n%s", before.Body.String())
	}

	res := authedPostForm(t, srv, sessions, "/sessions/"+s.ID+"/ops/auto-reconnect/enable", url.Values{})
	if res.Code != http.StatusOK {
		t.Fatalf("POST /sessions/%s/ops/auto-reconnect/enable = %d: %s", s.ID, res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "Enabled — error count: 0") {
		t.Errorf("panel does not reflect auto-reconnect enabled after the call; got:\n%s", res.Body.String())
	}
	if !enabled {
		t.Error("wa.SessionOps.SetAutoReconnect was never actually called")
	}
}

// TestSessionOpsHistorySyncTogglesThePanel is the same shape applied to the
// history-sync toggle — see TestSessionOpsAutoReconnectTogglesThePanel's
// doc comment for why this exists.
func TestSessionOpsHistorySyncTogglesThePanel(t *testing.T) {
	skip := false
	backend := &fakeOpsBackend{
		fakeGatewayBackend: &fakeGatewayBackend{name: "waxum-shaped"},
		historySyncFn: func() (bool, error) {
			return skip, nil
		},
		setHistorySyncFn: func(s bool) (bool, error) {
			skip = s
			return s, nil
		},
	}
	srv, sessions, s, _ := newSessionAdminTestServer(t, backend, true)

	before := authedGet(t, srv, sessions, "/sessions/"+s.ID+"/ops/panel")
	if strings.Contains(before.Body.String(), "Skipped — a reconnect will NOT replay message history.") {
		t.Fatalf("panel reports history sync skipped before SetHistorySync was ever called:\n%s", before.Body.String())
	}

	res := authedPostForm(t, srv, sessions, "/sessions/"+s.ID+"/ops/history-sync/disable", url.Values{})
	if res.Code != http.StatusOK {
		t.Fatalf("POST /sessions/%s/ops/history-sync/disable = %d: %s", s.ID, res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "Skipped — a reconnect will NOT replay message history.") {
		t.Errorf("panel does not reflect history sync being skipped after the call; got:\n%s", res.Body.String())
	}
	if !skip {
		t.Error("wa.SessionOps.SetHistorySync was never actually called")
	}
}

// TestSessionOpsAppStateResyncReachesTheBackendWithNoFilter is the same
// shape applied to appstate-resync, adapted to what this action actually is:
// unlike the two toggles above, resync has no persistent on/off state for
// the panel to disagree with. What DOES change and is worth pinning:
// PostSessionOpsAppStateResync must call AppStateResync with a nil
// collection filter and empty mode ("waxum's default mode", its own doc
// comment) — proving the button is wired at all, and with the args the
// comment promises, not just decorative.
//
// This test used to also pin opsAction (ops_ui.go) swallowing a failed
// action into an ordinary, error-free panel — slog.Warn only, nothing on
// the re-rendered panel, unlike loadOpsPanel's own three read calls, which
// do set Error. That was recorded here deliberately, as honest
// documentation of a real bug rather than an oversight, because at the
// time fixing it was out of scope. It has since been fixed (opsAction now
// surfaces fn's error the same way loadOpsPanel does); see
// TestAFailedSessionOpsActionTellsTheOperator for the corrected behaviour.
func TestSessionOpsAppStateResyncReachesTheBackendWithNoFilter(t *testing.T) {
	called := false
	var gotCollections []string
	var gotMode string
	backend := &fakeOpsBackend{
		fakeGatewayBackend: &fakeGatewayBackend{name: "waxum-shaped"},
		appStateResyncFn: func(collections []string, mode string) (wa.AppStateResyncResult, error) {
			called = true
			gotCollections = collections
			gotMode = mode
			return wa.AppStateResyncResult{AllSynced: true}, nil
		},
	}
	srv, sessions, s, _ := newSessionAdminTestServer(t, backend, true)

	res := authedPostForm(t, srv, sessions, "/sessions/"+s.ID+"/ops/appstate-resync", url.Values{})
	if res.Code != http.StatusOK {
		t.Fatalf("POST /sessions/%s/ops/appstate-resync = %d: %s", s.ID, res.Code, res.Body.String())
	}
	if !called {
		t.Fatal("wa.SessionOps.AppStateResync was never actually called")
	}
	if gotCollections != nil {
		t.Errorf("AppStateResync got a collection filter %v, want nil (full resync)", gotCollections)
	}
	if gotMode != "" {
		t.Errorf("AppStateResync got mode %q, want \"\" (waxum's default mode)", gotMode)
	}
	if !strings.Contains(res.Body.String(), "App-state resync") {
		t.Errorf("panel did not re-render after the action; got:\n%s", res.Body.String())
	}
}

// TestAFailedSessionOpsActionTellsTheOperator is the fix for the bug
// TestSessionOpsAppStateResyncReachesTheBackendWithNoFilter's doc comment
// used to pin: a failed Pause must land on the re-rendered #ops-panel as
// an Error, the same way loadOpsPanel's own read calls already surface
// one, instead of opsAction only slog.Warn-ing it and rendering back an
// ordinary, error-free panel that tells the operator nothing happened.
func TestAFailedSessionOpsActionTellsTheOperator(t *testing.T) {
	backend := &fakeOpsBackend{
		fakeGatewayBackend: &fakeGatewayBackend{name: "waxum-shaped"},
		pauseFn: func() (bool, error) {
			return false, errors.New("waxum: gateway unreachable")
		},
	}
	srv, sessions, s, _ := newSessionAdminTestServer(t, backend, true)

	res := authedPostForm(t, srv, sessions, "/sessions/"+s.ID+"/ops/pause", url.Values{})
	if res.Code != http.StatusOK {
		t.Fatalf("POST /sessions/%s/ops/pause = %d: %s", s.ID, res.Code, res.Body.String())
	}
	body := res.Body.String()
	if !strings.Contains(body, "waxum: gateway unreachable") {
		t.Errorf("panel does not surface the failed action's error; got:\n%s", body)
	}
	if !strings.Contains(body, "alert-error") {
		t.Errorf("panel does not render the failed action as an error alert; got:\n%s", body)
	}
}

// TestSessionWebhookRegisterAddsARowThatThePollPicksUp is the webhook-form
// equivalent of TestSessionOpsPauseTogglesThePanel: registering must reach
// wa.WebhookAdmin.RegisterWebhook AND the re-rendered #webhook-rows must
// show the new row, not just a 200.
func TestSessionWebhookRegisterAddsARowThatThePollPicksUp(t *testing.T) {
	var registered []wa.WebhookInfo
	backend := &fakeOpsBackend{
		fakeGatewayBackend: &fakeGatewayBackend{name: "waxum-shaped"},
		listWebhooksFn: func() ([]wa.WebhookInfo, error) {
			return registered, nil
		},
		registerWebhookFn: func(spec wa.WebhookSpec) (wa.WebhookInfo, error) {
			wh := wa.WebhookInfo{ID: "wh-1", URL: spec.URL, Events: spec.Events, Enabled: true}
			registered = append(registered, wh)
			return wh, nil
		},
	}
	srv, sessions, s, _ := newSessionAdminTestServer(t, backend, true)

	before := authedGet(t, srv, sessions, "/sessions/"+s.ID+"/webhooks/rows")
	if !strings.Contains(before.Body.String(), "No webhooks registered") {
		t.Fatalf("webhook rows already show a row before RegisterWebhook was ever called:\n%s", before.Body.String())
	}

	form := url.Values{"url": {"https://example.test/hook"}, "events": {"message,receipt"}}
	res := authedPostForm(t, srv, sessions, "/sessions/"+s.ID+"/webhooks", form)
	if res.Code != http.StatusOK {
		t.Fatalf("POST /sessions/%s/webhooks = %d: %s", s.ID, res.Code, res.Body.String())
	}
	if len(registered) != 1 {
		t.Fatalf("wa.WebhookAdmin.RegisterWebhook was never actually called; registered=%v", registered)
	}
	if !strings.Contains(res.Body.String(), "https://example.test/hook") {
		t.Errorf("webhook rows do not show the newly registered webhook; got:\n%s", res.Body.String())
	}
}

// TestSessionWebhookDLQReplayRemovesTheEntry is the DLQ-table equivalent:
// replaying must reach wa.WebhookAdmin.ReplayWebhookDLQEntry AND the
// re-rendered #webhook-dlq-rows must no longer show the entry.
func TestSessionWebhookDLQReplayRemovesTheEntry(t *testing.T) {
	entries := []wa.DLQEntry{{
		ID: "dlq-1", Event: "message", WebhookURL: "https://example.test/hook",
		Attempts: 3, LastError: "delivery timed out", FailedAt: 1735689600,
	}}
	replayed := false
	backend := &fakeOpsBackend{
		fakeGatewayBackend: &fakeGatewayBackend{name: "waxum-shaped"},
		webhookDLQFn: func() ([]wa.DLQEntry, error) {
			return entries, nil
		},
		replayWebhookDLQEntryFn: func(entryID string) error {
			replayed = true
			entries = nil
			return nil
		},
	}
	srv, sessions, s, _ := newSessionAdminTestServer(t, backend, true)

	before := authedGet(t, srv, sessions, "/sessions/"+s.ID+"/webhooks/dlq/rows")
	if !strings.Contains(before.Body.String(), "delivery timed out") {
		t.Fatalf("DLQ rows do not show the seeded entry before replay:\n%s", before.Body.String())
	}

	res := authedPostForm(t, srv, sessions, "/sessions/"+s.ID+"/webhooks/dlq/dlq-1/replay", url.Values{})
	if res.Code != http.StatusOK {
		t.Fatalf("POST /sessions/%s/webhooks/dlq/dlq-1/replay = %d: %s", s.ID, res.Code, res.Body.String())
	}
	if !replayed {
		t.Error("wa.WebhookAdmin.ReplayWebhookDLQEntry was never actually called")
	}
	if strings.Contains(res.Body.String(), "delivery timed out") {
		t.Errorf("DLQ rows still show the replayed entry; got:\n%s", res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "dead-letter queue is empty") {
		t.Errorf("DLQ rows do not report the queue as empty after the only entry was replayed; got:\n%s", res.Body.String())
	}
}

// TestTogglingMasterRerendersTheRowsWithTheBadge drives the UI toggle
// (POST {BasePath}/sessions/{id}/master, hx-vals master=true|false) end to
// end: session created through Manager.Create (newSessionAdminTestServer
// already does this — see its doc comment on why a hand-made id 404s on
// Manager.Get), toggled on, badge present in the rerendered rows partial,
// toggled back off, badge gone.
func TestTogglingMasterRerendersTheRowsWithTheBadge(t *testing.T) {
	srv, sessions, s, _ := newSessionAdminTestServer(t, whatsmeowShapedBackend(), true)

	on := authedPostForm(t, srv, sessions, "/sessions/"+s.ID+"/master", url.Values{"master": {"true"}})
	if on.Code != http.StatusOK {
		t.Fatalf("POST /sessions/%s/master (on) = %d: %s", s.ID, on.Code, on.Body.String())
	}
	if !strings.Contains(on.Body.String(), "badge-master") {
		t.Fatalf("rows do not carry badge-master after setting master; got:\n%s", on.Body.String())
	}

	off := authedPostForm(t, srv, sessions, "/sessions/"+s.ID+"/master", url.Values{"master": {"false"}})
	if off.Code != http.StatusOK {
		t.Fatalf("POST /sessions/%s/master (off) = %d: %s", s.ID, off.Code, off.Body.String())
	}
	if strings.Contains(off.Body.String(), "badge-master") {
		t.Fatalf("rows still carry badge-master after unsetting master; got:\n%s", off.Body.String())
	}
}

// TestEveryNewTableIsWrappedInTableScroll guards the JID-column hazard
// (CLAUDE.md, and datatable.css's own comment): every table this phase
// adds must sit inside a .table-scroll wrapper, or a wide table drags the
// whole document sideways on a 360px phone.
func TestEveryNewTableIsWrappedInTableScroll(t *testing.T) {
	for _, path := range []string{
		"web/templates/calls.html",
		"web/templates/sessions/webhooks.html",
		"web/templates/verify.html",
	} {
		t.Run(path, func(t *testing.T) {
			b, err := fs.ReadFile(wsm.FS, path)
			if err != nil {
				t.Fatal(err)
			}
			src := string(b)
			tables := strings.Count(src, "<table")
			scrollers := strings.Count(src, `class="table-scroll"`)
			if tables == 0 {
				t.Fatalf("%s has no <table> at all", path)
			}
			if scrollers < tables {
				t.Errorf("%s has %d <table> but only %d .table-scroll wrapper(s)", path, tables, scrollers)
			}
		})
	}
}

// TestTheEditDialogRendersNameChipsMasterAndSelfReminder drives
// GetSessionEdit end to end: the seeded session already carries a name,
// two groups and the master flag; the dialog must prefill all of it, plus
// offer every known group code (its own and a second session's) for the
// chip input's datalist.
func TestTheEditDialogRendersNameChipsMasterAndSelfReminder(t *testing.T) {
	srv, sessions, s, _ := newSessionAdminTestServer(t, whatsmeowShapedBackend(), true)

	if err := s.SetGroupCodes(context.Background(), []string{"kemang", "alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMaster(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	createRes := authedPostForm(t, srv, sessions, "/sessions", url.Values{
		"name": {"Second Gateway"}, "phone": {"+62 812-3333-4444"}, "group_code": {"ops"},
	})
	if createRes.Code != http.StatusSeeOther {
		t.Fatalf("POST /sessions (second session) = %d, want 303: %s", createRes.Code, createRes.Body.String())
	}

	res := authedGet(t, srv, sessions, "/sessions/"+s.ID+"/edit")
	if res.Code != http.StatusOK {
		t.Fatalf("GET /sessions/%s/edit = %d: %s", s.ID, res.Code, res.Body.String())
	}
	body := res.Body.String()
	if !strings.Contains(body, `value="gw"`) {
		t.Errorf("edit dialog does not prefill the session's own name; body:\n%s", body)
	}
	for _, code := range []string{"alpha", "kemang", "ops"} {
		if !strings.Contains(body, `<option value="`+code+`">`) {
			t.Errorf("edit dialog's datalist is missing an <option> for %q; body:\n%s", code, body)
		}
	}
	if !strings.Contains(body, "alpha,kemang") {
		t.Errorf("edit dialog does not carry the session's own sorted group set in its hidden field; body:\n%s", body)
	}
	if !strings.Contains(body, `name="master"`) || !strings.Contains(body, `name="self_reminder"`) {
		t.Errorf("edit dialog is missing the master or self_reminder toggle; body:\n%s", body)
	}
	if !strings.Contains(body, "checked") {
		t.Errorf("edit dialog does not show the master toggle as checked for a master session; body:\n%s", body)
	}
}

// TestTheEditDialogIs404ForAnUnknownSession matches every other
// single-session GET handler's behaviour on a stale/unknown id.
func TestTheEditDialogIs404ForAnUnknownSession(t *testing.T) {
	srv, sessions, _, _ := newSessionAdminTestServer(t, whatsmeowShapedBackend(), true)
	if res := authedGet(t, srv, sessions, "/sessions/does-not-exist/edit"); res.Code != http.StatusNotFound {
		t.Fatalf("GET = %d, want 404", res.Code)
	}
	if res := authedPostForm(t, srv, sessions, "/sessions/does-not-exist/edit", url.Values{"name": {"x"}}); res.Code != http.StatusNotFound {
		t.Fatalf("POST = %d, want 404", res.Code)
	}
}

// TestSavingTheEditDialogAppliesNameGroupsAndMasterTogetherAndFiresHXTrigger
// is the happy path for PostSessionEdit: one POST changes the name, the
// routing-group set AND the master flag at once, fires HX-Trigger:
// sessions-changed, and the rows reflect every change.
func TestSavingTheEditDialogAppliesNameGroupsAndMasterTogetherAndFiresHXTrigger(t *testing.T) {
	srv, sessions, s, _ := newSessionAdminTestServer(t, whatsmeowShapedBackend(), true)

	res := authedPostForm(t, srv, sessions, "/sessions/"+s.ID+"/edit", url.Values{
		"name": {"Renamed Gateway"}, "group_codes": {"beta,alpha"}, "master": {"true"},
	})
	if res.Code != http.StatusOK {
		t.Fatalf("POST edit = %d: %s", res.Code, res.Body.String())
	}
	if got := res.Header().Get("HX-Trigger"); got != "sessions-changed" {
		t.Errorf(`HX-Trigger = %q, want "sessions-changed"`, got)
	}

	snap := s.Snapshot()
	if snap.Name != "Renamed Gateway" {
		t.Errorf("Name = %q, want %q", snap.Name, "Renamed Gateway")
	}
	if len(snap.GroupCodes) != 2 || snap.GroupCodes[0] != "alpha" || snap.GroupCodes[1] != "beta" {
		t.Errorf("GroupCodes = %v, want [alpha beta]", snap.GroupCodes)
	}
	if !snap.IsMaster {
		t.Error("IsMaster = false after master=true in the edit form")
	}

	rows := authedGet(t, srv, sessions, "/sessions/rows").Body.String()
	if !strings.Contains(rows, "Renamed Gateway") || !strings.Contains(rows, "badge-master") {
		t.Errorf("rows do not reflect the edit; body:\n%s", rows)
	}
}

// TestSavingTheEditDialogCanMoveTheSelfReminderFlag proves PostSessionEdit
// also drives Manager.SetSelfReminder, and that the dialog's hint names the
// session the flag will move FROM.
func TestSavingTheEditDialogCanMoveTheSelfReminderFlag(t *testing.T) {
	srv, sessions, s, _ := newSessionAdminTestServer(t, whatsmeowShapedBackend(), true)
	// Flag a SECOND session as self-reminder first, so the dialog's hint
	// has someone to name.
	createRes := authedPostForm(t, srv, sessions, "/sessions", url.Values{
		"name": {"Holder"}, "phone": {"+62 812-5555-6666"},
	})
	if createRes.Code != http.StatusSeeOther {
		t.Fatalf("POST /sessions (holder) = %d, want 303: %s", createRes.Code, createRes.Body.String())
	}
	rows := authedGet(t, srv, sessions, "/sessions/rows").Body.String()
	holderID := findSessionIDByName(t, rows, "Holder")

	res := authedPostForm(t, srv, sessions, "/sessions/"+holderID+"/edit", url.Values{
		"name": {"Holder"}, "self_reminder": {"true"},
	})
	if res.Code != http.StatusOK {
		t.Fatalf("POST edit (flag holder) = %d: %s", res.Code, res.Body.String())
	}

	getRes := authedGet(t, srv, sessions, "/sessions/"+s.ID+"/edit")
	if !strings.Contains(getRes.Body.String(), "Holder") {
		t.Errorf("edit dialog for a non-holder session does not name the current self-reminder holder; body:\n%s", getRes.Body.String())
	}

	// Now move the flag to s.
	moveRes := authedPostForm(t, srv, sessions, "/sessions/"+s.ID+"/edit", url.Values{
		"name": {s.Snapshot().Name}, "self_reminder": {"true"},
	})
	if moveRes.Code != http.StatusOK {
		t.Fatalf("POST edit (move flag) = %d: %s", moveRes.Code, moveRes.Body.String())
	}
	if !s.Snapshot().SelfReminder {
		t.Error("SelfReminder = false on s after moving the flag to it")
	}
}

// findSessionIDByName is a small helper for tests that create a session
// through the form (which never returns the id directly) and then need it
// — it scans the rows partial's hx-post targets for the row carrying name.
func findSessionIDByName(t *testing.T, rowsBody, name string) string {
	t.Helper()
	idx := strings.Index(rowsBody, name)
	if idx == -1 {
		t.Fatalf("name %q not found in rows body:\n%s", name, rowsBody)
	}
	// The nearest preceding hx-post="/sessions/{id}/deactivate" (every row
	// has one activate/deactivate/pair action before its name cell's
	// close, but more robust: search for /sessions/ immediately before idx
	// is fragile across markup changes). Simpler and robust: re-parse via
	// a regex for hx-post="/sessions/<id>/(activate|deactivate)" paired
	// with the row text window around name.
	before := rowsBody[:idx]
	tagStart := strings.LastIndex(before, "<tr>")
	if tagStart == -1 {
		t.Fatalf("could not find enclosing <tr> for name %q", name)
	}
	rowEnd := strings.Index(rowsBody[idx:], "</tr>")
	row := rowsBody[tagStart : idx+rowEnd]
	m := regexp.MustCompile(`/sessions/([a-f0-9]+)/`).FindStringSubmatch(row)
	if m == nil {
		t.Fatalf("could not find a session id in row for name %q:\n%s", name, row)
	}
	return m[1]
}

// TestAnEditDialogValidationErrorLeavesEveryFieldUnchanged proves the
// "validate everything before changing anything" contract: an invalid
// group code must leave the name, groups, master AND self-reminder flags
// exactly as they were, even though the form also asked to change the
// name and set master.
func TestAnEditDialogValidationErrorLeavesEveryFieldUnchanged(t *testing.T) {
	srv, sessions, s, _ := newSessionAdminTestServer(t, whatsmeowShapedBackend(), true)
	before := s.Snapshot()

	res := authedPostForm(t, srv, sessions, "/sessions/"+s.ID+"/edit", url.Values{
		"name": {"Should Not Apply"}, "group_codes": {"Bad Code!"}, "master": {"true"},
	})
	if res.Code != http.StatusOK {
		t.Fatalf("POST edit (invalid) = %d: %s", res.Code, res.Body.String())
	}
	if got := res.Header().Get("HX-Trigger"); got != "" {
		t.Errorf("HX-Trigger = %q on a rejected save, want none", got)
	}
	body := res.Body.String()
	if !strings.Contains(body, "alert-error") {
		t.Errorf("rejected edit does not render alert-error; body:\n%s", body)
	}

	after := s.Snapshot()
	if after.Name != before.Name {
		t.Errorf("Name changed to %q despite a rejected save (was %q)", after.Name, before.Name)
	}
	if after.IsMaster != before.IsMaster {
		t.Errorf("IsMaster changed despite a rejected save")
	}
	if len(after.GroupCodes) != len(before.GroupCodes) {
		t.Errorf("GroupCodes changed despite a rejected save: %v -> %v", before.GroupCodes, after.GroupCodes)
	}
}

// TestAnEmptyNameInTheEditDialogIsRejectedWithoutTouchingGroups is the
// twin of the test above from the OTHER direction: a bad NAME must not
// silently let a valid group-codes change through either.
func TestAnEmptyNameInTheEditDialogIsRejectedWithoutTouchingGroups(t *testing.T) {
	srv, sessions, s, _ := newSessionAdminTestServer(t, whatsmeowShapedBackend(), true)

	res := authedPostForm(t, srv, sessions, "/sessions/"+s.ID+"/edit", url.Values{
		"name": {"   "}, "group_codes": {"alpha"},
	})
	if res.Code != http.StatusOK {
		t.Fatalf("POST edit (empty name) = %d: %s", res.Code, res.Body.String())
	}
	if len(s.Snapshot().GroupCodes) != 0 {
		t.Errorf("GroupCodes = %v after a rejected (empty-name) save, want untouched (empty)", s.Snapshot().GroupCodes)
	}
}

// TestTheDeleteConfirmDialogPrefillsNameAndPhone drives
// GetSessionDeleteConfirm end to end.
func TestTheDeleteConfirmDialogPrefillsNameAndPhone(t *testing.T) {
	srv, sessions, s, _ := newSessionAdminTestServer(t, whatsmeowShapedBackend(), true)
	res := authedGet(t, srv, sessions, "/sessions/"+s.ID+"/delete-confirm")
	if res.Code != http.StatusOK {
		t.Fatalf("GET delete-confirm = %d: %s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	snap := s.Snapshot()
	if !strings.Contains(body, snap.Name) || !strings.Contains(body, snap.Phone) {
		t.Errorf("delete confirmation does not show the session's name/phone; body:\n%s", body)
	}
	for _, want := range []string{"does NOT unlink", "Linked devices"} {
		if !strings.Contains(body, want) {
			t.Errorf("delete confirmation does not contain %q; body:\n%s", want, body)
		}
	}
}

// TestTheDeleteConfirmDialogIs404ForAnUnknownSession matches every other
// single-session GET handler's behaviour on a stale/unknown id.
func TestTheDeleteConfirmDialogIs404ForAnUnknownSession(t *testing.T) {
	srv, sessions, _, _ := newSessionAdminTestServer(t, whatsmeowShapedBackend(), true)
	if res := authedGet(t, srv, sessions, "/sessions/does-not-exist/delete-confirm"); res.Code != http.StatusNotFound {
		t.Fatalf("GET = %d, want 404", res.Code)
	}
}
