package renderer_test

import (
	"strings"
	"testing"
)

// TestEveryHeadTemplateLinksTheMediaStylesheet is
// TestEveryHeadTemplateLinksTheResponsiveStylesheet's (responsive_test.go)
// Phase 7 sibling: login.html duplicates base.html's whole <head>, so
// media.css (the TENTH stylesheet) has to land in both, same trap, same
// fix.
func TestEveryHeadTemplateLinksTheMediaStylesheet(t *testing.T) {
	for path, src := range headTemplates(t) {
		t.Run(path, func(t *testing.T) {
			if !strings.Contains(src, "/static/css/media.css") {
				t.Errorf("%s owns a <head> but does not link media.css — Phase 7's "+
					"send/calls/ops/webhooks styling will not apply to the pages it renders", path)
			}
		})
	}
}

// TestMediaStylesheetIsLinkedAfterTheResponsiveStylesheet: media.css must
// win the cascade over responsive.css's phone rules the same way
// responsive.css itself wins over charts.css (responsive_test.go's
// TestResponsiveStylesheetIsLinkedAfterChartsStylesheet) — link order, not
// specificity, decides which of two same-weight rules applies last.
func TestMediaStylesheetIsLinkedAfterTheResponsiveStylesheet(t *testing.T) {
	for path, src := range headTemplates(t) {
		t.Run(path, func(t *testing.T) {
			responsive := strings.Index(src, "/static/css/responsive.css")
			media := strings.Index(src, "/static/css/media.css")
			if responsive < 0 || media < 0 {
				t.Fatalf("%s: responsive.css at %d, media.css at %d — both must be linked",
					path, responsive, media)
			}
			if media < responsive {
				t.Errorf("%s links media.css before responsive.css; responsive.css would win "+
					"the cascade on anything the two both style", path)
			}
		})
	}
}
