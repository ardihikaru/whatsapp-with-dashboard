package waxum

import (
	"context"
	"net/http"
	"testing"
)

// TestCheckContactsHitsTheDocumentedMethodAndPath guards the same class of
// bug TestOperationsHitTheirDocumentedMethodsAndPaths exists for: a wrong
// method or path here compiles, is called, and hits nothing real in
// production while a permissive fake stays green.
func TestCheckContactsHitsTheDocumentedMethodAndPath(t *testing.T) {
	var gotMethod, gotPath string
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Write([]byte(`{"results":[]}`))
	})

	if _, err := client.CheckContacts(context.Background(), "sess-1", []string{"559999999999"}); err != nil {
		t.Fatalf("CheckContacts: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	wantPath := "/api/v1/sessions/sess-1/contacts/check"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
}

// TestCheckContactsSendsBarePhoneDigitsUnderThePhonesKey guards the request
// shape waxum's handler requires: a "phones" array of bare digits, no "@"
// suffix — waxum does Jid::pn(s) on each entry itself. A caller that wraps
// the numbers in JIDs first, or nests them under the wrong key, would 400
// against real waxum while a fake asserting only "no error" stays green.
func TestCheckContactsSendsBarePhoneDigitsUnderThePhonesKey(t *testing.T) {
	var gotBody map[string]any
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody = decodeJSONBody(t, r)
		w.Write([]byte(`{"results":[]}`))
	})

	phones := []string{"559999999999", "551234567890"}
	if _, err := client.CheckContacts(context.Background(), "sess-1", phones); err != nil {
		t.Fatalf("CheckContacts: %v", err)
	}

	raw, ok := gotBody["phones"]
	if !ok {
		t.Fatal("request body has no \"phones\" field")
	}
	got, ok := raw.([]any)
	if !ok || len(got) != 2 {
		t.Fatalf("phones = %v, want a 2-element array", raw)
	}
	for i, want := range phones {
		if got[i] != want {
			t.Errorf("phones[%d] = %v, want %q", i, got[i], want)
		}
	}
}

// TestCheckContactsDecodesANullJIDAndAnUnregisteredResult guards the two
// traps in the response shape: waxum's own "jid" field is Option<String>
// in Rust (a JSON null for an unregistered number), and "is_registered":
// false must decode as false, not be lost by an earlier version of this
// method that discarded the response entirely.
func TestCheckContactsDecodesANullJIDAndAnUnregisteredResult(t *testing.T) {
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"results":[{"phone":"551234567890","jid":null,"is_registered":false}]}`))
	})

	got, err := client.CheckContacts(context.Background(), "sess-1", []string{"551234567890"})
	if err != nil {
		t.Fatalf("CheckContacts: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(got))
	}
	if got[0].Phone != "551234567890" {
		t.Errorf("Phone = %q, want %q", got[0].Phone, "551234567890")
	}
	if got[0].JID != "" {
		t.Errorf("JID = %q, want empty string for a null jid", got[0].JID)
	}
	if got[0].IsRegistered {
		t.Errorf("IsRegistered = true, want false")
	}
}
