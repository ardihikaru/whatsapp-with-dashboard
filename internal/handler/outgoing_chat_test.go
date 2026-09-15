package handler

import (
	"context"
	"database/sql"
	"fmt"
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

// newOutgoingChatTestServer mirrors newLogsTestServer (logs_test.go) but
// additionally registers sessions in the LIVE manager (via manager.Create)
// rather than only inserting a wa_sessions row directly — GetOutgoingChat
// enumerates h.Manager.Snapshots() (every session, connected or not), which
// a nil-backend manager never populates on its own. sendRate<=0 leaves
// Deps.SendRate at its zero value, exercising the fallback in
// effectiveSendRate.
func newOutgoingChatTestServer(t *testing.T, sendRate float64, sendBurst int) (http.Handler, *auth.Store, *sql.DB, *message.SQLRepository, *wa.Manager) {
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
		SendRate:    sendRate, SendBurst: sendBurst,
	})
	return srv, sessions, pool, msgRepo, manager
}

// seedSentGap inserts n successful sends on sessionID, `gap` apart, starting
// at base, writing sent_at directly via raw SQL (not repo.Insert/MarkSent,
// which can only stamp time.Now()) — this test's whole point is to pin an
// exact, known cadence, the same reason message/gap_test.go seeds the same
// way.
func seedSentGap(t *testing.T, pool *sql.DB, sessionID string, n int, base time.Time, gap time.Duration, toPhone string) {
	t.Helper()
	for i := 0; i < n; i++ {
		ts := base.Add(time.Duration(i) * gap).Format(time.RFC3339)
		id := fmt.Sprintf("%s-m%d", sessionID, i)
		if _, err := pool.ExecContext(context.Background(),
			`INSERT INTO wa_messages (id, session_id, to_phone, body, status, created_at, sent_at)
			 VALUES (?, ?, ?, 'hi', 'sent', ?, ?)`, id, sessionID, toPhone, ts, ts); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOutgoingChatPageRendersRealData(t *testing.T) {
	srv, sessions, pool, repo, manager := newOutgoingChatTestServer(t, 1.0, 3)
	ctx := context.Background()

	paced, err := manager.Create(ctx, "Gateway Paced", "628111000")
	if err != nil {
		t.Fatal(err)
	}
	fast, err := manager.Create(ctx, "Gateway TooFast", "628222000")
	if err != nil {
		t.Fatal(err)
	}

	base := time.Now().UTC().Add(-time.Hour)
	// Paced: 2s apart, well above the 1s floor (SendRate=1.0).
	seedSentGap(t, pool, paced.ID, 3, base, 2*time.Second, "628999111")
	// TooFast: 100ms apart, well below the 1s floor.
	seedSentGap(t, pool, fast.ID, 3, base, 100*time.Millisecond, "628999222")
	// A failed send on the paced session, to prove Failed count is real.
	if err := repo.Insert(ctx, &message.Message{ID: "mfail", SessionID: paced.ID, ToPhone: "628999333", Body: "x", Status: message.StatusFailed}); err != nil {
		t.Fatal(err)
	}

	res := authedGet(t, srv, sessions, "/outgoing-chat")
	if res.Code != http.StatusOK {
		t.Fatalf("GET /outgoing-chat = %d: %s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	for _, want := range []string{
		"Outgoing Chat", "Gateway Paced", "628111000", "Gateway TooFast", "628222000",
		"2s", // Paced's Min/Max/Avg gap
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("/outgoing-chat body missing %q; body:\n%s", want, body)
		}
	}

	rows := authedGet(t, srv, sessions, "/outgoing-chat/rows")
	if rows.Code != http.StatusOK {
		t.Fatalf("GET /outgoing-chat/rows = %d", rows.Code)
	}
	if !strings.Contains(rows.Body.String(), "628999222") {
		t.Fatalf("/outgoing-chat/rows missing a TooFast-session message row")
	}
}

// TestOutgoingChatPageRequiresSession: platform-only, same guard as /logs
// (TestLogsPageRequiresSession).
func TestOutgoingChatPageRequiresSession(t *testing.T) {
	srv, _, _, _, _ := newOutgoingChatTestServer(t, 1.0, 3)
	req := httptest.NewRequest(http.MethodGet, "/outgoing-chat", nil)
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, req)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("unauth GET /outgoing-chat = %d, want 303 redirect to login", res.Code)
	}
}

// TestOutgoingChatFlagsASessionAveragingBelowTheConfiguredFloor is the test
// that would fail if the violation heuristic's direction were inverted (see
// outgoing_chat.go's Violation doc comment): a session whose average gap
// over the sampled window sits BELOW 1/SendRate must render the warning.
func TestOutgoingChatFlagsASessionAveragingBelowTheConfiguredFloor(t *testing.T) {
	srv, sessions, pool, _, manager := newOutgoingChatTestServer(t, 1.0, 3) // floor = 1s
	ctx := context.Background()
	fast, err := manager.Create(ctx, "Gateway TooFast", "628222000")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour)
	seedSentGap(t, pool, fast.ID, 5, base, 100*time.Millisecond, "628999222")

	body := authedGet(t, srv, sessions, "/outgoing-chat").Body.String()
	if !strings.Contains(body, "di bawah batas") {
		t.Fatalf("a session averaging 100ms against a 1s floor is not flagged; body:\n%s", body)
	}
}

// TestOutgoingChatDoesNotFlagASessionPacedAboveTheFloor is the inverse case:
// a session comfortably above the floor must render no warning at all.
func TestOutgoingChatDoesNotFlagASessionPacedAboveTheFloor(t *testing.T) {
	srv, sessions, pool, _, manager := newOutgoingChatTestServer(t, 1.0, 3) // floor = 1s
	ctx := context.Background()
	paced, err := manager.Create(ctx, "Gateway Paced", "628111000")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour)
	seedSentGap(t, pool, paced.ID, 5, base, 2*time.Second, "628999111")

	body := authedGet(t, srv, sessions, "/outgoing-chat").Body.String()
	if strings.Contains(body, "di bawah batas") {
		t.Fatalf("a session averaging 2s against a 1s floor must not be flagged; body:\n%s", body)
	}
}
