package renderer_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/renderer"
)

// The mounted shape. Everything else in this package asserts the UNMOUNTED one
// (basePath ""), which is what `make run-wsm` and the handler tests use — and
// which would keep passing forever while /wsm was completely broken.

// renderPartial is the important case: RenderPartial executes a named template
// with whatever the handler passes, and none of those values embed page chrome.
// A Base FIELD would be invisible here, so these six URLs would silently keep
// rendering root-relative. That is precisely why the prefix is a template
// FUNCTION instead.
func renderPartial(t *testing.T, basePath, name string, data any) string {
	t.Helper()
	r, err := renderer.New(webFS(t), basePath)
	if err != nil {
		t.Fatalf("parse templates (basePath=%q): %v", basePath, err)
	}
	rec := httptest.NewRecorder()
	r.RenderPartial(rec, name, data)
	if rec.Code != 200 {
		t.Fatalf("RenderPartial %s = %d, want 200", name, rec.Code)
	}
	return rec.Body.String()
}

// Shaped to match handler.sessionRowsData / wa.Snapshot as the template reads
// them. A mismatch renders as an empty body plus "render error", which an
// assertion phrased as "does not contain X" would happily call a PASS — so
// every check below also asserts on something that must be PRESENT.
type rowSession struct {
	ID            string
	Name          string
	Phone         string
	JID           string
	State         string
	QueueLen      int
	Stalled       bool
	StalledReason string
	IsMaster      bool
}

type rowsData struct{ Sessions []rowSession }

func TestPartialURLsCarryTheMount(t *testing.T) {
	data := rowsData{Sessions: []rowSession{{ID: "abc", Name: "Kasir", Phone: "628", JID: "628@s", State: "connected", IsMaster: true}}}

	mounted := renderPartial(t, "/wsm", "session_rows", data)
	if !strings.Contains(mounted, `/wsm/sessions/abc/`) {
		t.Errorf("mounted session_rows has no /wsm/sessions/abc/ URL:\n%s", mounted)
	}
	// The master toggle is a sixth URL sharing this same prefixing risk —
	// a fixture with IsMaster:true is required to even reach it, since it
	// only renders on the {{if .IsMaster}} branch.
	if !strings.Contains(mounted, `/wsm/sessions/abc/master`) {
		t.Errorf("mounted session_rows has no /wsm/sessions/abc/master URL:\n%s", mounted)
	}
	if !strings.Contains(mounted, `badge-master`) {
		t.Errorf("mounted session_rows dropped the master badge:\n%s", mounted)
	}
	// The failure this guards: a URL that kept its root-relative form. On a
	// shared origin `/sessions/abc/delete` is not a 404 — it is a POST at the
	// PLATFORM BACKOFFICE, which is a different product on the same host.
	if strings.Contains(mounted, `"/sessions/`) || strings.Contains(mounted, `="/admin/`) {
		t.Errorf("mounted session_rows still emits a root-relative URL:\n%s", mounted)
	}

	// And the unmounted shape must be byte-identical to what shipped before,
	// or `make run-wsm` and every existing test are asserting a fiction.
	plain := renderPartial(t, "", "session_rows", data)
	if !strings.Contains(plain, `/sessions/abc/`) {
		t.Errorf("unmounted session_rows lost its /sessions/abc/ URL:\n%s", plain)
	}
	if strings.Contains(plain, "/wsm/") {
		t.Errorf("unmounted session_rows leaked a /wsm/ prefix:\n%s", plain)
	}
}

// u must not invent a path when unmounted. The bug it guards is "wsmsessions":
// concatenating a prefix with a path that does not start with "/".
func TestUnmountedPrefixIsExactlyNothing(t *testing.T) {
	out := renderPartial(t, "", "session_rows", rowsData{Sessions: []rowSession{{ID: "x", State: "connected"}}})
	if !strings.Contains(out, `"/sessions/x/deactivate"`) {
		t.Fatalf("template did not render the row at all (empty body reads as a pass):\n%s", out)
	}
	if strings.Contains(out, `"sessions/`) {
		t.Errorf("unmounted output lost its leading slash:\n%s", out)
	}
}
