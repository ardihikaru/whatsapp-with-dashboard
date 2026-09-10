package renderer_test

import (
	"io/fs"
	"strings"
	"testing"

	wsm "github.com/ardihikaru/whatsapp-with-dashboard"
)

// TestBaseLayoutLinksTheHostChipStylesheet is the one head template that
// actually renders the "header" template (and so can render the host-label
// chip): it must link host-chip.css, or WSM_HOST_LABEL renders as unstyled
// text instead of a chip.
func TestBaseLayoutLinksTheHostChipStylesheet(t *testing.T) {
	b, err := fs.ReadFile(wsm.FS, "web/templates/base.html")
	if err != nil {
		t.Fatalf("read base.html: %v", err)
	}
	if !strings.Contains(string(b), "/static/css/host-chip.css") {
		t.Error("base.html does not link host-chip.css — the header's host-label " +
			"chip will render unstyled")
	}
}

// TestHostChipStylesheetIsLinkedAfterTheSelfConvoStylesheet: same
// last-link-wins reasoning as every other stylesheet-order test in this
// package (see responsive_test.go's chart/responsive pair) — host-chip.css
// is documented as the THIRTEENTH stylesheet, loaded after self-convo.css.
func TestHostChipStylesheetIsLinkedAfterTheSelfConvoStylesheet(t *testing.T) {
	b, err := fs.ReadFile(wsm.FS, "web/templates/base.html")
	if err != nil {
		t.Fatalf("read base.html: %v", err)
	}
	src := string(b)
	selfConvo := strings.Index(src, "/static/css/self-convo.css")
	hostChip := strings.Index(src, "/static/css/host-chip.css")
	if selfConvo < 0 || hostChip < 0 {
		t.Fatalf("base.html: self-convo.css at %d, host-chip.css at %d — both must be linked",
			selfConvo, hostChip)
	}
	if hostChip < selfConvo {
		t.Error("base.html links host-chip.css before self-convo.css; link order " +
			"decides which wins the cascade on anything the two both style")
	}
}

// TestLoginPageDeliberatelyDoesNotLinkTheHostChipStylesheet is the deliberate
// exception to every other stylesheet in this package's "every head template
// must link it" rule (responsive_test.go, media_stylesheet_test.go,
// verify_stylesheet_test.go, self_convo_stylesheet_test.go): the chip lives
// in the "header" template, and login.html never renders "header" — its own
// .auth-layout/.auth-card markup is self-contained (see login.html's own
// comment, and host-chip.css's header comment, for the full reasoning). This
// pins the decision so a future "keep login.html in step" pass doesn't add
// the link back out of habit and leave dead CSS with nothing to style.
func TestLoginPageDeliberatelyDoesNotLinkTheHostChipStylesheet(t *testing.T) {
	b, err := fs.ReadFile(wsm.FS, "web/templates/login.html")
	if err != nil {
		t.Fatalf("read login.html: %v", err)
	}
	if strings.Contains(string(b), "/static/css/host-chip.css") {
		t.Error("login.html links host-chip.css, but login.html never renders the " +
			"\"header\" template the chip lives in — this link is dead weight; if " +
			"login.html has grown a reason to need it, update this test to match " +
			"and explain why in both files' comments")
	}
}

// TestHostChipStylesheetAssetExistsInTheEmbeddedFS: linked in a <link> but
// missing from the embedded FS is a 404 in the browser and a green test
// suite everywhere else — same check responsive_test.go runs for its own
// stylesheet.
func TestHostChipStylesheetAssetExistsInTheEmbeddedFS(t *testing.T) {
	if _, err := fs.ReadFile(wsm.FS, "web/static/css/host-chip.css"); err != nil {
		t.Errorf("web/static/css/host-chip.css is linked by base.html but missing "+
			"from the embedded FS: %v", err)
	}
}
