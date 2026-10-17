// Package contactmatch is the pure, backend-agnostic scorer that answers
// "how well does this contact's name/alias fields match a search query" —
// used both by GET /api/v1/contacts' ranked ?q= (internal/handler) and by
// wsm-mcp's waxum_contacts tool (internal/mcp/registry) so a name like
// "Ria" ranks the operator's own saved contact above an unrelated synced
// WhatsApp contact that merely contains the substring "ria".
//
// Nothing in this package talks to a database, waxum, or WSM's own HTTP
// layer — it is given a query string and a list of candidates, each
// carrying a flat list of named text fields, and returns a score plus
// which field produced it, or no match at all.
package contactmatch

import (
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Score tiers, highest first. A field match only ever receives ONE of
// these per query token (the best one that applies); see matchToken.
const (
	// ScoreAliasExact is an alias field (Field.Alias == true) whose
	// normalized value equals the FULL normalized query. Ranks above a
	// plain name match on the theory that an alias is a name the operator
	// deliberately chose to search by.
	ScoreAliasExact = 100
	// ScoreWholeExact is any non-alias field whose normalized value equals
	// the full normalized query.
	ScoreWholeExact = 95
	// ScoreTokenExact is a per-token exact match: one whitespace/punctuation
	// -delimited token of the field equals one token of the query.
	ScoreTokenExact = 90
	// ScoreTokenPrefix is a field token that starts with a query token of
	// at least 2 characters (a 1-character query token is too likely to
	// prefix-match everything to be meaningful).
	ScoreTokenPrefix = 70
	// ScoreSubstring is the query token appearing anywhere inside the
	// field's normalized text, with no token-boundary alignment at all —
	// the weakest tier that still counts as a match.
	ScoreSubstring = 40
)

// Field is one named, searchable text value on a candidate — e.g. a
// contact's Name, or one of its comma-split Aliases, or a waxum
// StoredContact's FullName/FirstName/PushName/BusinessName/Phone. Alias
// fields win ties against a non-alias exact match (ScoreAliasExact above
// ScoreWholeExact) and are the only fields eligible for ScoreAliasExact at
// all.
type Field struct {
	Name  string // reported back as Result.Field, e.g. "name", "alias", "full_name"
	Value string
	Alias bool
}

// Result is one candidate's best match against a query.
type Result struct {
	Score int
	// Field is the Field.Name that produced Score.
	Field string
}

// transliterate runs s through NFKD decomposition, strips every combining
// mark (so "Réné" and "rene" compare equal), and recomposes — the same
// transform.Chain(norm.NFKD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
// idiom golang.org/x/text/runes' own doc comment demonstrates. A
// transform error (malformed UTF-8) falls back to the untransliterated
// input rather than failing the whole match — a user-visible name/alias is
// never rejected outright for one bad rune.
func transliterate(s string) string {
	t := transform.Chain(norm.NFKD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	out, _, err := transform.String(t, s)
	if err != nil {
		return s
	}
	return out
}

// normalize lowercases a transliterated string. Used for whole-value
// equality checks (ScoreAliasExact/ScoreWholeExact) and as the base string
// substring matching (ScoreSubstring) runs against.
func normalize(s string) string {
	return strings.ToLower(transliterate(s))
}

// tokenize splits a normalized string into maximal runs of letters/digits,
// dropping everything else (whitespace, punctuation, emoji) entirely — so
// "Ria ❤️" tokenizes to ["ria"], never ["ria", "❤"], and an emoji in a
// saved contact name can never itself become a spurious match target.
func tokenize(s string) []string {
	var tokens []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return tokens
}

// matchToken scores one query token against one field's token list plus
// its normalized full value (for substring fallback), returning the best
// tier that applies, or 0 for no match at all.
func matchToken(qToken string, valueTokens []string, normalizedValue string) int {
	best := 0
	for _, vt := range valueTokens {
		switch {
		case vt == qToken:
			if ScoreTokenExact > best {
				best = ScoreTokenExact
			}
		case len(qToken) >= 2 && strings.HasPrefix(vt, qToken):
			if ScoreTokenPrefix > best {
				best = ScoreTokenPrefix
			}
		}
	}
	if best == 0 && strings.Contains(normalizedValue, qToken) {
		best = ScoreSubstring
	}
	return best
}

// matchField scores one field against the query's tokens. Every query
// token must match the field at SOME tier — a multi-token query like
// "Garsinia Ely" against a field matching only "Garsinia" is NOT a match
// for that field at all, the same "every q token must match something"
// rule the matcher's own package doc references. When every token does
// match, the field's score is the AVERAGE of its per-token scores
// (rounded down) rather than the weakest one alone, so a field matching
// one token at ScoreTokenExact and another at ScoreTokenPrefix still
// outranks a field matching both tokens only at ScoreSubstring. A whole-
// value exact match short-circuits this entirely (ScoreAliasExact/
// ScoreWholeExact) since that is strictly the strongest possible claim
// regardless of how many tokens the query has.
func matchField(qTokens []string, normalizedQuery string, f Field) (int, bool) {
	nv := normalize(f.Value)
	if nv == "" {
		return 0, false
	}
	if nv == normalizedQuery {
		if f.Alias {
			return ScoreAliasExact, true
		}
		return ScoreWholeExact, true
	}
	valueTokens := tokenize(nv)
	sum := 0
	for _, qt := range qTokens {
		s := matchToken(qt, valueTokens, nv)
		if s == 0 {
			return 0, false
		}
		sum += s
	}
	return sum / len(qTokens), true
}

// Match scores one candidate's fields against query and returns its best
// field's Result, or ok=false when NO field matched every query token at
// any tier — an empty query always matches nothing (callers wanting
// "everything, unranked" should not call Match at all for q=="").
func Match(query string, fields []Field) (Result, bool) {
	nq := normalize(query)
	if nq == "" {
		return Result{}, false
	}
	qTokens := tokenize(nq)
	if len(qTokens) == 0 {
		return Result{}, false
	}
	var best Result
	found := false
	for _, f := range fields {
		score, ok := matchField(qTokens, nq, f)
		if !ok {
			continue
		}
		if !found || score > best.Score {
			best = Result{Score: score, Field: f.Name}
			found = true
		}
	}
	return best, found
}
