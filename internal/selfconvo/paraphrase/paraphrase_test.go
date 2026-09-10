package paraphrase

import (
	"math/rand/v2"
	"testing"
)

// distinctFormsFloor is set well below the 97 distinct forms actually
// measured over 200 seeds for the sentence below (go test -run
// TestMeasureDistinctForms during development; see the phase report for
// the raw count) — enough margin that a real regression (e.g. lexicon
// substitution silently becoming a no-op) trips it long before noise does.
const distinctFormsFloor = 40

func TestAPlaceholderSurvivesRewriteByteForByteEvenAdjacentToPunctuation(t *testing.T) {
	// {{apa}} deliberately reuses "apa", a real lexicon entry (the
	// question-word set) — a masking regression that lets word-level
	// substitution reach inside the braces would turn this into
	// "{{apaan}}" or similar under RegisterSlang, so this exercises real
	// protection rather than merely a var name the lexicon never touches.
	text := "Halo {{apa}}, gimana kabarnya {{apa}}?"
	for seed := uint64(0); seed < 100; seed++ {
		rng := rand.New(rand.NewPCG(seed, seed+1))
		reg := DrawRegister(rng)
		out := Rewrite(text, Options{Register: reg, MaxRunes: 4096}, rng)
		if got := countOccurrences(out, "{{apa}}"); got != 2 {
			t.Fatalf("seed %d register %s: Rewrite(%q) = %q, want exactly 2 occurrences of {{apa}} byte-for-byte, got %d",
				seed, reg, text, out, got)
		}
	}
}

func TestAURLATimeAndAPriceSurviveRewriteByteForByte(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		// Mixed-case letters INSIDE each protected span: the casing/
		// punctuation jitter step lower-cases the whole message under
		// kasual/slang, so a span left unprotected would come back
		// lower-cased and fail a byte-for-byte comparison — these cases
		// actually exercise that, unlike an all-lowercase or all-digit
		// span would.
		{"url", "Cek dulu di https://wa.me/ABC123xyz ya ka", "https://wa.me/ABC123xyz"},
		{"time", "Ketemuan jam 19:30WIB di stasiun ya", "19:30WIB"},
		{"price", "Cuma 50RB doang kok murah banget", "50RB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for seed := uint64(0); seed < 100; seed++ {
				rng := rand.New(rand.NewPCG(seed, seed+1))
				reg := DrawRegister(rng)
				out := Rewrite(tc.text, Options{Register: reg, MaxRunes: 4096}, rng)
				if countOccurrences(out, tc.want) != 1 {
					t.Fatalf("seed %d register %s: Rewrite(%q) = %q, want %q to survive byte-for-byte exactly once",
						seed, reg, tc.text, out, tc.want)
				}
			}
		})
	}
}

func TestRewriteIsDeterministicForAGivenSeed(t *testing.T) {
	text := "Halo, kamu mau ikut makan malam nanti gak?"
	for seed := uint64(0); seed < 50; seed++ {
		rng1 := rand.New(rand.NewPCG(seed, seed+1))
		reg1 := DrawRegister(rng1)
		out1 := Rewrite(text, Options{Register: reg1, MaxRunes: 4096}, rng1)

		rng2 := rand.New(rand.NewPCG(seed, seed+1))
		reg2 := DrawRegister(rng2)
		out2 := Rewrite(text, Options{Register: reg2, MaxRunes: 4096}, rng2)

		if out1 != out2 {
			t.Fatalf("seed %d: two runs from the same rng seed produced %q and %q, want identical output", seed, out1, out2)
		}
	}
}

func TestRewriteAcrossTwoHundredSeedsProducesAtLeastTheMeasuredVarietyFloor(t *testing.T) {
	text := "Halo, kamu mau ikut makan malam nanti gak?"
	seen := map[string]bool{}
	for seed := uint64(0); seed < 200; seed++ {
		rng := rand.New(rand.NewPCG(seed, seed+1))
		reg := DrawRegister(rng)
		out := Rewrite(text, Options{Register: reg, MaxRunes: 4096}, rng)
		seen[out] = true
	}
	if len(seen) < distinctFormsFloor {
		t.Fatalf("got %d distinct forms across 200 seeds, want at least %d", len(seen), distinctFormsFloor)
	}
}

func TestARewriteThatWouldExceedMaxRunesReturnsTheInputUnchanged(t *testing.T) {
	text := "Halo, apa kabar semua di rumah?"
	rng := rand.New(rand.NewPCG(1, 2))
	// MaxRunes shorter than the input itself: no rewrite of a non-empty
	// text can ever fit, so every seed must fall back to the input.
	out := Rewrite(text, Options{Register: RegisterSlang, MaxRunes: 3}, rng)
	if out != text {
		t.Fatalf("Rewrite with an unsatisfiable MaxRunes = %q, want the original input %q unchanged", out, text)
	}
}

func TestFormalRegisterKeepsSentenceCaseAndTerminalPunctuation(t *testing.T) {
	text := "Apakah Anda sudah makan malam?"
	for seed := uint64(0); seed < 100; seed++ {
		rng := rand.New(rand.NewPCG(seed, seed+1))
		out := Rewrite(text, Options{Register: RegisterFormal, MaxRunes: 4096}, rng)
		if out == "" {
			t.Fatalf("seed %d: Rewrite returned empty string", seed)
		}
		if out[len(out)-1] != '?' {
			t.Fatalf("seed %d: Rewrite(%q) = %q, want the terminal \"?\" preserved under RegisterFormal", seed, text, out)
		}
		if r := []rune(out)[0]; r < 'A' || r > 'Z' {
			// The sentence starts with "Apakah"/"Anda" either way (no
			// leading-greeting rule fires on this text), so formal must
			// never lower-case it.
			t.Fatalf("seed %d: Rewrite(%q) = %q, want sentence case preserved under RegisterFormal", seed, text, out)
		}
	}
}

func TestRewriteWithNoRegisterDrawnReturnsTheInputUnchanged(t *testing.T) {
	text := "Halo, apa kabar?"
	rng := rand.New(rand.NewPCG(1, 2))
	out := Rewrite(text, Options{MaxRunes: 4096}, rng)
	if out != text {
		t.Fatalf("Rewrite with Options.Register left at its zero value = %q, want the input %q returned unchanged", out, text)
	}
}

func countOccurrences(haystack, needle string) int {
	count := 0
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			count++
			i += len(needle) - 1
		}
	}
	return count
}
