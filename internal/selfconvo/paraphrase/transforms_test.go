package paraphrase

import (
	"math/rand/v2"
	"strings"
	"testing"
)

// TestApplyClauseReorderMovesATrailingTimeAdverbialToTheFront is the first
// of the two required reorder rules, with a concrete before/after.
func TestApplyClauseReorderMovesATrailingTimeAdverbialToTheFront(t *testing.T) {
	in := "Kita ketemu di kafe, besok."
	want := "Besok, kita ketemu di kafe."
	if got := applyClauseReorder(in); got != want {
		t.Fatalf("applyClauseReorder(%q) = %q, want %q", in, got, want)
	}
}

// TestApplyClauseReorderMovesALeadingTimeAdverbialToTheEnd is the inverse
// rule, with its own concrete before/after.
func TestApplyClauseReorderMovesALeadingTimeAdverbialToTheEnd(t *testing.T) {
	in := "Besok, kita ketemu di kafe."
	want := "Kita ketemu di kafe, besok."
	if got := applyClauseReorder(in); got != want {
		t.Fatalf("applyClauseReorder(%q) = %q, want %q", in, got, want)
	}
}

func TestApplyClauseReorderRecognisesTheMultiWordAdverbialNantiSore(t *testing.T) {
	in := "Kita ketemu nanti sore."
	want := "Nanti sore, kita ketemu."
	if got := applyClauseReorder(in); got != want {
		t.Fatalf("applyClauseReorder(%q) = %q, want %q", in, got, want)
	}
}

func TestApplyClauseReorderLeavesATextWithNoAdverbialUnchanged(t *testing.T) {
	in := "Kita ketemu di kafe, mungkin."
	if got := applyClauseReorder(in); got != in {
		t.Fatalf("applyClauseReorder(%q) = %q, want it unchanged (no recognised time adverbial)", in, got)
	}
}

func TestApplyClauseReorderLeavesATextThatIsOnlyTheAdverbialUnchanged(t *testing.T) {
	in := "Besok."
	if got := applyClauseReorder(in); got != in {
		t.Fatalf("applyClauseReorder(%q) = %q, want it unchanged (nothing to move it in front of)", in, got)
	}
}

func TestApplyCasingPunctuationNeverTouchesFormalRegister(t *testing.T) {
	text := "Apakah Anda sudah makan malam?"
	for seed := uint64(0); seed < 50; seed++ {
		rng := rand.New(rand.NewPCG(seed, seed+1))
		if got := applyCasingPunctuation(text, RegisterFormal, rng); got != text {
			t.Fatalf("seed %d: applyCasingPunctuation(%q, RegisterFormal) = %q, want unchanged", seed, text, got)
		}
	}
}

func TestApplyAbbreviationNeverFiresOnFormalRegister(t *testing.T) {
	text := "Saya akan pergi dengan yang lain, saja begitu."
	if got := applyAbbreviation(text, RegisterFormal); got != text {
		t.Fatalf("applyAbbreviation(%q, RegisterFormal) = %q, want unchanged", text, got)
	}
}

func TestApplyAbbreviationRewritesTheFourWordsUnderKasualAndSlang(t *testing.T) {
	text := "Yang itu dengan saja tidak ada"
	want := "Yg itu dgn aja gak ada"
	for _, reg := range []Register{RegisterKasual, RegisterSlang} {
		if got := applyAbbreviation(text, reg); got != want {
			t.Fatalf("applyAbbreviation(%q, %s) = %q, want %q", text, reg, got, want)
		}
	}
}

func TestApplyTypoNeverChangesTextWhenRateIsZero(t *testing.T) {
	text := "Halo apa kabar semua"
	rng := rand.New(rand.NewPCG(1, 2))
	if got := applyTypo(text, 0, rng); got != text {
		t.Fatalf("applyTypo(%q, 0) = %q, want unchanged (TypoRate 0 disables it)", text, got)
	}
}

func TestApplyTypoNeverTouchesAMaskToken(t *testing.T) {
	text := "Halo " + maskStart + "0" + maskEnd + " apa kabar"
	rng := rand.New(rand.NewPCG(1, 2))
	got := applyTypo(text, 1.0, rng) // rate=1: every eligible letter mutates
	if !containsSubstring(got, maskStart+"0"+maskEnd) {
		t.Fatalf("applyTypo(%q, 1.0) = %q, want the mask token to survive untouched", text, got)
	}
}

// TestApplyParticleNeverProducesADoubleSpaceWhenTheTextAlreadyEndsInASpace
// is the regression test for a bug caught by
// TestTriggerWithParaphraseDisabledStoresThePlainlySubstitutedBodyExactly's
// sibling in package selfconvo during development: a greeting swap that
// consumed a whole one-word message (leaving "Woy, " with nothing after
// the comma) fed straight into a naive TrimRight(".!?") that didn't
// recognise the trailing space as removable, so the particle was rejoined
// with TWO spaces ("Woy,  dong") instead of one.
func TestApplyParticleNeverProducesADoubleSpaceWhenTheTextAlreadyEndsInASpace(t *testing.T) {
	text := "Woy, "
	rng := rand.New(rand.NewPCG(2, 3)) // seed chosen (by trial) to actually draw a particle here
	got := applyParticle(text, RegisterSlang, rng)
	if strings.Contains(got, "  ") {
		t.Fatalf("applyParticle(%q) = %q, want no double space", text, got)
	}
}

func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
