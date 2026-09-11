package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// TestBothSendPanelsCarryTheGroupPicker guards send.html's own named trap
// (the hx-encoding/enctype comment above the media form): a field added to
// one panel and forgotten on the other. Exactly 2 occurrences of
// name="group_code" proves both the text and media forms carry the field,
// not just one of them.
func TestBothSendPanelsCarryTheGroupPicker(t *testing.T) {
	rend := sendTestRenderer(t)
	data := sendPageData{
		Connected:  []wa.Snapshot{{ID: "sess-1", Name: "Gateway 1", Phone: "628000"}},
		GroupCodes: []string{"kemang", "ops"},
	}
	rec := httptest.NewRecorder()
	rend.RenderPage(rec, "send", data)
	body := rec.Body.String()

	got := strings.Count(body, `name="group_code"`)
	if got != 2 {
		t.Fatalf(`/send has %d occurrence(s) of name="group_code", want exactly 2 (one per panel); body:\n%s`, got, body)
	}
	if !strings.Contains(body, `<option value="kemang">kemang</option>`) {
		t.Errorf("/send's group picker does not list the \"kemang\" group; body:\n%s", body)
	}
	if !strings.Contains(body, `<option value="ops">ops</option>`) {
		t.Errorf("/send's group picker does not list the \"ops\" group; body:\n%s", body)
	}
}

// TestTheSendGroupPickerDefaultsToUngrouped proves the empty option is both
// first and selected, so a caller that ignores the field still gets today's
// behaviour (ordinary ungrouped automatic routing).
func TestTheSendGroupPickerDefaultsToUngrouped(t *testing.T) {
	rend := sendTestRenderer(t)
	data := sendPageData{
		Connected:  []wa.Snapshot{{ID: "sess-1", Name: "Gateway 1", Phone: "628000"}},
		GroupCodes: []string{"kemang"},
	}
	rec := httptest.NewRecorder()
	rend.RenderPage(rec, "send", data)
	body := rec.Body.String()

	if !strings.Contains(body, `<option value="" selected>Ungrouped (default)</option>`) {
		t.Fatalf(`/send's group picker does not default to the selected, empty "Ungrouped" option; body:\n%s`, body)
	}
}

// TestDispatchEnqueueOutcomeAnswersFiveHundredForAnyOtherEnqueueError is the
// regression test for the review finding this pins: dispatchSend's old
// "else" branch treated ANY enqueue error other than the two known
// sentinels (ErrQueueFull/ErrNotConnected) as "the request's own ctx ended"
// and answered 202 queued with no error text — even though
// Manager.Dispatch, on ANY enqueue failure, already calls MarkFailed and
// returns the row's failed reason (Dispatch's doc comment,
// internal/wa/dispatch.go). That silently disagreed with the database: the
// row says failed, the response said queued. There is no third enqueue
// error today (internal/wa/sender.go's enqueue has exactly two failure
// modes), so this exercises dispatchEnqueueOutcome directly rather than
// through a live Manager — see the function's own doc comment for why an
// HTTP round trip cannot reach this branch, and this test's sibling below
// for the one dispatchEnqueueOutcome branch that CAN be driven live in
// principle but still can't be reached from package handler's test
// infrastructure (same reason, spelled out there).
func TestDispatchEnqueueOutcomeAnswersFiveHundredForAnyOtherEnqueueError(t *testing.T) {
	someOtherErr := errors.New("boom: not one of the known enqueue sentinels")

	outcome, status := dispatchEnqueueOutcome(nil, "msg-1", someOtherErr)

	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", status, http.StatusInternalServerError)
	}
	if outcome.Status != message.StatusFailed {
		t.Errorf("Status = %q, want %q", outcome.Status, message.StatusFailed)
	}
	if outcome.Error != "could not queue the message" {
		t.Errorf("Error = %q, want %q", outcome.Error, "could not queue the message")
	}
	if outcome.ID != "msg-1" {
		t.Errorf("ID = %q, want %q (the row was already inserted)", outcome.ID, "msg-1")
	}
}

// TestASendWhoseRequestContextEndsWhileQueuedIsReportedAsQueuedNotFailed is
// dispatchEnqueueOutcome's OTHER branch — the one the fix above must NOT
// sweep into the new 500 default: a request whose own context ended while
// still waiting for a terminal result (Dispatch's third branch) must still
// answer 202 queued with no error text, exactly as before.
//
// This is titled after the review finding's proposed HTTP-level test
// ("block the fake backend's SendText, cancel the request ctx") but is
// written against dispatchEnqueueOutcome directly instead: reaching this
// branch through a live *wa.Manager requires a session already in
// StateConnected with its sender loop started, and BOTH are unexported
// ManagedSession state that only a Backend living INSIDE package wa can
// set (production backends do this from backend_meow.go/backend_waxum.go,
// which are part of package wa; see also internal/wa/sender.go's enqueue
// admission check and internal/selfconvo/gateway.go's doc comment, which
// names the identical constraint for the self-convo runner's own tests).
// package handler's fakeGatewayBackend cannot reach StateConnected for the
// same reason dispatchEnqueueOutcome's default branch above cannot be
// driven live: there is no exported seam for it. Confirmed RED against the
// pre-fix mapping by temporarily reproducing the old logic inline: it
// falls into the same "anything not those two sentinels is 202 queued"
// branch as any other enqueue error, so it passed by ACCIDENT rather than
// by a branch that actually checked ctx — this test's sibling above is
// therefore the one that actually pins the fix; this one guards against a
// regression the fix could easily have introduced (routing ctx-ended into
// the new 500 default too).
func TestASendWhoseRequestContextEndsWhileQueuedIsReportedAsQueuedNotFailed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	outcome, status := dispatchEnqueueOutcome(ctx.Err(), "msg-2", context.Canceled)

	if status != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", status, http.StatusAccepted)
	}
	if outcome.Status != message.StatusQueued {
		t.Errorf("Status = %q, want %q", outcome.Status, message.StatusQueued)
	}
	if outcome.Error != "" {
		t.Errorf("Error = %q, want empty (no error text on this branch)", outcome.Error)
	}
	if outcome.ID != "msg-2" {
		t.Errorf("ID = %q, want %q", outcome.ID, "msg-2")
	}
}

// TestDispatchEnqueueOutcomeStillMapsTheTwoKnownEnqueueSentinels is a
// regression guard for the two branches the refactor must leave untouched:
// ErrQueueFull -> 429 and ErrNotConnected -> 409, both with their existing
// reason text.
func TestDispatchEnqueueOutcomeStillMapsTheTwoKnownEnqueueSentinels(t *testing.T) {
	outcome, status := dispatchEnqueueOutcome(nil, "msg-3", wa.ErrQueueFull)
	if status != http.StatusTooManyRequests || outcome.Error != "send queue full" {
		t.Errorf("ErrQueueFull -> (%+v, %d), want (send queue full, 429)", outcome, status)
	}

	outcome, status = dispatchEnqueueOutcome(nil, "msg-4", wa.ErrNotConnected)
	if status != http.StatusConflict || outcome.Error != "session not connected" {
		t.Errorf("ErrNotConnected -> (%+v, %d), want (session not connected, 409)", outcome, status)
	}
}
