package waxum

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

// TestPostStatusTextHitsTheDocumentedMethodAndPathAndBody guards the exact
// request shape waxum's /status/text handler requires: POST, the
// session-scoped path, and "text"/"recipients" present with
// "background_argb" omitted when zero (the spec's own documented default,
// /tmp/waxum-status-api.md).
func TestPostStatusTextHitsTheDocumentedMethodAndPathAndBody(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody = decodeJSONBody(t, r)
		json.NewEncoder(w).Encode(MessageResult{MessageID: "m1", To: "status@broadcast"})
	})

	res, err := client.PostStatusText(context.Background(), "sess-1", "hello", 0, []string{"628@s.whatsapp.net"})
	if err != nil {
		t.Fatalf("PostStatusText: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if want := "/api/v1/sessions/sess-1/status/text"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotBody["text"] != "hello" {
		t.Errorf(`body["text"] = %v, want "hello"`, gotBody["text"])
	}
	if _, ok := gotBody["background_argb"]; ok {
		t.Errorf(`body has "background_argb" key, want it omitted when 0`)
	}
	recips, ok := gotBody["recipients"].([]any)
	if !ok || len(recips) != 1 || recips[0] != "628@s.whatsapp.net" {
		t.Errorf(`body["recipients"] = %v, want ["628@s.whatsapp.net"]`, gotBody["recipients"])
	}
	if res.MessageID != "m1" {
		t.Errorf("MessageID = %q, want %q", res.MessageID, "m1")
	}
}

// TestPostStatusTextSendsBackgroundArgbWhenNonZero guards against the
// omission rule swallowing a genuinely-set value too.
func TestPostStatusTextSendsBackgroundArgbWhenNonZero(t *testing.T) {
	var gotBody map[string]any
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody = decodeJSONBody(t, r)
		json.NewEncoder(w).Encode(MessageResult{})
	})

	_, err := client.PostStatusText(context.Background(), "sess-1", "hi", 4280184399, []string{"628@s.whatsapp.net"})
	if err != nil {
		t.Fatalf("PostStatusText: %v", err)
	}
	got, ok := gotBody["background_argb"].(float64)
	if !ok || int64(got) != 4280184399 {
		t.Errorf(`body["background_argb"] = %v, want 4280184399`, gotBody["background_argb"])
	}
}

// TestPostStatusTextOnAnEmptyRecipientsListStillSendsTheKey guards that this
// method does not itself invent any client-side recipients validation —
// waxum's own 400 ("recipients empty") is what enforces that, per the spec.
func TestPostStatusTextOnAnEmptyRecipientsListStillSendsTheKey(t *testing.T) {
	var gotBody map[string]any
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody = decodeJSONBody(t, r)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"recipients empty"}`))
	})

	_, err := client.PostStatusText(context.Background(), "sess-1", "hi", 0, []string{})
	if err == nil {
		t.Fatal("PostStatusText: want an error on empty recipients, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v (%T), want *APIError", err, err)
	}
	if apiErr.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d, want 400", apiErr.StatusCode)
	}
	if _, ok := gotBody["recipients"]; !ok {
		t.Errorf(`body has no "recipients" key`)
	}
}

// TestPostStatusImageWrapsTheDescriptorUnderTheMediaKeyAndOmitsEmptyCaption
// guards the request shape for the image endpoint: "media" nests the
// descriptor object (mirroring SendUploaded's own "nested object, not a
// flat URL" trap), and "caption" is omitted when empty rather than sent as
// "".
func TestPostStatusImageWrapsTheDescriptorUnderTheMediaKeyAndOmitsEmptyCaption(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody = decodeJSONBody(t, r)
		json.NewEncoder(w).Encode(MessageResult{MessageID: "m2"})
	})

	d := MediaDescriptor{URL: "https://example.com/u", DirectPath: "/dp", MediaKey: "mk", Mimetype: "image/jpeg"}
	res, err := client.PostStatusImage(context.Background(), "sess-1", d, "", []string{"628@s.whatsapp.net"})
	if err != nil {
		t.Fatalf("PostStatusImage: %v", err)
	}
	if want := "/api/v1/sessions/sess-1/status/image"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	mediaVal, ok := gotBody["media"].(map[string]any)
	if !ok {
		t.Fatalf(`body["media"] = %#v, want a JSON object`, gotBody["media"])
	}
	if mediaVal["url"] != d.URL {
		t.Errorf(`body["media"]["url"] = %v, want %q`, mediaVal["url"], d.URL)
	}
	if _, ok := gotBody["caption"]; ok {
		t.Errorf(`body has "caption" key, want it omitted when empty`)
	}
	if res.MessageID != "m2" {
		t.Errorf("MessageID = %q, want %q", res.MessageID, "m2")
	}
}

// TestPostStatusVideoUsesTheVideoPathAndSendsANonEmptyCaption guards the
// sibling path (not image's) and that a non-empty caption IS sent.
func TestPostStatusVideoUsesTheVideoPathAndSendsANonEmptyCaption(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody = decodeJSONBody(t, r)
		json.NewEncoder(w).Encode(MessageResult{MessageID: "m3"})
	})

	d := MediaDescriptor{URL: "https://example.com/v", Mimetype: "video/mp4"}
	_, err := client.PostStatusVideo(context.Background(), "sess-1", d, "a caption", []string{"628@s.whatsapp.net"})
	if err != nil {
		t.Fatalf("PostStatusVideo: %v", err)
	}
	if want := "/api/v1/sessions/sess-1/status/video"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotBody["caption"] != "a caption" {
		t.Errorf(`body["caption"] = %v, want %q`, gotBody["caption"], "a caption")
	}
}

// TestPostStatusTextReturns503AsAnAPIErrorWhenNotConnected guards the
// mapErr-relevant status codes (404/503) surfacing as *APIError rather than
// being swallowed, matching the other status endpoints' documented error
// table.
func TestPostStatusTextReturns503AsAnAPIErrorWhenNotConnected(t *testing.T) {
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":"not connected"}`))
	})

	_, err := client.PostStatusText(context.Background(), "sess-1", "hi", 0, []string{"628@s.whatsapp.net"})
	if err == nil {
		t.Fatal("want an error")
	}
	if !errors.Is(err, ErrClientNotConnected) {
		t.Errorf("err = %v, want ErrClientNotConnected", err)
	}
}
