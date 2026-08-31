package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	wsm "github.com/ardihikaru/whatsapp-with-dashboard"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/auth"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/apiclient"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/inbound"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/renderer"
)

// newInboundTestServer is a sibling of newAuthTestServer (api_auth_test.go)
// that additionally wires Deps.Inbound to a real SQLite-backed
// inbound.Repository, so GetAPIInbound (api_inbound.go) can be driven
// through the real HTTP surface rather than called directly.
func newInboundTestServer(t *testing.T) (http.Handler, *apiclient.Repository, inbound.Repository) {
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
	inboundRepo := inbound.NewRepository(pool)
	srv := New(Deps{
		Renderer: rend, Sessions: sessions,
		Middleware: auth.NewMiddleware(sessions, apiClients, ""),
		APIClients: apiClients, SessionTTL: time.Hour, AssetsFS: wsm.FS,
		Inbound: inboundRepo,
	})
	return srv, apiClients, inboundRepo
}

// getInbound issues GET /api/v1/inbound with the given query string,
// authenticated with Basic auth (loginID/secret), and returns the raw
// response.
func getInbound(t *testing.T, srv http.Handler, loginID, secret, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/inbound"+query, nil)
	if loginID != "" {
		req.SetBasicAuth(loginID, secret)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// TestGetAPIInboundRejectsAMissingSinceWithTheExactPinnedMessage proves the
// brief's non-negotiable wire contract: since is required, and the error
// body's text is read verbatim by a deployed consumer
// (pkg/wsmclient/inbound.go), so it must not be reworded.
func TestGetAPIInboundRejectsAMissingSinceWithTheExactPinnedMessage(t *testing.T) {
	srv, clients, _ := newInboundTestServer(t)
	secret := newBasicClient(t, clients, "clino")

	rec := getInbound(t, srv, "clino", secret, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("GET /api/v1/inbound with no since got %d, want 400: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode error body: %v (%s)", err, rec.Body.String())
	}
	if out.Error != "since is required (RFC3339)" {
		t.Fatalf("error = %q, want the pinned message %q", out.Error, "since is required (RFC3339)")
	}
}

// TestGetAPIInboundRejectsAnUnparseableSinceWithTheExactPinnedMessage covers
// the other pinned 400 message, for a since value that fails
// time.Parse(time.RFC3339, ...).
func TestGetAPIInboundRejectsAnUnparseableSinceWithTheExactPinnedMessage(t *testing.T) {
	srv, clients, _ := newInboundTestServer(t)
	secret := newBasicClient(t, clients, "clino")

	rec := getInbound(t, srv, "clino", secret, "?since=not-a-date")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("GET /api/v1/inbound?since=not-a-date got %d, want 400: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode error body: %v (%s)", err, rec.Body.String())
	}
	if out.Error != "since must be RFC3339" {
		t.Fatalf("error = %q, want the pinned message %q", out.Error, "since must be RFC3339")
	}
}

// TestGetAPIInboundReturnsAStoredArrivalWithEveryFieldMappedAsABareJSONArray
// proves the response shape end to end: every apiInbound field maps from
// the stored row, and the body is a bare array (not an object wrapping
// one) — the consumer decodes straight into a slice.
func TestGetAPIInboundReturnsAStoredArrivalWithEveryFieldMappedAsABareJSONArray(t *testing.T) {
	srv, clients, repo := newInboundTestServer(t)
	secret := newBasicClient(t, clients, "clino")

	received := time.Date(2026, 8, 31, 5, 14, 0, 0, time.UTC)
	if err := repo.Insert(context.Background(), &inbound.Message{
		ID: "wamid-1", SessionID: "sess-1", FromPhone: "628511110000",
		FromJID: "628511110000@s.whatsapp.net", Body: "sudah", ReceivedAt: received,
		ClientRef: "client-abc", TenantRef: "tenant-xyz",
	}); err != nil {
		t.Fatalf("seed insert: %v", err)
	}

	rec := getInbound(t, srv, "clino", secret, "?since=2026-08-31T00:00:00Z")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/inbound got %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var out []apiInbound
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response as array: %v (%s)", err, rec.Body.String())
	}
	if len(out) != 1 {
		t.Fatalf("got %d rows, want 1: %+v", len(out), out)
	}
	got := out[0]
	if got.ID != "wamid-1" || got.FromPhone != "628511110000" || got.Body != "sudah" ||
		!got.ReceivedAt.Equal(received) || got.ClientRef != "client-abc" || got.TenantRef != "tenant-xyz" {
		t.Fatalf("row = %+v, want id=wamid-1 from_phone=628511110000 body=sudah "+
			"received_at=%s client_ref=client-abc tenant_ref=tenant-xyz", got, received)
	}
}

// TestGetAPIInboundReturnsTwoArrivalsOldestFirstThroughTheRealHTTPSurface
// proves ordering is honoured end to end (not just at the repository
// layer, which internal/domain/inbound already covers): a consumer
// resolving a reply against an open question must not see a later message
// before an earlier one.
func TestGetAPIInboundReturnsTwoArrivalsOldestFirstThroughTheRealHTTPSurface(t *testing.T) {
	srv, clients, repo := newInboundTestServer(t)
	secret := newBasicClient(t, clients, "clino")

	older := time.Date(2026, 8, 31, 5, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 8, 31, 6, 0, 0, 0, time.UTC)
	// Insert newer first, to prove the returned order is by received_at,
	// not insertion order.
	if err := repo.Insert(context.Background(), &inbound.Message{
		ID: "wamid-newer", SessionID: "sess-1", FromPhone: "628511110000",
		FromJID: "628511110000@s.whatsapp.net", Body: "second", ReceivedAt: newer,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Insert(context.Background(), &inbound.Message{
		ID: "wamid-older", SessionID: "sess-1", FromPhone: "628511110000",
		FromJID: "628511110000@s.whatsapp.net", Body: "first", ReceivedAt: older,
	}); err != nil {
		t.Fatal(err)
	}

	rec := getInbound(t, srv, "clino", secret, "?since=2026-08-31T00:00:00Z")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var out []apiInbound
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if len(out) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(out), out)
	}
	if out[0].Body != "first" || out[1].Body != "second" {
		t.Fatalf("order = [%q, %q], want [\"first\", \"second\"] (oldest first)", out[0].Body, out[1].Body)
	}
}

// TestGetAPIInboundReturnsAnEmptyJSONArrayNeverNullWhenNothingMatches
// asserts on the raw response bytes, not a decoded slice — a decoded Go
// slice hides the null-vs-[] distinction, but the consumer's own decoder
// does not have to, and a bare "null" is not a valid array to range over
// in every client.
func TestGetAPIInboundReturnsAnEmptyJSONArrayNeverNullWhenNothingMatches(t *testing.T) {
	srv, clients, _ := newInboundTestServer(t)
	secret := newBasicClient(t, clients, "clino")

	rec := getInbound(t, srv, "clino", secret, "?since=2026-08-31T00:00:00Z")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := bytes.TrimSpace(rec.Body.Bytes())
	if string(body) != "[]" {
		t.Fatalf("body = %q, want exactly %q (never null)", body, "[]")
	}
}

// TestGetAPIInboundAnswers503NotFoundOr500WhenInboundIsUnset proves the
// nil-guard: a deployment (or, as here, any pre-existing test server) that
// never wires Deps.Inbound must get a clear "not configured" signal, not a
// 404 (which would look like a routing mistake) and not a 500 (which would
// look like a bug).
func TestGetAPIInboundAnswers503NotFoundOr500WhenInboundIsUnset(t *testing.T) {
	srv, _, clients := newAuthTestServer(t) // Deps.Inbound left nil, deliberately
	secret := newBasicClient(t, clients, "clino")

	rec := getInbound(t, srv, "clino", secret, "?since=2026-08-31T00:00:00Z")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode error body: %v (%s)", err, rec.Body.String())
	}
	if out.Error != "inbound log unavailable" {
		t.Fatalf("error = %q, want %q", out.Error, "inbound log unavailable")
	}
}

// TestGetAPIInboundRejectsABrowserSessionCookieAndAcceptsBasicAuth proves
// "auth surfaces do not mix" (CLAUDE.md) holds for this new route too: a
// browser cookie minted by the login page must be rejected on /api/v1, and
// HTTP Basic — the consumer's actual auth method — must be accepted.
func TestGetAPIInboundRejectsABrowserSessionCookieAndAcceptsBasicAuth(t *testing.T) {
	srv, sessions, clients := newAuthTestServer(t)

	// A real browser session cookie, rejected on /api/v1/inbound.
	webSess, err := sessions.Create("admin")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/inbound?since=2026-08-31T00:00:00Z", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: webSess.Token})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("cookie-authenticated request got %d, want 401 (cookies must not work on /api/v1): %s",
			rec.Code, rec.Body.String())
	}

	// Basic auth, accepted — the endpoint answers 503 here only because
	// this fixture leaves Deps.Inbound nil, which proves auth ran and
	// passed BEFORE the nil-guard, not that Basic itself failed.
	secret := newBasicClient(t, clients, "clino")
	rec2 := getInbound(t, srv, "clino", secret, "?since=2026-08-31T00:00:00Z")
	if rec2.Code == http.StatusUnauthorized {
		t.Fatalf("Basic-authenticated request got 401, want it to be admitted (503 from the nil Inbound guard is fine): %s",
			rec2.Body.String())
	}
}

// TestGetAPIInboundHonoursLimitAndTheRepositorysClampAppliesEndToEnd proves
// limit is passed straight through to the repository (which owns the
// clamp) rather than being re-clamped in the handler: limit=1 returns
// exactly one row out of several, and a garbage (non-numeric) limit does
// NOT 400 — it becomes 0, which the repository clamps up to its own
// default.
func TestGetAPIInboundHonoursLimitAndTheRepositorysClampAppliesEndToEnd(t *testing.T) {
	srv, clients, repo := newInboundTestServer(t)
	secret := newBasicClient(t, clients, "clino")

	base := time.Date(2026, 8, 31, 5, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if err := repo.Insert(context.Background(), &inbound.Message{
			ID: "wamid-" + string(rune('a'+i)), SessionID: "sess-1", FromPhone: "628511110000",
			FromJID: "628511110000@s.whatsapp.net", Body: "x", ReceivedAt: base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}

	// limit=1 must return exactly one row.
	rec := getInbound(t, srv, "clino", secret, "?since=2026-08-31T00:00:00Z&limit=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var out []apiInbound
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if len(out) != 1 {
		t.Fatalf("limit=1 returned %d rows, want 1: %+v", len(out), out)
	}

	// A garbage limit must NOT 400 — strconv.Atoi's error is ignored and
	// the zero value is clamped up by the repository, not rejected by the
	// handler.
	recGarbage := getInbound(t, srv, "clino", secret, "?since=2026-08-31T00:00:00Z&limit=not-a-number")
	if recGarbage.Code != http.StatusOK {
		t.Fatalf("garbage limit got %d, want 200 (repo clamps, handler must not reject): %s",
			recGarbage.Code, recGarbage.Body.String())
	}
	var outGarbage []apiInbound
	if err := json.Unmarshal(recGarbage.Body.Bytes(), &outGarbage); err != nil {
		t.Fatalf("decode: %v (%s)", err, recGarbage.Body.String())
	}
	if len(outGarbage) != 3 {
		t.Fatalf("garbage limit returned %d rows, want all 3 seeded rows (clamped-up default): %+v", len(outGarbage), outGarbage)
	}
}
