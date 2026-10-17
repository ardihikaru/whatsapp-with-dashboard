package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/mcp/upstream"
)

// TestWaxumContactsToolRanksAcrossFakeWSMAndWaxumUpstreams drives the real
// waxum_contacts Tool.Run against fake WSM and waxum HTTP servers — the
// "fake upstreams" shape the task names explicitly, on top of
// rankContacts' own direct unit tests above.
func TestWaxumContactsToolRanksAcrossFakeWSMAndWaxumUpstreams(t *testing.T) {
	wsmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/sessions":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"id":"sess-1","phone":"6281200000003"}]`))
		case "/api/v1/contacts":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"contacts":[{"id":"w1","name":"Ria","phone":"6281200000003","aliases":[]}],"total":1}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer wsmSrv.Close()

	waxumSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/sessions/sess-1/contacts":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"contacts":[
				{"jid":"6281200000001@s.whatsapp.net","full_name":"Maria"},
				{"jid":"6281200000002@s.whatsapp.net","full_name":"Garsinia Ely Riani"}
			],"total":2,"limit":1000,"offset":0}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer waxumSrv.Close()

	pool, err := upstream.New(upstream.Config{
		HostsSpec: "x=" + wsmSrv.URL + "|" + waxumSrv.URL, WSMUser: "u", WSMPass: "p",
	})
	if err != nil {
		t.Fatal(err)
	}
	reg := New(pool)

	raw, _ := json.Marshal(map[string]any{"session_id": "sess-1", "q": "Ria"})
	out, err := reg.Call(context.Background(), "waxum_contacts", raw)
	if err != nil {
		t.Fatalf("Call(waxum_contacts): %v", err)
	}
	outMap, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("result is %T, want map[string]any", out)
	}
	data, ok := outMap["data"].(map[string]any)
	if !ok {
		t.Fatalf("result[\"data\"] is %T, want map[string]any; result: %+v", outMap["data"], outMap)
	}
	contacts, ok := data["contacts"].([]rankedContactOut)
	if !ok {
		t.Fatalf("data[\"contacts\"] is %T, want []rankedContactOut", data["contacts"])
	}
	if len(contacts) == 0 || contacts[0].Name != "Ria" {
		t.Fatalf("contacts = %+v, want Ria first", contacts)
	}
	if contacts[0].Source != "wsm" {
		t.Errorf("contacts[0].Source = %q, want %q (the WSM owner contact)", contacts[0].Source, "wsm")
	}
}
