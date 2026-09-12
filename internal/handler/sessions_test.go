package handler

import (
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	wsm "github.com/ardihikaru/whatsapp-with-dashboard"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/renderer"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// TestAStalledSessionRowNeedsAttention is the UI half of the reconnect
// supervisor's "needs attention" decision (internal/wa/reconnect.go):
// Snapshot.Stalled/StalledReason exist specifically so a handler can render
// them (see Snapshot's own doc comment) — this proves session_rows.html
// actually does, rather than silently dropping the flag on the floor the
// way a template-only change is easy to get wrong without a test watching
// it. Renders the partial directly against a hand-built []wa.Snapshot
// (Stalled/StalledReason are both exported) rather than driving a live
// Manager + reconnect supervisor through a real stall, which
// reconnect_test.go (package wa) already covers in depth.
func TestAStalledSessionRowNeedsAttention(t *testing.T) {
	webFS, err := fs.Sub(wsm.FS, "web")
	if err != nil {
		t.Fatal(err)
	}
	rend, err := renderer.New(webFS, "")
	if err != nil {
		t.Fatal(err)
	}

	data := sessionRowsData{Sessions: []wa.Snapshot{
		{ID: "s1", Name: "Gateway 1", State: wa.StateDisconnected,
			Stalled: true, StalledReason: "waxum accepted /connect 5 consecutive times but never reached socket_alive"},
	}}
	res := httptest.NewRecorder()
	rend.RenderPartial(res, "session_rows", data)
	body := res.Body.String()

	if !strings.Contains(body, "needs attention") {
		t.Errorf("a Stalled session's row does not say it needs attention; body:\n%s", body)
	}
	if !strings.Contains(body, data.Sessions[0].StalledReason) {
		t.Errorf("a Stalled session's row does not carry its StalledReason anywhere (e.g. a title attribute); body:\n%s", body)
	}
}

// TestANonStalledSessionRowSaysNothingAboutAttention: the addition above
// must be conditional — a normal disconnected session (not yet stalled)
// must not show the "needs attention" text, or an operator can no longer
// tell the two apart.
func TestANonStalledSessionRowSaysNothingAboutAttention(t *testing.T) {
	webFS, err := fs.Sub(wsm.FS, "web")
	if err != nil {
		t.Fatal(err)
	}
	rend, err := renderer.New(webFS, "")
	if err != nil {
		t.Fatal(err)
	}

	data := sessionRowsData{Sessions: []wa.Snapshot{
		{ID: "s1", Name: "Gateway 1", State: wa.StateDisconnected, Stalled: false},
	}}
	res := httptest.NewRecorder()
	rend.RenderPartial(res, "session_rows", data)
	body := res.Body.String()

	if strings.Contains(body, "needs attention") {
		t.Errorf("a non-stalled session's row says it needs attention; body:\n%s", body)
	}
}

// TestAMasterSessionRowShowsTheMasterBadge is the UI half of the master
// routing-exclusion flag (wa.Snapshot.IsMaster, internal/wa/load_balance.go's
// predicate): this proves session_rows.html actually renders it, rather
// than silently dropping the flag on the floor the way a template-only
// change is easy to get wrong without a test watching it. Same technique as
// TestAStalledSessionRowNeedsAttention above: a literal []wa.Snapshot fed
// straight to the partial, not a live Manager.
func TestAMasterSessionRowShowsTheMasterBadge(t *testing.T) {
	webFS, err := fs.Sub(wsm.FS, "web")
	if err != nil {
		t.Fatal(err)
	}
	rend, err := renderer.New(webFS, "")
	if err != nil {
		t.Fatal(err)
	}

	data := sessionRowsData{Sessions: []wa.Snapshot{
		{ID: "s1", Name: "Gateway 1", State: wa.StateConnected, IsMaster: true},
	}}
	res := httptest.NewRecorder()
	rend.RenderPartial(res, "session_rows", data)
	body := res.Body.String()

	if !strings.Contains(body, `class="badge badge-master"`) {
		t.Errorf("a master session's row does not carry the badge-master pill; body:\n%s", body)
	}
	if !strings.Contains(body, "Master") {
		t.Errorf("a master session's row does not say \"Master\" anywhere; body:\n%s", body)
	}
}

// TestANonMasterSessionRowShowsNoMasterBadge is the test that stops
// TestAMasterSessionRowShowsTheMasterBadge above passing against a template
// that renders the master badge unconditionally.
func TestANonMasterSessionRowShowsNoMasterBadge(t *testing.T) {
	webFS, err := fs.Sub(wsm.FS, "web")
	if err != nil {
		t.Fatal(err)
	}
	rend, err := renderer.New(webFS, "")
	if err != nil {
		t.Fatal(err)
	}

	data := sessionRowsData{Sessions: []wa.Snapshot{
		{ID: "s1", Name: "Gateway 1", State: wa.StateConnected, IsMaster: false},
	}}
	res := httptest.NewRecorder()
	rend.RenderPartial(res, "session_rows", data)
	body := res.Body.String()

	if strings.Contains(body, "badge-master") {
		t.Errorf("a non-master session's row carries the badge-master pill; body:\n%s", body)
	}
}

// TestTheDeleteConfirmationDoesNotPromiseAnUnlinkTheWaxumBackendCannotDo
// pins operator-facing copy that was actively wrong and cost a live
// debugging session on wa-phkg (2026-09-02).
//
// The confirmation used to read "This unlinks the device from WhatsApp."
// That holds only under WSM_WA_BACKEND=whatsmeow, whose Logout calls
// cli.Logout(ctx). Under the DEFAULT backend (config.DefaultWABackend is
// "waxum") Logout is waxum's DeleteSession, and waxum's API has no unlink
// call at all — so the session vanishes from the dashboard while the device
// stays in the handset's "Linked devices" list. WhatsApp caps that list, so
// each create-pair-delete cycle silently burns a slot, and once the cap is
// hit a newly paired device is logged out within seconds of connecting. The
// operator, having been told the delete unlinked, has no reason to look at
// the phone — which is exactly the wrong place to be blind.
//
// Asserting the absence of a phrase is normally a weak test; here the phrase
// IS the defect, and the positive half (the row must point the operator at
// the handset) is what makes it a claim rather than a spelling check.
func TestTheDeleteConfirmationDoesNotPromiseAnUnlinkTheWaxumBackendCannotDo(t *testing.T) {
	webFS, err := fs.Sub(wsm.FS, "web")
	if err != nil {
		t.Fatal(err)
	}
	rend, err := renderer.New(webFS, "")
	if err != nil {
		t.Fatal(err)
	}

	data := sessionRowsData{Sessions: []wa.Snapshot{
		{ID: "s1", Name: "Gateway 1", State: wa.StateConnected},
	}}
	res := httptest.NewRecorder()
	rend.RenderPartial(res, "session_rows", data)
	body := res.Body.String()

	if strings.Contains(body, "This unlinks the device from WhatsApp") {
		t.Errorf("the delete confirmation still promises an unlink; waxum "+
			"(config.DefaultWABackend) exposes no unlink call, so the device "+
			"stays in the handset's Linked devices list; body:\n%s", body)
	}
	for _, want := range []string{"does NOT unlink", "Linked devices"} {
		if !strings.Contains(body, want) {
			t.Errorf("the delete confirmation does not contain %q — an operator "+
				"deleting a session must be told to remove the device on the "+
				"handset too, or the account's linked-device slots leak; body:\n%s",
				want, body)
		}
	}
}

// TestASessionRowShowsItsGroupCodeBadge is the group-code equivalent of
// TestAMasterSessionRowShowsTheMasterBadge: proves session_rows.html
// actually renders wa.Snapshot.GroupCode, rather than silently dropping it
// on the floor the way a template-only change is easy to get wrong without
// a test watching it.
func TestASessionRowShowsItsGroupCodeBadge(t *testing.T) {
	webFS, err := fs.Sub(wsm.FS, "web")
	if err != nil {
		t.Fatal(err)
	}
	rend, err := renderer.New(webFS, "")
	if err != nil {
		t.Fatal(err)
	}

	data := sessionRowsData{Sessions: []wa.Snapshot{
		{ID: "s1", Name: "Gateway 1", State: wa.StateConnected, GroupCode: "kemang"},
	}}
	res := httptest.NewRecorder()
	rend.RenderPartial(res, "session_rows", data)
	body := res.Body.String()

	if !strings.Contains(body, `class="badge badge-group"`) {
		t.Errorf("a grouped session's row does not carry the badge-group pill; body:\n%s", body)
	}
	if !strings.Contains(body, "kemang") {
		t.Errorf("a grouped session's row does not say its group code anywhere; body:\n%s", body)
	}
}

// TestASessionRowWithNoGroupCodeShowsNoGroupBadge is the test that stops
// TestASessionRowShowsItsGroupCodeBadge above passing against a template
// that renders the group badge unconditionally.
func TestASessionRowWithNoGroupCodeShowsNoGroupBadge(t *testing.T) {
	webFS, err := fs.Sub(wsm.FS, "web")
	if err != nil {
		t.Fatal(err)
	}
	rend, err := renderer.New(webFS, "")
	if err != nil {
		t.Fatal(err)
	}

	data := sessionRowsData{Sessions: []wa.Snapshot{
		{ID: "s1", Name: "Gateway 1", State: wa.StateConnected, GroupCode: ""},
	}}
	res := httptest.NewRecorder()
	rend.RenderPartial(res, "session_rows", data)
	body := res.Body.String()

	if strings.Contains(body, "badge-group") {
		t.Errorf("an ungrouped session's row carries the badge-group pill; body:\n%s", body)
	}
}

// TestTheSessionsTableColspanMatchesItsHeaderCount guards a two-file drift
// with no visual symptom until the table is empty or loading: list.html's
// header row and session_rows.html's own loading/empty-state rows each
// hardcode a colspan that must equal the header's <th> count, or an
// operator sees a table that visually shifts (or an empty row that does not
// span the whole table) the moment there are zero sessions, or before the
// first poll lands.
func TestTheSessionsTableColspanMatchesItsHeaderCount(t *testing.T) {
	webFS, err := fs.Sub(wsm.FS, "web")
	if err != nil {
		t.Fatal(err)
	}
	rend, err := renderer.New(webFS, "")
	if err != nil {
		t.Fatal(err)
	}

	pageRec := httptest.NewRecorder()
	rend.RenderPage(pageRec, "sessions", sessionsPageData{})
	pageBody := pageRec.Body.String()

	headerCount := strings.Count(pageBody, "<th>")
	if headerCount == 0 {
		t.Fatalf("sessions page has no <th> at all:\n%s", pageBody)
	}

	wantColspan := fmt.Sprintf(`colspan="%d"`, headerCount)
	if !strings.Contains(pageBody, wantColspan) {
		t.Errorf("sessions page's loading row does not carry %s (header has %d <th>); body:\n%s",
			wantColspan, headerCount, pageBody)
	}

	rowsRec := httptest.NewRecorder()
	rend.RenderPartial(rowsRec, "session_rows", sessionRowsData{})
	rowsBody := rowsRec.Body.String()
	if !strings.Contains(rowsBody, wantColspan) {
		t.Errorf("session_rows' empty-state row does not carry %s (header has %d <th>); body:\n%s",
			wantColspan, headerCount, rowsBody)
	}
}

// TestTheSessionFormOffersEveryExistingGroupCodeAndStillAcceptsANewOne
// pins session-new's routing-group control to an <input>+<datalist>, not a
// bare <select>: swapping to a <select> would remove "type a new one" with
// no other symptom, since a <select> can also legitimately carry <option>
// elements built from the same GroupCodes slice.
func TestTheSessionFormOffersEveryExistingGroupCodeAndStillAcceptsANewOne(t *testing.T) {
	webFS, err := fs.Sub(wsm.FS, "web")
	if err != nil {
		t.Fatal(err)
	}
	rend, err := renderer.New(webFS, "")
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	rend.RenderPage(rec, "session-new", sessionsPageData{GroupCodes: []string{"kemang", "ops"}})
	body := rec.Body.String()

	if !strings.Contains(body, `<input list="wa-group-codes" name="group_code"`) {
		t.Fatalf("session-new does not carry an <input name=\"group_code\"> wired to a <datalist> — free text must still reach the server; body:\n%s", body)
	}
	if !strings.Contains(body, `<datalist id="wa-group-codes">`) {
		t.Errorf("session-new does not carry the <datalist id=\"wa-group-codes\">; body:\n%s", body)
	}
	for _, code := range []string{"kemang", "ops"} {
		if !strings.Contains(body, `<option value="`+code+`">`) {
			t.Errorf("session-new's datalist is missing an <option> for %q; body:\n%s", code, body)
		}
	}
}

// TestCreatingASessionWithAMalformedGroupCodeCreatesNothingAndShowsTheError
// pins the normalise-first ordering PostSessionCreate's doc comment
// describes: a malformed group_code must be rejected BEFORE Manager.Create
// ever runs, so a typo creates no orphaned, silently-ungrouped session.
func TestCreatingASessionWithAMalformedGroupCodeCreatesNothingAndShowsTheError(t *testing.T) {
	srv, sessions, _, _ := newSessionAdminTestServer(t, whatsmeowShapedBackend(), true)

	res := authedPostForm(t, srv, sessions, "/sessions", url.Values{
		"name": {"New Gateway"}, "phone": {"+62 812-1111-2222"}, "group_code": {"Ops Alert"},
	})
	if res.Code != http.StatusOK {
		t.Fatalf("POST /sessions with a malformed group_code = %d, want 200 (form_error partial); body:\n%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "alert-error") {
		t.Errorf("a malformed group_code did not render the form_error partial; body:\n%s", res.Body.String())
	}

	rows := authedGet(t, srv, sessions, "/sessions/rows")
	if strings.Contains(rows.Body.String(), "New Gateway") {
		t.Errorf("a session was created despite the malformed group_code — normalise-first ordering was lost; rows:\n%s", rows.Body.String())
	}
}

// TestTheSessionsPageCarriesAGroupEditorSlotOutsideThePolledTbody pins the
// inline-edit design's load-bearing layout fact: #session-group-editor must
// sit OUTSIDE #session-rows (which re-polls itself every 5s with
// hx-swap="innerHTML" and would wipe a half-typed value mid-edit — see
// session_rows.html's own comment), and the tbody's own hx-trigger must
// listen for the "sessions-changed" event a save fires, or the row never
// picks up the new badge without waiting out the next poll.
func TestTheSessionsPageCarriesAGroupEditorSlotOutsideThePolledTbody(t *testing.T) {
	webFS, err := fs.Sub(wsm.FS, "web")
	if err != nil {
		t.Fatal(err)
	}
	rend, err := renderer.New(webFS, "")
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	rend.RenderPage(rec, "sessions", sessionsPageData{})
	body := rec.Body.String()

	tbodyEnd := strings.Index(body, "</tbody>")
	editorIdx := strings.Index(body, `id="session-group-editor"`)
	if tbodyEnd == -1 {
		t.Fatalf("sessions page has no </tbody> at all; body:\n%s", body)
	}
	if editorIdx == -1 {
		t.Fatalf(`sessions page does not carry id="session-group-editor"; body:`+"\n%s", body)
	}
	if editorIdx < tbodyEnd {
		t.Errorf("#session-group-editor appears INSIDE the polled tbody (index %d < </tbody> index %d) — it would be wiped by the next 5s poll; body:\n%s",
			editorIdx, tbodyEnd, body)
	}

	tbodyStart := strings.Index(body, `id="session-rows"`)
	if tbodyStart == -1 {
		t.Fatalf(`sessions page does not carry id="session-rows"; body:`+"\n%s", body)
	}
	tbodyTag := body[tbodyStart:tbodyEnd]
	if !strings.Contains(tbodyTag, "sessions-changed from:body") {
		t.Errorf(`#session-rows hx-trigger does not listen for "sessions-changed from:body" — a save from the list editor would not refresh the row; tag:`+"\n%s", tbodyTag)
	}
}

// TestEverySessionRowOffersEditGroupRegardlessOfMaster is the test that
// stops a stray {{if .IsMaster}} guard from creeping onto the Edit group
// button: group_code is deliberately uncoupled from is_master
// (list.html's own explainer paragraph), so the button must render on both
// a master and a non-master row alike.
func TestEverySessionRowOffersEditGroupRegardlessOfMaster(t *testing.T) {
	webFS, err := fs.Sub(wsm.FS, "web")
	if err != nil {
		t.Fatal(err)
	}
	rend, err := renderer.New(webFS, "")
	if err != nil {
		t.Fatal(err)
	}

	data := sessionRowsData{Sessions: []wa.Snapshot{
		{ID: "master-1", Name: "Master Gateway", State: wa.StateConnected, IsMaster: true},
		{ID: "plain-1", Name: "Plain Gateway", State: wa.StateConnected, IsMaster: false},
	}}
	rec := httptest.NewRecorder()
	rend.RenderPartial(rec, "session_rows", data)
	body := rec.Body.String()

	for _, id := range []string{"master-1", "plain-1"} {
		wantGet := `hx-get="/sessions/` + id + `/group-code/edit"`
		if !strings.Contains(body, wantGet) {
			t.Errorf("row %q does not carry %s; body:\n%s", id, wantGet, body)
		}
	}
	if got := strings.Count(body, `hx-target="#session-group-editor"`); got != 2 {
		t.Errorf(`hx-target="#session-group-editor" appears %d times, want 2 (one per row); body:`+"\n%s", got, body)
	}
}
