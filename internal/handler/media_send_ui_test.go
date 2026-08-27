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

// sendTestRenderer builds a bare *renderer.Renderer over the embedded
// templates — the same technique responsive_test.go's
// TestLoginPageRendersTheResponsiveStylesheetUnderBasePath uses, needed
// here because /send's media form only renders once at least one session
// is CONNECTED (sendPageData.Connected), and neither newLogsTestServer nor
// newMediaTestServer's created-but-never-activated session satisfies that
// without a live backend to Activate() through. Rendering sendPageData
// directly exercises the exact markup a real connected session would
// produce, without needing one.
func sendTestRenderer(t *testing.T) *renderer.Renderer {
	t.Helper()
	webFS, err := fs.Sub(wsm.FS, "web")
	if err != nil {
		t.Fatal(err)
	}
	rend, err := renderer.New(webFS, "")
	if err != nil {
		t.Fatal(err)
	}
	return rend
}

// TestTheMediaFormCarriesBothHxEncodingAndEnctype pins the phase brief's
// named trap: htmx reads hx-encoding to send a real multipart body, and
// the no-JS action/method fallback needs enctype — shipping only one
// silently breaks whichever path nobody happened to test.
func TestTheMediaFormCarriesBothHxEncodingAndEnctype(t *testing.T) {
	rend := sendTestRenderer(t)
	data := sendPageData{Connected: []wa.Snapshot{{ID: "sess-1", Name: "Gateway 1", Phone: "628000"}}}
	rec := httptest.NewRecorder()
	rend.RenderPage(rec, "send", data)
	body := rec.Body.String()

	if !strings.Contains(body, `hx-post="/send/media"`) {
		t.Fatalf("the media form does not hx-post to /send/media; body:\n%s", body)
	}
	if !strings.Contains(body, `hx-encoding="multipart/form-data"`) {
		t.Error(`the media form is missing hx-encoding="multipart/form-data" — htmx would send it ` +
			"urlencoded-in-a-string instead of a real multipart body")
	}
	if !strings.Contains(body, `enctype="multipart/form-data"`) {
		t.Error(`the media form is missing enctype="multipart/form-data" — the no-JS action/method ` +
			"fallback would submit it as application/x-www-form-urlencoded instead")
	}
	if !strings.Contains(body, `action="/send/media"`) {
		t.Error(`the media form is missing its no-JS action="/send/media" fallback`)
	}
}
