package handler

import (
	"io/fs"
	"net/http/httptest"
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
