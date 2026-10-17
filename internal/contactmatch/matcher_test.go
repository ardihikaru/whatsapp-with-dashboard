package contactmatch

import "testing"

// candidate is a tiny test fixture: a name plus Match's own Field list.
type candidate struct {
	label  string
	fields []Field
}

func nameField(v string) []Field { return []Field{{Name: "name", Value: v}} }

// TestRiaRanksAboveLongerPrefixAndSubstringMatches is the exact ranking
// example the task names: q="Ria" must rank the exact-token "Ria ❤️" above
// the prefix match "Garsinia Ely Riani", which in turn must rank above the
// substring-only "Maria".
func TestRiaRanksAboveLongerPrefixAndSubstringMatches(t *testing.T) {
	candidates := []candidate{
		{label: "Ria ❤️", fields: nameField("Ria ❤️")},
		{label: "Garsinia Ely Riani", fields: nameField("Garsinia Ely Riani")},
		{label: "Maria", fields: nameField("Maria")},
	}
	scores := map[string]int{}
	for _, c := range candidates {
		res, ok := Match("Ria", c.fields)
		if !ok {
			t.Fatalf("Match(%q, %q) did not match at all, want a hit", "Ria", c.label)
		}
		scores[c.label] = res.Score
	}
	if !(scores["Ria ❤️"] > scores["Garsinia Ely Riani"]) {
		t.Errorf(`scores = %+v, want "Ria ❤️" > "Garsinia Ely Riani"`, scores)
	}
	if !(scores["Garsinia Ely Riani"] > scores["Maria"]) {
		t.Errorf(`scores = %+v, want "Garsinia Ely Riani" > "Maria"`, scores)
	}
}

// TestAccentedNameMatchesItsPlainAsciiQuery pins NFKD transliteration:
// "rene" must match a contact literally named "Réné" — as a whole-value
// exact match (ScoreWholeExact), since the entire (transliterated) name
// equals the entire query, not merely one of its tokens.
func TestAccentedNameMatchesItsPlainAsciiQuery(t *testing.T) {
	res, ok := Match("rene", nameField("Réné"))
	if !ok {
		t.Fatalf(`Match("rene", "Réné") did not match, want a hit`)
	}
	if res.Score != ScoreWholeExact {
		t.Errorf("score = %d, want ScoreWholeExact (%d)", res.Score, ScoreWholeExact)
	}
}

// TestMatchIsCaseInsensitive pins case-folding on both the query and the
// field value, as a whole-value exact match (full query == full value).
func TestMatchIsCaseInsensitive(t *testing.T) {
	res, ok := Match("RIA", nameField("ria"))
	if !ok {
		t.Fatal(`Match("RIA", "ria") did not match, want a hit`)
	}
	if res.Score != ScoreWholeExact {
		t.Errorf("score = %d, want ScoreWholeExact (%d)", res.Score, ScoreWholeExact)
	}
}

// TestTokenExactRanksBelowWholeValueExact pins the DISTINCT token-exact
// tier (ScoreTokenExact) on a name that has MORE than one token, so the
// query matches one token exactly but not the whole field value.
func TestTokenExactRanksBelowWholeValueExact(t *testing.T) {
	res, ok := Match("ria", nameField("Ria Wijaya"))
	if !ok {
		t.Fatal(`Match("ria", "Ria Wijaya") did not match, want a hit`)
	}
	if res.Score != ScoreTokenExact {
		t.Errorf("score = %d, want ScoreTokenExact (%d)", res.Score, ScoreTokenExact)
	}
}

// TestNoMatchIsExcluded pins that a query sharing nothing with the field
// returns ok=false, not a zero-score hit a caller might accidentally still
// render.
func TestNoMatchIsExcluded(t *testing.T) {
	if _, ok := Match("zzz-nope", nameField("Ria")); ok {
		t.Fatal(`Match("zzz-nope", "Ria") matched, want no match at all`)
	}
}

// TestAliasExactOutranksWholeNameExact is the task's documented tier
// ordering: an exact alias match (100) beats an exact whole-name match on
// a DIFFERENT candidate (95), even though both are "exact".
func TestAliasExactOutranksWholeNameExact(t *testing.T) {
	aliasHit, ok := Match("ria <3", []Field{{Name: "alias", Value: "ria <3", Alias: true}})
	if !ok {
		t.Fatal("alias candidate did not match")
	}
	nameHit, ok := Match("ria <3", []Field{{Name: "name", Value: "ria <3"}})
	if !ok {
		t.Fatal("name candidate did not match")
	}
	if aliasHit.Score != ScoreAliasExact {
		t.Errorf("alias exact score = %d, want %d", aliasHit.Score, ScoreAliasExact)
	}
	if nameHit.Score != ScoreWholeExact {
		t.Errorf("whole-name exact score = %d, want %d", nameHit.Score, ScoreWholeExact)
	}
	if !(aliasHit.Score > nameHit.Score) {
		t.Errorf("alias exact (%d) must outrank whole-name exact (%d)", aliasHit.Score, nameHit.Score)
	}
}

// TestMultiTokenQueryRequiresEveryTokenToMatchSomething pins the
// documented multi-token rule: "Garsinia Zzz" must NOT match "Garsinia Ely
// Riani" even though "Garsinia" alone matches perfectly — "Zzz" matches
// nothing on that candidate, so the whole field is excluded.
func TestMultiTokenQueryRequiresEveryTokenToMatchSomething(t *testing.T) {
	if _, ok := Match("Garsinia Zzz", nameField("Garsinia Ely Riani")); ok {
		t.Fatal(`Match("Garsinia Zzz", "Garsinia Ely Riani") matched, want no match (one token has no hit at all)`)
	}
	// Sanity check the positive case still matches on its own.
	if _, ok := Match("Garsinia Ely", nameField("Garsinia Ely Riani")); !ok {
		t.Fatal(`Match("Garsinia Ely", "Garsinia Ely Riani") did not match, want a hit`)
	}
}

// TestMatchedFieldNamesTheWinningField pins Result.Field: the best-scoring
// field's Name travels with the score so a caller (e.g. waxum_contacts) can
// report matched_field.
func TestMatchedFieldNamesTheWinningField(t *testing.T) {
	fields := []Field{
		{Name: "name", Value: "Someone Else"},
		{Name: "alias", Value: "Ria", Alias: true},
	}
	res, ok := Match("Ria", fields)
	if !ok {
		t.Fatal("did not match, want a hit")
	}
	if res.Field != "alias" {
		t.Errorf("Field = %q, want %q (the exact-alias field, not the non-matching name)", res.Field, "alias")
	}
}

// TestEmptyQueryNeverMatches documents Match's own contract: callers
// wanting an unranked "everything" list must not call Match with q=="" at
// all, and this pins that doing so anyway is inert rather than silently
// matching every candidate.
func TestEmptyQueryNeverMatches(t *testing.T) {
	if _, ok := Match("", nameField("Ria")); ok {
		t.Fatal(`Match("", "Ria") matched, want no match at all for an empty query`)
	}
}
