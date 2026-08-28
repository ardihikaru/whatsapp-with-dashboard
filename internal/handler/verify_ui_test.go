package handler

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// TestConnectedSnapshotsExcludesDisconnectedSessions pins GetVerify's
// picker filter directly: a disconnected session must never reach the
// options the /verify form offers to route to, so an operator cannot
// accidentally pick a session with nothing behind it. See
// connectedSnapshots' own doc comment (verify_ui.go) for why this is
// tested against literal wa.Snapshot values rather than through a real
// wa.Manager — Connected is computed from unexported ManagedSession state
// this package has no way to fabricate as true.
func TestConnectedSnapshotsExcludesDisconnectedSessions(t *testing.T) {
	in := []wa.Snapshot{
		{ID: "up", Name: "Up", Phone: "628000", Connected: true},
		{ID: "down", Name: "Down", Phone: "628111", Connected: false},
	}
	got := connectedSnapshots(in)
	if len(got) != 1 || got[0].ID != "up" {
		t.Fatalf("connectedSnapshots(%+v) = %+v, want exactly the one Connected session", in, got)
	}
}

// TestSplitPhonesInputSplitsOnBothNewlinesAndCommas pins PostVerify's
// parsing of the phones textarea: an operator pasting a comma-separated
// export must not have to reformat it into one number per line first.
func TestSplitPhonesInputSplitsOnBothNewlinesAndCommas(t *testing.T) {
	got := splitPhonesInput("+62 812-0000-0000\n+62 899-0000-0000,+1 415-555-0100\r\n\n , +44 20-0000-0000")
	want := []string{"+62 812-0000-0000", "+62 899-0000-0000", "+1 415-555-0100", "+44 20-0000-0000"}
	if len(got) != len(want) {
		t.Fatalf("splitPhonesInput(...) = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %q, want %q (full: %#v)", i, got[i], want[i], got)
		}
	}
}

// TestGetVerifyRendersTheUnsupportedStateWhenTheBackendHasNoContactDirectory
// is the phase brief's §3-shaped requirement for this page: newLogsTestServer
// wires a nil wa.Backend (see that helper's doc comment), so
// Manager.ContactDirectory() answers wa.ErrUnsupported and the page must
// explain that rather than render a form that could only ever 200-with-an-
// error. No submit form means no phones textarea at all.
func TestGetVerifyRendersTheUnsupportedStateWhenTheBackendHasNoContactDirectory(t *testing.T) {
	srv, sessions, _, _ := newLogsTestServer(t)
	res := authedGet(t, srv, sessions, "/verify")
	if res.Code != http.StatusOK {
		t.Fatalf("GET /verify = %d, want 200 even though the backend is unsupported; body:\n%s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	if !strings.Contains(body, "not supported by this backend") {
		t.Errorf("/verify does not explain that verification is unsupported; got:\n%s", body)
	}
	if strings.Contains(body, `name="phones"`) {
		t.Errorf("/verify renders a phones form even though the backend cannot verify anything; got:\n%s", body)
	}
}

// TestPostVerifyRendersOneRowPerInputWithDistinctStatuses drives POST
// /verify end to end against a fake backend returning a mix of
// registered/not-registered numbers plus one unparseable line, and asserts
// verify_rows renders all three distinctly.
func TestPostVerifyRendersOneRowPerInputWithDistinctStatuses(t *testing.T) {
	backend := &fakeGatewayBackend{
		name: "waxum-shaped",
		checkContactsFn: func(ctx context.Context, sessionID string, phones []string) ([]wa.ContactCheck, error) {
			return []wa.ContactCheck{
				{Phone: "6281200000000", JID: "6281200000000@s.whatsapp.net", IsRegistered: true},
				{Phone: "6289900000000", IsRegistered: false},
			}, nil
		},
	}
	srv, sessions, s, _ := newSessionAdminTestServer(t, backend, false)

	res := postFormAsAdmin(t, srv, sessions, "/verify", url.Values{
		"routing": {"explicit"}, "session_id": {s.ID},
		"phones": {"+62 812-0000-0000\n+62 899-0000-0000\nnot-a-number"},
	})
	if res.Code != http.StatusOK {
		t.Fatalf("POST /verify = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	for _, want := range []string{
		`badge-registered`, `badge-not_registered`, `badge-invalid`,
		"812-0000-0000", "899-0000-0000", "not-a-number",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("verify_rows is missing %q; got:\n%s", want, body)
		}
	}
}

// TestPostVerifyValidationFailuresRender200NotA4xx is the htmx trap this
// phase brief calls out by name: htmx only swaps a 2xx response
// (send.go's PostSend documents the same rule for /send), so EITHER
// validation failure below — an empty submission, or one over
// maxVerifyPhones — must render verify_rows at HTTP 200 with the reason in
// Error, never as a 4xx that would leave the table looking like nothing
// happened.
func TestPostVerifyValidationFailuresRender200NotA4xx(t *testing.T) {
	backend := &fakeGatewayBackend{
		name: "waxum-shaped",
		checkContactsFn: func(ctx context.Context, sessionID string, phones []string) ([]wa.ContactCheck, error) {
			panic("the backend must not be called on a request that should fail validation first")
		},
	}
	srv, sessions, s, _ := newSessionAdminTestServer(t, backend, false)

	t.Run("empty phones", func(t *testing.T) {
		res := postFormAsAdmin(t, srv, sessions, "/verify", url.Values{
			"routing": {"explicit"}, "session_id": {s.ID}, "phones": {"   \n  \n"},
		})
		if res.Code != http.StatusOK {
			t.Fatalf("POST /verify (empty) = %d, want 200; body:\n%s", res.Code, res.Body.String())
		}
		if !strings.Contains(res.Body.String(), "alert-error") {
			t.Errorf("empty submission does not render an inline error; got:\n%s", res.Body.String())
		}
	})

	t.Run("over the 100-number cap", func(t *testing.T) {
		lines := make([]string, maxVerifyPhones+1)
		for i := range lines {
			lines[i] = "+6281200000000"
		}
		res := postFormAsAdmin(t, srv, sessions, "/verify", url.Values{
			"routing": {"explicit"}, "session_id": {s.ID}, "phones": {strings.Join(lines, "\n")},
		})
		if res.Code != http.StatusOK {
			t.Fatalf("POST /verify (101 numbers) = %d, want 200; body:\n%s", res.Code, res.Body.String())
		}
		if !strings.Contains(res.Body.String(), "too many phone numbers") {
			t.Errorf("oversized submission does not report the cap; got:\n%s", res.Body.String())
		}
	})
}

// TestPostVerifyReportsNoConnectedSessionInAutoRouting proves the
// auto-routing branch (selectSendSession, send.go) is reached the same way
// /send and /calls use it, and its "no connected sessions available"
// message renders inline at 200 rather than a 4xx — newSessionAdminTestServer's
// one session is created but never activated (Connected == false), so
// SelectConnected finds nothing.
func TestPostVerifyReportsNoConnectedSessionInAutoRouting(t *testing.T) {
	backend := &fakeGatewayBackend{name: "waxum-shaped"}
	srv, sessions, _, _ := newSessionAdminTestServer(t, backend, false)

	res := postFormAsAdmin(t, srv, sessions, "/verify", url.Values{
		"routing": {"auto"}, "phones": {"+6281200000000"},
	})
	if res.Code != http.StatusOK {
		t.Fatalf("POST /verify = %d, want 200; body:\n%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "no connected sessions available") {
		t.Errorf("auto-routing with no connected session does not explain why; got:\n%s", res.Body.String())
	}
}
