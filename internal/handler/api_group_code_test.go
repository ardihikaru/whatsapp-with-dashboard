package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// A note on what this file CANNOT test, and why (read this before any test
// below that looks like it should assert an actual successful send):
//
// Several of layer 2's tests are specified as "one connected session ...
// success". package handler has NO way to make a real *wa.ManagedSession
// report Connected: true — Snapshot gates Connected on `state ==
// StateConnected`, and EVERY assignment to that field lives in
// backend_meow.go/backend_waxum.go (package wa's own backend
// implementations), reached only through their own unexported setState —
// never through Activate, StartPairing, or any other exported entry point a
// Backend implemented in a DIFFERENT package (like this file's
// fakeGatewayBackend) could drive. This is not a gap layer 2 introduced —
// send.go's own dispatchEnqueueOutcome doc comment already says so in as
// many words: "package handler has no way to fabricate a connected,
// sendable session". Confirmed empirically while writing this file: a
// session loaded via a real wasession.Repository row (JID set, Active:
// true, LastStatus: "connected") through Manager.StartAll, against a
// backend whose Live() unconditionally returns true, still reports
// State: "disconnected" after Activate's own Backend.Connect call returns
// nil — because Activate itself never assigns state either.
//
// So every test below that needs "a session the automatic router would
// pick" instead proves the same wiring (decode -> wa.Route -> selectForSend
// -> noSendRouteMessage, or the routing-mode label) through the one
// observable channel that IS reachable without connectivity: which 409
// BODY a real HTTP round trip produces. That is still real regression
// protection — see each test's own comment for exactly what bug it would
// catch — but it is not proof of an actual delivered message, and this
// file says so instead of pretending otherwise.

// TestEverySendDecoderReadsGroupCodeFromItsOwnWireFormat drives all four
// send/check decoders directly (the media one against a real multipart body,
// like TestTheMediaSendDecoderReadsAllowMasterFromTheMultipartForm does):
// " Alpha " normalizes to "alpha", an omitted field leaves "", and an
// invalid value 400s naming group_code. Red without NormalizeGroupCode
// wired into each decoder.
func TestEverySendDecoderReadsGroupCodeFromItsOwnWireFormat(t *testing.T) {
	t.Run("text (decodeSendRequest)", func(t *testing.T) {
		newReq := func(body string) *http.Request {
			return httptest.NewRequest(http.MethodPost, "/api/v1/messages", strings.NewReader(body))
		}
		t.Run("trims and lowercases", func(t *testing.T) {
			rec := httptest.NewRecorder()
			got, ok := decodeSendRequest(rec, newReq(`{"to":"+62 812-0000-0000","text":"hi","group_code":" Alpha "}`))
			if !ok {
				t.Fatalf("rejected a valid body: %s", rec.Body.String())
			}
			if got.Route.GroupCode != "alpha" {
				t.Errorf("GroupCode = %q, want %q", got.Route.GroupCode, "alpha")
			}
		})
		t.Run("omitted stays empty", func(t *testing.T) {
			rec := httptest.NewRecorder()
			got, ok := decodeSendRequest(rec, newReq(`{"to":"+62 812-0000-0000","text":"hi"}`))
			if !ok {
				t.Fatalf("rejected a valid body: %s", rec.Body.String())
			}
			if got.Route.GroupCode != "" {
				t.Errorf("GroupCode = %q, want \"\"", got.Route.GroupCode)
			}
		})
		t.Run("invalid 400s naming group_code", func(t *testing.T) {
			rec := httptest.NewRecorder()
			_, ok := decodeSendRequest(rec, newReq(`{"to":"+62 812-0000-0000","text":"hi","group_code":"Bad Code!"}`))
			if ok {
				t.Fatal("decodeSendRequest accepted an invalid group_code")
			}
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "group_code") {
				t.Errorf("body does not name group_code: %s", rec.Body.String())
			}
		})
	})

	t.Run("group (decodeGroupSendRequest)", func(t *testing.T) {
		newReq := func(body string) *http.Request {
			return httptest.NewRequest(http.MethodPost, "/api/v1/messages/group", strings.NewReader(body))
		}
		t.Run("trims and lowercases", func(t *testing.T) {
			rec := httptest.NewRecorder()
			got, _, ok := decodeGroupSendRequest(rec, newReq(`{"group_jid":"120363021234567890@g.us","text":"hi","group_code":" Alpha "}`))
			if !ok {
				t.Fatalf("rejected a valid body: %s", rec.Body.String())
			}
			if got.Route.GroupCode != "alpha" {
				t.Errorf("GroupCode = %q, want %q", got.Route.GroupCode, "alpha")
			}
		})
		t.Run("omitted stays empty", func(t *testing.T) {
			rec := httptest.NewRecorder()
			got, _, ok := decodeGroupSendRequest(rec, newReq(`{"group_jid":"120363021234567890@g.us","text":"hi"}`))
			if !ok {
				t.Fatalf("rejected a valid body: %s", rec.Body.String())
			}
			if got.Route.GroupCode != "" {
				t.Errorf("GroupCode = %q, want \"\"", got.Route.GroupCode)
			}
		})
		t.Run("invalid 400s naming group_code", func(t *testing.T) {
			rec := httptest.NewRecorder()
			_, _, ok := decodeGroupSendRequest(rec, newReq(`{"group_jid":"120363021234567890@g.us","text":"hi","group_code":"Bad Code!"}`))
			if ok {
				t.Fatal("decodeGroupSendRequest accepted an invalid group_code")
			}
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "group_code") {
				t.Errorf("body does not name group_code: %s", rec.Body.String())
			}
		})
	})

	t.Run("contacts (decodeContactsCheckRequest)", func(t *testing.T) {
		newReq := func(body string) *http.Request {
			return httptest.NewRequest(http.MethodPost, "/api/v1/contacts/check", strings.NewReader(body))
		}
		t.Run("trims and lowercases", func(t *testing.T) {
			rec := httptest.NewRecorder()
			got, ok := decodeContactsCheckRequest(rec, newReq(`{"phones":["+6281200000000"],"group_code":" Alpha "}`))
			if !ok {
				t.Fatalf("rejected a valid body: %s", rec.Body.String())
			}
			if got.Route.GroupCode != "alpha" {
				t.Errorf("GroupCode = %q, want %q", got.Route.GroupCode, "alpha")
			}
		})
		t.Run("omitted stays empty", func(t *testing.T) {
			rec := httptest.NewRecorder()
			got, ok := decodeContactsCheckRequest(rec, newReq(`{"phones":["+6281200000000"]}`))
			if !ok {
				t.Fatalf("rejected a valid body: %s", rec.Body.String())
			}
			if got.Route.GroupCode != "" {
				t.Errorf("GroupCode = %q, want \"\"", got.Route.GroupCode)
			}
		})
		t.Run("invalid 400s naming group_code", func(t *testing.T) {
			rec := httptest.NewRecorder()
			_, ok := decodeContactsCheckRequest(rec, newReq(`{"phones":["+6281200000000"],"group_code":"Bad Code!"}`))
			if ok {
				t.Fatal("decodeContactsCheckRequest accepted an invalid group_code")
			}
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "group_code") {
				t.Errorf("body does not name group_code: %s", rec.Body.String())
			}
		})
	})

	t.Run("media (parseMediaSendRequest)", func(t *testing.T) {
		h := &handlers{Deps: Deps{MediaMaxBytes: 4 << 20}}
		newRequest := func(t *testing.T, fields map[string]string) *http.Request {
			t.Helper()
			body, ct := buildMultipartMedia(t, fields, pngMagic)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/messages/media", bytes.NewReader(body.Bytes()))
			req.Header.Set("Content-Type", ct)
			return req
		}
		withField := func(k, v string) map[string]string {
			fields := map[string]string{}
			for fk, fv := range baseImageFields {
				fields[fk] = fv
			}
			fields[k] = v
			return fields
		}
		t.Run("trims and lowercases", func(t *testing.T) {
			req := newRequest(t, withField("group_code", " Alpha "))
			rec := httptest.NewRecorder()
			got, derr := h.parseMediaSendRequest(rec, req)
			if derr != nil {
				t.Fatalf("rejected a valid form: %d %s", derr.status, derr.msg)
			}
			if got.Route.GroupCode != "alpha" {
				t.Errorf("GroupCode = %q, want %q", got.Route.GroupCode, "alpha")
			}
		})
		t.Run("omitted stays empty", func(t *testing.T) {
			req := newRequest(t, baseImageFields)
			rec := httptest.NewRecorder()
			got, derr := h.parseMediaSendRequest(rec, req)
			if derr != nil {
				t.Fatalf("rejected a valid form: %d %s", derr.status, derr.msg)
			}
			if got.Route.GroupCode != "" {
				t.Errorf("GroupCode = %q, want \"\"", got.Route.GroupCode)
			}
		})
		t.Run("invalid 400s naming group_code", func(t *testing.T) {
			req := newRequest(t, withField("group_code", "Bad Code!"))
			rec := httptest.NewRecorder()
			_, derr := h.parseMediaSendRequest(rec, req)
			if derr == nil {
				t.Fatal("parseMediaSendRequest accepted an invalid group_code")
			}
			if derr.status != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", derr.status)
			}
			if !strings.Contains(derr.msg, "group_code") {
				t.Errorf("message does not name group_code: %s", derr.msg)
			}
		})
	})
}

// TestAnInvalidGroupCodeIsRejectedByEveryAutoRoutingEndpoint tables all four
// auto-routing paths against one bad value ("Bad Code!") and asserts 400 —
// three different decoding mechanisms (JSON body twice over, a JSON body
// with a different shape, and a multipart form field) are involved, which
// is exactly the asymmetry a per-endpoint fix could miss on one of the four.
func TestAnInvalidGroupCodeIsRejectedByEveryAutoRoutingEndpoint(t *testing.T) {
	backend := &fakeGatewayBackend{name: "fake"}
	srv, _, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend})
	token := apiToken(t, srv, apiClients, "group-code-400-table")

	imgFields := map[string]string{}
	for k, v := range baseImageFields {
		imgFields[k] = v
	}
	imgFields["group_code"] = "Bad Code!"
	imgBody, imgCT := buildMultipartMedia(t, imgFields, pngMagic)

	cases := []struct {
		name        string
		method      string
		path        string
		contentType string
		body        func() *bytes.Reader
	}{
		{"text auto", http.MethodPost, "/api/v1/messages", "application/json", func() *bytes.Reader {
			return bytes.NewReader([]byte(`{"to":"+62 812-0000-0000","text":"hi","group_code":"Bad Code!"}`))
		}},
		{"group auto", http.MethodPost, "/api/v1/messages/group", "application/json", func() *bytes.Reader {
			return bytes.NewReader([]byte(`{"group_jid":"120363021234567890@g.us","text":"hi","group_code":"Bad Code!"}`))
		}},
		{"media auto", http.MethodPost, "/api/v1/messages/media", imgCT, func() *bytes.Reader {
			return bytes.NewReader(imgBody.Bytes())
		}},
		{"contacts auto", http.MethodPost, "/api/v1/contacts/check", "application/json", func() *bytes.Reader {
			return bytes.NewReader([]byte(`{"phones":["+6281200000000"],"group_code":"Bad Code!"}`))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, tc.body())
			req.Header.Set("Content-Type", tc.contentType)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s %s = %d, want 400: %s", tc.method, tc.path, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "group_code") {
				t.Errorf("%s %s body does not name group_code: %s", tc.method, tc.path, rec.Body.String())
			}
		})
	}
}

// TestAGroupScopedSendDoesNotFallBackToTheUngroupedPool proves group_code
// reaches the real routing decision through a genuine HTTP round trip, not
// just the pure noSendRouteMessage table below: a request naming a group
// gets the GROUP-scoped 409 (naming the group), and the identical request
// with group_code omitted gets the byte-identical PLAIN 409 this endpoint
// has always returned. If decodeSendRequest ever stopped reading group_code
// (or silently defaulted it to ""), the first case would collapse into the
// second and this test would fail — see this file's own top-of-file comment
// for why "a genuinely connected session was excluded" cannot be observed
// any more directly than this from package handler.
func TestAGroupScopedSendDoesNotFallBackToTheUngroupedPool(t *testing.T) {
	backend := &fakeGatewayBackend{name: "fake"}
	srv, _, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend})
	token := apiToken(t, srv, apiClients, "group-no-fallback")

	grouped := postJSON(srv, token, http.MethodPost, "/api/v1/messages",
		`{"to":"+62 812-0000-0000","text":"hi","group_code":"alpha"}`)
	if grouped.Code != http.StatusConflict {
		t.Fatalf("grouped send = %d, want 409: %s", grouped.Code, grouped.Body.String())
	}
	var groupedBody struct{ Error string }
	if err := json.Unmarshal(grouped.Body.Bytes(), &groupedBody); err != nil {
		t.Fatalf("could not decode body: %v; body=%s", err, grouped.Body.String())
	}
	if !strings.Contains(groupedBody.Error, "alpha") {
		t.Errorf("grouped 409 does not name the group: %q", groupedBody.Error)
	}
	if groupedBody.Error == noRouteMessage || strings.HasPrefix(groupedBody.Error, noRouteMessage) {
		t.Errorf("grouped 409 must not equal/be prefixed by the plain message: %q", groupedBody.Error)
	}

	plain := postJSON(srv, token, http.MethodPost, "/api/v1/messages",
		`{"to":"+62 812-0000-0000","text":"hi"}`)
	if plain.Code != http.StatusConflict {
		t.Fatalf("plain send = %d, want 409: %s", plain.Code, plain.Body.String())
	}
	var plainBody struct{ Error string }
	if err := json.Unmarshal(plain.Body.Bytes(), &plainBody); err != nil {
		t.Fatalf("could not decode body: %v; body=%s", err, plain.Body.String())
	}
	if plainBody.Error != noRouteMessage {
		t.Errorf("plain 409 = %q, want the unchanged %q", plainBody.Error, noRouteMessage)
	}
}

// TestAnUngroupedSendNeverPicksAGroupCodedSession is the behaviour change
// existing callers DO see: a fleet whose only session carries a group_code
// must not leak into the plain (ungrouped) 409 wording. Connectivity is not
// what this test can observe (see the file-level comment) — what it DOES
// pin is that a plain request's message stays exactly noRouteMessage
// (never noGroupRouteMessage's wording, which WOULD mention the group) even
// though the fleet's only session is scoped to "alpha". A regression where
// the ungrouped path started consulting group state would flip this.
func TestAnUngroupedSendNeverPicksAGroupCodedSession(t *testing.T) {
	backend := &fakeGatewayBackend{name: "fake"}
	srv, s, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend})
	token := apiToken(t, srv, apiClients, "ungrouped-never-picks-grouped")

	if err := s.SetGroupCode(context.Background(), "alpha"); err != nil {
		t.Fatalf("SetGroupCode: %v", err)
	}

	rec := postJSON(srv, token, http.MethodPost, "/api/v1/messages", `{"to":"+62 812-0000-0000","text":"hi"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	var body struct{ Error string }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("could not decode body: %v; body=%s", err, rec.Body.String())
	}
	if body.Error != noRouteMessage {
		t.Errorf("409 = %q, want the unchanged plain %q (must not mention the group)", body.Error, noRouteMessage)
	}
	if strings.Contains(body.Error, "alpha") {
		t.Errorf("plain 409 leaked the group name: %q", body.Error)
	}
}

// TestMediaAndContactsAutoRoutingStillIgnoreMasterOnly pins §3's hardcoded
// false: PostAPISendMediaAuto and PostAPIContactsCheckAuto must not gain
// master_only — that was not asked for in this change and their wire
// contract stays unchanged. Two levels of evidence: the decoders never set
// Route.MasterOnly from an extra master_only key/field, and — reachable
// without any connected session at all — a real HTTP round trip carrying
// master_only:true still produces the PLAIN 409 (noRouteMessage), not the
// master-specific one (noMasterRouteMessage) it would produce if
// master_only had been wired in by accident (see selectForSend/
// noSendRouteMessage: rt.MasterOnly alone picks noMasterRouteMessage
// regardless of what is actually connected).
func TestMediaAndContactsAutoRoutingStillIgnoreMasterOnly(t *testing.T) {
	t.Run("contacts decoder", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/contacts/check",
			strings.NewReader(`{"phones":["+6281200000000"],"master_only":true}`))
		rec := httptest.NewRecorder()
		got, ok := decodeContactsCheckRequest(rec, req)
		if !ok {
			t.Fatalf("rejected a valid body: %s", rec.Body.String())
		}
		if got.Route.MasterOnly {
			t.Error("Route.MasterOnly = true, want false — this endpoint must not read master_only")
		}
	})
	t.Run("media decoder", func(t *testing.T) {
		h := &handlers{Deps: Deps{MediaMaxBytes: 4 << 20}}
		fields := map[string]string{}
		for k, v := range baseImageFields {
			fields[k] = v
		}
		fields["master_only"] = "true"
		body, ct := buildMultipartMedia(t, fields, pngMagic)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/messages/media", bytes.NewReader(body.Bytes()))
		req.Header.Set("Content-Type", ct)
		rec := httptest.NewRecorder()
		got, derr := h.parseMediaSendRequest(rec, req)
		if derr != nil {
			t.Fatalf("rejected a valid form: %d %s", derr.status, derr.msg)
		}
		if got.Route.MasterOnly {
			t.Error("Route.MasterOnly = true, want false — this endpoint must not read master_only")
		}
	})

	backend := &fakeGatewayBackend{name: "fake"}
	srv, _, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend})
	token := apiToken(t, srv, apiClients, "media-contacts-ignore-master-only")

	t.Run("contacts HTTP round trip", func(t *testing.T) {
		rec := postJSON(srv, token, http.MethodPost, "/api/v1/contacts/check",
			`{"phones":["+6281200000000"],"master_only":true}`)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
		}
		var body struct{ Error string }
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("could not decode body: %v; body=%s", err, rec.Body.String())
		}
		if body.Error != noRouteMessage {
			t.Errorf("409 = %q, want the plain %q (master_only must be ignored)", body.Error, noRouteMessage)
		}
	})

	t.Run("media HTTP round trip", func(t *testing.T) {
		fields := map[string]string{}
		for k, v := range baseImageFields {
			fields[k] = v
		}
		fields["master_only"] = "true"
		body, ct := buildMultipartMedia(t, fields, pngMagic)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/messages/media", bytes.NewReader(body.Bytes()))
		req.Header.Set("Content-Type", ct)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
		}
		var respBody struct{ Error string }
		if err := json.Unmarshal(rec.Body.Bytes(), &respBody); err != nil {
			t.Fatalf("could not decode body: %v; body=%s", err, rec.Body.String())
		}
		if respBody.Error != noRouteMessage {
			t.Errorf("409 = %q, want the plain %q (master_only must be ignored)", respBody.Error, noRouteMessage)
		}
	})
}

// TestNoSendRouteMessageNamesTheGroupAndNotAGatewayOutage tables
// noGroupRouteMessage's five rules (api.go), one row per rule, asserting:
// the group name appears, the right hint is chosen, and — critically — that
// none of the five messages equals or is prefixed by the plain noRouteMessage
// text (a group-scoped 409 reads as "this group has no candidate", never as
// "the gateway is down"). Also pins that the ungrouped wordings this
// function delegates to are still byte-identical to today's constants.
func TestNoSendRouteMessageNamesTheGroupAndNotAGatewayOutage(t *testing.T) {
	group := "alpha"

	cases := []struct {
		name     string
		snaps    []wa.Snapshot
		rt       wa.Route
		wantHint string
	}{
		{
			name:     "rule 1: nothing carries the group_code at all",
			snaps:    []wa.Snapshot{{ID: "u", Connected: true}},
			rt:       wa.Route{GroupCode: group},
			wantHint: "assigned",
		},
		{
			name: "rule 2: master_only, group has a connected non-master",
			snaps: []wa.Snapshot{
				{ID: "g1", Connected: true, GroupCode: group, IsMaster: false},
			},
			rt:       wa.Route{GroupCode: group, MasterOnly: true},
			wantHint: "none is flagged master",
		},
		{
			name: "rule 3: not master_only/allow_master, group has a connected master",
			snaps: []wa.Snapshot{
				{ID: "g1", Connected: true, GroupCode: group, IsMaster: true},
			},
			rt:       wa.Route{GroupCode: group},
			wantHint: "allow_master",
		},
		{
			name: "rule 4: something outside the group is connected",
			snaps: []wa.Snapshot{
				{ID: "g1", Connected: false, GroupCode: group},
				{ID: "outside", Connected: true},
			},
			rt:       wa.Route{GroupCode: group},
			wantHint: "not down",
		},
		{
			name: "rule 5: genuinely dark — group present but nothing connected anywhere",
			snaps: []wa.Snapshot{
				{ID: "g1", Connected: false, GroupCode: group},
			},
			rt:       wa.Route{GroupCode: group},
			wantHint: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := noSendRouteMessage(tc.snaps, tc.rt)
			if !strings.Contains(got, group) {
				t.Errorf("message does not name the group %q: %q", group, got)
			}
			if got == noRouteMessage || strings.HasPrefix(got, noRouteMessage) {
				t.Errorf("group message must not equal/be prefixed by the plain message: %q", got)
			}
			if tc.wantHint != "" && !strings.Contains(got, tc.wantHint) {
				t.Errorf("message = %q, want it to contain %q", got, tc.wantHint)
			}
			if tc.wantHint == "" {
				// Rule 5: must be exactly base + escape hatch, no extra hint.
				want := groupRouteMessage(groupRouteBaseMessage(tc.rt), "")
				if got != want {
					t.Errorf("rule 5 message = %q, want exactly %q", got, want)
				}
			}
			if !strings.Contains(got, "omit group_code") {
				t.Errorf("message does not offer the escape hatch: %q", got)
			}
		})
	}

	// The ungrouped paths must still be byte-identical to what they always
	// returned — noSendRouteMessage's whole "no rewrite for GroupCode==''"
	// guarantee.
	ungroupedSnaps := []wa.Snapshot{{ID: "a", Connected: false}}
	if got := noSendRouteMessage(ungroupedSnaps, wa.Route{}); got != noRouteMessage {
		t.Errorf("ungrouped auto message = %q, want unchanged %q", got, noRouteMessage)
	}
	if got := noSendRouteMessage(ungroupedSnaps, wa.Route{MasterOnly: true}); got != noMasterRouteMessage {
		t.Errorf("ungrouped master_only message = %q, want unchanged %q", got, noMasterRouteMessage)
	}
}

// TestRoutingModeForNamesEveryScopeModeCombination is routingModeFor's own
// unit table (api.go) — the JSON "routing" field's whole vocabulary.
func TestRoutingModeForNamesEveryScopeModeCombination(t *testing.T) {
	cases := []struct {
		name string
		rt   wa.Route
		want routingMode
	}{
		{"nothing set", wa.Route{}, routingAuto},
		{"allow_master only", wa.Route{AllowMaster: true}, routingAuto},
		{"master_only", wa.Route{MasterOnly: true}, routingMasterOnly},
		{"group_code only", wa.Route{GroupCode: "alpha"}, routingGroup},
		{"group_code + allow_master", wa.Route{GroupCode: "alpha", AllowMaster: true}, routingGroup},
		{"group_code + master_only", wa.Route{GroupCode: "alpha", MasterOnly: true}, routingGroupMasterOnly},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := routingModeFor(tc.rt); got != tc.want {
				t.Errorf("routingModeFor(%+v) = %q, want %q", tc.rt, got, tc.want)
			}
		})
	}
}

// TestASendResponseReportsTheRoutingModeAndTheChosenSessionsGroup drives
// sendResponseFields (api.go) directly — the pure helper both
// postAPISendThrough (api_messages.go) and sendMedia (api_media.go) merge
// into their success response. Direct rather than through a real HTTP send:
// dispatchSend's own doc comment (send.go) documents that package handler
// cannot fabricate a connected, sendable session, so the success branch of
// either handler is unreachable from a real request in this package's test
// suite (see this file's top-of-file comment) — this is the only place the
// contract "routing is present, session_group_code is present-with-”
// when ungrouped" can actually be exercised red/green. Red if
// sendResponseFields loses a key, or if it is no longer wired into either
// caller (that second half is a structural fact checked by
// TestPostAPISendThroughAndSendMediaBothMergeSendResponseFields below).
func TestASendResponseReportsTheRoutingModeAndTheChosenSessionsGroup(t *testing.T) {
	t.Run("ungrouped auto send reports routing=auto and session_group_code=\"\"", func(t *testing.T) {
		got := sendResponseFields(wa.Snapshot{ID: "s1", GroupCode: "", IsMaster: false}, routingModeFor(wa.Route{}))
		if got["routing"] != string(routingAuto) {
			t.Errorf("routing = %v, want %q", got["routing"], routingAuto)
		}
		if _, present := got["session_group_code"]; !present {
			t.Error(`"session_group_code" key is absent — an absent key is indistinguishable from an older gateway`)
		}
		if got["session_group_code"] != "" {
			t.Errorf("session_group_code = %v, want \"\"", got["session_group_code"])
		}
		if got["session_is_master"] != false {
			t.Errorf("session_is_master = %v, want false", got["session_is_master"])
		}
	})

	t.Run("group_code + master_only send reports routing=group_master_only and the session's own group/master flags", func(t *testing.T) {
		rt := wa.Route{GroupCode: "alpha", MasterOnly: true}
		got := sendResponseFields(wa.Snapshot{ID: "s2", GroupCode: "alpha", IsMaster: true}, routingModeFor(rt))
		if got["routing"] != string(routingGroupMasterOnly) {
			t.Errorf("routing = %v, want %q", got["routing"], routingGroupMasterOnly)
		}
		if got["session_group_code"] != "alpha" {
			t.Errorf("session_group_code = %v, want %q", got["session_group_code"], "alpha")
		}
		if got["session_is_master"] != true {
			t.Errorf("session_is_master = %v, want true", got["session_is_master"])
		}
	})

	t.Run("explicit-session send reports routing=explicit", func(t *testing.T) {
		got := sendResponseFields(wa.Snapshot{ID: "s3"}, routingExplicit)
		if got["routing"] != string(routingExplicit) {
			t.Errorf("routing = %v, want %q", got["routing"], routingExplicit)
		}
	})
}

// TestPostAPISendThroughAndSendMediaBothMergeSendResponseFields is a
// source-level check (go/ast, the same technique api_openapi_test.go's
// routesFromRoutesAPI uses) that both response builders actually call
// sendResponseFields — sendResponseFields alone proves the KEYS are right,
// not that either caller still merges them in. Red if either call site is
// removed while sendResponseFields itself is left untouched, which the unit
// test above cannot catch on its own.
func TestPostAPISendThroughAndSendMediaBothMergeSendResponseFields(t *testing.T) {
	for _, tc := range []struct{ file, fn string }{
		{"api_messages.go", "postAPISendThrough"},
		{"api_media.go", "sendMedia"},
	} {
		t.Run(tc.fn, func(t *testing.T) {
			src, err := os.ReadFile(tc.file)
			if err != nil {
				t.Fatal(err)
			}
			fn := findFuncSource(t, string(src), tc.fn)
			if !strings.Contains(fn, "sendResponseFields(") {
				t.Errorf("%s no longer calls sendResponseFields(...)", tc.fn)
			}
		})
	}
}

// findFuncSource returns the source text of the named top-level function —
// from its "func " line up to (but not including) the next top-level "func "
// or "}\n\n" boundary. Good enough for this file's own functions, which are
// never nested inside another function.
func findFuncSource(t *testing.T, src, name string) string {
	t.Helper()
	marker := "func (h *handlers) " + name + "("
	start := strings.Index(src, marker)
	if start < 0 {
		t.Fatalf("could not find function %s in source", name)
	}
	rest := src[start:]
	end := strings.Index(rest[1:], "\nfunc ")
	if end < 0 {
		return rest
	}
	return rest[:end+1]
}

// TestCreatingASessionWithAGroupCodeScopesIt is api_master_test.go's
// TestCreatingASessionWithIsMasterFlagsIt, adapted for group_code: sets it
// at creation time, clears it via PUT, and 400s on an invalid value.
func TestCreatingASessionWithAGroupCodeScopesIt(t *testing.T) {
	backend := whatsmeowShapedBackend()
	srv, _, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend})
	token := apiToken(t, srv, apiClients, "group-code-create")

	body := []byte(`{"name":"gw2","phone":"+62 812-1111-3333","group_code":" Alpha "}`)
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
	if got.GroupCode != "alpha" {
		t.Errorf("group_code = %q, want %q (trim+lowercase); body=%s", got.GroupCode, "alpha", rec.Body.String())
	}

	t.Run("invalid group_code answers 400, not a silently-created ungrouped session", func(t *testing.T) {
		body := []byte(`{"name":"gw3","phone":"+62 812-1111-4444","group_code":"Bad Code!"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
		}
	})
}

// TestPutAPISessionGroupCodeEchoesTheNewScope is
// TestPutAPISessionMasterEchoesTheNewFlag's group_code twin: set, clear back
// to "", 400 on invalid, 404 on an unknown id.
func TestPutAPISessionGroupCodeEchoesTheNewScope(t *testing.T) {
	backend := whatsmeowShapedBackend()
	srv, s, apiClients, _, _ := newMediaTestServer(t, mediaTestConfig{backend: backend})
	token := apiToken(t, srv, apiClients, "group-code-put")

	putGroupCode := func(id, code string) *httptest.ResponseRecorder {
		body := []byte(`{"group_code":"` + code + `"}`)
		req := httptest.NewRequest(http.MethodPut, "/api/v1/sessions/"+id+"/group-code", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}

	rec := putGroupCode(s.ID, "alpha")
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT .../group-code {alpha} = %d: %s", rec.Code, rec.Body.String())
	}
	var got apiSession
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("could not decode response: %v; body=%s", err, rec.Body.String())
	}
	if got.GroupCode != "alpha" {
		t.Errorf("group_code = %q, want %q; body=%s", got.GroupCode, "alpha", rec.Body.String())
	}

	rec = putGroupCode(s.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT .../group-code {\"\"} = %d: %s", rec.Code, rec.Body.String())
	}
	got = apiSession{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("could not decode response: %v; body=%s", err, rec.Body.String())
	}
	if got.GroupCode != "" {
		t.Errorf("group_code = %q, want \"\" after clearing; body=%s", got.GroupCode, rec.Body.String())
	}

	rec = putGroupCode(s.ID, "Bad Code!")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("PUT .../group-code {invalid} = %d, want 400: %s", rec.Code, rec.Body.String())
	}

	rec = putGroupCode("no-such-session", "alpha")
	if rec.Code != http.StatusNotFound {
		t.Errorf("PUT .../group-code on an unknown id = %d, want 404", rec.Code)
	}
}
