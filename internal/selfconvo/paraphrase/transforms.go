package paraphrase

import (
	"math/rand/v2"
	"regexp"
	"strings"
	"unicode"
)

// capitalizeFirst/decapitalizeFirst adjust only the first rune of s,
// leaving the rest untouched — used when moving a fragment to a position
// where it needs (or loses) a sentence-initial capital, without
// re-casing words further in that only happen to already be capitalised
// (a name inside the sentence, say).
func capitalizeFirst(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

func decapitalizeFirst(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return strings.ToLower(string(r[0])) + string(r[1:])
}

// --- Step 4: opener/greeting handling ---------------------------------

// greetingWords is the register-appropriate greeting pool applyGreeting
// draws from, both when swapping an existing greeting and when adding one
// to a message that has none. Formal has no add/swap greeting at all —
// distinct from RegisterFormal simply not being represented below (an
// empty/absent list, not an entry with an empty slice, so the len() == 0
// guards in applyGreeting fire identically either way).
var greetingWords = map[Register][]string{
	RegisterKasual: {"Halo", "Hai", "Pagi", "Siang", "Sore", "Malam"},
	RegisterSlang:  {"Woy", "Gan", "Bro", "Hai", "Halo"},
}

// greetingLeadRE detects an EXISTING leading greeting, in any register, so
// applyGreeting can decide whether to swap or to (maybe) add one. Matches
// only at the very start of text and consumes any trailing comma/colon and
// following spaces along with it.
var greetingLeadRE = regexp.MustCompile(`(?i)^(halo|hai|hi|selamat\s+(?:pagi|siang|sore|malam)|permisi|woy+|gan|bro|pagi|siang|sore|malam)\s*[,:]?\s*`)

const (
	greetingSwapProb = 0.5  // probability an existing greeting is touched at all
	greetingDropProb = 0.3  // within the swap branch: drop instead of replace
	greetingAddProb  = 0.25 // probability a greeting-less message gains one
)

// applyGreeting swaps a leading greeting for a register-appropriate one (or
// drops it), or — for a message with none — occasionally adds one, all
// probability-driven via rng. Formal register never adds or swaps (an
// operator who authored a formal script chose its greeting deliberately).
func applyGreeting(text string, reg Register, rng *rand.Rand) string {
	list := greetingWords[reg]

	if loc := greetingLeadRE.FindStringIndex(text); loc != nil {
		if rng.Float64() >= greetingSwapProb {
			return text
		}
		rest := text[loc[1]:]
		if len(list) == 0 || rng.Float64() < greetingDropProb {
			return capitalizeFirst(rest)
		}
		g := list[rng.IntN(len(list))]
		return g + ", " + decapitalizeFirst(rest)
	}

	if len(list) == 0 {
		return text
	}
	if rng.Float64() >= greetingAddProb {
		return text
	}
	g := list[rng.IntN(len(list))]
	return g + ", " + decapitalizeFirst(text)
}

// --- Step 5: filler & particle insertion -------------------------------

// fillerWords/particleWords are the register-gated pools applyFiller and
// applyParticle draw from. Formal has neither — a script authored formally
// never picks up "eh," or a trailing "dong".
var fillerWords = map[Register][]string{
	RegisterKasual: {"Eh", "Btw", "Oiya"},
	RegisterSlang:  {"Eh", "Btw", "Oiya", "Anyway"},
}

var particleWords = map[Register][]string{
	RegisterKasual: {"ya", "kan", "deh"},
	RegisterSlang:  {"dong", "nih", "sih", "deh", "kan", "lho"},
}

var fillerLeadRE = regexp.MustCompile(`(?i)^(eh|btw|oiya|anyway)\s*[,:]?\s*`)

// trailingParticleRE detects a particle ALREADY at the end of the message
// (any of the words this package would itself add), so applyParticle never
// stacks a second one on top of one an author or an earlier rewrite left
// there.
var trailingParticleRE = regexp.MustCompile(`(?i)\b(ya|dong|nih|sih|deh|kan|lho)\s*[.!?]*\s*$`)

const (
	fillerProb   = 0.35
	particleProb = 0.35
)

// applyFillersAndParticles runs applyFiller then applyParticle. Each is
// independently gated and each inserts at most one word — "at most one of
// each" per Rewrite call falls out of that, not out of extra bookkeeping.
func applyFillersAndParticles(text string, reg Register, rng *rand.Rand) string {
	text = applyFiller(text, reg, rng)
	text = applyParticle(text, reg, rng)
	return text
}

func applyFiller(text string, reg Register, rng *rand.Rand) string {
	list := fillerWords[reg]
	if len(list) == 0 || fillerLeadRE.MatchString(text) || rng.Float64() >= fillerProb {
		return text
	}
	f := list[rng.IntN(len(list))]
	return f + ", " + decapitalizeFirst(text)
}

func applyParticle(text string, reg Register, rng *rand.Rand) string {
	list := particleWords[reg]
	if len(list) == 0 || trailingParticleRE.MatchString(text) || rng.Float64() >= particleProb {
		return text
	}
	p := list[rng.IntN(len(list))]
	// Trim trailing punctuation AND any whitespace before it (e.g. a
	// greeting swap that left "Woy, " with nothing after the comma), so
	// re-joining with exactly one space never produces "Woy,  dong" —
	// double-spaced — nor loses the punctuation itself.
	trimmed := strings.TrimRight(text, " .!?")
	trailingPunct := strings.TrimLeft(text[len(trimmed):], " ")
	return trimmed + " " + p + trailingPunct
}

// --- Step 6: safe clause reorder ----------------------------------------

// timeAdverbials is the fixed set of trailing/leading time phrases the two
// reorder rules below recognise. Deliberately small and literal — "ONLY
// regex-guarded patterns you can defend" per this package's design brief.
const timeAdverbials = `besok|nanti sore|nanti|abis magrib|senin|selasa|rabu|kamis|jumat|sabtu|minggu`

// trailingAdverbialRE / leadingAdverbialRE are inverse rules: a message
// ending "..., <adverbial>[.!]" is moved to lead with the adverbial, and
// vice versa for one that already leads with it. Both anchor on the WHOLE
// string ("^...$") so they only ever fire on an unambiguous match, and both
// require a non-empty remainder (checked in applyClauseReorder) so a
// message that is ONLY the adverbial is left alone.
var (
	trailingAdverbialRE = regexp.MustCompile(`(?i)^(.*?),?\s*(` + timeAdverbials + `)\s*([.!]?)$`)
	leadingAdverbialRE  = regexp.MustCompile(`(?i)^(` + timeAdverbials + `)\s*,\s*(.*?)\s*([.!]?)$`)
)

func applyClauseReorder(text string) string {
	if m := trailingAdverbialRE.FindStringSubmatch(text); m != nil {
		rest, adv, punct := strings.TrimSpace(m[1]), m[2], m[3]
		if rest == "" {
			return text
		}
		return capitalizeFirst(adv) + ", " + decapitalizeFirst(rest) + punct
	}
	if m := leadingAdverbialRE.FindStringSubmatch(text); m != nil {
		adv, rest, punct := m[1], strings.TrimSpace(m[2]), m[3]
		if rest == "" {
			return text
		}
		return capitalizeFirst(rest) + ", " + strings.ToLower(adv) + punct
	}
	return text
}

// --- Step 7: register-gated abbreviation --------------------------------

// abbreviations are applied kasual/slang only, never formal — see
// applyAbbreviation. Case-preserving via classifyCase/applyCase, the same
// helpers substituteLexicon uses, so "Yang" abbreviates to "Yg", not "yg".
var abbreviations = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?i)\byang\b`), "yg"},
	{regexp.MustCompile(`(?i)\bdengan\b`), "dgn"},
	{regexp.MustCompile(`(?i)\bsaja\b`), "aja"},
	{regexp.MustCompile(`(?i)\btidak\b`), "gak"},
}

func applyAbbreviation(text string, reg Register) string {
	if reg == RegisterFormal {
		return text
	}
	for _, a := range abbreviations {
		text = a.re.ReplaceAllStringFunc(text, func(w string) string {
			return applyCase(classifyCase(w), a.repl)
		})
	}
	return text
}

// --- Step 8: casing & punctuation jitter --------------------------------

// applyCasingPunctuation is a no-op for RegisterFormal — formal keeps
// sentence case and terminal punctuation, unconditionally. For kasual/slang
// it may lower-case the whole message and/or loosen terminal punctuation
// (drop a trailing period, "." <-> "!", ".." <-> "...").
func applyCasingPunctuation(text string, reg Register, rng *rand.Rand) string {
	if reg == RegisterFormal {
		return text
	}
	if rng.Float64() < 0.5 {
		text = strings.ToLower(text)
	}
	switch {
	case strings.HasSuffix(text, "..."):
		if rng.Float64() < 0.5 {
			text = strings.TrimSuffix(text, "...") + ".."
		}
	case strings.HasSuffix(text, ".."):
		if rng.Float64() < 0.5 {
			text = strings.TrimSuffix(text, "..") + "..."
		}
	case strings.HasSuffix(text, "."):
		switch rng.IntN(3) {
		case 0:
			text = strings.TrimSuffix(text, ".")
		case 1:
			text = strings.TrimSuffix(text, ".") + "!"
		}
	}
	return text
}

// --- Step 10: typo simulation --------------------------------------------

// qwertyAdjacent maps a lowercase letter to the other lowercase letters
// physically adjacent to it on a standard QWERTY keyboard.
var qwertyAdjacent = map[rune]string{
	'q': "wa", 'w': "qes", 'e': "wrd", 'r': "etf", 't': "ryg",
	'y': "tuh", 'u': "yij", 'i': "uok", 'o': "ipl", 'p': "ol",
	'a': "qsz", 's': "awdx", 'd': "sefc", 'f': "drgv", 'g': "fthb",
	'h': "gyjn", 'j': "hukm", 'k': "jil", 'l': "kop",
	'z': "asx", 'x': "zsdc", 'c': "xdfv", 'v': "cfgb",
	'b': "vghn", 'n': "bhjm", 'm': "njk",
}

// applyTypo simulates a fat-fingered adjacent-key press, independently per
// letter, gated by rate (0 disables it entirely — Options.TypoRate's
// documented default). Mask tokens (see maskProtectedSpans) contain no
// letters this map recognises, so a protected span is never touched — not
// by a special case, just because there is nothing here for it to match.
func applyTypo(text string, rate float64, rng *rand.Rand) string {
	if rate <= 0 {
		return text
	}
	runes := []rune(text)
	for i, r := range runes {
		adj, ok := qwertyAdjacent[unicode.ToLower(r)]
		if !ok || rng.Float64() >= rate {
			continue
		}
		choices := []rune(adj)
		repl := choices[rng.IntN(len(choices))]
		if unicode.IsUpper(r) {
			repl = unicode.ToUpper(repl)
		}
		runes[i] = repl
	}
	return string(runes)
}
