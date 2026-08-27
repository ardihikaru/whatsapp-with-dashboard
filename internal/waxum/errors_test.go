package waxum

import "testing"

// TestAPIErrorMessageExtractsWaxumsOwnEnvelope proves Message() pulls the
// human-readable reason out of waxum's {"success":false,"error":{...}}
// envelope rather than returning the whole raw JSON blob — the shape
// PostAPISessionWebhooks (internal/handler) needs to surface WHY a
// callback URL was refused, not just that it was.
func TestAPIErrorMessageExtractsWaxumsOwnEnvelope(t *testing.T) {
	e := &APIError{StatusCode: 400, Body: `{"success":false,"error":{"code":400,"message":"Callback URL resolves to a private/reserved address"}}`}
	if got, want := e.Message(), "Callback URL resolves to a private/reserved address"; got != want {
		t.Errorf("Message() = %q, want %q", got, want)
	}
}

// TestAPIErrorMessageFallsBackToTheRawBodyWhenItIsNotWaxumsEnvelope covers
// a body that never reached waxum's own handler at all (a proxy error
// page, say) — Message() must not silently return "" and hide the failure
// from an operator.
func TestAPIErrorMessageFallsBackToTheRawBodyWhenItIsNotWaxumsEnvelope(t *testing.T) {
	e := &APIError{StatusCode: 502, Body: "<html>Bad Gateway</html>"}
	if got, want := e.Message(), "<html>Bad Gateway</html>"; got != want {
		t.Errorf("Message() = %q, want %q", got, want)
	}
}
