package handler

import (
	"context"
	"errors"
	"io/fs"
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
	callRepo := call.NewRepository(pool)
	mgr := wa.NewManager(context.Background(), backend, wasession.NewRepository(pool),
		msgRepo, nil, wa.Config{SendRate: 100, SendBurst: 5, ReconcilePar: 2})
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

// TestTheGroupEditorPrefillsTheCurrentCodeAndOffersExistingOnes drives
// GetSessionGroupCodeEditor end to end: the seeded session already carries
// a code (set directly through wa.ManagedSession.SetGroupCode, the same
// call PostSessionSetGroupCode itself makes), a second session is created
// with a DIFFERENT code through the ordinary create form, and the editor
// GET must prefill the FIRST session's own code while offering BOTH codes
// in the datalist — plus the hidden view=list field the save handler
// branches on.
func TestTheGroupEditorPrefillsTheCurrentCodeAndOffersExistingOnes(t *testing.T) {
	srv, sessions, s, _ := newSessionAdminTestServer(t, whatsmeowShapedBackend(), true)

	if err := s.SetGroupCode(context.Background(), "kemang"); err != nil {
		t.Fatal(err)
	}
	createRes := authedPostForm(t, srv, sessions, "/sessions", url.Values{
		"name": {"Second Gateway"}, "phone": {"+62 812-3333-4444"}, "group_code": {"ops"},
	})
	// PostSessionCreate redirects (303) a non-HX-Request POST — see its own
	// doc comment — so a plain form post like this one is expected to
	// redirect, not render a partial.
	if createRes.Code != http.StatusSeeOther {
		t.Fatalf("POST /sessions (second session) = %d, want 303: %s", createRes.Code, createRes.Body.String())
	}

	res := authedGet(t, srv, sessions, "/sessions/"+s.ID+"/group-code/edit")
	if res.Code != http.StatusOK {
		t.Fatalf("GET /sessions/%s/group-code/edit = %d: %s", s.ID, res.Code, res.Body.String())
	}
	body := res.Body.String()
	if !strings.Contains(body, `value="kemang"`) {
		t.Errorf("editor does not prefill the session's own group code (kemang); body:\n%s", body)
	}
	for _, code := range []string{"kemang", "ops"} {
		if !strings.Contains(body, `<option value="`+code+`">`) {
			t.Errorf("editor's datalist is missing an <option> for %q; body:\n%s", code, body)
		}
	}
	if !strings.Contains(body, `name="view" value="list"`) {
		t.Errorf(`editor form does not carry hidden name="view" value="list"; body:`+"\n%s", body)
	}
}

// TestTheGroupEditorIs404ForAnUnknownSession matches every other
// single-session GET handler's behaviour on a stale/unknown id.
func TestTheGroupEditorIs404ForAnUnknownSession(t *testing.T) {
	srv, sessions, _, _ := newSessionAdminTestServer(t, whatsmeowShapedBackend(), true)

	res := authedGet(t, srv, sessions, "/sessions/does-not-exist/group-code/edit")
	if res.Code != http.StatusNotFound {
		t.Fatalf("GET /sessions/does-not-exist/group-code/edit = %d, want 404: %s", res.Code, res.Body.String())
	}
}

// TestSavingFromTheListEditorRefreshesTheRowsViaHXTrigger drives the
// list-editor save path end to end: view=list must render the editor
// partial back (not the ops page's session_group_code_result), report the
// save with "Saved" copy, and fire HX-Trigger: sessions-changed so
// #session-rows' hx-trigger (list.html) picks up the new badge — proven
// here by re-fetching /sessions/rows and finding the badge for real,
// rather than trusting the header alone.
func TestSavingFromTheListEditorRefreshesTheRowsViaHXTrigger(t *testing.T) {
	srv, sessions, s, _ := newSessionAdminTestServer(t, whatsmeowShapedBackend(), true)

	res := authedPostForm(t, srv, sessions, "/sessions/"+s.ID+"/group-code",
		url.Values{"view": {"list"}, "group_code": {"kemang"}})
	if res.Code != http.StatusOK {
		t.Fatalf("POST /sessions/%s/group-code (view=list) = %d: %s", s.ID, res.Code, res.Body.String())
	}
	if got := res.Header().Get("HX-Trigger"); got != "sessions-changed" {
		t.Errorf(`HX-Trigger = %q, want "sessions-changed"`, got)
	}
	if !strings.Contains(res.Body.String(), "Saved") {
		t.Errorf("list-editor save response does not say \"Saved\"; body:\n%s", res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "<form") {
		t.Errorf("list-editor save response does not still carry the editor form; body:\n%s", res.Body.String())
	}

	rows := authedGet(t, srv, sessions, "/sessions/rows")
	rowsBody := rows.Body.String()
	if !strings.Contains(rowsBody, "badge-group") || !strings.Contains(rowsBody, "kemang") {
		t.Errorf("rows do not show the saved group code after the list-editor save; body:\n%s", rowsBody)
	}
}

// TestAMalformedCodeFromTheListEditorStaysInTheEditorWithTheError is the
// failure-path twin of the test above: an invalid code must re-render the
// editor (not the rows) with the operator's own typed value still in the
// field, an inline error, and NO HX-Trigger — a bad save must not tell the
// tbody anything changed.
func TestAMalformedCodeFromTheListEditorStaysInTheEditorWithTheError(t *testing.T) {
	srv, sessions, s, _ := newSessionAdminTestServer(t, whatsmeowShapedBackend(), true)

	res := authedPostForm(t, srv, sessions, "/sessions/"+s.ID+"/group-code",
		url.Values{"view": {"list"}, "group_code": {"Bad Code!"}})
	if res.Code != http.StatusOK {
		t.Fatalf("POST /sessions/%s/group-code (malformed, view=list) = %d: %s", s.ID, res.Code, res.Body.String())
	}
	if got := res.Header().Get("HX-Trigger"); got != "" {
		t.Errorf("HX-Trigger = %q on a failed save, want empty", got)
	}
	body := res.Body.String()
	if !strings.Contains(body, "alert-error") {
		t.Errorf("malformed list-editor save does not render alert-error; body:\n%s", body)
	}
	if !strings.Contains(body, `value="Bad Code!"`) {
		t.Errorf("malformed list-editor save does not keep the operator's typed value in the field; body:\n%s", body)
	}
	if !strings.Contains(body, "<form") {
		t.Errorf("malformed list-editor save does not still carry the editor form; body:\n%s", body)
	}

	rows := authedGet(t, srv, sessions, "/sessions/rows")
	if strings.Contains(rows.Body.String(), "badge-group") {
		t.Errorf("rows show a group badge after a malformed save that should not have persisted anything; body:\n%s", rows.Body.String())
	}
}

// TestTheOpsPageGroupSaveStillRendersItsOwnResultPartial guards the branch
// split in PostSessionSetGroupCode: a POST with no "view" field (the ops
// page's own form, ops.html) must keep rendering
// session_group_code_result, unchanged, with no HX-Trigger header — proving
// the list-editor addition did not alter the ops page's existing behaviour.
func TestTheOpsPageGroupSaveStillRendersItsOwnResultPartial(t *testing.T) {
	srv, sessions, s, _ := newSessionAdminTestServer(t, whatsmeowShapedBackend(), true)

	res := authedPostForm(t, srv, sessions, "/sessions/"+s.ID+"/group-code",
		url.Values{"group_code": {"kemang"}})
	if res.Code != http.StatusOK {
		t.Fatalf("POST /sessions/%s/group-code (ops page, no view) = %d: %s", s.ID, res.Code, res.Body.String())
	}
	if got := res.Header().Get("HX-Trigger"); got != "" {
		t.Errorf("HX-Trigger = %q on the ops page's own save, want empty", got)
	}
	body := res.Body.String()
	if strings.Contains(body, `id="session-group-editor"`) || strings.Contains(body, "<form") {
		t.Errorf("ops page's save rendered the list editor instead of session_group_code_result; body:\n%s", body)
	}
	if !strings.Contains(body, "Saved") {
		t.Errorf("ops page's save response does not say \"Saved\"; body:\n%s", body)
	}
}
