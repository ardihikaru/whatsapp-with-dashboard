package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/auth"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/contact"
)

// apiRequest builds an authenticated (bearer) API request with no body —
// GET/DELETE's own shape, postJSON's companion for everything that is not
// a POST with a JSON body.
func apiRequest(srv http.Handler, token, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

type apiContactsListResponse struct {
	Contacts []struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		Phone        string `json:"phone"`
		WAStatus     string `json:"wa_status"`
		MatchScore   int    `json:"match_score"`
		MatchedField string `json:"matched_field"`
	} `json:"contacts"`
	Total int `json:"total"`
}

// TestAPIContactCreateListDeleteRoundTrips is the explicit API CRUD round
// trip: create, see it in GET /api/v1/contacts, delete, see it gone — the
// API-surface analogue of TestContactCreateThenListThenDeleteRoundTrips.
func TestAPIContactCreateListDeleteRoundTrips(t *testing.T) {
	srv, _, _, _, apiClients := newContactsTestServer(t, nil)
	token := apiToken(t, srv, apiClients, "contacts-api")

	res := postJSON(srv, token, http.MethodPost, "/api/v1/contacts", `{"name":"Ria","phone":"+62 812-0000-0001","aliases":["ria <3"]}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/contacts = %d, want 201; body: %s", res.Code, res.Body.String())
	}
	var created struct {
		Contact struct {
			ID    string `json:"id"`
			Phone string `json:"phone"`
		} `json:"contact"`
		VerifyError string `json:"verify_error"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v; body: %s", err, res.Body.String())
	}
	if created.Contact.Phone != "6281200000001" {
		t.Errorf("Phone = %q, want canonical digits", created.Contact.Phone)
	}
	if created.VerifyError == "" {
		t.Error("VerifyError = \"\", want a reported verify failure (no connected session in this test server)")
	}

	list := apiRequest(srv, token, http.MethodGet, "/api/v1/contacts")
	if list.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/contacts = %d, want 200; body: %s", list.Code, list.Body.String())
	}
	var out apiContactsListResponse
	if err := json.Unmarshal(list.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode list response: %v; body: %s", err, list.Body.String())
	}
	if out.Total != 1 || len(out.Contacts) != 1 {
		t.Fatalf("list = %+v, want exactly 1 contact", out)
	}

	del := apiRequest(srv, token, http.MethodDelete, "/api/v1/contacts/"+created.Contact.ID)
	if del.Code != http.StatusNoContent {
		t.Fatalf("DELETE /api/v1/contacts/{contactID} = %d, want 204; body: %s", del.Code, del.Body.String())
	}
	after := apiRequest(srv, token, http.MethodGet, "/api/v1/contacts")
	var afterOut apiContactsListResponse
	if err := json.Unmarshal(after.Body.Bytes(), &afterOut); err != nil {
		t.Fatalf("decode post-delete list: %v", err)
	}
	if afterOut.Total != 0 {
		t.Errorf("list after delete = %+v, want 0 contacts", afterOut)
	}
}

// TestAPIContactCreateAnswers409OnADuplicatePhone pins the dup-phone status
// code the task names explicitly.
func TestAPIContactCreateAnswers409OnADuplicatePhone(t *testing.T) {
	srv, _, _, _, apiClients := newContactsTestServer(t, nil)
	token := apiToken(t, srv, apiClients, "contacts-dup")

	first := postJSON(srv, token, http.MethodPost, "/api/v1/contacts", `{"name":"Ria","phone":"+62 812-0000-0001"}`)
	if first.Code != http.StatusCreated {
		t.Fatalf("first create = %d, want 201; body: %s", first.Code, first.Body.String())
	}
	second := postJSON(srv, token, http.MethodPost, "/api/v1/contacts", `{"name":"Ria Dup","phone":"+62 812-0000-0001"}`)
	if second.Code != http.StatusConflict {
		t.Fatalf("duplicate create = %d, want 409; body: %s", second.Code, second.Body.String())
	}
}

// TestAPIContactCreateAnswers400OnAnInvalidPhone pins the invalid-phone
// status code the task names explicitly.
func TestAPIContactCreateAnswers400OnAnInvalidPhone(t *testing.T) {
	srv, _, _, _, apiClients := newContactsTestServer(t, nil)
	token := apiToken(t, srv, apiClients, "contacts-400")

	res := postJSON(srv, token, http.MethodPost, "/api/v1/contacts", `{"name":"Ria","phone":"not-a-number"}`)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("invalid phone create = %d, want 400; body: %s", res.Code, res.Body.String())
	}
}

// TestAPIContactVerifyNeverReportsNotRegisteredOnABackendFailure is the API
// analogue of the UI's verify-failure test: with no connected session, a
// re-verify must leave wa_status at "unknown", report verify_error, and
// answer 200 (the contact row itself, not an error status — the VERIFY
// request succeeded in the sense that it ran; it is the underlying check
// that could not complete).
func TestAPIContactVerifyNeverReportsNotRegisteredOnABackendFailure(t *testing.T) {
	srv, _, _, repo, apiClients := newContactsTestServer(t, nil)
	token := apiToken(t, srv, apiClients, "contacts-verify")

	create := postJSON(srv, token, http.MethodPost, "/api/v1/contacts", `{"name":"Ria","phone":"+62 812-0000-0001"}`)
	var created struct {
		Contact struct {
			ID string `json:"id"`
		} `json:"contact"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v; body: %s", err, create.Body.String())
	}

	res := apiRequest(srv, token, http.MethodPost, "/api/v1/contacts/"+created.Contact.ID+"/verify")
	if res.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/contacts/{contactID}/verify = %d, want 200; body: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "verify_error") {
		t.Errorf("verify response does not report verify_error; body: %s", res.Body.String())
	}
	stored, err := repo.Get(context.Background(), created.Contact.ID)
	if err != nil {
		t.Fatalf("repo.Get: %v", err)
	}
	if stored.WAStatus != contact.StatusUnknown {
		t.Errorf("WAStatus = %q, want %q", stored.WAStatus, contact.StatusUnknown)
	}
}

// TestAPIContactVerifyAnswers404ForAnUnknownContact pins 404 on a bad id.
func TestAPIContactVerifyAnswers404ForAnUnknownContact(t *testing.T) {
	srv, _, _, _, apiClients := newContactsTestServer(t, nil)
	token := apiToken(t, srv, apiClients, "contacts-verify-404")
	res := apiRequest(srv, token, http.MethodPost, "/api/v1/contacts/does-not-exist/verify")
	if res.Code != http.StatusNotFound {
		t.Fatalf("verify on unknown contact = %d, want 404; body: %s", res.Code, res.Body.String())
	}
}

// TestAPIContactDeleteAnswers404ForAnUnknownContact pins 404 on a bad id.
func TestAPIContactDeleteAnswers404ForAnUnknownContact(t *testing.T) {
	srv, _, _, _, apiClients := newContactsTestServer(t, nil)
	token := apiToken(t, srv, apiClients, "contacts-delete-404")
	res := apiRequest(srv, token, http.MethodDelete, "/api/v1/contacts/does-not-exist")
	if res.Code != http.StatusNotFound {
		t.Fatalf("delete on unknown contact = %d, want 404; body: %s", res.Code, res.Body.String())
	}
}

// TestAPIContactsListIsRankedByQ is the task's named ranking example at the
// API surface: q=Ria must rank an exact-alias "Ria ❤️"-equivalent contact
// above a merely-prefix "Garsinia Ely Riani", above a substring-only
// "Maria".
func TestAPIContactsListIsRankedByQ(t *testing.T) {
	srv, _, _, _, apiClients := newContactsTestServer(t, nil)
	token := apiToken(t, srv, apiClients, "contacts-ranked")

	create := func(name, phone string) {
		res := postJSON(srv, token, http.MethodPost, "/api/v1/contacts", `{"name":"`+name+`","phone":"`+phone+`"}`)
		if res.Code != http.StatusCreated {
			t.Fatalf("create %q = %d, want 201; body: %s", name, res.Code, res.Body.String())
		}
	}
	create("Maria", "+62 812-0000-0001")
	create("Garsinia Ely Riani", "+62 812-0000-0002")
	create("Ria", "+62 812-0000-0003")

	res := apiRequest(srv, token, http.MethodGet, "/api/v1/contacts?q=Ria")
	if res.Code != http.StatusOK {
		t.Fatalf("ranked GET = %d, want 200; body: %s", res.Code, res.Body.String())
	}
	var out apiContactsListResponse
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v; body: %s", err, res.Body.String())
	}
	names := make([]string, len(out.Contacts))
	for i, c := range out.Contacts {
		names[i] = c.Name
	}
	if len(names) < 3 || names[0] != "Ria" || names[1] != "Garsinia Ely Riani" || names[2] != "Maria" {
		t.Fatalf("ranked names = %v, want [Ria, Garsinia Ely Riani, Maria] in that order", names)
	}
}

// TestAPIContactsBearerIsRejectedOnThePageAndCookieIsRejectedOnTheAPI pins
// the house rule ("Auth surfaces do not mix", CLAUDE.md) specifically for
// the new Contacts routes — the same technique
// TestAPIRejectsWebSessionBearerAndInvalidToken (api_auth_test.go) uses for
// /api/v1/sessions, applied here to /api/v1/contacts and GET /contacts.
func TestAPIContactsBearerIsRejectedOnThePageAndCookieIsRejectedOnTheAPI(t *testing.T) {
	srv, sessions, _, _, apiClients := newContactsTestServer(t, nil)
	token := apiToken(t, srv, apiClients, "contacts-authsplit")

	// A bearer token on the page route must be rejected (treated as
	// unauthenticated), not accepted as an alternative to the cookie.
	req := httptest.NewRequest(http.MethodGet, "/contacts", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Errorf("GET /contacts with a bearer token (no cookie) = 200, want a redirect/401 (bearer must not authenticate a page route)")
	}

	// A browser session cookie on the API route must be rejected too.
	webSess, err := sessions.Create("admin")
	if err != nil {
		t.Fatal(err)
	}
	cookieReq := httptest.NewRequest(http.MethodGet, "/api/v1/contacts", nil)
	cookieReq.AddCookie(&http.Cookie{Name: auth.CookieName, Value: webSess.Token})
	cookieRec := httptest.NewRecorder()
	srv.ServeHTTP(cookieRec, cookieReq)
	if cookieRec.Code == http.StatusOK {
		t.Errorf("GET /api/v1/contacts with only a browser session cookie (no bearer) = 200, want 401")
	}
}
