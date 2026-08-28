package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// TestTheTextSendDecoderReadsAllowMasterFromTheBody asserts decodeSendRequest
// (api_messages.go) carries allow_master through from the JSON body — true
// when set, false when omitted. Red without the AllowMaster field/decode.
func TestTheTextSendDecoderReadsAllowMasterFromTheBody(t *testing.T) {
	t.Run("true", func(t *testing.T) {
		body := `{"to":"+62 812-0000-0000","text":"hi","allow_master":true}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/messages", strings.NewReader(body))
		rec := httptest.NewRecorder()
		got, ok := decodeSendRequest(rec, req)
		if !ok {
			t.Fatalf("decodeSendRequest rejected a valid body: %s", rec.Body.String())
		}
		if !got.AllowMaster {
			t.Error("AllowMaster = false, want true")
		}
	})
	t.Run("default false", func(t *testing.T) {
		body := `{"to":"+62 812-0000-0000","text":"hi"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/messages", strings.NewReader(body))
		rec := httptest.NewRecorder()
		got, ok := decodeSendRequest(rec, req)
		if !ok {
			t.Fatalf("decodeSendRequest rejected a valid body: %s", rec.Body.String())
		}
		if got.AllowMaster {
			t.Error("AllowMaster = true, want false when the field is omitted")
		}
	})
}

// TestTheGroupSendDecoderReadsAllowMasterFromTheBody is the group-send
// equivalent of the text-send decoder test above, against
// decodeGroupSendRequest (api_messages_group.go).
func TestTheGroupSendDecoderReadsAllowMasterFromTheBody(t *testing.T) {
	t.Run("true", func(t *testing.T) {
		body := `{"group_jid":"120363021234567890@g.us","text":"hi","allow_master":true}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/messages/group", strings.NewReader(body))
		rec := httptest.NewRecorder()
		got, _, ok := decodeGroupSendRequest(rec, req)
		if !ok {
			t.Fatalf("decodeGroupSendRequest rejected a valid body: %s", rec.Body.String())
		}
		if !got.AllowMaster {
			t.Error("AllowMaster = false, want true")
		}
	})
	t.Run("default false", func(t *testing.T) {
		body := `{"group_jid":"120363021234567890@g.us","text":"hi"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/messages/group", strings.NewReader(body))
		rec := httptest.NewRecorder()
		got, _, ok := decodeGroupSendRequest(rec, req)
		if !ok {
			t.Fatalf("decodeGroupSendRequest rejected a valid body: %s", rec.Body.String())
		}
		if got.AllowMaster {
			t.Error("AllowMaster = true, want false when the field is omitted")
		}
	})
}

// TestTheContactsCheckDecoderReadsAllowMasterFromTheBody is the contacts/check
// equivalent, against decodeContactsCheckRequest (api_contacts.go).
func TestTheContactsCheckDecoderReadsAllowMasterFromTheBody(t *testing.T) {
	t.Run("true", func(t *testing.T) {
		body := `{"phones":["+62 812-0000-0000"],"allow_master":true}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/contacts/check", strings.NewReader(body))
		rec := httptest.NewRecorder()
		got, ok := decodeContactsCheckRequest(rec, req)
		if !ok {
			t.Fatalf("decodeContactsCheckRequest rejected a valid body: %s", rec.Body.String())
		}
		if !got.AllowMaster {
			t.Error("AllowMaster = false, want true")
		}
	})
	t.Run("default false", func(t *testing.T) {
		body := `{"phones":["+62 812-0000-0000"]}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/contacts/check", strings.NewReader(body))
		rec := httptest.NewRecorder()
		got, ok := decodeContactsCheckRequest(rec, req)
		if !ok {
			t.Fatalf("decodeContactsCheckRequest rejected a valid body: %s", rec.Body.String())
		}
		if got.AllowMaster {
			t.Error("AllowMaster = true, want false when the field is omitted")
		}
	})
}

// TestTheMediaSendDecoderReadsAllowMasterFromTheMultipartForm drives
// (*handlers).parseMediaSendRequest directly, mirroring
// newContactsCheckTestHandlers' (contacts_check_test.go) house pattern of
// building a bare &handlers{Deps: Deps{...}} rather than a full server —
// this is the multipart FORM-FIELD equivalent of the JSON-body tests above
// (see mediaSendRequest's doc comment on why it is a form field, not JSON).
func TestTheMediaSendDecoderReadsAllowMasterFromTheMultipartForm(t *testing.T) {
	h := &handlers{Deps: Deps{MediaMaxBytes: 4 << 20}}

	newRequest := func(t *testing.T, fields map[string]string) *http.Request {
		t.Helper()
		body, ct := buildMultipartMedia(t, fields, pngMagic)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/messages/media", bytes.NewReader(body.Bytes()))
		req.Header.Set("Content-Type", ct)
		return req
	}

	t.Run("true", func(t *testing.T) {
		fields := map[string]string{}
		for k, v := range baseImageFields {
			fields[k] = v
		}
		fields["allow_master"] = "true"
		req := newRequest(t, fields)
		rec := httptest.NewRecorder()
		got, derr := h.parseMediaSendRequest(rec, req)
		if derr != nil {
			t.Fatalf("parseMediaSendRequest rejected a valid form: %d %s", derr.status, derr.msg)
		}
		if !got.AllowMaster {
			t.Error("AllowMaster = false, want true")
		}
	})
	t.Run("default false", func(t *testing.T) {
		req := newRequest(t, baseImageFields)
		rec := httptest.NewRecorder()
		got, derr := h.parseMediaSendRequest(rec, req)
		if derr != nil {
			t.Fatalf("parseMediaSendRequest rejected a valid form: %d %s", derr.status, derr.msg)
		}
		if got.AllowMaster {
			t.Error("AllowMaster = true, want false when the field is omitted")
		}
	})
}

// TestTheNoRouteMessageNamesMasterExclusionOnlyWhenAMasterIsConnected is a
// table over []wa.Snapshot fixtures against the pure helper noAutoRouteMessage
// (api.go): no sessions -> plain text; a connected non-master -> plain; a
// connected master -> hint; a connected master with allowMaster=true ->
// plain (the caller already opted in, so nothing was actually excluded).
func TestTheNoRouteMessageNamesMasterExclusionOnlyWhenAMasterIsConnected(t *testing.T) {
	cases := []struct {
		name        string
		snaps       []wa.Snapshot
		allowMaster bool
		wantHint    bool
	}{
		{"no sessions at all", nil, false, false},
		{"one connected non-master", []wa.Snapshot{{ID: "a", Connected: true, IsMaster: false}}, false, false},
		{"one connected master", []wa.Snapshot{{ID: "a", Connected: true, IsMaster: true}}, false, true},
		{"connected master but allowMaster already true", []wa.Snapshot{{ID: "a", Connected: true, IsMaster: true}}, true, false},
		{"disconnected master must not trigger the hint", []wa.Snapshot{{ID: "a", Connected: false, IsMaster: true}}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := noAutoRouteMessage(tc.snaps, tc.allowMaster)
			if !strings.HasPrefix(got, noRouteMessage) {
				t.Fatalf("noAutoRouteMessage() = %q, want it to start with the base message %q", got, noRouteMessage)
			}
			hasHint := got != noRouteMessage
			if hasHint != tc.wantHint {
				t.Errorf("noAutoRouteMessage() = %q, want hint=%v", got, tc.wantHint)
			}
		})
	}
}

// TestPutAPISessionMasterEchoesTheNewFlag exercises PUT
// /api/v1/sessions/{id}/master end to end through a real SQLite-backed
// wa.Manager (newMediaTestServer, media_calls_test_backend_test.go), bearer
// auth minted the same way apiToken/api_group_test.go does for every other
// bearer test in this package. Asserts 200 + is_master:true, a flip back to
// false, and 404 for an unknown id.
func TestPutAPISessionMasterEchoesTheNewFlag(t *testing.T) {
	backend := whatsmeowShapedBackend()
	srv, s, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend})
	token := apiToken(t, srv, apiClients, "master-flag")

	putMaster := func(id string, isMaster bool) *httptest.ResponseRecorder {
		body := []byte(`{"is_master":` + boolLiteral(isMaster) + `}`)
		req := httptest.NewRequest(http.MethodPut, "/api/v1/sessions/"+id+"/master", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}

	rec := putMaster(s.ID, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT .../master {true} = %d: %s", rec.Code, rec.Body.String())
	}
	var got apiSession
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("could not decode response: %v; body=%s", err, rec.Body.String())
	}
	if !got.IsMaster {
		t.Errorf("is_master = false after setting it true; body=%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"is_master":true`) {
		t.Errorf("body does not contain the literal is_master:true: %s", rec.Body.String())
	}

	rec = putMaster(s.ID, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT .../master {false} = %d: %s", rec.Code, rec.Body.String())
	}
	got = apiSession{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("could not decode response: %v; body=%s", err, rec.Body.String())
	}
	if got.IsMaster {
		t.Errorf("is_master = true after flipping it back to false; body=%s", rec.Body.String())
	}

	rec = putMaster("no-such-session", true)
	if rec.Code != http.StatusNotFound {
		t.Errorf("PUT .../master on an unknown id = %d, want 404", rec.Code)
	}
}

// TestCreatingASessionWithIsMasterFlagsIt is the json-tag gotcha test: an
// untagged IsMaster field would bind "ismaster" case-insensitively but NOT
// the documented snake_case "is_master" key, so this proves the wire format
// that actually works.
func TestCreatingASessionWithIsMasterFlagsIt(t *testing.T) {
	backend := whatsmeowShapedBackend()
	srv, _, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend})
	token := apiToken(t, srv, apiClients, "master-create")

	body := []byte(`{"name":"master gw","phone":"+62 812-1111-2222","is_master":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/sessions = %d: %s", rec.Code, rec.Body.String())
	}
	var got apiSession
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("could not decode response: %v; body=%s", err, rec.Body.String())
	}
	if !got.IsMaster {
		t.Errorf("is_master = false, want true; the snake_case key must bind. body=%s", rec.Body.String())
	}
}

func boolLiteral(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
