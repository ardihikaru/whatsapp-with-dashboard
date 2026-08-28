package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// TestContactsCheckAutoAnswers409WhenNoSessionIsConnected mirrors
// TestMediaSendAutoRoutesAndAnswers409WithNoConnectedSession
// (api_media_test.go) for the auto-routed contacts endpoint: the one
// created-but-never-activated session newMediaTestServer hands back is
// never connected, so SelectConnected must find nothing.
func TestContactsCheckAutoAnswers409WhenNoSessionIsConnected(t *testing.T) {
	backend := &fakeGatewayBackend{
		name: "fake",
		checkContactsFn: func(ctx context.Context, sessionID string, phones []string) ([]wa.ContactCheck, error) {
			t.Fatal("the backend must not be called when no session is connected")
			return nil, nil
		},
	}
	srv, _, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend})
	token := apiToken(t, srv, apiClients, "contacts-auto-409")

	rec := postJSON(srv, token, http.MethodPost, "/api/v1/contacts/check", `{"phones":["+6281200000000"]}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d (no connected session); body = %s", rec.Code, http.StatusConflict, rec.Body.String())
	}
}

// TestContactsCheckExplicitAnswers404ForAnUnknownSessionID proves the
// explicit route resolves the session BEFORE anything else.
func TestContactsCheckExplicitAnswers404ForAnUnknownSessionID(t *testing.T) {
	srv, _, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: whatsmeowShapedBackend()})
	token := apiToken(t, srv, apiClients, "contacts-404")

	rec := postJSON(srv, token, http.MethodPost, "/api/v1/sessions/does-not-exist/contacts/check", `{"phones":["+6281200000000"]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

// TestContactsCheckAnswers400OnEmptyOrOversizedPhones covers both 400
// triggers on the explicit route: an empty phones list, and one number over
// the 100-number cap.
func TestContactsCheckAnswers400OnEmptyOrOversizedPhones(t *testing.T) {
	backend := &fakeGatewayBackend{
		name: "fake",
		checkContactsFn: func(ctx context.Context, sessionID string, phones []string) ([]wa.ContactCheck, error) {
			t.Fatal("the backend must not be called on a request that should 400 before reaching it")
			return nil, nil
		},
	}
	srv, s, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend})
	token := apiToken(t, srv, apiClients, "contacts-400")

	t.Run("empty phones", func(t *testing.T) {
		rec := postJSON(srv, token, http.MethodPost, "/api/v1/sessions/"+s.ID+"/contacts/check", `{"phones":[]}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
		}
	})

	t.Run("101 numbers", func(t *testing.T) {
		phones := make([]string, maxVerifyPhones+1)
		for i := range phones {
			phones[i] = `"+6281200000000"`
		}
		body := `{"phones":[` + strings.Join(phones, ",") + `]}`
		rec := postJSON(srv, token, http.MethodPost, "/api/v1/sessions/"+s.ID+"/contacts/check", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
		}
	})
}

// TestContactsCheckAnswers501OnAWhatsmeowShapedBackend proves the missing
// wa.ContactDirectory capability surfaces as 501, exactly like every other
// optional-capability route (api_ops_test.go, api_calls_test.go's disabled
// case).
func TestContactsCheckAnswers501OnAWhatsmeowShapedBackend(t *testing.T) {
	srv, s, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: whatsmeowShapedBackend()})
	token := apiToken(t, srv, apiClients, "contacts-501")

	rec := postJSON(srv, token, http.MethodPost, "/api/v1/sessions/"+s.ID+"/contacts/check", `{"phones":["+6281200000000"]}`)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusNotImplemented, rec.Body.String())
	}
}

// TestContactsCheckHappyPathReturnsMixedVerdicts drives the explicit route
// end to end and asserts the JSON body: session metadata plus one verdict
// per input line, registered/not_registered/invalid all represented.
func TestContactsCheckHappyPathReturnsMixedVerdicts(t *testing.T) {
	backend := &fakeGatewayBackend{
		name: "fake",
		checkContactsFn: func(ctx context.Context, sessionID string, phones []string) ([]wa.ContactCheck, error) {
			return []wa.ContactCheck{
				{Phone: "6281200000000", JID: "6281200000000@s.whatsapp.net", IsRegistered: true},
				{Phone: "6289900000000", IsRegistered: false},
			}, nil
		},
	}
	srv, s, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend})
	token := apiToken(t, srv, apiClients, "contacts-happy")

	rec := postJSON(srv, token, http.MethodPost, "/api/v1/sessions/"+s.ID+"/contacts/check",
		`{"phones":["+62 812-0000-0000","+62 899-0000-0000","not-a-number"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Session struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"session"`
		Results []contactVerdict `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("could not decode response: %v; body = %s", err, rec.Body.String())
	}
	if body.Session.ID != s.ID {
		t.Errorf("session.id = %q, want %q", body.Session.ID, s.ID)
	}
	if len(body.Results) != 3 {
		t.Fatalf("got %d results, want 3: %+v", len(body.Results), body.Results)
	}
	if body.Results[0].Status != contactStatusRegistered {
		t.Errorf("result 0 status = %q, want %q", body.Results[0].Status, contactStatusRegistered)
	}
	if body.Results[0].JID == "" {
		t.Error("result 0 JID is empty, want the registered number's JID")
	}
	if body.Results[1].Status != contactStatusNotRegistered {
		t.Errorf("result 1 status = %q, want %q", body.Results[1].Status, contactStatusNotRegistered)
	}
	if body.Results[2].Status != contactStatusInvalid {
		t.Errorf("result 2 status = %q, want %q", body.Results[2].Status, contactStatusInvalid)
	}
	if body.Results[2].Error == "" {
		t.Error("result 2 Error is empty, want the NormalizePhone rejection reason")
	}
}
