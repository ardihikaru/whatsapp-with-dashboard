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

// TestRegisterWebhookMapsADuplicateURLConflictToErrAlreadyExists proves the
// special-case in RegisterWebhook (backend_waxum_webhooks.go): waxum's
// generic 409 (waxum.ErrConflict) must NOT reach the operator as
// ErrNotConnected ("session not connected", mapErr's generic mapping for
// waxum.ErrConflict) when what actually happened is "this webhook URL is
// already registered."
func TestRegisterWebhookMapsADuplicateURLConflictToErrAlreadyExists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()
	client := waxum.New(srv.URL, "test-token", 5*time.Second,
		waxum.WithClientRateLimit(1e6, 1e6),
		waxum.WithRetryPolicy(1, func(int, string) time.Duration { return time.Millisecond }),
	)
	backend := NewWaxumBackend(client, WaxumConfig{}).(WebhookAdmin)

	_, err := backend.RegisterWebhook(context.Background(), "sess-1",
		WebhookSpec{URL: "https://example.com/hook", Events: []string{"message"}})
	if !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("RegisterWebhook error = %v, want ErrAlreadyExists", err)
	}
	if errors.Is(err, ErrNotConnected) {
		t.Fatalf("RegisterWebhook error = %v, must NOT also read as ErrNotConnected (misleading: this is a duplicate, not a connection problem)", err)
	}
}
