package renderer_test

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/renderer"
)

// TestFmtTimeFormatsInTheConfiguredLocationNotUTC is the test that goes red
// if fmtTime (or the loc New is given) stops converting — it renders the
// "widgets" partial, whose widgets.html calls {{fmtTime .GeneratedAt
// "15:04:05 MST"}}, against a fixed UTC+7 zone distinct from both UTC and
// whatever the test host's own time.Local happens to be.
func TestFmtTimeFormatsInTheConfiguredLocationNotUTC(t *testing.T) {
	loc := time.FixedZone("WIB", 7*3600)
	r, err := renderer.New(webFS(t), "", loc)
	if err != nil {
		t.Fatalf("parse embedded templates: %v", err)
	}
	generatedAt := time.Date(2026, 10, 22, 3, 4, 5, 0, time.UTC)
	data := struct {
		Widgets     []struct{ Label, Value, Variant string }
		GeneratedAt time.Time
	}{GeneratedAt: generatedAt}

	rec := httptest.NewRecorder()
	r.RenderPartial(rec, "widgets", data)
	body := rec.Body.String()

	if !strings.Contains(body, "10:04:05") {
		t.Errorf("widgets partial did not render the +7h-converted time; got:\n%s", body)
	}
	if strings.Contains(body, "03:04:05") {
		t.Errorf("widgets partial rendered the UTC time verbatim instead of converting; got:\n%s", body)
	}
}

// TestNewWithANilLocationFallsBackToUTC pins the documented nil-safe
// default: a caller that does not supply a location (every pre-WSM_TIMEZONE
// renderer.New call) must get UTC, not panic and not silently adopt the
// test process's own time.Local.
func TestNewWithANilLocationFallsBackToUTC(t *testing.T) {
	r, err := renderer.New(webFS(t), "", nil)
	if err != nil {
		t.Fatalf("parse embedded templates: %v", err)
	}
	generatedAt := time.Date(2026, 10, 22, 3, 4, 5, 0, time.UTC)
	data := struct {
		Widgets     []struct{ Label, Value, Variant string }
		GeneratedAt time.Time
	}{GeneratedAt: generatedAt}

	rec := httptest.NewRecorder()
	r.RenderPartial(rec, "widgets", data)
	body := rec.Body.String()

	if !strings.Contains(body, "03:04:05") {
		t.Errorf("widgets partial with a nil location did not render the UTC time verbatim; got:\n%s", body)
	}
}
