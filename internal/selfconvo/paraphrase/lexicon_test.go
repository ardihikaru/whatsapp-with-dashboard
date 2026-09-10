package paraphrase

import (
	"math/rand/v2"
	"strings"
	"testing"
)

func TestLexiconHasAtLeastOneHundredFiftyEntries(t *testing.T) {
	if got := len(lexicon); got < 150 {
		t.Fatalf("lexicon has %d entries, want at least 150", got)
	}
}

// TestNoSurfaceWordIsSharedByTwoDifferentLexiconEntries guards the exact
// bug this package shipped with once during development: "oke" was both
// its own entry AND listed as another entry's variant, so
// lexiconIndex["oke"] silently pointed at whichever entry's init() loop
// ran last, and substituting the word "oke" produced a token from the
// WRONG entry's register lists — the precise failure
// TestSubstituteLexiconNeverEmitsATokenOutsideTheTargetRegistersOwnListAcrossTheWholeLexicon
// caught. Every surface form (lemma or any register's variant) must belong
// to exactly one entry.
func TestNoSurfaceWordIsSharedByTwoDifferentLexiconEntries(t *testing.T) {
	owner := map[string]string{} // lowercased word -> owning lemma
	for i := range lexicon {
		e := &lexicon[i]
		words := append([]string{e.Lemma}, e.Formal...)
		words = append(words, e.Kasual...)
		words = append(words, e.Slang...)
		for _, w := range words {
			key := strings.ToLower(w)
			if prior, ok := owner[key]; ok && prior != e.Lemma {
				t.Fatalf("word %q belongs to both entry %q and entry %q — lexiconIndex can only point at one",
					key, prior, e.Lemma)
			}
			owner[key] = e.Lemma
		}
	}
}

// TestSubstituteLexiconNeverEmitsATokenOutsideTheTargetRegistersOwnListAcrossTheWholeLexicon
// is the "drive it over the whole lexicon" check: for every entry and every
// register, substituting that entry's own lemma must always produce either
// a member of that register's variant list, or (when the register has no
// variants at all) the untouched original — never a word that belongs to a
// DIFFERENT register's list. This is the property that keeps a slang
// rewrite from ever emitting a formal-only token, and vice versa.
func TestSubstituteLexiconNeverEmitsATokenOutsideTheTargetRegistersOwnListAcrossTheWholeLexicon(t *testing.T) {
	if lexiconIndex == nil {
		t.Fatal("lexicon failed to load at init — lexiconIndex is nil")
	}
	regs := []Register{RegisterFormal, RegisterKasual, RegisterSlang}
	for i := range lexicon {
		e := &lexicon[i]
		for _, reg := range regs {
			variants := e.variants(reg)
			for seed := uint64(0); seed < 20; seed++ {
				rng := rand.New(rand.NewPCG(seed, seed+1))
				out := substituteLexicon(e.Lemma, reg, rng)
				if len(variants) == 0 {
					if out != e.Lemma {
						t.Fatalf("lemma %q register %s seed %d: substituteLexicon = %q, want the untouched lemma (no variants for this register)",
							e.Lemma, reg, seed, out)
					}
					continue
				}
				if !containsFold(variants, out) {
					t.Fatalf("lemma %q register %s seed %d: substituteLexicon = %q, want one of %v",
						e.Lemma, reg, seed, out, variants)
				}
			}
		}
	}
}

func TestSubstituteLexiconPreservesCapitalisationOfTheMatchedWord(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	out := substituteLexicon("Aku mau pergi", RegisterFormal, rng)
	if !strings.HasPrefix(out, "Saya") {
		t.Fatalf("substituteLexicon(%q) = %q, want it to start with the capitalised replacement \"Saya\"", "Aku mau pergi", out)
	}
}

func TestSubstituteLexiconLeavesAnUnknownWordUnchanged(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	text := "Zephyrion quibble"
	if out := substituteLexicon(text, RegisterSlang, rng); out != text {
		t.Fatalf("substituteLexicon(%q) = %q, want unchanged text for words absent from the lexicon", text, out)
	}
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}
