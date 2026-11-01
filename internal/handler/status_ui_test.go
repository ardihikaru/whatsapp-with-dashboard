package handler

import (
	"bytes"
	"context"
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
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/wasession"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/renderer"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// postMultipartAsAdmin is postFormAsAdmin's multipart sibling: an ordinary
// HTML form posts one "recipients" value PER CHECKED BOX, which
// url.Values/postFormAsAdmin cannot express (it x-www-form-urlencodes a
// map, one value per key) — this builds a real multipart/form-data body
// with repeated field names, the shape status.html's checklist actually
// produces.
func postMultipartAsAdmin(t *testing.T, srv http.Handler, sessions *auth.Store, path string, fields []struct{ key, value string }, fileField, filename string, fileBytes []byte) *httptest.ResponseRecorder {
	t.Helper()
	sess, err := sessions.Create("admin")
	if err != nil {
		t.Fatal(err)
	}
	body, contentType := buildMultipartRepeated(t, fields, fileField, filename, fileBytes)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", contentType)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: sess.Token})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// newStatusTestServer is newSessionAdminTestServer's (session_admin_pages_test.go)
// sibling for /status: the same cookie-authenticated, one-created-session
// shape, but also returns the message repository so a test can assert the
// exact wa_messages row a status post leaves behind — something
// newSessionAdminTestServer's callers have never needed.
func newStatusTestServer(t *testing.T, backend wa.Backend) (http.Handler, *auth.Store, *wa.ManagedSession, *message.SQLRepository) {
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
	mgr := wa.NewManager(context.Background(), backend, wasession.NewRepository(pool),
		msgRepo, nil, wa.Config{SendRate: 100, SendBurst: 5, ReconcilePar: 2})
	s, err := mgr.Create(context.Background(), "gw", "+62 812-0000-9999")
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Deps{
		Renderer: rend, Sessions: sessions, Middleware: auth.NewMiddleware(sessions, apiClients, ""),
		Manager: mgr, Messages: msgRepo, MessageStats: msgRepo, APIClients: apiClients,
		MediaMaxBytes: 4 << 20, MediaUploadTimeout: 5 * time.Second, MediaUploadConcurrency: 2,
		SessionTTL: time.Hour, AssetsFS: wsm.FS,
	})
	return srv, sessions, s, msgRepo
}

// buildMultipartRepeated is buildMultipartMedia's (api_media_test.go)
// sibling that allows more than one value per field name — needed for
// "recipients", which status.html's checklist posts once per checked box.
func buildMultipartRepeated(t *testing.T, fields []struct{ key, value string }, fileField, filename string, fileBytes []byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	for _, f := range fields {
		if err := mw.WriteField(f.key, f.value); err != nil {
			t.Fatal(err)
		}
	}
	if fileBytes != nil {
		fw, err := mw.CreateFormFile(fileField, filename)
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

// TestGetStatusRendersTheFormWithNoConnectedSessions mirrors GetSend's own
// "no connected sessions" fallback (send_test.go-shaped coverage) — a
// session that was only Create'd, never activated, is not Connected, so
// the page must explain that rather than render a session <select> with
// nothing valid in it.
func TestGetStatusRendersTheFormWithNoConnectedSessions(t *testing.T) {
	srv, sessions, _, _ := newStatusTestServer(t, whatsmeowShapedBackend())
	res := authedGet(t, srv, sessions, "/status")
	if res.Code != http.StatusOK {
		t.Fatalf("GET /status = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "No connected sessions") {
		t.Errorf("GET /status does not explain there is no connected session; body:\n%s", res.Body.String())
	}
}

// TestPostStatusTextUIAnswers200WithAnErrorWhenNoSessionIsSelected covers
// the simplest failure: a POST naming no session_id at all must render the
// status_result partial's error branch, not a 4xx — mirroring PostSend's
// own "every outcome renders the partial" rule (send.go).
func TestPostStatusTextUIAnswers200WithAnErrorWhenNoSessionIsSelected(t *testing.T) {
	srv, sessions, _, _ := newStatusTestServer(t, whatsmeowShapedBackend())
	res := postFormAsAdmin(t, srv, sessions, "/status/text", url.Values{
		"text": {"hello"}, "recipients": {"628@s.whatsapp.net"},
	})
	if res.Code != http.StatusOK {
		t.Fatalf("POST /status/text = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "select a connected session") {
		t.Errorf("body does not name the missing-session error; got:\n%s", res.Body.String())
	}
}

// TestPostStatusTextUISucceedsAndLogsASentWAMessagesRow drives the happy
// path end to end through the browser form handler: a 200 naming the
// message id and recipient count, AND exactly one wa_messages row
// (to_phone status@broadcast, status sent) — the same audit-row guarantee
// api_status_test.go pins for the JSON API, now for the UI path that calls
// a different handler entirely.
func TestPostStatusTextUISucceedsAndLogsASentWAMessagesRow(t *testing.T) {
	backend := &fakeGatewayBackend{name: "fake", statusTextFn: func(ctx context.Context, sessionID, text string, bg uint32, recipients []string) (string, error) {
		return "wamid-ui-1", nil
	}}
	srv, sessions, s, msgRepo := newStatusTestServer(t, backend)

	res := postFormAsAdmin(t, srv, sessions, "/status/text", url.Values{
		"session_id": {s.ID}, "text": {"hello from the UI"}, "recipients": {"628@s.whatsapp.net"},
	})
	if res.Code != http.StatusOK {
		t.Fatalf("POST /status/text = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	if !strings.Contains(body, "wamid-ui-1") {
		t.Errorf("body does not carry the message id; got:\n%s", body)
	}
	if !strings.Contains(body, "visible to 1 recipient") {
		t.Errorf("body does not name the recipient count; got:\n%s", body)
	}

	rows, err := msgRepo.Recent(context.Background(), s.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	if rows[0].ToPhone != "status@broadcast" {
		t.Errorf("ToPhone = %q, want %q", rows[0].ToPhone, "status@broadcast")
	}
	if rows[0].Status != message.StatusSent {
		t.Errorf("Status = %q, want %q", rows[0].Status, message.StatusSent)
	}
	if rows[0].WAMessageID != "wamid-ui-1" {
		t.Errorf("WAMessageID = %q, want %q", rows[0].WAMessageID, "wamid-ui-1")
	}
}

// TestPostStatusImageUIAcceptsRepeatedRecipientsFormValues pins the new
// behaviour parseStatusMediaRequest (api_status.go) needed for THIS page
// specifically: status.html's checklist posts one "recipients" value per
// checked box (an ordinary HTML form), not the single JSON-array-string
// value the JSON API/MCP convenience form sends — both shapes must reach
// StatusPoster as the same two-element list.
func TestPostStatusImageUIAcceptsRepeatedRecipientsFormValues(t *testing.T) {
	var gotRecipients []string
	backend := &fakeGatewayBackend{
		name: "fake",
		uploadFn: func(context.Context, string, wa.MediaKind, string, string, []byte) (wa.MediaDescriptor, error) {
			return wa.MediaDescriptor{Mimetype: "image/png"}, nil
		},
		statusImageFn: func(ctx context.Context, sessionID string, d wa.MediaDescriptor, caption string, recipients []string) (string, error) {
			gotRecipients = recipients
			return "wamid-img-ui-1", nil
		},
	}
	srv, sessions, s, _ := newStatusTestServer(t, backend)

	rec := postMultipartAsAdmin(t, srv, sessions, "/status/image", []struct{ key, value string }{
		{"session_id", s.ID},
		{"recipients", "628111@s.whatsapp.net"},
		{"recipients", "628222@s.whatsapp.net"},
	}, "file", "status.png", pngMagic)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /status/image = %d, want 200; body:\n%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "wamid-img-ui-1") {
		t.Errorf("body does not carry the message id; got:\n%s", rec.Body.String())
	}
	if len(gotRecipients) != 2 || gotRecipients[0] != "628111@s.whatsapp.net" || gotRecipients[1] != "628222@s.whatsapp.net" {
		t.Errorf("PostStatusImage recipients = %v, want both checked boxes in order", gotRecipients)
	}
}

// TestPostStatusTextUIAnswers200WithAnErrorOnEmptyRecipients covers the
// browser form's own validation path (parseStatusTextFormRequest), which
// is NOT the JSON decoder decodeStatusTextRequest uses — a regression
// here would not be caught by api_status_test.go at all.
func TestPostStatusTextUIAnswers200WithAnErrorOnEmptyRecipients(t *testing.T) {
	backend := &fakeGatewayBackend{name: "fake", statusTextFn: func(context.Context, string, string, uint32, []string) (string, error) {
		t.Fatal("the backend must not be called when recipients is empty")
		return "", nil
	}}
	srv, sessions, s, _ := newStatusTestServer(t, backend)

	res := postFormAsAdmin(t, srv, sessions, "/status/text", url.Values{
		"session_id": {s.ID}, "text": {"hello"},
	})
	if res.Code != http.StatusOK {
		t.Fatalf("POST /status/text = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "recipients") {
		t.Errorf("body does not name the recipients error; got:\n%s", res.Body.String())
	}
}
