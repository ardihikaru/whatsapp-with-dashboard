package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestNormalizePhoneAppliesTheDocumentedRules pins the exact transforms
// the plan specifies: leading "0" -> "62", a leading "+" stripped, and
// spaces/dashes stripped — a table test so a future change to one rule
// can't silently also change another.
func TestNormalizePhoneAppliesTheDocumentedRules(t *testing.T) {
	cases := []struct{ in, want string }{
		{"085132148259", "6285132148259"},
		{"+62 851-3214-8259", "6285132148259"},
		{"6285132148259", "6285132148259"},
		{"0851 3214 8259", "6285132148259"},
		{"", ""},
	}
	for _, c := range cases {
		if got := NormalizePhone(c.in); got != c.want {
			t.Errorf("NormalizePhone(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func newTestServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
}

// TestAReadFailsOverOnA5xxFromTheFirstHost is the read half of the
// failover rule: a GET must try the second host when the first answers
// 5xx, and return that host's successful result.
func TestAReadFailsOverOnA5xxFromTheFirstHost(t *testing.T) {
	bad := newTestServer(t, 500, `{"error":"boom"}`)
	defer bad.Close()
	good := newTestServer(t, 200, `{"ok":true}`)
	defer good.Close()

	p, err := New(Config{HostsSpec: "a=" + bad.URL + "|http://127.0.0.1:1,b=" + good.URL + "|http://127.0.0.1:1", WSMUser: "u", WSMPass: "p"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.CallWSM(context.Background(), "GET", "/api/v1/health", nil, nil, nil, false, "")
	if err != nil {
		t.Fatalf("CallWSM: %v", err)
	}
	if res.Host != "b" {
		t.Fatalf("expected failover to host b, got %q (status %d)", res.Host, res.Status)
	}
}

// TestAWriteDoesNotFailOverOnA5xxFromTheFirstHost is the write half: a
// write must return the first host's 5xx verbatim rather than retrying the
// second, because the first host may already have acted on the request.
func TestAWriteDoesNotFailOverOnA5xxFromTheFirstHost(t *testing.T) {
	bad := newTestServer(t, 500, `{"error":"boom"}`)
	defer bad.Close()
	good := newTestServer(t, 200, `{"ok":true}`)
	defer good.Close()

	p, err := New(Config{HostsSpec: "a=" + bad.URL + "|http://127.0.0.1:1,b=" + good.URL + "|http://127.0.0.1:1", WSMUser: "u", WSMPass: "p"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.CallWSM(context.Background(), "POST", "/api/v1/messages", nil, nil, map[string]string{"to": "x"}, true, "")
	if err != nil {
		t.Fatalf("CallWSM: %v", err)
	}
	if res.Host != "a" || res.Status != 500 {
		t.Fatalf("expected the FIRST host's 500 to be returned verbatim, got host=%q status=%d", res.Host, res.Status)
	}
}

// TestAWriteDoesFailOverOnADialError is the one case a write DOES fail
// over: the request never left the client (connection refused), so it
// cannot have been double-sent.
func TestAWriteDoesFailOverOnADialError(t *testing.T) {
	good := newTestServer(t, 200, `{"ok":true}`)
	defer good.Close()

	// Port 1 is a privileged port nothing listens on in a test sandbox —
	// dialing it reliably refuses.
	p, err := New(Config{HostsSpec: "a=http://127.0.0.1:1|http://127.0.0.1:1,b=" + good.URL + "|http://127.0.0.1:1", WSMUser: "u", WSMPass: "p"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.CallWSM(context.Background(), "POST", "/api/v1/messages", nil, nil, map[string]string{"to": "x"}, true, "")
	if err != nil {
		t.Fatalf("CallWSM: %v", err)
	}
	if res.Host != "b" {
		t.Fatalf("expected a write to fail over on a dial error to host b, got %q", res.Host)
	}
}

// TestA4xxNeverFailsOver pins the proxy_next_upstream-style rule: a 4xx is
// a real answer from a reachable host, not a reason to try another one,
// for either a read or a write.
func TestA4xxNeverFailsOver(t *testing.T) {
	first := newTestServer(t, 404, `{"error":"not found"}`)
	defer first.Close()
	second := newTestServer(t, 200, `{"ok":true}`)
	defer second.Close()

	p, err := New(Config{HostsSpec: "a=" + first.URL + "|http://127.0.0.1:1,b=" + second.URL + "|http://127.0.0.1:1", WSMUser: "u", WSMPass: "p"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.CallWSM(context.Background(), "GET", "/api/v1/sessions/{id}", map[string]string{"id": "x"}, nil, nil, false, "")
	if err != nil {
		t.Fatalf("CallWSM: %v", err)
	}
	if res.Host != "a" || res.Status != 404 {
		t.Fatalf("expected the first host's 404 verbatim, got host=%q status=%d", res.Host, res.Status)
	}
}

// TestTargetPinsOneHostAndDisablesFailover asserts that passing target
// skips every other configured host even when the pinned one would
// otherwise have failed over.
func TestTargetPinsOneHostAndDisablesFailover(t *testing.T) {
	bad := newTestServer(t, 500, `{"error":"boom"}`)
	defer bad.Close()
	good := newTestServer(t, 200, `{"ok":true}`)
	defer good.Close()

	p, err := New(Config{HostsSpec: "a=" + bad.URL + "|http://127.0.0.1:1,b=" + good.URL + "|http://127.0.0.1:1", WSMUser: "u", WSMPass: "p"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.CallWSM(context.Background(), "GET", "/api/v1/health", nil, nil, nil, false, "a")
	if err != nil {
		t.Fatalf("CallWSM: %v", err)
	}
	if res.Host != "a" || res.Status != 500 {
		t.Fatalf("target=a should pin host a and return its 500 verbatim, got host=%q status=%d", res.Host, res.Status)
	}
}

// TestUnknownTargetIsAnErrorNotASilentFallback asserts that pinning a host
// name that isn't configured fails loudly rather than trying every host.
func TestUnknownTargetIsAnErrorNotASilentFallback(t *testing.T) {
	good := newTestServer(t, 200, `{"ok":true}`)
	defer good.Close()
	p, err := New(Config{HostsSpec: "a=" + good.URL + "|http://127.0.0.1:1", WSMUser: "u", WSMPass: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.CallWSM(context.Background(), "GET", "/api/v1/health", nil, nil, nil, false, "nope"); err == nil {
		t.Fatal("expected an error for an unknown target host, got nil")
	}
}

// TestResolveSessionPicksTheHostThatOwnsAnExplicitSessionID is the
// per-host session id behaviour the plan describes: the same phone has a
// DIFFERENT session_id per host, so an explicit session_id must pin to
// whichever host's /api/v1/sessions actually lists it, not just the first
// configured host.
func TestResolveSessionPicksTheHostThatOwnsAnExplicitSessionID(t *testing.T) {
	hostA := newTestServer(t, 200, `[{"id":"sess-a","phone":"6281111111111"}]`)
	defer hostA.Close()
	hostB := newTestServer(t, 200, `[{"id":"sess-b","phone":"6281111111111"}]`)
	defer hostB.Close()

	p, err := New(Config{HostsSpec: "a=" + hostA.URL + "|http://127.0.0.1:1,b=" + hostB.URL + "|http://127.0.0.1:1", WSMUser: "u", WSMPass: "p"})
	if err != nil {
		t.Fatal(err)
	}
	h, id, err := p.ResolveSession(context.Background(), "sess-b", "", "")
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	if h.Name != "b" || id != "sess-b" {
		t.Fatalf("expected session_id sess-b to resolve to host b, got host=%q id=%q", h.Name, id)
	}
}

// TestResolveSessionByPhoneUsesTheNormalisedForm asserts a "0…" phone
// still resolves against a session list keyed by the "62…" form waxum/WSM
// actually store.
func TestResolveSessionByPhoneUsesTheNormalisedForm(t *testing.T) {
	host := newTestServer(t, 200, `[{"id":"sess-1","phone":"6285132148259"}]`)
	defer host.Close()
	p, err := New(Config{HostsSpec: "a=" + host.URL + "|http://127.0.0.1:1", WSMUser: "u", WSMPass: "p"})
	if err != nil {
		t.Fatal(err)
	}
	_, id, err := p.ResolveSession(context.Background(), "", "085132148259", "")
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	if id != "sess-1" {
		t.Fatalf("expected sess-1, got %q", id)
	}
}
