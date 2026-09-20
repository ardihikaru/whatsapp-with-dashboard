package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// sendFormRequest builds a form-encoded *http.Request the way /send's own
// browser forms submit it, already ParseForm'd so sendRouteFromForm can read
// r.FormValue directly — mirroring TestTheSendDecodersReadMasterOnlyFromTheBody's
// JSON-body coverage (api_master_only_test.go) for the /send form-encoded
// surface instead.
func sendFormRequest(t *testing.T, values url.Values) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/send", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := req.ParseForm(); err != nil {
		t.Fatalf("ParseForm: %v", err)
	}
	return req
}

// TestSendRouteFromFormReadsMasterOnly is sendRouteFromForm's own coverage of
// the new field: master_only=true must produce Route.MasterOnly true, and
// leaving the checkbox unticked (the field absent from the body entirely,
// exactly what an unchecked HTML checkbox submits) must keep it false — the
// same "must default to today's behaviour" guarantee
// TestTheSendDecodersReadMasterOnlyFromTheBody pins for the JSON API.
func TestSendRouteFromFormReadsMasterOnly(t *testing.T) {
	t.Run("checked", func(t *testing.T) {
		req := sendFormRequest(t, url.Values{"master_only": {"true"}})
		rt, err := sendRouteFromForm(req)
		if err != nil {
			t.Fatalf("sendRouteFromForm: %v", err)
		}
		if !rt.MasterOnly {
			t.Error("MasterOnly = false, want true")
		}
	})
	t.Run("unchecked (field absent)", func(t *testing.T) {
		req := sendFormRequest(t, url.Values{})
		rt, err := sendRouteFromForm(req)
		if err != nil {
			t.Fatalf("sendRouteFromForm: %v", err)
		}
		if rt.MasterOnly {
			t.Error("MasterOnly = true, want false when the field is absent — " +
				"every existing /send submission must keep auto-routing unchanged")
		}
	})
}

// TestSendRouteFromFormStillReadsGroupCode is a regression guard that adding
// master_only did not disturb the pre-existing group_code decode.
func TestSendRouteFromFormStillReadsGroupCode(t *testing.T) {
	req := sendFormRequest(t, url.Values{"group_code": {"kemang"}})
	rt, err := sendRouteFromForm(req)
	if err != nil {
		t.Fatalf("sendRouteFromForm: %v", err)
	}
	if rt.GroupCode != "kemang" {
		t.Errorf("GroupCode = %q, want %q", rt.GroupCode, "kemang")
	}
}

// TestNoUISendRouteMessageCoversAllFourRoutingScopes pins noUISendRouteMessage's
// exact text for all four wa.Route shapes it can be asked about. The
// ungrouped/group branches (MasterOnly false) are BYTE-IDENTICAL to
// selectSendSession's pre-existing wording — verify_ui_test.go already pins
// the plain "no connected sessions available" string, so this test must
// never diverge from it — and the two MasterOnly branches are the only new
// text this change introduces.
func TestNoUISendRouteMessageCoversAllFourRoutingScopes(t *testing.T) {
	cases := []struct {
		name string
		rt   wa.Route
		want string
	}{
		{
			name: "ungrouped, any session",
			rt:   wa.Route{},
			want: "no connected sessions available",
		},
		{
			name: "grouped, any session",
			rt:   wa.Route{GroupCode: "kemang"},
			want: `no connected sessions available in routing group "kemang"`,
		},
		{
			name: "ungrouped, master only",
			rt:   wa.Route{MasterOnly: true},
			want: "no connected master session available",
		},
		{
			name: "grouped, master only",
			rt:   wa.Route{GroupCode: "kemang", MasterOnly: true},
			want: `no connected master session available in routing group "kemang"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := noUISendRouteMessage(tc.rt); got != tc.want {
				t.Errorf("noUISendRouteMessage(%+v) = %q, want %q", tc.rt, got, tc.want)
			}
		})
	}
}

// TestBothSendPanelsCarryTheMasterOnlyToggle mirrors
// TestBothSendPanelsCarryTheGroupPicker for the new checkbox: exactly 2
// occurrences of name="master_only" proves both the text and media forms
// carry the field, not just one of them — adding it to one panel and
// forgetting the other is send.html's own named trap (see the hx-encoding/
// enctype comment above the media form).
func TestBothSendPanelsCarryTheMasterOnlyToggle(t *testing.T) {
	rend := sendTestRenderer(t)
	data := sendPageData{
		Connected: []wa.Snapshot{{ID: "sess-1", Name: "Gateway 1", Phone: "628000"}},
	}
	rec := httptest.NewRecorder()
	rend.RenderPage(rec, "send", data)
	body := rec.Body.String()

	got := strings.Count(body, `name="master_only"`)
	if got != 2 {
		t.Fatalf(`/send has %d occurrence(s) of name="master_only", want exactly 2 (one per panel); body:\n%s`, got, body)
	}
}
