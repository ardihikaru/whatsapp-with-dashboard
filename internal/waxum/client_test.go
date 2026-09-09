package waxum

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

const testToken = "test-token-xyz"

// newFakeServer wires up an httptest server whose handler is exactly what
// the caller passes, plus newTestClient bound to it with testToken.
//
// Both overrides below replace a PRODUCTION-scale schedule with a
// millisecond one: WithClientRateLimit is set far above anything a test
// here could plausibly burst (the limiter's own behaviour has a dedicated
// test, TestClientThrottlesRequestsAcrossASharedBucket), and WithRetryPolicy
// replaces do()'s real 429 backoff (up to ~31s across 5 retries) with a
// 1ms one — TestNotFoundAndConflictAndRateLimitedBecomeTypedErrors'
// "Too_Many_Requests" case took over 30s before this override was added,
// because every OTHER test in this file also goes through newFakeServer
// and must not pay a production-scale retry schedule for request/response
// shapes that have nothing to do with rate limiting.
func newFakeServer(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(srv.URL, testToken, 5*time.Second,
		WithClientRateLimit(1e6, 1e6),
		WithRetryPolicy(2, func(int, string) time.Duration { return time.Millisecond }),
	), srv
}

func decodeJSONBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	return m
}

func TestEveryRequestCarriesTheBearerToken(t *testing.T) {
	var gotAuth string
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	})

	calls := map[string]func() error{
		"CreateSession": func() error { return client.CreateSession(context.Background(), "s1", "name", true) },
		"Connect":       func() error { return client.Connect(context.Background(), "s1") },
		"Disconnect":    func() error { return client.Disconnect(context.Background(), "s1") },
		"Status": func() error {
			_, err := client.Status(context.Background(), "s1")
			return err
		},
		"QR": func() error {
			_, err := client.QR(context.Background(), "s1")
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			gotAuth = ""
			if err := call(); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			want := "Bearer " + testToken
			if gotAuth != want {
				t.Errorf("%s: Authorization header = %q, want %q", name, gotAuth, want)
			}
		})
	}
}

func TestHealthDoesNotCarryTheBearerTokenAndHitsTheRootPath(t *testing.T) {
	var gotAuth, gotPath string
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	})
	if err := client.Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if gotPath != "/health" {
		t.Errorf("Health path = %q, want %q (no /api/v1 prefix)", gotPath, "/health")
	}
	if gotAuth != "" {
		t.Errorf("Health sent Authorization %q, want none (health is unauthenticated)", gotAuth)
	}
}

func TestSendTextPostsToTheSessionScopedTextEndpointWithToAndText(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]any
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		gotBody = decodeJSONBody(t, r)
		json.NewEncoder(w).Encode(MessageResult{MessageID: "msg-1", Timestamp: 123, To: "628123@s.whatsapp.net"})
	})

	res, err := client.SendText(context.Background(), "sess-1", "628123@s.whatsapp.net", "hello there")
	if err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	wantPath := "/api/v1/sessions/sess-1/messages/text"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
	if gotBody["to"] != "628123@s.whatsapp.net" {
		t.Errorf(`body["to"] = %v, want "628123@s.whatsapp.net"`, gotBody["to"])
	}
	if gotBody["text"] != "hello there" {
		t.Errorf(`body["text"] = %v, want "hello there"`, gotBody["text"])
	}
	if len(gotBody) != 2 {
		t.Errorf("body has extra/missing keys: %v", gotBody)
	}
	if res.MessageID != "msg-1" {
		t.Errorf("MessageID = %q, want %q", res.MessageID, "msg-1")
	}
}

func TestSendImageWrapsTheURLInAnObjectUnderTheImageKey(t *testing.T) {
	var gotBody map[string]any
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody = decodeJSONBody(t, r)
		json.NewEncoder(w).Encode(MessageResult{MessageID: "m1"})
	})

	_, err := client.SendMedia(context.Background(), "sess-1", MediaImage,
		"628123@s.whatsapp.net", "https://example.com/pic.jpg", "a caption")
	if err != nil {
		t.Fatalf("SendMedia: %v", err)
	}

	imageVal, ok := gotBody["image"]
	if !ok {
		t.Fatalf(`body has no "image" key: %v`, gotBody)
	}
	imageObj, ok := imageVal.(map[string]any)
	if !ok {
		t.Fatalf(`body["image"] = %#v (%T), want a JSON OBJECT, not a flat string`, imageVal, imageVal)
	}
	if imageObj["url"] != "https://example.com/pic.jpg" {
		t.Errorf(`body["image"]["url"] = %v, want the image URL`, imageObj["url"])
	}
	if gotBody["to"] != "628123@s.whatsapp.net" {
		t.Errorf(`body["to"] = %v`, gotBody["to"])
	}
	if gotBody["caption"] != "a caption" {
		t.Errorf(`body["caption"] = %v`, gotBody["caption"])
	}
}

func TestSendVideoUsesTheVideoKeyNotTheImageKey(t *testing.T) {
	var gotBody map[string]any
	var gotPath string
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody = decodeJSONBody(t, r)
		json.NewEncoder(w).Encode(MessageResult{})
	})

	_, err := client.SendMedia(context.Background(), "sess-1", MediaVideo,
		"628123@s.whatsapp.net", "https://example.com/clip.mp4", "")
	if err != nil {
		t.Fatalf("SendMedia: %v", err)
	}
	if _, ok := gotBody["image"]; ok {
		t.Errorf("body carries an image key for a VIDEO send: %v", gotBody)
	}
	videoVal, ok := gotBody["video"]
	if !ok {
		t.Fatalf(`body has no "video" key: %v`, gotBody)
	}
	videoObj, ok := videoVal.(map[string]any)
	if !ok || videoObj["url"] != "https://example.com/clip.mp4" {
		t.Errorf(`body["video"] = %#v, want {"url": "https://example.com/clip.mp4"}`, videoVal)
	}
	wantPath := "/api/v1/sessions/sess-1/messages/video"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
}

func TestGroupsMapsSubjectToNameAndCountsParticipantsFromTheArrayLength(t *testing.T) {
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"groups":[{"jid":"1@g.us","subject":"Ops","participants":[{},{},{}],"addressing_mode":"lid"}],"total":1}`))
	})

	groups, err := client.Groups(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("len(groups) = %d, want 1", len(groups))
	}
	if groups[0].Name != "Ops" {
		t.Errorf("Name = %q, want %q (mapped from \"subject\")", groups[0].Name, "Ops")
	}
	if groups[0].ParticipantCount != 3 {
		t.Errorf("ParticipantCount = %d, want 3 (len of the participants array)", groups[0].ParticipantCount)
	}
	if groups[0].JID != "1@g.us" {
		t.Errorf("JID = %q, want %q", groups[0].JID, "1@g.us")
	}
}

func TestCreateSessionSendsTheClientSuppliedID(t *testing.T) {
	var gotBody map[string]any
	var gotPath string
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody = decodeJSONBody(t, r)
		w.WriteHeader(http.StatusCreated)
	})

	const clientID = "wa-sessions-row-42"
	if err := client.CreateSession(context.Background(), clientID, "my session", true); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if gotPath != "/api/v1/sessions" {
		t.Errorf("path = %q, want %q", gotPath, "/api/v1/sessions")
	}
	if gotBody["id"] != clientID {
		t.Errorf(`body["id"] = %v, want %q (the caller-supplied id, verbatim)`, gotBody["id"], clientID)
	}
	if gotBody["reuse"] != true {
		t.Errorf(`body["reuse"] = %v, want true`, gotBody["reuse"])
	}
}

func TestQRReturnsTheRawPayloadStrings(t *testing.T) {
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(QRResult{
			QRCodes:        []string{"2@abc123", "2@def456"},
			TimeoutSeconds: 60,
			Status:         "waiting_for_qr",
		})
	})

	qr, err := client.QR(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("QR: %v", err)
	}
	if len(qr.QRCodes) != 2 || qr.QRCodes[0] != "2@abc123" {
		t.Errorf("QRCodes = %v, want the raw payload strings unchanged", qr.QRCodes)
	}
	if qr.TimeoutSeconds != 60 {
		t.Errorf("TimeoutSeconds = %d, want 60", qr.TimeoutSeconds)
	}
}

func TestDeleteSessionTreatsA404AsSuccess(t *testing.T) {
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	if err := client.DeleteSession(context.Background(), "already-gone"); err != nil {
		t.Fatalf("DeleteSession on a 404: got error %v, want nil (404 is success here)", err)
	}
}

func TestDeleteSessionPropagatesANonNotFoundFailure(t *testing.T) {
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	err := client.DeleteSession(context.Background(), "sess-1")
	if err == nil {
		t.Fatal("DeleteSession on a 500: got nil error, want a failure")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Errorf("error = %v (%T), want an *APIError", err, err)
	}
}

func TestNotFoundAndConflictAndRateLimitedBecomeTypedErrors(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{
		{http.StatusNotFound, ErrNotFound},
		{http.StatusConflict, ErrConflict},
		{http.StatusTooManyRequests, ErrRateLimited},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			})
			_, err := client.Status(context.Background(), "sess-1")
			if err != tc.want {
				t.Errorf("Status on HTTP %d: err = %v, want %v", tc.status, err, tc.want)
			}
		})
	}

	t.Run("other non-2xx becomes an APIError carrying the status and body", func(t *testing.T) {
		client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("db on fire"))
		})
		_, err := client.Status(context.Background(), "sess-1")
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("err = %v (%T), want *APIError", err, err)
		}
		if apiErr.StatusCode != http.StatusInternalServerError {
			t.Errorf("StatusCode = %d, want %d", apiErr.StatusCode, http.StatusInternalServerError)
		}
		if !strings.Contains(apiErr.Body, "db on fire") {
			t.Errorf("Body = %q, want it to contain the response body", apiErr.Body)
		}
	})
}

// TestStatusDecodesANullReachabilityAsNilNotAnEmptyString pins the reason
// Status.Reachability (models.go) is a *string rather than a plain string:
// waxum sends a JSON `null` for this field precisely when there is no live
// client for the session in its process at all (the orphaned-runtime wedge
// internal/wa/reconnect.go's package doc comment describes) — the one
// condition internal/wa/backend_waxum.go's isOrphanedClientWedge must be
// able to tell apart from a live client reporting a real, if unhealthy,
// value like "reconnecting". encoding/json leaves a plain `string` field
// completely untouched (not reset to "") when unmarshaling a JSON null, so
// a bare `string` here could never distinguish "absent" from a waxum build
// that legitimately sends "" for some unrelated reason — this test would
// not fail against the old `string` field (both cases decode to ""); it
// exists to prove the *string field actually round-trips null as nil, not
// as "".
func TestStatusDecodesANullReachabilityAsNilNotAnEmptyString(t *testing.T) {
	client, srv := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"connecting","is_logged_in":true,"socket_alive":false,"paused":false,"phone_number":"6281200000001","push_name":"","reachability":null}`))
	})
	defer srv.Close()

	got, err := client.Status(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("Status: err = %v, want nil", err)
	}
	if got.Reachability != nil {
		t.Fatalf("Reachability = %q, want nil (a null reachability must decode to a nil pointer, not an empty string)", *got.Reachability)
	}

	client2, srv2 := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"connecting","is_logged_in":true,"socket_alive":false,"reachability":"reconnecting"}`))
	})
	defer srv2.Close()

	got2, err := client2.Status(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("Status: err = %v, want nil", err)
	}
	if got2.Reachability == nil || *got2.Reachability != "reconnecting" {
		t.Fatalf("Reachability = %v, want a non-nil pointer to %q", got2.Reachability, "reconnecting")
	}
}

func TestCallTTSDefaultsTheIndonesianVoiceWhenEmpty(t *testing.T) {
	var gotBody map[string]any
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody = decodeJSONBody(t, r)
		json.NewEncoder(w).Encode(CallResult{CallID: "call-1"})
	})

	if _, err := client.CallTTS(context.Background(), "sess-1", "628123@s.whatsapp.net", "halo", ""); err != nil {
		t.Fatalf("CallTTS: %v", err)
	}
	if gotBody["voice"] != DefaultTTSVoice {
		t.Errorf(`body["voice"] = %v, want default %q`, gotBody["voice"], DefaultTTSVoice)
	}

	t.Run("an explicit voice is passed through unchanged", func(t *testing.T) {
		if _, err := client.CallTTS(context.Background(), "sess-1", "to", "text", "en-US-SomeVoice"); err != nil {
			t.Fatalf("CallTTS: %v", err)
		}
		if gotBody["voice"] != "en-US-SomeVoice" {
			t.Errorf(`body["voice"] = %v, want "en-US-SomeVoice"`, gotBody["voice"])
		}
	})
}

func TestTerminateOmitsTheReasonWhenEmptySoWaxumAppliesItsDefault(t *testing.T) {
	var gotBody map[string]any
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody = decodeJSONBody(t, r)
		w.WriteHeader(http.StatusOK)
	})

	if err := client.Terminate(context.Background(), "sess-1", "peer@s.whatsapp.net", "call-1", ""); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	if _, ok := gotBody["reason"]; ok {
		t.Errorf(`body has a "reason" key (%v) for an empty reason — want it OMITTED so waxum's server-side default ("hangup") applies`, gotBody["reason"])
	}
	if gotBody["peer"] != "peer@s.whatsapp.net" {
		t.Errorf(`body["peer"] = %v`, gotBody["peer"])
	}
	if gotBody["call_id"] != "call-1" {
		t.Errorf(`body["call_id"] = %v`, gotBody["call_id"])
	}

	t.Run("a non-empty reason is sent", func(t *testing.T) {
		if err := client.Terminate(context.Background(), "sess-1", "peer@s.whatsapp.net", "call-1", "caller left"); err != nil {
			t.Fatalf("Terminate: %v", err)
		}
		if gotBody["reason"] != "caller left" {
			t.Errorf(`body["reason"] = %v, want "caller left"`, gotBody["reason"])
		}
	})
}

// TestNoRingCallMethodExists is a source-level guard: waxum's "ring" call
// endpoint transmits silence, not real audio, so this package must never
// grow a method that reaches it — an operator who called it would believe a
// call was placed. The forbidden path is assembled from two halves, and
// this doc comment never spells it out contiguously either, so this test's
// own source can never accidentally satisfy itself.
func TestNoRingCallMethodExists(t *testing.T) {
	forbidden := "/calls/" + "ring"
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var offenders []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(string(data), forbidden) {
			offenders = append(offenders, name)
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("package waxum references %q in %v — waxum's ring endpoint transmits "+
			"silence; only tts and play push real audio, so no Ring method may exist here",
			forbidden, offenders)
	}
}

// TestClientThrottlesRequestsAcrossASharedBucket is the direct test for
// ratelimit.go's clientBucket: with a burst of 1 and a fast-but-nonzero
// refill rate, the first request is admitted immediately and every request
// after it must wait roughly 1/rate — proving Client actually enforces a
// process-wide cap on outgoing requests, not just that WithClientRateLimit
// compiles. This is the regression test for the token-bucket half of
// CLAUDE.md's waxum rate-limit hazard note: a burst of pumps sharing one
// *Client must not be able to send them all at once regardless of how many
// pumps exist.
func TestClientThrottlesRequestsAcrossASharedBucket(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	// burst=1, rate=10/s -> the 2nd and 3rd calls each wait ~100ms.
	client := New(srv.URL, testToken, 5*time.Second, WithClientRateLimit(10, 1))

	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := client.Health(context.Background()); err != nil {
			t.Fatalf("Health call %d: %v", i, err)
		}
	}
	elapsed := time.Since(start)

	// 2 waits of ~100ms each; allow generous slack for scheduling but this
	// MUST be well above "3 calls with no throttling at all" (a few ms).
	if elapsed < 150*time.Millisecond {
		t.Fatalf("3 calls against burst=1/rate=10 took %v, want >= ~150ms — "+
			"the client-wide rate limiter does not appear to be throttling requests", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("3 calls took %v, want well under 2s — the limiter is stalling far more than its own schedule implies", elapsed)
	}
	if hits != 3 {
		t.Fatalf("server saw %d requests, want 3 (throttling must delay requests, not drop them)", hits)
	}
}

// TestClientRateLimiterReturnsPromptlyWhenTheContextIsCancelled proves the
// limiter's wait() is itself bounded by the caller's context, the same
// hazard Manager.Close's prompt-return guarantee depends on elsewhere in
// this codebase (TestManagerCloseReturnsPromptlyWithAPairingPumpRunning) —
// an exhausted bucket with a slow refill rate must not make a request hang
// past a cancelled ctx.
func TestClientRateLimiterReturnsPromptlyWhenTheContextIsCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	// burst=1, rate effectively never refills within this test's lifetime.
	client := New(srv.URL, testToken, 5*time.Second, WithClientRateLimit(0.0001, 1))

	// Spend the single token.
	if err := client.Health(context.Background()); err != nil {
		t.Fatalf("first Health call: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- client.Health(ctx) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("second Health call succeeded despite an exhausted bucket and a near-never refill rate")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second Health call did not return promptly after its context expired — " +
			"the rate limiter must not be able to hang a caller past its own ctx")
	}
}
