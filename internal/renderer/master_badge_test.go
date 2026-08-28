package renderer_test

import (
	"testing"

	wsm "github.com/ardihikaru/whatsapp-with-dashboard"
)

// TestTheMasterBadgeHasAParsedRule pins session_rows.html's
// `badge-master` class (the /sessions routing-exclusion flag) to an
// actually-parsed rule in datatable.css, the same way
// TestEveryVerifyStatusHasAParsedBadgeRule pins verify.css's status badges —
// see that test's doc comment for why a plain strings.Contains would not
// catch a rule dropped by an early-closed comment.
func TestTheMasterBadgeHasAParsedRule(t *testing.T) {
	b, err := wsm.FS.ReadFile("web/static/css/datatable.css")
	if err != nil {
		t.Fatalf("read datatable.css: %v", err)
	}
	selectors := parsedTopLevelSelectors(string(b))
	have := map[string]bool{}
	for _, s := range selectors {
		have[s] = true
	}
	if !have[".badge-master"] {
		t.Errorf("no parsed .badge-master rule found in datatable.css — session_rows.html's "+
			"master badge will render without its pill styling. Parsed selectors were: %v", selectors)
	}
}
