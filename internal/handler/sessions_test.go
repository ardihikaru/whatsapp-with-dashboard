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
