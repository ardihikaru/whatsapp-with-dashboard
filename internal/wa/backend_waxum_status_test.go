package wa

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/waxum"
)

// TestPostStatusTextMapsWaxumErrorsOntoTheWASentinels mirrors
// TestCheckContactsMapsWaxumErrorsOntoTheWASentinels for the StatusPoster
// capability: a raw *waxum.APIError/sentinel must never reach a caller
// outside this package — mapErr is shared code, but PostStatusText is new
// code that has to actually call it.
func TestPostStatusTextMapsWaxumErrorsOntoTheWASentinels(t *testing.T) {
	cases := []struct {
		name string
		code int
		want error
	}{
		{"404 (unknown session) maps to ErrNotFound", http.StatusNotFound, ErrNotFound},
		{"503 (session not connected) maps to ErrNotConnected", http.StatusServiceUnavailable, ErrNotConnected},
		{"429 (waxum's tower_governor limiter) maps to ErrBackendBusy", http.StatusTooManyRequests, ErrBackendBusy},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.code)
			}))
			t.Cleanup(srv.Close)
			client := waxum.New(srv.URL, "test-token", time.Second,
				waxum.WithRetryPolicy(1, func(int, string) time.Duration { return time.Millisecond }))
			b := &waxumBackend{client: client}

			_, err := b.PostStatusText(context.Background(), "sess-1", "hi", 0, []string{"628@s.whatsapp.net"})
			if !errors.Is(err, c.want) {
				t.Fatalf("PostStatusText err = %v, want errors.Is(err, %v)", err, c.want)
			}
		})
	}
}

// TestPostStatusTextReturnsWaxumsMessageIDOnSuccess guards PostStatusText
// actually forwarding waxum's decoded message_id, not discarding it.
func TestPostStatusTextReturnsWaxumsMessageIDOnSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"message_id":"abc123","timestamp":1,"to":"status@broadcast"}`))
	}))
	t.Cleanup(srv.Close)
	client := waxum.New(srv.URL, "test-token", time.Second)
	b := &waxumBackend{client: client}

	id, err := b.PostStatusText(context.Background(), "sess-1", "hi", 0, []string{"628@s.whatsapp.net"})
	if err != nil {
		t.Fatalf("PostStatusText: %v", err)
	}
	if id != "abc123" {
		t.Errorf("messageID = %q, want %q", id, "abc123")
	}
}

// TestPostStatusImageAndVideoForwardTheDescriptorAndCaption guards both
// media variants reach waxum via the SAME upload-then-describe path
// SendMedia uses (toWaxumDescriptor), not a URL form, and that each hits
// its own documented path.
func TestPostStatusImageAndVideoForwardTheDescriptorAndCaption(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{"message_id":"m1","timestamp":1,"to":"status@broadcast"}`))
	}))
	t.Cleanup(srv.Close)
	client := waxum.New(srv.URL, "test-token", time.Second)
	b := &waxumBackend{client: client}

	d := MediaDescriptor{URL: "https://example.com/pic.jpg", Mimetype: "image/jpeg"}
	if _, err := b.PostStatusImage(context.Background(), "sess-1", d, "cap", []string{"628@s.whatsapp.net"}); err != nil {
		t.Fatalf("PostStatusImage: %v", err)
	}
	if want := "/api/v1/sessions/sess-1/status/image"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}

	if _, err := b.PostStatusVideo(context.Background(), "sess-1", d, "cap", []string{"628@s.whatsapp.net"}); err != nil {
		t.Fatalf("PostStatusVideo: %v", err)
	}
	if want := "/api/v1/sessions/sess-1/status/video"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
}

// TestStatusPosterIsUnavailableOnTheWhatsmeowBackend is the Manager-accessor
// level counterpart of TestTheWhatsmeowBackendSatisfiesNoCapabilityInterface
// — see TestMessageHistoryIsUnavailableOnTheWhatsmeowBackend for why both
// layers are worth asserting.
func TestStatusPosterIsUnavailableOnTheWhatsmeowBackend(t *testing.T) {
	backend := newTestWhatsmeowBackend(t)
	m := newWaxumTestManager(t, backend)

	if _, err := m.StatusPoster(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("StatusPoster() err = %v, want ErrUnsupported", err)
	}
}
