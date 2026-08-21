package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/apiclient"
)

// apiToken mints a real API-client bearer token through the login endpoint — the
// same path the alerter uses.
func apiToken(t *testing.T, srv http.Handler, repo *apiclient.Repository, login string) string {
	t.Helper()
	ttl := 86400
	created, err := repo.Create(context.Background(), "Alerter", login, "",
		apiclient.TokenSettings{TTLSeconds: &ttl})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"login_id":"` + login + `","secret":"` + created.Secret + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("login got %d: %s", res.Code, res.Body.String())
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil || out.Token == "" {
		t.Fatalf("no token in login response: %s", res.Body.String())
	}
	return out.Token
}

func postGroup(t *testing.T, srv http.Handler, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/messages/group", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, req)
	return res
}

// These cases all resolve BEFORE the handler reaches the session manager, which is
// why they run against the manager-less test server. The connected-send path is
// covered by internal/wa (the queue is JID-generic and already tested) and, because
// no unit test can prove a phone buzzed, by the live delivery check in
// docs/DEPLOY-VERIFICATION.md.
func TestPostAPISendGroupMessageValidation(t *testing.T) {
	srv, _, repo := newAuthTestServer(t)
	token := apiToken(t, srv, repo, "alerter-validation")

	cases := []struct {
		name, body string
		want       int
	}{
		// The reason this endpoint exists: a user JID must not be delivered to a
		// private chat and reported as a successful group alert.
		{"user jid is refused", `{"group_jid":"6281234567890@s.whatsapp.net","text":"hi"}`, http.StatusBadRequest},
		{"broadcast is refused", `{"group_jid":"status@broadcast","text":"hi"}`, http.StatusBadRequest},
		{"missing jid", `{"text":"hi"}`, http.StatusBadRequest},
		{"empty text", `{"group_jid":"120363021234567890@g.us","text":"   "}`, http.StatusBadRequest},
		{"oversized text", `{"group_jid":"120363021234567890@g.us","text":"` + strings.Repeat("x", 4097) + `"}`, http.StatusBadRequest},
		{"malformed json", `{`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := postGroup(t, srv, token, tc.body)
			if res.Code != tc.want {
				t.Fatalf("got %d want %d: %s", res.Code, tc.want, res.Body.String())
			}
		})
	}
}

// The endpoint is machine-facing and must be unreachable without a bearer token.
// An open group-send endpoint is an open relay for the platform's WhatsApp number.
func TestGroupEndpointsRequireAuth(t *testing.T) {
	srv, _, _ := newAuthTestServer(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/messages/group"},
		{http.MethodGet, "/api/v1/sessions/some-id/groups"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`))
		res := httptest.NewRecorder()
		srv.ServeHTTP(res, req)
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s got %d, want 401", tc.method, tc.path, res.Code)
		}
	}
}
