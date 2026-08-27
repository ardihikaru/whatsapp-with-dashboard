package waxum

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestWebhookListDecodesIdsAndEnabledFlags(t *testing.T) {
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"webhooks":[` +
			`{"id":"wh-1","url":"https://example.com/hook","events":["message"],"secret":null,"enabled":true},` +
			`{"id":"wh-2","url":"https://example.com/other","events":["status"],"secret":"shh","enabled":false}` +
			`],"count":2}`))
	})

	hooks, err := client.ListWebhooks(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("ListWebhooks: %v", err)
	}
	if len(hooks) != 2 {
		t.Fatalf("len(hooks) = %d, want 2", len(hooks))
	}
	if hooks[0].ID != "wh-1" || !hooks[0].Enabled {
		t.Errorf("hooks[0] = %+v, want ID=wh-1 Enabled=true", hooks[0])
	}
	if hooks[1].ID != "wh-2" || hooks[1].Enabled {
		t.Errorf("hooks[1] = %+v, want ID=wh-2 Enabled=false", hooks[1])
	}
	if hooks[1].Secret == nil || *hooks[1].Secret != "shh" {
		t.Errorf("hooks[1].Secret = %v, want a pointer to \"shh\"", hooks[1].Secret)
	}
	if hooks[0].Secret != nil {
		t.Errorf("hooks[0].Secret = %v, want nil for a null wire secret", hooks[0].Secret)
	}
}

func TestReenableAllWebhooksHitsTheFleetLevelPath(t *testing.T) {
	var gotPath, gotMethod string
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		w.WriteHeader(http.StatusOK)
	})

	if err := client.ReenableAllWebhooks(context.Background()); err != nil {
		t.Fatalf("ReenableAllWebhooks: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	wantPath := "/api/v1/webhooks/reenable-all"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
	if strings.Contains(gotPath, "/sessions/") {
		t.Errorf("path %q is scoped under /sessions/ — this operation is FLEET-level, not per-session", gotPath)
	}
}

// TestWebhookMethodsHitTheirDocumentedPaths covers the rest of this file's
// surface the same way TestOperationsHitTheirDocumentedMethodsAndPaths does
// for operations.go: a wrong method or path here compiles fine and only
// fails against a real waxum instance.
func TestWebhookMethodsHitTheirDocumentedPaths(t *testing.T) {
	var gotMethod, gotPath string
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusOK)
	})

	cases := []struct {
		name       string
		wantMethod string
		wantPath   string
		call       func() error
	}{
		{"UnregisterWebhook", http.MethodDelete, "/api/v1/sessions/sess-1/webhooks/wh-1", func() error {
			return client.UnregisterWebhook(context.Background(), "sess-1", "wh-1")
		}},
		{"ReenableWebhook", http.MethodPost, "/api/v1/sessions/sess-1/webhooks/wh-1/enable", func() error {
			return client.ReenableWebhook(context.Background(), "sess-1", "wh-1")
		}},
		{"ReplayWebhookDLQ", http.MethodPost, "/api/v1/sessions/sess-1/webhooks/dlq/entry-1/replay", func() error {
			return client.ReplayWebhookDLQ(context.Background(), "sess-1", "entry-1")
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if gotMethod != tc.wantMethod {
				t.Errorf("%s: method = %q, want %q", tc.name, gotMethod, tc.wantMethod)
			}
			if gotPath != tc.wantPath {
				t.Errorf("%s: path = %q, want %q", tc.name, gotPath, tc.wantPath)
			}
		})
	}
}

func TestWebhookDLQDecodesEntries(t *testing.T) {
	var gotPath, gotMethod string
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		w.Write([]byte(`{"entries":[{"id":"e1","session_id":"sess-1","webhook_url":"https://example.com/hook",` +
			`"event":"message","payload":"{}","last_error":"timeout","attempts":3,"failed_at":1700000000},` +
			`{"id":"e2","session_id":"sess-1","webhook_url":"https://example.com/hook","event":"status",` +
			`"payload":"{}","last_error":"connection refused","attempts":5,"failed_at":1700000100}],"count":2}`))
	})

	entries, err := client.WebhookDLQ(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("WebhookDLQ: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	wantPath := "/api/v1/sessions/sess-1/webhooks/dlq"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	if entries[0].ID != "e1" || entries[0].Attempts != 3 {
		t.Errorf("entries[0] = %+v, want ID=e1 Attempts=3", entries[0])
	}
	if entries[1].LastError != "connection refused" {
		t.Errorf("entries[1].LastError = %q, want %q", entries[1].LastError, "connection refused")
	}
}

func TestRegisterWebhookReturnsTheCreatedConfigInsteadOfDiscardingIt(t *testing.T) {
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"wh-new","url":"https://example.com/hook","events":["message"],"secret":"shh","enabled":true}`))
	})

	got, err := client.RegisterWebhook(context.Background(), "sess-1", "https://example.com/hook", "shh", []string{"message"})
	if err != nil {
		t.Fatalf("RegisterWebhook: %v", err)
	}
	if got == nil {
		t.Fatal("RegisterWebhook returned a nil config, want the created config")
	}
	if got.ID != "wh-new" {
		t.Errorf("ID = %q, want %q", got.ID, "wh-new")
	}
}
