package renderer_test

import (
	"strings"
	"testing"
)

// TestEveryHeadTemplateLinksTheStatusStylesheet mirrors
// TestEveryHeadTemplateLinksTheMediaStylesheet (media_stylesheet_test.go)
// for the /status page's own stylesheet: login.html duplicates base.html's
// whole <head>, so status.css has to land in both.
func TestEveryHeadTemplateLinksTheStatusStylesheet(t *testing.T) {
	for path, src := range headTemplates(t) {
		t.Run(path, func(t *testing.T) {
			if !strings.Contains(src, "/static/css/status.css") {
				t.Errorf("%s owns a <head> but does not link status.css — /status's "+
					"own styling will not apply to the pages it renders", path)
			}
		})
	}
}

// TestStatusStylesheetIsLinkedAfterTheResponsiveStylesheet: status.css
// must win the cascade over responsive.css's phone rules, the same link-
// order rule TestMediaStylesheetIsLinkedAfterTheResponsiveStylesheet pins
// for media.css.
func TestStatusStylesheetIsLinkedAfterTheResponsiveStylesheet(t *testing.T) {
	for path, src := range headTemplates(t) {
		t.Run(path, func(t *testing.T) {
			responsive := strings.Index(src, "/static/css/responsive.css")
			status := strings.Index(src, "/static/css/status.css")
			if responsive < 0 || status < 0 {
				t.Fatalf("%s: responsive.css at %d, status.css at %d — both must be linked",
					path, responsive, status)
			}
			if status < responsive {
				t.Errorf("%s links status.css before responsive.css; responsive.css would win "+
					"the cascade on anything the two both style", path)
			}
		})
	}
}
