package waxum

import (
	"context"
	"net/http"
	"testing"
)

func TestSearchMessagesHitsDocumentedPathAndMapsUTC(t *testing.T) {
	var gotPath, gotQ string
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQ = r.URL.Path, r.URL.RawQuery
		w.Write([]byte(`{"messages":[{"id":7,"message_id":"A1","session_id":"s","chat_jid":"1@g.us","sender_jid":"2@s.whatsapp.net","direction":"in","msg_type":"text","body":"makan siang?","snippet":null,"msg_timestamp":"2026-10-16 05:30:00","push_name":null,"media":null},{"id":8,"msg_timestamp":"bad"}],"count":2}`))
	})
	page, err := client.SearchMessages(context.Background(), "sess-1", "makan siang", 5, 10)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/sessions/sess-1/messages/search" || gotQ != "limit=5&offset=10&q=makan+siang" {
		t.Errorf("path/query = %q ?%q", gotPath, gotQ)
	}
	if page.Count != 1 || page.Messages[0].ReceivedAt.UTC().Format("15:04") != "05:30" || *page.Messages[0].Body != "makan siang?" {
		t.Errorf("page = %+v (malformed row must be skipped, time read as UTC)", page)
	}
}

func TestChatMessagesEscapesTheJID(t *testing.T) {
	var gotPath string
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.Write([]byte(`{"messages":[],"count":0}`))
	})
	if _, err := client.ChatMessages(context.Background(), "sess-1", "123@g.us", 0, 0); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/sessions/sess-1/messages/chat/123@g.us" && gotPath != "/api/v1/sessions/sess-1/messages/chat/123%40g.us" {
		t.Errorf("path = %q", gotPath)
	}
}

func TestContactsDecodes(t *testing.T) {
	var gotPath string
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		w.Write([]byte(`{"contacts":[{"jid":"1@s.whatsapp.net","phone":"62811","full_name":"Ria"}],"total":1,"limit":50,"offset":0}`))
	})
	page, err := client.Contacts(context.Background(), "sess-1", "ria", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/sessions/sess-1/contacts?limit=50&q=ria" || page.Total != 1 || *page.Contacts[0].FullName != "Ria" {
		t.Errorf("got %q %+v", gotPath, page)
	}
}
