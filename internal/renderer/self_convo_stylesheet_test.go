package renderer_test

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	wsm "github.com/ardihikaru/whatsapp-with-dashboard"
)

// TestEveryHeadTemplateLinksTheSelfConvoStylesheet is
// TestEveryHeadTemplateLinksTheVerifyStylesheet's (verify_stylesheet_test.go)
// sibling for self-convo.css, the TWELFTH stylesheet: login.html duplicates
// base.html's whole <head>, so this has to land in both, same trap, same fix.
func TestEveryHeadTemplateLinksTheSelfConvoStylesheet(t *testing.T) {
	for path, src := range headTemplates(t) {
		t.Run(path, func(t *testing.T) {
			if !strings.Contains(src, "/static/css/self-convo.css") {
				t.Errorf("%s owns a <head> but does not link self-convo.css — the run "+
					"badges/participant grid/JSON preview will not be styled on the "+
					"pages it renders", path)
			}
		})
	}
}

// TestSelfConvoStylesheetIsLinkedAfterTheVerifyStylesheet: self-convo.css
// must win the cascade over verify.css the same way verify.css itself wins
// over media.css (verify_stylesheet_test.go's
// TestVerifyStylesheetIsLinkedAfterTheMediaStylesheet) — link order, not
// specificity, decides which of two same-weight rules applies last.
func TestSelfConvoStylesheetIsLinkedAfterTheVerifyStylesheet(t *testing.T) {
	for path, src := range headTemplates(t) {
		t.Run(path, func(t *testing.T) {
			verify := strings.Index(src, "/static/css/verify.css")
			selfConvo := strings.Index(src, "/static/css/self-convo.css")
			if verify < 0 || selfConvo < 0 {
				t.Fatalf("%s: verify.css at %d, self-convo.css at %d — both must be linked",
					path, verify, selfConvo)
			}
			if selfConvo < verify {
				t.Errorf("%s links self-convo.css before verify.css; verify.css would "+
					"win the cascade on anything the two both style", path)
			}
		})
	}
}

// TestEveryHeadTemplateLinksTheSelfConvoScript pins the JS half of the same
// wiring rule: self-convo.js (the tz_offset filler) has to be reachable
// from every page that owns a <head>, base.html and login.html alike.
func TestEveryHeadTemplateLinksTheSelfConvoScript(t *testing.T) {
	for path, src := range headTemplates(t) {
		t.Run(path, func(t *testing.T) {
			if !strings.Contains(src, "/static/js/self-convo.js") {
				t.Errorf("%s owns a <head> but does not link self-convo.js — the "+
					"trigger form's start_at would always be interpreted as UTC", path)
			}
		})
	}
}

// cssCommentRE strips /* ... */ comments before any selector parsing below —
// without this, a comment merely MENTIONING a class name (as self-convo.css's
// own header now does, on purpose, explaining why it no longer redefines
// badge-failed) is indistinguishable from an actual rule defining it, since a
// C-style comment is not special syntax to a plain "text before the next {"
// scan.
var cssCommentRE = regexp.MustCompile(`(?s)/\*.*?\*/`)

// badgeSelectorRE matches a whole flat CSS rule (no nested braces, true of
// every hand-written file in web/static/css) so class names can be pulled
// from the SELECTOR half only, once comments are already stripped.
var badgeSelectorRE = regexp.MustCompile(`(?s)([^{}]+)\{[^{}]*\}`)

// badgeClassRE pulls every ".badge-xxx" token out of a selector string.
var badgeClassRE = regexp.MustCompile(`\.badge-[a-zA-Z0-9_-]+`)

// badgeClassesDefinedIn returns every distinct ".badge-*" class name that
// css actually DEFINES (appears in a rule's selector, not its body or a
// comment).
func badgeClassesDefinedIn(css string) map[string]bool {
	css = cssCommentRE.ReplaceAllString(css, "")
	out := map[string]bool{}
	for _, rule := range badgeSelectorRE.FindAllStringSubmatch(css, -1) {
		selector := rule[1]
		for _, cls := range badgeClassRE.FindAllString(selector, -1) {
			out[cls] = true
		}
	}
	return out
}

// TestSelfConvoStylesheetDoesNotRedefineBadgesDatatableAlreadyOwns pins the
// fix for a real duplicate: self-convo.css used to redefine .badge-failed
// (light and dark) when datatable.css:75/90 already owns it. Because
// self-convo.css is the TWELFTH stylesheet, loaded last, its copy always won
// the cascade — a future contrast fix landed only in datatable.css would
// silently leave the self-convo run-rows table on the old colour. Every
// badge class self-convo.css needs (scheduled/running/done/cancelled) is
// genuinely its own and stays; only a class datatable.css ALREADY defines
// would be a regression here.
func TestSelfConvoStylesheetDoesNotRedefineBadgesDatatableAlreadyOwns(t *testing.T) {
	datatable, err := fs.ReadFile(wsm.FS, "web/static/css/datatable.css")
	if err != nil {
		t.Fatalf("read datatable.css: %v", err)
	}
	selfConvo, err := fs.ReadFile(wsm.FS, "web/static/css/self-convo.css")
	if err != nil {
		t.Fatalf("read self-convo.css: %v", err)
	}

	owned := badgeClassesDefinedIn(string(datatable))
	if len(owned) == 0 {
		t.Fatalf("found no .badge-* rules in datatable.css — badgeSelectorRE/badgeClassRE regressed")
	}
	redefined := badgeClassesDefinedIn(string(selfConvo))

	for cls := range redefined {
		if owned[cls] {
			t.Errorf("self-convo.css redefines %s, which datatable.css already owns — "+
				"self-convo.css loads twelfth (last) and would silently win the cascade over "+
				"any future fix to datatable.css's rule", cls)
		}
	}
}
