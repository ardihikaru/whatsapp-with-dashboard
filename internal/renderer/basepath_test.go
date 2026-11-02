package renderer_test

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	r, err := renderer.New(webFS(t), basePath, nil)
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
	GroupCode     string
	GroupCodes    []string
	SelfReminder  bool
}

type rowsData struct{ Sessions []rowSession }

func TestPartialURLsCarryTheMount(t *testing.T) {
	data := rowsData{Sessions: []rowSession{{ID: "abc", Name: "Kasir", Phone: "628", JID: "628@s", State: "connected", IsMaster: true, GroupCode: "kemang", GroupCodes: []string{"kemang"}}}}

	mounted := renderPartial(t, "/wsm", "session_rows", data)
	if !strings.Contains(mounted, `/wsm/sessions/abc/`) {
		t.Errorf("mounted session_rows has no /wsm/sessions/abc/ URL:\n%s", mounted)
	}
	// The Edit dialog's URL is another one sharing this same prefixing
	// risk, reachable from every row regardless of IsMaster.
	if !strings.Contains(mounted, `/wsm/sessions/abc/edit`) {
		t.Errorf("mounted session_rows has no /wsm/sessions/abc/edit URL:\n%s", mounted)
	}
	if !strings.Contains(mounted, `/wsm/sessions/abc/delete-confirm`) {
		t.Errorf("mounted session_rows has no /wsm/sessions/abc/delete-confirm URL:\n%s", mounted)
	}
	if !strings.Contains(mounted, `badge-master`) {
		t.Errorf("mounted session_rows dropped the master badge:\n%s", mounted)
	}
	if !strings.Contains(mounted, `badge-group`) || !strings.Contains(mounted, `kemang`) {
		t.Errorf("mounted session_rows dropped the group badge:\n%s", mounted)
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

// The reconcile banner names each disagreeing session with both views, always
// carries the "Check now" button when a reconciler runs, and renders NOTHING
// when none does (asserting presence AND absence, per the note above
// rowSession).
func TestTheReconcileBannerNamesTheMismatchAndCarriesTheCheckNowButton(t *testing.T) {
	type mm struct {
		Name, WSM, Waxum string
		Since            time.Time
	}
	type report struct {
		Enabled, OK    bool
		CheckedAt      time.Time
		Mismatches     []mm
		BackendProblem *struct {
			Since       time.Time
			Sessions    int
			RateLimited bool
		}
	}
	type data struct {
		Report   report
		Cooldown bool
	}

	bad := renderPartial(t, "/wsm", "reconcile_banner", data{Report: report{Enabled: true, CheckedAt: time.Now(),
		Mismatches: []mm{{Name: "Arie", WSM: "logged_out", Waxum: "logged_in=true socket_alive=true", Since: time.Now()}}}})
	for _, want := range []string{"alert-error", "Arie", "logged_out", "socket_alive=true",
		`hx-post="/wsm/sessions/reconcile"`, `hx-disabled-elt="this"`, "Check now", "Checking..."} {
		if !strings.Contains(bad, want) {
			t.Errorf("banner missing %q in %q", want, bad)
		}
	}
	green := renderPartial(t, "", "reconcile_banner", data{Report: report{Enabled: true, OK: true, CheckedAt: time.Now()}, Cooldown: true})
	if strings.Contains(green, "alert-error") || !strings.Contains(green, "Check now") || !strings.Contains(green, "Checked moments ago") {
		t.Errorf("green banner = %q", green)
	}
	if out := strings.TrimSpace(renderPartial(t, "", "reconcile_banner", data{Report: report{}})); out != "" {
		t.Errorf("banner with no reconciler = %q, want empty", out)
	}
}
