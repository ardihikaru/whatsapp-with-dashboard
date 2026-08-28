package renderer_test

import (
	"strings"
	"testing"

	wsm "github.com/ardihikaru/whatsapp-with-dashboard"
)

// stripCSSComments removes /* ... */ spans the way a real CSS tokenizer
// does: left to right, the first "/*" pairs with the very NEXT "*/" it
// finds — not the one the author visually intended to close it. That
// distinction is exactly what let a stray "*/" inside a comment's prose
// (verify.css's old "--warning-*/--error-*") terminate the comment early.
// An unterminated trailing comment (no closing "*/" at all) consumes
// everything to EOF, matching a real parser's recovery at end of input.
func stripCSSComments(src string) string {
	var b strings.Builder
	for {
		start := strings.Index(src, "/*")
		if start < 0 {
			b.WriteString(src)
			break
		}
		b.WriteString(src[:start])
		rest := src[start+2:]
		end := strings.Index(rest, "*/")
		if end < 0 {
			break
		}
		src = rest[end+2:]
	}
	return b.String()
}

// preludeParsesAsASelector rejects text that could never appear in a real
// CSS selector prelude. Comment prose in this repo (see verify.css's own
// header, or any file under web/static/css) reliably contains an em dash or
// curly quotes; a genuine selector list — classes, ids, combinators,
// attribute matchers — never does. This is the check that makes the
// difference: when a stray "*/" closes a comment early, everything from
// there to the next real "{" becomes the prelude of the following rule, and
// a real browser discards a qualified rule outright when its prelude fails
// to parse as a selector list — it does not keep the block with an empty or
// partial selector. Without this check, the leaked prose plus the literal
// text ".badge-registered" would still be found by a plain substring
// search, which is exactly how this bug shipped unnoticed.
func preludeParsesAsASelector(prelude string) bool {
	if strings.TrimSpace(prelude) == "" {
		return false
	}
	for _, bad := range []string{"—", "–", "‘", "’", "“", "”"} {
		if strings.Contains(prelude, bad) {
			return false
		}
	}
	return true
}

// parsedTopLevelSelectors walks comment-stripped CSS and returns every
// individual selector (comma-split, trimmed) belonging to a TOP-LEVEL rule
// whose prelude actually parses. A rule whose prelude does not parse is
// dropped along with its block, same as a real browser: the block's
// declarations are never reachable through a selector that never existed.
func parsedTopLevelSelectors(css string) []string {
	css = stripCSSComments(css)
	var selectors []string
	depth := 0
	var prelude strings.Builder
	for _, r := range css {
		switch r {
		case '{':
			p := prelude.String()
			prelude.Reset()
			if depth == 0 && preludeParsesAsASelector(p) {
				for _, s := range strings.Split(p, ",") {
					if s = strings.TrimSpace(s); s != "" {
						selectors = append(selectors, s)
					}
				}
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
			prelude.Reset()
		default:
			if depth == 0 {
				prelude.WriteRune(r)
			}
		}
	}
	return selectors
}

// TestStripCSSCommentsClosesOnTheFirstStarSlashNotTheIntendedOne pins the
// comment stripper's own semantics against the exact shape of the bug: a
// comment body that itself contains "*/" before its real, intended closer.
// A real tokenizer closes the comment right there — it has no notion of
// which "*/" the author meant — so everything after it, including the
// prose the author thought was still inside the comment, comes back out as
// ordinary (non-comment) text. That leftover prose is real CSS's problem,
// not the stripper's: parsedTopLevelSelectors is what rejects it later.
func TestStripCSSCommentsClosesOnTheFirstStarSlashNotTheIntendedOne(t *testing.T) {
	src := "/* --warning-*/--error-* still open here */\n.rule { color: red; }"
	got := stripCSSComments(src)
	if strings.Contains(got, "--warning-") {
		t.Fatalf("stripCSSComments left text from BEFORE the first */ in the output — "+
			"it should have been consumed as the comment body: %q", got)
	}
	if !strings.Contains(got, "--error-* still open here */") {
		t.Fatalf("stripCSSComments did not leave the leaked prose (everything after the "+
			"early */) as ordinary text, which is what a real tokenizer does too: %q", got)
	}
	if !strings.Contains(got, ".rule { color: red; }") {
		t.Fatalf("stripCSSComments ate real CSS after the comment: %q", got)
	}
}

// TestAPreludeThatLeaksCommentProseIsRejected pins
// parsedTopLevelSelectors' second half: even after an early-closed comment
// leaks its prose into what follows, the following rule must not be
// reported as parsed, because a real browser discards a rule whose prelude
// fails to parse as a selector list — it does not keep the block.
func TestAPreludeThatLeaksCommentProseIsRejected(t *testing.T) {
	src := `/* deliberately uses --info-*, not --warning-*/--error-* — it must
	   read as "we don't have an answer". */
	.badge-registered { color: red; }
	.badge-unknown { color: blue; }`
	got := parsedTopLevelSelectors(src)
	for _, s := range got {
		if s == ".badge-registered" {
			t.Fatalf("parsedTopLevelSelectors reported .badge-registered as parsed, but its "+
				"prelude was comment prose leaked by an early-closed comment: %v", got)
		}
	}
	found := false
	for _, s := range got {
		if s == ".badge-unknown" {
			found = true
		}
	}
	if !found {
		t.Fatalf(".badge-unknown should still parse (it is its own, properly-terminated rule): %v", got)
	}
}

// TestEveryVerifyStatusHasAParsedBadgeRule pins the actual bug: verify.css
// used to read
//
//	"unknown" deliberately uses --info-*, not --warning-*/--error-* — it must
//
// and the "*/" inside "--warning-*/--error-*" closed the surrounding block
// comment early. Everything from there up to the next real "{" — including
// the comment's own intended closing "*/" a few lines later — became the
// prelude of the .badge-registered rule. That prelude cannot parse as a
// selector, so a real browser drops the entire rule, prelude and block
// together: .badge-registered never made it into the CSSOM at all. The
// "Registered" badge rendered as bare text with no pill background, while
// .badge-not_registered, .badge-invalid and .badge-unknown — each its own,
// separately-terminated rule — rendered fine.
//
// verify_rows.html (internal/renderer's sibling template) emits exactly
// these four badge-{{.Status}} classes; this test asserts each one has a
// selector that actually parses, once comments are stripped and rules whose
// prelude cannot parse as a selector are excluded. A plain
// strings.Contains(src, ".badge-registered") would pass against the broken
// file too — the literal text is still there, just inside a doomed prelude
// — which is exactly why that check would prove nothing.
func TestEveryVerifyStatusHasAParsedBadgeRule(t *testing.T) {
	b, err := wsm.FS.ReadFile("web/static/css/verify.css")
	if err != nil {
		t.Fatalf("read verify.css: %v", err)
	}
	selectors := parsedTopLevelSelectors(string(b))
	have := map[string]bool{}
	for _, s := range selectors {
		have[s] = true
	}

	for _, status := range []string{"registered", "not_registered", "invalid", "unknown"} {
		t.Run(status, func(t *testing.T) {
			want := ".badge-" + status
			if !have[want] {
				t.Errorf("no parsed %s rule found in verify.css — the verify_rows status %q "+
					"will render without its badge styling. Parsed selectors were: %v",
					want, status, selectors)
			}
		})
	}
}
