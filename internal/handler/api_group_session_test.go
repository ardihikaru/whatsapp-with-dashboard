package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func postSessionGroup(srv http.Handler, token, sessionID, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID+"/messages/group", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, req)
	return res
}

// The explicit group route names its own session: an unknown id is 404 (never "route elsewhere"), a
// bad group jid / empty text is 400 BEFORE anything is queued, and a valid body on a session that is not
// connected is 409 — it must NOT fall back to another session, which is the whole reason the route exists.
// (Like the other send routes, a connected send cannot be fabricated from package handler; see the note at
// the top of api_group_code_test.go.)
func TestPostAPISendGroupMessageViaSession(t *testing.T) {
	backend := &fakeGatewayBackend{name: "fake"}
	srv, s, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend})
	token := apiToken(t, srv, apiClients, "group-explicit")
	const group = "120363021234567890@g.us"

	cases := []struct {
		name, session, body string
		want                int
	}{
		{"unknown session", "nope", `{"group_jid":"` + group + `","text":"hi"}`, http.StatusNotFound},
		{"user jid is refused", s.ID, `{"group_jid":"6281234567890@s.whatsapp.net","text":"hi"}`, http.StatusBadRequest},
		{"broadcast is refused", s.ID, `{"group_jid":"status@broadcast","text":"hi"}`, http.StatusBadRequest},
		{"empty text", s.ID, `{"group_jid":"` + group + `","text":"  "}`, http.StatusBadRequest},
		{"malformed json", s.ID, `{`, http.StatusBadRequest},
		// group_code / master_only / allow_master are routing inputs of the AUTO route; here they are ignored,
		// so they must not turn a valid body into a different status.
		{"routing fields are ignored", s.ID, `{"group_jid":"` + group + `","text":"hi","group_code":"clino","master_only":true}`, http.StatusConflict},
		{"valid body, session not connected", s.ID, `{"group_jid":"` + group + `","text":"hi"}`, http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := postSessionGroup(srv, token, tc.session, tc.body)
			if res.Code != tc.want {
				t.Fatalf("got %d want %d: %s", res.Code, tc.want, res.Body.String())
			}
		})
	}
}

func TestSessionGroupSendRequiresAuth(t *testing.T) {
	srv, _, _ := newAuthTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/some-id/messages/group", strings.NewReader(`{}`))
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", res.Code)
	}
}
