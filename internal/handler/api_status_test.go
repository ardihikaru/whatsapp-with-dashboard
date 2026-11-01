package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// TestPostStatusTextAnswers404ForAnUnknownSessionID proves the explicit
// route resolves the session BEFORE anything else, mirroring
// TestContactsCheckExplicitAnswers404ForAnUnknownSessionID.
func TestPostStatusTextAnswers404ForAnUnknownSessionID(t *testing.T) {
	srv, _, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: whatsmeowShapedBackend()})
	token := apiToken(t, srv, apiClients, "status-404")

	rec := postJSON(srv, token, http.MethodPost, "/api/v1/sessions/does-not-exist/status/text",
		`{"text":"hi","recipients":["628@s.whatsapp.net"]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

// TestPostStatusTextAnswers400OnEmptyTextOrRecipients covers both 400
// triggers before the backend is ever reached.
func TestPostStatusTextAnswers400OnEmptyTextOrRecipients(t *testing.T) {
	backend := &fakeGatewayBackend{name: "fake", statusTextFn: func(context.Context, string, string, uint32, []string) (string, error) {
		t.Fatal("the backend must not be called on a request that should 400 before reaching it")
		return "", nil
	}}
	srv, s, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend})
	token := apiToken(t, srv, apiClients, "status-400")
	path := "/api/v1/sessions/" + s.ID + "/status/text"

	cases := []string{
		`{"text":"","recipients":["628@s.whatsapp.net"]}`,
		`{"text":"hi","recipients":[]}`,
		`{"text":"hi","recipients":["not-a-jid"]}`,
	}
	for _, body := range cases {
		rec := postJSON(srv, token, http.MethodPost, path, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body=%s: status = %d, want 400; resp = %s", body, rec.Code, rec.Body.String())
		}
	}
}

// TestPostStatusTextOn501WhenTheBackendHasNoStatusPoster proves the
// capability lookup itself maps to 501 on a whatsmeow-shaped backend,
// mirroring every other optional-capability route in this package.
func TestPostStatusTextOn501WhenTheBackendHasNoStatusPoster(t *testing.T) {
	srv, s, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: whatsmeowShapedBackend()})
	token := apiToken(t, srv, apiClients, "status-501")

	rec := postJSON(srv, token, http.MethodPost, "/api/v1/sessions/"+s.ID+"/status/text",
		`{"text":"hi","recipients":["628@s.whatsapp.net"]}`)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusNotImplemented, rec.Body.String())
	}
}

// TestPostStatusTextSucceedsAndLogsASentWAMessagesRow is the happy path:
// a 200 carrying waxum's message_id, AND exactly one wa_messages row
// (to_phone status@broadcast, category manual, status sent) — the
// decision this phase made instead of a schema change.
func TestPostStatusTextSucceedsAndLogsASentWAMessagesRow(t *testing.T) {
	backend := &fakeGatewayBackend{name: "fake", statusTextFn: func(ctx context.Context, sessionID, text string, bg uint32, recipients []string) (string, error) {
		if text != "hello" {
			t.Errorf("text = %q, want %q", text, "hello")
		}
		if len(recipients) != 1 || recipients[0] != "628@s.whatsapp.net" {
			t.Errorf("recipients = %v", recipients)
		}
		return "wamid-text-1", nil
	}}
	srv, s, apiClients, msgRepo, _ := newMediaTestServer(t, mediaTestConfig{backend: backend})
	token := apiToken(t, srv, apiClients, "status-200")

	rec := postJSON(srv, token, http.MethodPost, "/api/v1/sessions/"+s.ID+"/status/text",
		`{"text":"hello","recipients":["628@s.whatsapp.net"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}

	rows, err := msgRepo.Recent(context.Background(), s.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.ToPhone != "status@broadcast" {
		t.Errorf("ToPhone = %q, want %q", row.ToPhone, "status@broadcast")
	}
	// Recent (unlike Insert) does not SELECT the category column at all —
	// an existing SQLRepository limitation, not something this change
	// introduces; only Insert's own write of it is this phase's concern.
	if row.Status != message.StatusSent {
		t.Errorf("Status = %q, want %q", row.Status, message.StatusSent)
	}
	if row.WAMessageID != "wamid-text-1" {
		t.Errorf("WAMessageID = %q, want %q", row.WAMessageID, "wamid-text-1")
	}
}

// TestPostStatusTextMapsABackendErrorTo409AndStillLogsAFailedRow proves a
// disconnected-session failure (waxum's 503, surfaced here as
// wa.ErrNotConnected — this package never pre-checks connection state
// itself, see api_status.go's header comment) reaches the caller as 409
// AND still leaves a FAILED audit row, not a silently dropped one.
func TestPostStatusTextMapsABackendErrorTo409AndStillLogsAFailedRow(t *testing.T) {
	backend := &fakeGatewayBackend{name: "fake", statusTextFn: func(context.Context, string, string, uint32, []string) (string, error) {
		return "", wa.ErrNotConnected
	}}
	srv, s, apiClients, msgRepo, _ := newMediaTestServer(t, mediaTestConfig{backend: backend})
	token := apiToken(t, srv, apiClients, "status-409")

	rec := postJSON(srv, token, http.MethodPost, "/api/v1/sessions/"+s.ID+"/status/text",
		`{"text":"hello","recipients":["628@s.whatsapp.net"]}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", rec.Code, rec.Body.String())
	}

	rows, err := msgRepo.Recent(context.Background(), s.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	if rows[0].Status != message.StatusFailed {
		t.Errorf("Status = %q, want %q", rows[0].Status, message.StatusFailed)
	}
}

// TestPostStatusImageUploadsThenCallsPostStatusImageWithTheDescriptor
// guards the image route's whole orchestration: the file goes through
// uploadAndDescribeMedia (MediaStore.UploadMedia) BEFORE
// StatusPoster.PostStatusImage ever runs, and the descriptor that comes
// back from the upload is EXACTLY what gets passed through — the same
// upload-then-describe path ordinary media sends use.
func TestPostStatusImageUploadsThenCallsPostStatusImageWithTheDescriptor(t *testing.T) {
	var uploadCalled, statusCalled bool
	backend := &fakeGatewayBackend{
		name: "fake",
		uploadFn: func(ctx context.Context, sessionID string, kind wa.MediaKind, filename, mimetype string, data []byte) (wa.MediaDescriptor, error) {
			uploadCalled = true
			return wa.MediaDescriptor{URL: "https://example.invalid/d", Mimetype: mimetype}, nil
		},
		statusImageFn: func(ctx context.Context, sessionID string, d wa.MediaDescriptor, caption string, recipients []string) (string, error) {
			statusCalled = true
			if !uploadCalled {
				t.Error("PostStatusImage called before UploadMedia")
			}
			if d.URL != "https://example.invalid/d" {
				t.Errorf("descriptor URL = %q, want the uploaded descriptor's URL", d.URL)
			}
			if caption != "a caption" {
				t.Errorf("caption = %q, want %q", caption, "a caption")
			}
			return "wamid-img-1", nil
		},
	}
	srv, s, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend})
	token := apiToken(t, srv, apiClients, "status-image")

	body, contentType := buildMultipartMedia(t, map[string]string{
		"caption": "a caption", "recipients": `["628@s.whatsapp.net"]`,
	}, pngMagic)
	rec := postMultipartMedia(srv, token, "/api/v1/sessions/"+s.ID+"/status/image", contentType, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if !statusCalled {
		t.Error("PostStatusImage was never called")
	}
}

// TestPostStatusVideoUsesTheVideoCapabilityMethodNotImage guards the video
// route hits PostStatusVideo, not PostStatusImage — a copy/paste risk given
// how similar the two routes are.
func TestPostStatusVideoUsesTheVideoCapabilityMethodNotImage(t *testing.T) {
	backend := &fakeGatewayBackend{
		name: "fake",
		uploadFn: func(ctx context.Context, sessionID string, kind wa.MediaKind, filename, mimetype string, data []byte) (wa.MediaDescriptor, error) {
			if kind != wa.MediaVideo {
				t.Errorf("UploadMedia kind = %q, want %q", kind, wa.MediaVideo)
			}
			return wa.MediaDescriptor{Mimetype: mimetype}, nil
		},
		statusImageFn: func(context.Context, string, wa.MediaDescriptor, string, []string) (string, error) {
			t.Fatal("PostStatusImage must not be called for the video route")
			return "", nil
		},
		statusVideoFn: func(ctx context.Context, sessionID string, d wa.MediaDescriptor, caption string, recipients []string) (string, error) {
			return "wamid-vid-1", nil
		},
	}
	srv, s, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend})
	token := apiToken(t, srv, apiClients, "status-video")

	body, contentType := buildMultipartMedia(t, map[string]string{
		"recipients": `["628@s.whatsapp.net"]`,
	}, webmMagic)
	rec := postMultipartMedia(srv, token, "/api/v1/sessions/"+s.ID+"/status/video", contentType, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
}

// TestPostStatusImageAnswers400WhenRecipientsIsNotAJSONArray guards the
// multipart form's "recipients" field shape: it must be a JSON array
// string, and a malformed value must 400 before any upload is attempted.
func TestPostStatusImageAnswers400WhenRecipientsIsNotAJSONArray(t *testing.T) {
	backend := &fakeGatewayBackend{name: "fake", uploadFn: func(context.Context, string, wa.MediaKind, string, string, []byte) (wa.MediaDescriptor, error) {
		t.Fatal("UploadMedia must not be called when recipients fails to parse")
		return wa.MediaDescriptor{}, nil
	}}
	srv, s, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend})
	token := apiToken(t, srv, apiClients, "status-image-400")

	body, contentType := buildMultipartMedia(t, map[string]string{"recipients": "not-json"}, pngMagic)
	rec := postMultipartMedia(srv, token, "/api/v1/sessions/"+s.ID+"/status/image", contentType, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
}
