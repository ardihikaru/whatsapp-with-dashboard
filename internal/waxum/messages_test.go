package waxum

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"
)

func mustParseQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	v, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatalf("parsing query %q: %v", raw, err)
	}
	return v
}

// TestSessionMessagesHitsTheDocumentedMethodAndPath guards the same class
// of bug TestOperationsHitTheirDocumentedMethodsAndPaths and
// TestCheckContactsHitsTheDocumentedMethodAndPath exist for: a wrong
// method or path here compiles, is called, and hits nothing real in
// production while a permissive fake stays green.
func TestSessionMessagesHitsTheDocumentedMethodAndPath(t *testing.T) {
	var gotMethod, gotPath string
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Write([]byte(`{"messages":[],"count":0,"next_cursor":null}`))
	})

	if _, err := client.SessionMessages(context.Background(), "sess-1", nil, 0); err != nil {
		t.Fatalf("SessionMessages: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	wantPath := "/api/v1/sessions/sess-1/messages"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
}

// TestSessionMessagesOmitsAfterWhenNilAndSendsBothQueryParamsWhenSet guards
// the query-string shape: "after" must be absent entirely (not "after=0"
// or "after=") when the caller wants the newest page, and both params must
// be present, correctly named, when the caller supplies them.
func TestSessionMessagesOmitsAfterWhenNilAndSendsBothQueryParamsWhenSet(t *testing.T) {
	t.Run("after nil: no after param at all", func(t *testing.T) {
		var gotQuery string
		client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
			gotQuery = r.URL.RawQuery
			w.Write([]byte(`{"messages":[],"count":0,"next_cursor":null}`))
		})
		if _, err := client.SessionMessages(context.Background(), "sess-1", nil, 100); err != nil {
			t.Fatalf("SessionMessages: %v", err)
		}
		if got := mustParseQuery(t, gotQuery); got.Has("after") {
			t.Errorf("query = %q, want no \"after\" param when after is nil", gotQuery)
		}
		if got := mustParseQuery(t, gotQuery).Get("limit"); got != "100" {
			t.Errorf("limit = %q, want %q", got, "100")
		}
	})

	t.Run("after set: both params present", func(t *testing.T) {
		var gotQuery string
		client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
			gotQuery = r.URL.RawQuery
			w.Write([]byte(`{"messages":[],"count":0,"next_cursor":null}`))
		})
		after := int64(89681)
		if _, err := client.SessionMessages(context.Background(), "sess-1", &after, 50); err != nil {
			t.Fatalf("SessionMessages: %v", err)
		}
		q := mustParseQuery(t, gotQuery)
		if got := q.Get("after"); got != "89681" {
			t.Errorf("after = %q, want %q", got, "89681")
		}
		if got := q.Get("limit"); got != "50" {
			t.Errorf("limit = %q, want %q", got, "50")
		}
	})
}

// TestSessionMessagesDecodesTheLiveResponseShape guards the exact field
// mapping confirmed live against production
// (ardihikaru/waxum:0.12.2-uploaded-media) in the phase brief: id,
// message_id, chat_jid, sender_jid, direction, msg_type, body (nullable),
// and next_cursor (nullable) all round-trip correctly.
func TestSessionMessagesDecodesTheLiveResponseShape(t *testing.T) {
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"messages":[{"id":89683,"message_id":"3EB0ABC","session_id":"sess-1",
			"chat_jid":"1234:19@lid","sender_jid":"5678:19@lid","direction":"in","msg_type":"text",
			"body":"sudah","snippet":null,"msg_timestamp":"2026-08-31 07:46:44","push_name":null,
			"media":null}],"count":3,"next_cursor":89681}`))
	})

	page, err := client.SessionMessages(context.Background(), "sess-1", nil, 0)
	if err != nil {
		t.Fatalf("SessionMessages: %v", err)
	}
	if page.Count != 3 {
		t.Errorf("Count = %d, want 3", page.Count)
	}
	if page.NextCursor == nil || *page.NextCursor != 89681 {
		t.Fatalf("NextCursor = %v, want pointer to 89681", page.NextCursor)
	}
	if len(page.Messages) != 1 {
		t.Fatalf("len(Messages) = %d, want 1", len(page.Messages))
	}
	m := page.Messages[0]
	if m.ID != 89683 {
		t.Errorf("ID = %d, want 89683", m.ID)
	}
	if m.MessageID != "3EB0ABC" {
		t.Errorf("MessageID = %q, want %q", m.MessageID, "3EB0ABC")
	}
	if m.ChatJID != "1234:19@lid" {
		t.Errorf("ChatJID = %q, want %q", m.ChatJID, "1234:19@lid")
	}
	if m.SenderJID != "5678:19@lid" {
		t.Errorf("SenderJID = %q, want %q", m.SenderJID, "5678:19@lid")
	}
	if m.Direction != "in" {
		t.Errorf("Direction = %q, want %q", m.Direction, "in")
	}
	if m.Body == nil || *m.Body != "sudah" {
		t.Fatalf("Body = %v, want pointer to %q", m.Body, "sudah")
	}
}

// TestSessionMessagesDecodesANullBodyAsNilRatherThanAnEmptyString guards
// the sticker/media-without-caption case the brief calls out: "body" is
// nullable, and a null must decode to a nil *string, distinguishable from
// an explicit "" body, not silently collapsed into one.
func TestSessionMessagesDecodesANullBodyAsNilRatherThanAnEmptyString(t *testing.T) {
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"messages":[{"id":1,"message_id":"m1","session_id":"sess-1",
			"chat_jid":"1@lid","sender_jid":"2@lid","direction":"in","msg_type":"sticker",
			"body":null,"msg_timestamp":"2026-08-31 07:46:44"}],"count":1,"next_cursor":null}`))
	})

	page, err := client.SessionMessages(context.Background(), "sess-1", nil, 0)
	if err != nil {
		t.Fatalf("SessionMessages: %v", err)
	}
	if len(page.Messages) != 1 {
		t.Fatalf("len(Messages) = %d, want 1", len(page.Messages))
	}
	if page.Messages[0].Body != nil {
		t.Errorf("Body = %q, want nil for a null body", *page.Messages[0].Body)
	}
}

// TestParseMessageTimestampInterpretsTheValueAsUTCRegardlessOfHostTZ is the
// direct regression test for this file's central trap: msg_timestamp
// carries no zone marker at all, and the value IS UTC — parsing it with
// the host's local zone (a real, easy mistake — time.ParseInLocation's
// second argument is exactly the kind of thing that is easy to get wrong
// silently, since it never errors) would shift every ReceivedAt by
// whatever the host's offset happens to be. This test pins a fixed input
// to a fixed instant, under two different TZ values, and requires BOTH to
// agree — proving the parse does not depend on the host's local zone.
func TestParseMessageTimestampInterpretsTheValueAsUTCRegardlessOfHostTZ(t *testing.T) {
	want := time.Date(2026, 8, 31, 7, 46, 44, 0, time.UTC)

	for _, tz := range []string{"UTC", "America/New_York", "Asia/Jakarta"} {
		t.Run(tz, func(t *testing.T) {
			loc, err := time.LoadLocation(tz)
			if err != nil {
				t.Skipf("tzdata for %q not available in this environment: %v", tz, err)
			}
			t.Setenv("TZ", tz)
			time.Local = loc

			got, err := parseMessageTimestamp("2026-08-31 07:46:44")
			if err != nil {
				t.Fatalf("parseMessageTimestamp: %v", err)
			}
			if !got.Equal(want) {
				t.Errorf("parseMessageTimestamp = %v, want %v (TZ=%s)", got, want, tz)
			}
			if got.Location() != time.UTC {
				t.Errorf("parseMessageTimestamp location = %v, want time.UTC (TZ=%s)", got.Location(), tz)
			}
		})
	}
}

// TestSessionMessagesReturns503AsErrClientNotConnected guards the specific
// status the brief documents: waxum answers 503 (not 404, not a generic
// error) when the session exists but is not connected, because the live
// chat store this endpoint reads needs a running connection.
func TestSessionMessagesReturns503AsErrClientNotConnected(t *testing.T) {
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"success":false,"error":{"code":503,"message":"Client not connected"}}`))
	})

	_, err := client.SessionMessages(context.Background(), "sess-1", nil, 0)
	if !errors.Is(err, ErrClientNotConnected) {
		t.Errorf("err = %v, want ErrClientNotConnected", err)
	}
}

// TestResolveLIDHitsTheDocumentedMethodAndPathAndEscapesTheJID guards both
// the same method/path class of bug as the other Test*HitsTheDocumented*
// tests in this package, AND that a JID containing "@" and ":" characters
// is correctly path-escaped rather than breaking the URL.
func TestResolveLIDHitsTheDocumentedMethodAndPathAndEscapesTheJID(t *testing.T) {
	var gotMethod, gotPath string
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Write([]byte(`{"lid":"1234@lid","phone_number":"559999999999","learning_source":"contact_sync"}`))
	})

	if _, err := client.ResolveLID(context.Background(), "sess-1", "1234:19@lid"); err != nil {
		t.Fatalf("ResolveLID: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	wantPath := "/api/v1/sessions/sess-1/contacts/1234:19@lid/lid"
	if gotPath != wantPath {
		t.Errorf("path (decoded) = %q, want %q", gotPath, wantPath)
	}
}

// TestResolveLIDDecodesThePhoneNumberField guards the field this whole
// method exists for: internal/wa's inbound pump reads PhoneNumber alone to
// resolve a @lid sender to bare MSISDN digits (see this package's own
// doc comment on the LID trap).
func TestResolveLIDDecodesThePhoneNumberField(t *testing.T) {
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"lid":"1234@lid","phone_number":"559999999999","learning_source":"contact_sync"}`))
	})

	got, err := client.ResolveLID(context.Background(), "sess-1", "1234@lid")
	if err != nil {
		t.Fatalf("ResolveLID: %v", err)
	}
	if got.PhoneNumber != "559999999999" {
		t.Errorf("PhoneNumber = %q, want %q", got.PhoneNumber, "559999999999")
	}
}

// TestResolveLIDReturns404AsErrNotFoundForAnUnmappableContact guards the
// unresolvable-LID case internal/wa/inbound.go must handle by storing the
// row anyway (FromLID set, FromPhone empty) rather than dropping it.
func TestResolveLIDReturns404AsErrNotFoundForAnUnmappableContact(t *testing.T) {
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"success":false,"error":{"code":404,"message":"no mapping"}}`))
	})

	_, err := client.ResolveLID(context.Background(), "sess-1", "9999@lid")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
