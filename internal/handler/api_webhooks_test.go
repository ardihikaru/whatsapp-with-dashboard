package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// TestWebhookRegistrationRejectsAnUnknownEventName proves an event name
// outside waxumWebhookEvents is refused with a 400 that NAMES the bad
// value, rather than reaching waxum and coming back as an opaque 422.
func TestWebhookRegistrationRejectsAnUnknownEventName(t *testing.T) {
	called := false
	backend := &fakeOpsBackend{
		fakeGatewayBackend: &fakeGatewayBackend{name: "fake"},
		registerWebhookFn: func(spec wa.WebhookSpec) (wa.WebhookInfo, error) {
			called = true
			return wa.WebhookInfo{}, nil
		},
	}
	srv, s, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend})
	token := apiToken(t, srv, apiClients, "webhook-bad-event")

	rec := postJSON(srv, token, http.MethodPost, "/api/v1/sessions/"+s.ID+"/webhooks",
		`{"url":"https://example.com/hook","events":["message","not_a_real_event"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not_a_real_event") {
		t.Fatalf("body = %s, want it to name the bad event value", rec.Body.String())
	}
	if called {
		t.Fatal("RegisterWebhook was called despite the bad event name — validation must happen BEFORE reaching the backend")
	}
}

// TestWebhookResponsesNeverIncludeTheSecret proves the HMAC signing secret
// never appears in a response body — over BOTH register and list, the two
// routes that return a webhook's shape at all. A leak here is a credential
// leak (CLAUDE.md's WAXUM_TOKEN-grade treatment of this field).
func TestWebhookResponsesNeverIncludeTheSecret(t *testing.T) {
	const secret = "super-secret-hmac-key-do-not-leak"
	backend := &fakeOpsBackend{
		fakeGatewayBackend: &fakeGatewayBackend{name: "fake"},
		registerWebhookFn: func(spec wa.WebhookSpec) (wa.WebhookInfo, error) {
			s := spec.Secret
			return wa.WebhookInfo{ID: "wh-1", URL: spec.URL, Events: spec.Events, Secret: &s, Enabled: true}, nil
		},
		listWebhooksFn: func() ([]wa.WebhookInfo, error) {
			s := secret
			return []wa.WebhookInfo{{ID: "wh-1", URL: "https://example.com/hook", Events: []string{"message"}, Secret: &s, Enabled: true}}, nil
		},
	}
	srv, s, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend})
	token := apiToken(t, srv, apiClients, "webhook-secret-leak")

	regRec := postJSON(srv, token, http.MethodPost, "/api/v1/sessions/"+s.ID+"/webhooks",
		`{"url":"https://example.com/hook","events":["message"],"secret":"`+secret+`"}`)
	if regRec.Code != http.StatusOK {
		t.Fatalf("register status = %d, want 200; body = %s", regRec.Code, regRec.Body.String())
	}
	if strings.Contains(regRec.Body.String(), secret) {
		t.Fatalf("register response leaked the secret: %s", regRec.Body.String())
	}
	var regBody map[string]any
	if err := json.Unmarshal(regRec.Body.Bytes(), &regBody); err != nil {
		t.Fatal(err)
	}
	if _, hasSecretField := regBody["secret"]; hasSecretField {
		t.Fatalf("register response has a literal \"secret\" key at all: %v", regBody)
	}
	if hs, _ := regBody["has_secret"].(bool); !hs {
		t.Errorf("register response has_secret = %v, want true", regBody["has_secret"])
	}

	listRec := postJSON(srv, token, http.MethodGet, "/api/v1/sessions/"+s.ID+"/webhooks", "")
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200; body = %s", listRec.Code, listRec.Body.String())
	}
	if strings.Contains(listRec.Body.String(), secret) {
		t.Fatalf("list response leaked the secret: %s", listRec.Body.String())
	}
	var listBody []map[string]any
	if err := json.Unmarshal(listRec.Body.Bytes(), &listBody); err != nil {
		t.Fatal(err)
	}
	if len(listBody) != 1 {
		t.Fatalf("list returned %d entries, want 1", len(listBody))
	}
	if _, hasSecretField := listBody[0]["secret"]; hasSecretField {
		t.Fatalf("list response has a literal \"secret\" key at all: %v", listBody[0])
	}
	if hs, _ := listBody[0]["has_secret"].(bool); !hs {
		t.Errorf("list response has_secret = %v, want true", listBody[0]["has_secret"])
	}
}

// TestWebhookRegistrationSurfacesAPrivateAddressRejectionClearly proves a
// 422 from waxum's SSRF guard is surfaced with a message NAMING the actual
// cause — "most operator-run receivers ARE on a private network" is the
// single most likely mistake here, per the phase brief, so a generic
// "backend rejected the request" is not good enough. The fake backend
// returns the SAME wrapped-error shape mapErr actually produces
// (internal/wa/backend_waxum.go's mapErr, exercised directly by
// TestMapErrNamesTheActualCauseWhenWaxumsBodyCarriesOne in internal/wa) —
// this test is what proves the handler layer actually PASSES that message
// through to the response body instead of dropping it.
func TestWebhookRegistrationSurfacesAPrivateAddressRejectionClearly(t *testing.T) {
	const cause = "Callback URL resolves to a private/reserved address"
	backend := &fakeOpsBackend{
		fakeGatewayBackend: &fakeGatewayBackend{name: "fake"},
		registerWebhookFn: func(spec wa.WebhookSpec) (wa.WebhookInfo, error) {
			return wa.WebhookInfo{}, fmt.Errorf("%w: %s", wa.ErrRejectedByBackend, cause)
		},
	}
	srv, s, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend})
	token := apiToken(t, srv, apiClients, "webhook-private-address")

	rec := postJSON(srv, token, http.MethodPost, "/api/v1/sessions/"+s.ID+"/webhooks",
		`{"url":"http://10.0.0.5:9000/hook","events":["message"]}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), cause) {
		t.Fatalf("body = %s, want it to name the actual cause (%q)", rec.Body.String(), cause)
	}
}
