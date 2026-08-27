package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// pngMagic is a real PNG signature — enough for http.DetectContentType to
// report "image/png"; the rest of the file need not be a valid image.
var pngMagic = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// buildMultipartMedia assembles a multipart/form-data body for the media
// send endpoints. fileBytes == nil omits the "file" part entirely (for the
// missing-file-part test).
func buildMultipartMedia(t *testing.T, fields map[string]string, fileBytes []byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if fileBytes != nil {
		fw, err := mw.CreateFormFile("file", "upload.bin")
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

func postMultipartMedia(srv http.Handler, token, path, contentType string, body *bytes.Buffer) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

var baseImageFields = map[string]string{"kind": "image", "to": "628123456789", "caption": "hi"}

// TestMediaSendRejectsABodyOverTheConfiguredLimit pins the 413 path: a
// multipart body larger than Deps.MediaMaxBytes (+ slack) must be refused
// at the socket, before any file/kind/to validation runs.
func TestMediaSendRejectsABodyOverTheConfiguredLimit(t *testing.T) {
	backend := &fakeGatewayBackend{name: "fake"}
	srv, s, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend, mediaMaxBytes: 1024})
	token := apiToken(t, srv, apiClients, "media-413")

	big := append(append([]byte{}, pngMagic...), bytes.Repeat([]byte("x"), 200*1024)...)
	body, ct := buildMultipartMedia(t, baseImageFields, big)

	rec := postMultipartMedia(srv, token, "/api/v1/sessions/"+s.ID+"/messages/media", ct, body)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}
}

// TestMediaSendRejectsAMimeTypeThatDoesNotMatchTheDeclaredKind pins the 415
// path: a PNG declared as "audio" must be rejected, because the SNIFFED
// bytes — not the declared kind — decide.
func TestMediaSendRejectsAMimeTypeThatDoesNotMatchTheDeclaredKind(t *testing.T) {
	backend := &fakeGatewayBackend{name: "fake"}
	srv, s, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend})
	token := apiToken(t, srv, apiClients, "media-415")

	fields := map[string]string{"kind": "audio", "to": "628123456789"}
	body, ct := buildMultipartMedia(t, fields, pngMagic)

	rec := postMultipartMedia(srv, token, "/api/v1/sessions/"+s.ID+"/messages/media", ct, body)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusUnsupportedMediaType, rec.Body.String())
	}
}

// TestMediaSendRejectsAMissingFilePart pins the 400 path for a multipart
// body with no "file" part at all.
func TestMediaSendRejectsAMissingFilePart(t *testing.T) {
	backend := &fakeGatewayBackend{name: "fake"}
	srv, s, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend})
	token := apiToken(t, srv, apiClients, "media-missing-file")

	body, ct := buildMultipartMedia(t, baseImageFields, nil)

	rec := postMultipartMedia(srv, token, "/api/v1/sessions/"+s.ID+"/messages/media", ct, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

// TestMediaSendOnAWhatsmeowBackendAnswers501 proves a backend with no
// MediaStore support (uploadFn left nil — see fakeGatewayBackend's doc
// comment) answers 501 via writeWAError/wa.ErrUnsupported, the same as a
// real whatsmeow deployment would.
func TestMediaSendOnAWhatsmeowBackendAnswers501(t *testing.T) {
	backend := &fakeGatewayBackend{name: "whatsmeow-like"} // uploadFn nil
	srv, s, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend})
	token := apiToken(t, srv, apiClients, "media-501")

	body, ct := buildMultipartMedia(t, baseImageFields, pngMagic)

	rec := postMultipartMedia(srv, token, "/api/v1/sessions/"+s.ID+"/messages/media", ct, body)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusNotImplemented, rec.Body.String())
	}
}

// TestMediaSendArchivesToObjectStorageBeforeUploadingToTheBackend proves
// the ordering the phase brief's step 8-10 requires: MinIO Put happens
// strictly before the backend's UploadMedia call.
func TestMediaSendArchivesToObjectStorageBeforeUploadingToTheBackend(t *testing.T) {
	log := &callLog{}
	backend := &fakeGatewayBackend{
		name: "fake",
		uploadFn: func(ctx context.Context, sessionID string, kind wa.MediaKind, filename, mimetype string, data []byte) (wa.MediaDescriptor, error) {
			log.record("upload")
			return wa.MediaDescriptor{URL: "https://mmg.example/x", Mimetype: mimetype}, nil
		},
	}
	objects := &fakeObjectStore{log: log}
	srv, s, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend, objects: objects})
	token := apiToken(t, srv, apiClients, "media-archive-order")

	body, ct := buildMultipartMedia(t, baseImageFields, pngMagic)
	postMultipartMedia(srv, token, "/api/v1/sessions/"+s.ID+"/messages/media", ct, body)

	calls := log.snapshot()
	if len(calls) != 2 {
		t.Fatalf("call log = %v, want exactly 2 entries (one put, one upload)", calls)
	}
	if !strings.HasPrefix(calls[0], "put:") {
		t.Fatalf("first call = %q, want a MinIO put", calls[0])
	}
	if calls[1] != "upload" {
		t.Fatalf("second call = %q, want the backend upload", calls[1])
	}
}

// TestMediaSendStillSucceedsWhenTheArchiveWriteFails pins the best-effort
// archive decision: a MinIO Put failure must NOT stop the backend upload
// from being attempted, and must NOT surface as a 500 — see
// archiveMedia's doc comment (api_media.go). The failure is instead
// visible on the session's own event ring (RingWarn).
func TestMediaSendStillSucceedsWhenTheArchiveWriteFails(t *testing.T) {
	log := &callLog{}
	uploaded := false
	backend := &fakeGatewayBackend{
		name: "fake",
		uploadFn: func(ctx context.Context, sessionID string, kind wa.MediaKind, filename, mimetype string, data []byte) (wa.MediaDescriptor, error) {
			uploaded = true
			log.record("upload")
			return wa.MediaDescriptor{URL: "https://mmg.example/x", Mimetype: mimetype}, nil
		},
	}
	objects := &fakeObjectStore{log: log, putErr: context.DeadlineExceeded}
	srv, s, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend, objects: objects})
	token := apiToken(t, srv, apiClients, "media-archive-fail")

	body, ct := buildMultipartMedia(t, baseImageFields, pngMagic)
	rec := postMultipartMedia(srv, token, "/api/v1/sessions/"+s.ID+"/messages/media", ct, body)

	if rec.Code >= 500 {
		t.Fatalf("status = %d, want < 500 (archive failure must not fail the send); body = %s", rec.Code, rec.Body.String())
	}
	if !uploaded {
		t.Fatal("backend UploadMedia was never called — an archive failure must not abort the send")
	}
	found := false
	for _, ev := range s.Snapshot().LastEvents {
		if strings.Contains(ev.Note, "archive") {
			found = true
		}
	}
	if !found {
		t.Fatal("no ring event recorded the archive failure")
	}
}

// TestMediaSendPersistsTheFilenameAndKindOnTheMessageRow proves the
// wa_messages row (Insert, which happens after a successful upload) carries
// the media columns regardless of whether the session was connected enough
// to actually enqueue — see dispatchSend's ordering.
func TestMediaSendPersistsTheFilenameAndKindOnTheMessageRow(t *testing.T) {
	backend := &fakeGatewayBackend{
		name: "fake",
		uploadFn: func(ctx context.Context, sessionID string, kind wa.MediaKind, filename, mimetype string, data []byte) (wa.MediaDescriptor, error) {
			return wa.MediaDescriptor{URL: "https://mmg.example/x", Mimetype: mimetype}, nil
		},
	}
	srv, s, apiClients, msgs, _ := newMediaTestServer(t, mediaTestConfig{backend: backend})
	token := apiToken(t, srv, apiClients, "media-persists-row")

	body, ct := buildMultipartMedia(t, baseImageFields, pngMagic)
	postMultipartMedia(srv, token, "/api/v1/sessions/"+s.ID+"/messages/media", ct, body)

	rows, err := msgs.Recent(context.Background(), s.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("wa_messages has %d rows, want 1", len(rows))
	}
	row := rows[0]
	if row.MediaKind != "image" {
		t.Errorf("MediaKind = %q, want %q", row.MediaKind, "image")
	}
	if row.MediaFilename != "upload.bin" {
		t.Errorf("MediaFilename = %q, want %q", row.MediaFilename, "upload.bin")
	}
	if row.MediaMime != "image/png" {
		t.Errorf("MediaMime = %q, want %q", row.MediaMime, "image/png")
	}
}

// TestTheMultipartSpillFileIsRemovedAfterTheRequest is the disk-leak
// regression: a file bigger than mediaFormMaxMemory (1 MiB) forces
// ParseMultipartForm to spill to TMPDIR, and the deferred
// r.MultipartForm.RemoveAll() must delete it once the request completes.
func TestTheMultipartSpillFileIsRemovedAfterTheRequest(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	backend := &fakeGatewayBackend{
		name: "fake",
		uploadFn: func(ctx context.Context, sessionID string, kind wa.MediaKind, filename, mimetype string, data []byte) (wa.MediaDescriptor, error) {
			return wa.MediaDescriptor{URL: "https://mmg.example/x", Mimetype: mimetype}, nil
		},
	}
	srv, s, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend, mediaMaxBytes: 8 << 20})
	token := apiToken(t, srv, apiClients, "media-spill-cleanup")

	big := append(append([]byte{}, pngMagic...), bytes.Repeat([]byte("y"), 2<<20)...) // >1 MiB spill threshold
	body, ct := buildMultipartMedia(t, baseImageFields, big)
	postMultipartMedia(srv, token, "/api/v1/sessions/"+s.ID+"/messages/media", ct, body)

	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	var leftover []string
	for _, e := range entries {
		leftover = append(leftover, e.Name())
	}
	if len(leftover) != 0 {
		t.Fatalf("TMPDIR has leftover file(s) after the request: %v", leftover)
	}
}

// TestMediaSendAutoRoutesAndAnswers409WithNoConnectedSession proves the
// auto-route (/api/v1/messages/media) still validates the body BEFORE
// discovering there is no connected session, mirroring
// PostAPISendMessageAuto's own ordering.
func TestMediaSendAutoRoutesAndAnswers409WithNoConnectedSession(t *testing.T) {
	backend := &fakeGatewayBackend{
		name: "fake",
		uploadFn: func(ctx context.Context, sessionID string, kind wa.MediaKind, filename, mimetype string, data []byte) (wa.MediaDescriptor, error) {
			return wa.MediaDescriptor{URL: "https://mmg.example/x", Mimetype: mimetype}, nil
		},
	}
	srv, _, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend})
	token := apiToken(t, srv, apiClients, "media-auto-409")

	body, ct := buildMultipartMedia(t, baseImageFields, pngMagic)
	rec := postMultipartMedia(srv, token, "/api/v1/messages/media", ct, body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d (no connected session); body = %s", rec.Code, http.StatusConflict, rec.Body.String())
	}
}

// TestMediaDownloadReturnsTheDecodedBytesAsBase64 is a light smoke test on
// the new download endpoint's happy path: JSON descriptor in, base64 bytes
// out.
func TestMediaDownloadReturnsTheDecodedBytesAsBase64(t *testing.T) {
	backend := &fakeGatewayBackend{name: "fake"}
	backend2 := &fakeGatewayBackendWithDownload{fakeGatewayBackend: backend, data: []byte("hello media")}
	srv, s, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend2})
	token := apiToken(t, srv, apiClients, "media-download")

	reqBody := `{"kind":"image","url":"https://mmg.example/x","mimetype":"image/png"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+s.ID+"/media/download", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		DataBase64 string `json:"data_base64"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.DataBase64 == "" {
		t.Fatal("data_base64 is empty")
	}
}

// fakeGatewayBackendWithDownload overrides DownloadMedia so
// TestMediaDownloadReturnsTheDecodedBytesAsBase64 has a real byte payload
// to decode, without complicating fakeGatewayBackend's shared shape for
// every other test in this file.
type fakeGatewayBackendWithDownload struct {
	*fakeGatewayBackend
	data []byte
}

func (f *fakeGatewayBackendWithDownload) DownloadMedia(context.Context, string, wa.MediaDescriptor, wa.MediaKind) ([]byte, error) {
	return f.data, nil
}

// TestEveryNewEndpointRejectsACookieAndAcceptsABearer is the open-relay
// regression table for all seven Phase 6a routes: a request carrying only
// the WEB session cookie must be rejected (RequireAPI never looks at
// cookies at all — see internal/auth/middleware.go's doc comment), and the
// same route with a valid bearer token must not be rejected for
// authentication reasons (i.e. must not answer 401).
func TestEveryNewEndpointRejectsACookieAndAcceptsABearer(t *testing.T) {
	backend := &fakeGatewayBackend{
		name: "fake",
		uploadFn: func(ctx context.Context, sessionID string, kind wa.MediaKind, filename, mimetype string, data []byte) (wa.MediaDescriptor, error) {
			return wa.MediaDescriptor{URL: "https://mmg.example/x", Mimetype: mimetype}, nil
		},
		ttsFn: func(to wa.JID, text, voice string) (wa.CallOutcome, error) {
			return wa.CallOutcome{CallID: "call-1"}, nil
		},
		playFn: func(to wa.JID, audioURL string) (wa.CallOutcome, error) {
			return wa.CallOutcome{CallID: "call-2"}, nil
		},
		termFn: func(peer wa.JID, callID, reason string) error { return nil },
	}
	srv, s, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend, callsEnabled: true})

	webCookieValue := mintWebSessionToken(t, srv)
	token := apiToken(t, srv, apiClients, "media-calls-auth-table")

	imgBody, imgCT := buildMultipartMedia(t, baseImageFields, pngMagic)
	callsBody := `{"to":"628123456789","text":"hi","voice":""}`
	playBody := `{"to":"628123456789","audio_url":"https://example.invalid/a.mp3"}`
	termBody := `{"peer":"628123456789","call_id":"call-1"}`
	dlBody := `{"kind":"image","url":"https://mmg.example/x","mimetype":"image/png"}`

	cases := []struct {
		name        string
		method      string
		path        string
		contentType string
		body        func() io.Reader
	}{
		{"send media (explicit session)", http.MethodPost, "/api/v1/sessions/" + s.ID + "/messages/media", imgCT, func() io.Reader { return bytes.NewReader(imgBody.Bytes()) }},
		{"send media (auto)", http.MethodPost, "/api/v1/messages/media", imgCT, func() io.Reader { return bytes.NewReader(imgBody.Bytes()) }},
		{"media download", http.MethodPost, "/api/v1/sessions/" + s.ID + "/media/download", "application/json", func() io.Reader { return strings.NewReader(dlBody) }},
		{"calls tts", http.MethodPost, "/api/v1/sessions/" + s.ID + "/calls/tts", "application/json", func() io.Reader { return strings.NewReader(callsBody) }},
		{"calls play", http.MethodPost, "/api/v1/sessions/" + s.ID + "/calls/play", "application/json", func() io.Reader { return strings.NewReader(playBody) }},
		{"calls terminate", http.MethodPost, "/api/v1/sessions/" + s.ID + "/calls/terminate", "application/json", func() io.Reader { return strings.NewReader(termBody) }},
		{"calls list", http.MethodGet, "/api/v1/sessions/" + s.ID + "/calls", "", func() io.Reader { return nil }},

		// Phase 6b: session operations, webhook administration and gateway
		// health. This test only cares about AUTH — not the business
		// result — so these run against the SAME whatsmeow-shaped
		// fakeGatewayBackend as everything above: it 501s every one of
		// them (no SessionOps/WebhookAdmin/GatewayHealth), which is a
		// perfectly good "not 401" for the bearer half below.
		{"operations", http.MethodGet, "/api/v1/sessions/" + s.ID + "/operations", "", func() io.Reader { return nil }},
		{"pause", http.MethodPost, "/api/v1/sessions/" + s.ID + "/pause", "", func() io.Reader { return nil }},
		{"resume", http.MethodPost, "/api/v1/sessions/" + s.ID + "/resume", "", func() io.Reader { return nil }},
		{"get auto-reconnect", http.MethodGet, "/api/v1/sessions/" + s.ID + "/auto-reconnect", "", func() io.Reader { return nil }},
		{"put auto-reconnect", http.MethodPut, "/api/v1/sessions/" + s.ID + "/auto-reconnect", "application/json", func() io.Reader { return strings.NewReader(`{"enabled":true}`) }},
		{"get history-sync", http.MethodGet, "/api/v1/sessions/" + s.ID + "/history-sync", "", func() io.Reader { return nil }},
		{"put history-sync", http.MethodPut, "/api/v1/sessions/" + s.ID + "/history-sync", "application/json", func() io.Reader { return strings.NewReader(`{"skip":true}`) }},
		{"appstate resync", http.MethodPost, "/api/v1/sessions/" + s.ID + "/appstate/resync", "application/json", func() io.Reader { return strings.NewReader(`{"collections":["critical_block"],"mode":"full"}`) }},
		{"spam report", http.MethodPost, "/api/v1/sessions/" + s.ID + "/spam-report", "application/json", func() io.Reader { return strings.NewReader(`{"message_id":"m1","message_timestamp":1234567890}`) }},
		{"issue trust tokens", http.MethodPost, "/api/v1/sessions/" + s.ID + "/trust-tokens", "application/json", func() io.Reader { return strings.NewReader(`{"jids":["628123456789@s.whatsapp.net"]}`) }},
		{"list trust tokens", http.MethodGet, "/api/v1/sessions/" + s.ID + "/trust-tokens", "", func() io.Reader { return nil }},
		{"get trust token", http.MethodGet, "/api/v1/sessions/" + s.ID + "/trust-tokens/628123456789@s.whatsapp.net", "", func() io.Reader { return nil }},
		{"prune trust tokens", http.MethodDelete, "/api/v1/sessions/" + s.ID + "/trust-tokens/expired", "", func() io.Reader { return nil }},
		{"list webhooks", http.MethodGet, "/api/v1/sessions/" + s.ID + "/webhooks", "", func() io.Reader { return nil }},
		{"register webhook", http.MethodPost, "/api/v1/sessions/" + s.ID + "/webhooks", "application/json", func() io.Reader { return strings.NewReader(`{"url":"https://example.com/hook","events":["message"]}`) }},
		{"delete webhook", http.MethodDelete, "/api/v1/sessions/" + s.ID + "/webhooks/wh-1", "", func() io.Reader { return nil }},
		{"enable webhook", http.MethodPost, "/api/v1/sessions/" + s.ID + "/webhooks/wh-1/enable", "", func() io.Reader { return nil }},
		{"webhook dlq", http.MethodGet, "/api/v1/sessions/" + s.ID + "/webhooks/dlq", "", func() io.Reader { return nil }},
		{"webhook dlq replay", http.MethodPost, "/api/v1/sessions/" + s.ID + "/webhooks/dlq/entry-1/replay", "", func() io.Reader { return nil }},
		{"reenable all webhooks", http.MethodPost, "/api/v1/webhooks/reenable-all", "", func() io.Reader { return nil }},
		{"gateway health", http.MethodGet, "/api/v1/gateway/health", "", func() io.Reader { return nil }},
	}

	for _, tc := range cases {
		t.Run(tc.name+"/cookie only", func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, tc.body())
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			req.AddCookie(&http.Cookie{Name: "wsm_session", Value: webCookieValue})
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s with only a cookie: status = %d, want 401; body = %s", tc.method, tc.path, rec.Code, rec.Body.String())
			}
		})
		t.Run(tc.name+"/bearer", func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, tc.body())
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			if rec.Code == http.StatusUnauthorized {
				t.Fatalf("%s %s with a valid bearer: status = 401, want anything else; body = %s", tc.method, tc.path, rec.Body.String())
			}
		})
	}
}

// mintWebSessionToken signs the admin in through the real login flow, the
// same path the browser uses, and returns the session-cookie VALUE (not the
// full Set-Cookie header) so this test isn't tied to auth.Store internals.
func mintWebSessionToken(t *testing.T, srv http.Handler) string {
	t.Helper()
	// This server is built with no auth.AdminCredential (see
	// newMediaTestServer), so logging in for real isn't possible here.
	// What matters for THIS test is only that RequireAPI ignores cookies
	// unconditionally — a syntactically well-formed cookie with an
	// arbitrary value already proves that, since the assertion is "still
	// 401", not "401 because the cookie itself was invalid".
	return "not-a-real-session-token"
}
