package paraphrase

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// PlaceholderPattern is the exact regexp source of a `{{var_name}}`
// substitution placeholder. It is declared HERE, not in
// internal/selfconvo, and internal/selfconvo's own placeholderRE is kept as
// a separately compiled regexp built from the same literal source — this
// package must not import its parent (selfconvo imports paraphrase, a
// paraphrase->selfconvo import would be a cycle), so the two regexps can't
// share a compiled *regexp.Regexp value. Keeping the PATTERN STRING here as
// the single source of truth, with selfconvo's copy asserted equal to it in
// TestPlaceholderPatternMatchesSelfconvosPlaceholderRE (package selfconvo),
// is how the two are kept from drifting apart silently — the alternative
// (duplicating the literal in both files with no cross-check) is exactly
// the kind of "two hand-maintained copies" hazard this codebase has already
// been bitten by elsewhere (see CLAUDE.md's schema-mirror entry).
const PlaceholderPattern = `\{\{\s*([a-z_][a-z0-9_]*)\s*\}\}`

var (
	placeholderRE = regexp.MustCompile(PlaceholderPattern)
	// urlRE matches http(s):// and www. URLs. Deliberately greedy to the
	// next whitespace rather than RFC-precise — a paraphrase engine only
	// needs to avoid mangling a URL, not validate one.
	urlRE = regexp.MustCompile(`(?i)https?://\S+|www\.\S+`)
	// emailRE matches a plain address. As with urlRE, generous rather than
	// RFC-precise.
	emailRE = regexp.MustCompile(`(?i)[\w.+-]+@[\w-]+\.[\w.-]+`)
	// digitRunRE protects any whitespace-delimited token containing at
	// least one digit — times (19:30), dates, prices (50rb), phone numbers.
	// Deliberately greedy across the whole token (not just the digits) so
	// attached punctuation/units travel with it untouched.
	digitRunRE = regexp.MustCompile(`\S*\d\S*`)
	// backtickRE and dquoteRE protect anything the author wrapped in
	// backticks or straight double quotes — treated as a literal the
	// author wanted preserved verbatim.
	backtickRE = regexp.MustCompile("`[^`]*`")
	dquoteRE   = regexp.MustCompile(`"[^"]*"`)

	// wordRE finds a rewritable Indonesian word: a run of letters, with
	// interior apostrophes/hyphens allowed (e.g. "nge-charge", "nggak'in"
	// are unusual but not word-breaking). It only ever matches inside
	// already-masked text, so it can never match INTO a protected span —
	// mask tokens (see below) contain no letters.
	wordRE = regexp.MustCompile(`\p{L}+(?:['\-]\p{L}+)*`)

	// maskRE finds a mask token in already-masked text, for unmask.
	maskRE = regexp.MustCompile(maskStart + `(\d+)` + maskEnd)
)

// maskStart/maskEnd delimit a mask token. \x00/\x01 are not valid inside
// ordinary WhatsApp message text, are not letters or digits (so wordRE and
// every digit-aware step ignore them), and are not regexp metacharacters
// (so they're safe to splice into pattern strings literally).
const (
	maskStart = "\x00"
	maskEnd   = "\x01"
)

// span is a half-open byte range [start, end) in the original text.
type span struct{ start, end int }

// maskProtectedSpans finds every protected span in text (placeholders,
// backtick/quoted spans, URLs, emails, digit runs — in that precedence
// order, so a looser pattern never re-carves text a tighter one already
// claimed) and replaces each with an atomic mask token, returning the
// masked text and the slice of original substrings (indexed by the digits
// inside each mask token) needed to restore them via unmask. No
// transformation downstream of this call can see inside a protected span,
// let alone insert text into one — mask tokens are opaque to every other
// regexp this package uses.
func maskProtectedSpans(text string) (string, []string) {
	patterns := []*regexp.Regexp{placeholderRE, backtickRE, dquoteRE, urlRE, emailRE, digitRunRE}

	claimed := make([]bool, len(text)+1)
	var spans []span
	for _, re := range patterns {
		for _, loc := range re.FindAllStringIndex(text, -1) {
			s, e := loc[0], loc[1]
			overlap := false
			for i := s; i < e; i++ {
				if claimed[i] {
					overlap = true
					break
				}
			}
			if overlap {
				continue
			}
			for i := s; i < e; i++ {
				claimed[i] = true
			}
			spans = append(spans, span{s, e})
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })

	var b strings.Builder
	originals := make([]string, 0, len(spans))
	last := 0
	for _, sp := range spans {
		b.WriteString(text[last:sp.start])
		idx := len(originals)
		originals = append(originals, text[sp.start:sp.end])
		b.WriteString(maskStart)
		b.WriteString(strconv.Itoa(idx))
		b.WriteString(maskEnd)
		last = sp.end
	}
	b.WriteString(text[last:])
	return b.String(), originals
}

// unmask reverses maskProtectedSpans, restoring every mask token to its
// original byte-for-byte substring. A mask token whose index is out of
// range (unreachable in practice — every token unmask ever sees was
// produced by maskProtectedSpans against the same originals slice) is left
// as-is rather than panicking.
func unmask(text string, originals []string) string {
	return maskRE.ReplaceAllStringFunc(text, func(m string) string {
		sub := maskRE.FindStringSubmatch(m)
		i, err := strconv.Atoi(sub[1])
		if err != nil || i < 0 || i >= len(originals) {
			return m
		}
		return originals[i]
	})
}
