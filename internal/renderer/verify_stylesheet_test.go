package renderer_test

import (
	"strings"
	"testing"
)

// TestEveryHeadTemplateLinksTheVerifyStylesheet is
// TestEveryHeadTemplateLinksTheMediaStylesheet's (media_stylesheet_test.go)
// sibling for verify.css, the ELEVENTH stylesheet: login.html duplicates
// base.html's whole <head>, so this has to land in both, same trap, same
// fix.
func TestEveryHeadTemplateLinksTheVerifyStylesheet(t *testing.T) {
	for path, src := range headTemplates(t) {
		t.Run(path, func(t *testing.T) {
			if !strings.Contains(src, "/static/css/verify.css") {
				t.Errorf("%s owns a <head> but does not link verify.css — the chip field's "+
					"styling will not apply to the pages it renders", path)
			}
		})
	}
}

// TestVerifyStylesheetIsLinkedAfterTheMediaStylesheet: verify.css must win
// the cascade over media.css the same way media.css itself wins over
// responsive.css (media_stylesheet_test.go's
// TestMediaStylesheetIsLinkedAfterTheResponsiveStylesheet) — link order,
// not specificity, decides which of two same-weight rules applies last.
func TestVerifyStylesheetIsLinkedAfterTheMediaStylesheet(t *testing.T) {
	for path, src := range headTemplates(t) {
		t.Run(path, func(t *testing.T) {
			media := strings.Index(src, "/static/css/media.css")
			verify := strings.Index(src, "/static/css/verify.css")
			if media < 0 || verify < 0 {
				t.Fatalf("%s: media.css at %d, verify.css at %d — both must be linked",
					path, media, verify)
			}
			if verify < media {
				t.Errorf("%s links verify.css before media.css; media.css would win "+
					"the cascade on anything the two both style", path)
			}
		})
	}
}
