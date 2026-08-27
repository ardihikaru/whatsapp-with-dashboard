package waxum

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestUploadMediaPostsMultipartWithTheFileFieldAndFilename(t *testing.T) {
	var gotPath, gotMethod string
	var gotFilename, gotFileContent, gotMediaType, gotMimetype string
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("FormFile(\"file\"): %v", err)
		}
		defer file.Close()
		gotFilename = header.Filename
		data, err := io.ReadAll(file)
		if err != nil {
			t.Fatalf("reading uploaded file part: %v", err)
		}
		gotFileContent = string(data)
		gotMediaType = r.FormValue("media_type")
		gotMimetype = r.FormValue("mimetype")
		json.NewEncoder(w).Encode(MediaDescriptor{
			URL: "https://mmg.whatsapp.net/x", DirectPath: "/x", MediaKey: "k",
			FileSHA256: "sh", FileEncSHA256: "esh", FileLength: 5,
			MediaType: "image", Mimetype: "image/png",
		})
	})

	desc, err := client.UploadMedia(context.Background(), "sess-1", MediaImage, "pic.png", "image/png", []byte("hello"))
	if err != nil {
		t.Fatalf("UploadMedia: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	wantPath := "/api/v1/sessions/sess-1/media/upload"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
	if gotFilename != "pic.png" {
		t.Errorf(`uploaded filename = %q, want "pic.png"`, gotFilename)
	}
	if gotFileContent != "hello" {
		t.Errorf("uploaded file content = %q, want %q", gotFileContent, "hello")
	}
	if gotMediaType != "image" {
		t.Errorf(`form field "media_type" = %q, want "image"`, gotMediaType)
	}
	if gotMimetype != "image/png" {
		t.Errorf(`form field "mimetype" = %q, want "image/png"`, gotMimetype)
	}
	if desc.URL != "https://mmg.whatsapp.net/x" {
		t.Errorf("descriptor URL = %q, want the fake server's response", desc.URL)
	}
	if desc.MediaKey != "k" {
		t.Errorf("descriptor MediaKey = %q, want %q", desc.MediaKey, "k")
	}
}

// TestUploadMediaReplaysTheBodyAfterARateLimitRetry is the regression test
// for the exact bug transport.go's send() doc comment warns about: a
// multipart body built once and read from a spent reader would arrive
// EMPTY (or truncated) on a 429 retry. The fake server answers 429 on the
// first attempt and asserts the SECOND attempt's file part carries the
// full original payload.
func TestUploadMediaReplaysTheBodyAfterARateLimitRetry(t *testing.T) {
	var attempts int
	var secondFileContent string
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Fatalf("ParseMultipartForm on retry: %v", err)
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("FormFile on retry: %v", err)
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			t.Fatalf("reading retried file part: %v", err)
		}
		secondFileContent = string(data)
		json.NewEncoder(w).Encode(MediaDescriptor{URL: "https://mmg.whatsapp.net/x"})
	})

	payload := []byte("this is the full multipart payload body, not empty")
	if _, err := client.UploadMedia(context.Background(), "sess-1", MediaDocument, "f.bin", "application/octet-stream", payload); err != nil {
		t.Fatalf("UploadMedia: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("server saw %d attempts, want 2 (one 429, one retry)", attempts)
	}
	if secondFileContent != string(payload) {
		t.Fatalf("second attempt's file content = %q (%d bytes), want the FULL payload %q (%d bytes) — "+
			"a spent reader would send an empty or truncated body on retry",
			secondFileContent, len(secondFileContent), payload, len(payload))
	}
}

func TestSendUploadedImageNestsTheDescriptorUnderTheImageKey(t *testing.T) {
	var gotBody map[string]any
	var gotPath string
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody = decodeJSONBody(t, r)
		json.NewEncoder(w).Encode(MessageResult{MessageID: "m1"})
	})

	desc := MediaDescriptor{
		URL: "https://mmg.whatsapp.net/x", DirectPath: "/x", MediaKey: "k",
		FileSHA256: "sh", FileEncSHA256: "esh", FileLength: 123, Mimetype: "image/png",
	}
	_, err := client.SendUploaded(context.Background(), "sess-1", MediaImage, "628123@s.whatsapp.net", desc, "a caption", "", false)
	if err != nil {
		t.Fatalf("SendUploaded: %v", err)
	}
	wantPath := "/api/v1/sessions/sess-1/messages/image"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
	imgVal, ok := gotBody["image"]
	if !ok {
		t.Fatalf(`body has no "image" key: %v`, gotBody)
	}
	imgObj, ok := imgVal.(map[string]any)
	if !ok {
		t.Fatalf(`body["image"] = %#v (%T), want a JSON OBJECT, not a flat string`, imgVal, imgVal)
	}
	if imgObj["url"] != desc.URL {
		t.Errorf(`body["image"]["url"] = %v, want %q`, imgObj["url"], desc.URL)
	}
	if imgObj["media_key"] != desc.MediaKey {
		t.Errorf(`body["image"]["media_key"] = %v, want %q`, imgObj["media_key"], desc.MediaKey)
	}
	if gotBody["caption"] != "a caption" {
		t.Errorf(`body["caption"] = %v, want "a caption"`, gotBody["caption"])
	}
	if _, ok := gotBody["filename"]; ok {
		t.Errorf(`body has a "filename" key for an IMAGE send: %v`, gotBody)
	}
	if _, ok := gotBody["ptt"]; ok {
		t.Errorf(`body has a "ptt" key for an IMAGE send: %v`, gotBody)
	}
}

func TestSendUploadedDocumentSendsTheRequiredFilename(t *testing.T) {
	var gotBody map[string]any
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody = decodeJSONBody(t, r)
		json.NewEncoder(w).Encode(MessageResult{})
	})

	desc := MediaDescriptor{URL: "https://mmg.whatsapp.net/d"}
	_, err := client.SendUploaded(context.Background(), "sess-1", MediaDocument, "628123@s.whatsapp.net", desc, "", "report.pdf", false)
	if err != nil {
		t.Fatalf("SendUploaded: %v", err)
	}
	if gotBody["filename"] != "report.pdf" {
		t.Errorf(`body["filename"] = %v, want "report.pdf" — REQUIRED and non-optional for a document send`, gotBody["filename"])
	}
}

func TestSendUploadedAudioSendsPTTAndOmitsTheCaption(t *testing.T) {
	var gotBody map[string]any
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody = decodeJSONBody(t, r)
		json.NewEncoder(w).Encode(MessageResult{})
	})

	desc := MediaDescriptor{URL: "https://mmg.whatsapp.net/a"}
	_, err := client.SendUploaded(context.Background(), "sess-1", MediaAudio, "628123@s.whatsapp.net", desc, "should never appear", "", true)
	if err != nil {
		t.Fatalf("SendUploaded: %v", err)
	}
	if gotBody["ptt"] != true {
		t.Errorf(`body["ptt"] = %v, want true`, gotBody["ptt"])
	}
	if _, ok := gotBody["caption"]; ok {
		t.Errorf(`body has a "caption" key (%v) for an AUDIO send — waxum's SendAudioRequest has no caption field at all`, gotBody["caption"])
	}
}

// TestDownloadMediaAcceptsAResponseLargerThanTheJSONResponseCap is the
// regression test for maxMediaResponseBytes: a file whose raw bytes exceed
// the package's ordinary 4 MiB response cap (maxResponseBytes), but whose
// base64-inflated JSON envelope still fits comfortably under
// maxMediaResponseBytes. Without the raised cap, io.LimitReader silently
// truncates the body, the truncated JSON fails to unmarshal, and
// DownloadMedia returns an error instead of the full file.
func TestDownloadMediaAcceptsAResponseLargerThanTheJSONResponseCap(t *testing.T) {
	raw := bytes.Repeat([]byte("A"), 5<<20) // 5 MiB > maxResponseBytes (4 MiB)
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		encoded := base64.StdEncoding.EncodeToString(raw)
		json.NewEncoder(w).Encode(map[string]any{"data": encoded, "size": len(raw)})
	})

	desc := MediaDescriptor{DirectPath: "/x", MediaKey: "k", FileSHA256: "sh", FileEncSHA256: "esh", FileLength: int64(len(raw))}
	got, err := client.DownloadMedia(context.Background(), "sess-1", desc, MediaImage)
	if err != nil {
		t.Fatalf("DownloadMedia: %v (a %d-byte body is above the package's ordinary %d-byte JSON cap and must not be truncated)", err, len(raw), maxResponseBytes)
	}
	if len(got) != len(raw) {
		t.Fatalf("DownloadMedia returned %d bytes, want %d", len(got), len(raw))
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("DownloadMedia returned data that does not match what the fake server sent")
	}
}
