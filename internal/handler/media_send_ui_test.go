package handler

import (
	"bytes"
	"io/fs"
	"net/http"
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

// TestMediaKindDisplaysReadsFromDepsConfigNotAConstant is the handler-half
// of the "limits come from config, not hardcoded" proof (the OTHER half is
// TestSendPageRendersEveryKindsConfiguredLimit below, at the template
// layer): two DIFFERENT Deps produce two DIFFERENT results from the exact
// same code path send.go's GetSend calls. A hardcoded set of numbers
// (in the template OR in this function) could not do that.
func TestMediaKindDisplaysReadsFromDepsConfigNotAConstant(t *testing.T) {
	h1 := &handlers{Deps: Deps{
		MediaMaxBytes:      100 * 1024 * 1024,
		MediaMaxBytesImage: 1024 * 1024, MediaMaxBytesVideo: 2 * 1024 * 1024,
		MediaMaxBytesAudio: 3 * 1024 * 1024, MediaMaxBytesDocument: 4 * 1024 * 1024,
	}}
	h2 := &handlers{Deps: Deps{
		MediaMaxBytes:      100 * 1024 * 1024,
		MediaMaxBytesImage: 9 * 1024 * 1024, MediaMaxBytesVideo: 10 * 1024 * 1024,
		MediaMaxBytesAudio: 11 * 1024 * 1024, MediaMaxBytesDocument: 12 * 1024 * 1024,
	}}
	got1 := h1.mediaKindDisplays()
	got2 := h2.mediaKindDisplays()
	if len(got1) != 4 || len(got2) != 4 {
		t.Fatalf("mediaKindDisplays returned %d/%d rows, want 4/4", len(got1), len(got2))
	}
	for i, want := range []int64{1024 * 1024, 2 * 1024 * 1024, 3 * 1024 * 1024, 4 * 1024 * 1024} {
		if got1[i].Bytes != want {
			t.Errorf("h1 row %d (%s): Bytes = %d, want %d", i, got1[i].Kind, got1[i].Bytes, want)
		}
	}
	for i, want := range []int64{9 * 1024 * 1024, 10 * 1024 * 1024, 11 * 1024 * 1024, 12 * 1024 * 1024} {
		if got2[i].Bytes != want {
			t.Errorf("h2 row %d (%s): Bytes = %d, want %d", i, got2[i].Kind, got2[i].Bytes, want)
		}
	}
	if got1[0].Bytes == got2[0].Bytes {
		t.Fatal("h1 and h2 produced the same image limit despite different Deps — mediaKindDisplays is not reading Deps")
	}
}

// TestSendPageRendersEveryKindsConfiguredLimit is the template-half of the
// "limits come from config, not hardcoded" proof: two different MediaLimits
// slices, passed through the exact same send.html, must produce two
// DIFFERENT rendered bodies (each containing ITS OWN numbers, not the
// other's, and not WhatsApp's published defaults) — a template with the
// numbers baked in could not do that.
func TestSendPageRendersEveryKindsConfiguredLimit(t *testing.T) {
	rend := sendTestRenderer(t)
	connected := []wa.Snapshot{{ID: "sess-1", Name: "Gateway 1", Phone: "628000"}}

	limitsA := []mediaKindDisplay{
		{Kind: "image", Label: "Image", Bytes: 1234, Human: "1.2 KB"},
		{Kind: "video", Label: "Video", Bytes: 2345, Human: "2.3 KB"},
		{Kind: "audio", Label: "Audio", Bytes: 3456, Human: "3.4 KB"},
		{Kind: "document", Label: "Document", Bytes: 4567, Human: "4.5 KB"},
	}
	limitsB := []mediaKindDisplay{
		{Kind: "image", Label: "Image", Bytes: 9999, Human: "9.8 KB"},
		{Kind: "video", Label: "Video", Bytes: 8888, Human: "8.7 KB"},
		{Kind: "audio", Label: "Audio", Bytes: 7777, Human: "7.6 KB"},
		{Kind: "document", Label: "Document", Bytes: 6666, Human: "6.5 KB"},
	}

	recA := httptest.NewRecorder()
	rend.RenderPage(recA, "send", sendPageData{Connected: connected, MediaLimits: limitsA})
	bodyA := recA.Body.String()

	recB := httptest.NewRecorder()
	rend.RenderPage(recB, "send", sendPageData{Connected: connected, MediaLimits: limitsB})
	bodyB := recB.Body.String()

	for _, want := range []string{"Image 1.2 KB", "Video 2.3 KB", "Audio 3.4 KB", "Document 4.5 KB"} {
		if !strings.Contains(bodyA, want) {
			t.Errorf("body A missing %q; body:\n%s", want, bodyA)
		}
	}
	for _, want := range []string{"Image 9.8 KB", "Video 8.7 KB", "Audio 7.6 KB", "Document 6.5 KB"} {
		if !strings.Contains(bodyB, want) {
			t.Errorf("body B missing %q; body:\n%s", want, bodyB)
		}
	}
	// The cross-check that actually rules out hardcoding: A's numbers must
	// NOT appear in B's body, and vice versa.
	if strings.Contains(bodyB, "1.2 KB") || strings.Contains(bodyA, "9.8 KB") {
		t.Error("the two renders share a limit that should differ — send.html is not reading .MediaLimits")
	}
}

// TestSendPageDataMaxBytesMatchesTheSelectedKind pins §2's client-side
// requirement: the "kind" select's default selection (Image, the first
// option — no JS has run yet) must carry the SAME byte figure in its own
// data-max-bytes as media-upload.js will read on load, so the very first
// pre-check (before an operator ever touches the Kind select) already
// compares against the right number.
func TestSendPageDataMaxBytesMatchesTheSelectedKind(t *testing.T) {
	rend := sendTestRenderer(t)
	data := sendPageData{
		Connected: []wa.Snapshot{{ID: "sess-1", Name: "Gateway 1", Phone: "628000"}},
		MediaLimits: []mediaKindDisplay{
			{Kind: "image", Label: "Image", Bytes: 5242880, Human: "5.0 MB"},
			{Kind: "video", Label: "Video", Bytes: 16777216, Human: "16.0 MB"},
			{Kind: "audio", Label: "Audio", Bytes: 16777216, Human: "16.0 MB"},
			{Kind: "document", Label: "Document", Bytes: 104857600, Human: "100.0 MB"},
		},
	}
	rec := httptest.NewRecorder()
	rend.RenderPage(rec, "send", data)
	body := rec.Body.String()

	if !strings.Contains(body, `<option value="image" data-max-bytes="5242880">`) {
		t.Errorf("the image option does not carry data-max-bytes=5242880; body:\n%s", body)
	}
	if !strings.Contains(body, `<option value="video" data-max-bytes="16777216">`) {
		t.Errorf("the video option does not carry data-max-bytes=16777216; body:\n%s", body)
	}
	if !strings.Contains(body, `<option value="document" data-max-bytes="104857600">`) {
		t.Errorf("the document option does not carry data-max-bytes=104857600; body:\n%s", body)
	}
}

// TestPostSendMediaRendersThePerKindSizeErrorIntoSendResult is the browser
// form's own proof of §2's last bullet: an oversized-for-its-kind upload
// through POST /send/media (not the JSON API) must render send_result with
// a message naming the kind, the actual size and that kind's limit — the
// SAME text the JSON API returns, since both wrap parseMediaSendRequest.
// Reaches decodeMediaSendRequestUI's early-return path, which returns
// before ever touching h.Manager/h.Messages, so a bare Renderer-only Deps
// is enough here (see decodeMediaSendRequestUI's doc comment).
func TestPostSendMediaRendersThePerKindSizeErrorIntoSendResult(t *testing.T) {
	h := &handlers{Deps: Deps{
		Renderer:           sendTestRenderer(t),
		MediaMaxBytes:      10 * 1024 * 1024,
		MediaMaxBytesImage: 2048,
	}}

	over := append(append([]byte{}, pngMagic...), bytes.Repeat([]byte("x"), 2048-len(pngMagic)+1)...)
	body, ct := buildMultipartMedia(t, map[string]string{"kind": "image", "to": "628123456789"}, over)

	req := httptest.NewRequest(http.MethodPost, "/send/media", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	h.PostSendMedia(rec, req)

	resBody := rec.Body.String()
	for _, want := range []string{"image", formatMediaBytes(2049), formatMediaBytes(2048)} {
		if !strings.Contains(resBody, want) {
			t.Errorf("send_result does not contain %q; body:\n%s", want, resBody)
		}
	}
}
