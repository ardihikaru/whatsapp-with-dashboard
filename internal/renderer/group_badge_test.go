package renderer_test

import (
	"testing"

	wsm "github.com/ardihikaru/whatsapp-with-dashboard"
)

// TestTheGroupBadgeHasAParsedRule is TestTheMasterBadgeHasAParsedRule's twin
// for session_rows.html's `badge-group` class (the /sessions routing-scope
// flag) — see that test's doc comment for why a plain strings.Contains would
// not catch a rule dropped by an early-closed comment.
func TestTheGroupBadgeHasAParsedRule(t *testing.T) {
	b, err := wsm.FS.ReadFile("web/static/css/datatable.css")
	if err != nil {
		t.Fatalf("read datatable.css: %v", err)
	}
	selectors := parsedTopLevelSelectors(string(b))
	have := map[string]bool{}
	for _, s := range selectors {
		have[s] = true
	}
	if !have[".badge-group"] {
		t.Errorf("no parsed .badge-group rule found in datatable.css — session_rows.html's "+
			"group badge will render without its pill styling. Parsed selectors were: %v", selectors)
	}
}
