// Vary resolves a Script's author-declared variation fields into one
// concrete Script for a single run, THEN surface-paraphrases each turn's
// resolved text via internal/selfconvo/paraphrase. It:
//
//  1. draws exactly one value per Script.Vars key for the whole run, so a
//     placeholder that appears in more than one turn resolves to the SAME
//     value everywhere it appears;
//  2. for each turn, picks uniformly among Text and Alternatives;
//  3. substitutes every {{key}} placeholder in the picked text with that
//     run's drawn value, via plain string replacement — never html/template,
//     since the result is a WhatsApp message body, not HTML;
//  4. — unless paraphrasing is disabled (see VaryOptions) — draws ONE
//     paraphrase.Register for the whole run (the same tone lock applies to
//     every turn, never redrawn per message) and runs paraphrase.Rewrite
//     over each turn's substituted text, passing Script.Topic through and
//     capping the result at MaxTextChars;
//  5. drops each Optional turn with probability (1 − p), where p is
//     OptionalProbability if set, else 0.5 — but never lets the result fall
//     below 2 turns, and never removes the last remaining turn of either
//     speaker; a drop that would do either is overridden to a keep instead;
//  6. clears Alternatives, Optional, OptionalProbability and Vars on the
//     result, so the returned Script is plain and Expand never needs to
//     know variation fields existed.
//
// Vary never mutates the input s — Runner.Trigger re-validates and reuses
// the same stored template across many runs.
//
// Be honest about what step 4 actually is: paraphrase is a SURFACE
// rewrite, not a semantic one. It substitutes lexicon synonyms, adjusts
// greetings/fillers/particles, reorders a small set of recognised clauses,
// abbreviates register-appropriate words, and jitters casing/punctuation —
// all of it mechanical and register-locked, none of it understanding what
// the sentence means. There is still no Go-native model or synonym toolkit
// that rewrites Indonesian without any risk of meaning drift; what changed
// since this package's earlier honest refusal is that a bounded,
// protected-span-aware, register-consistent mechanical rewrite turned out
// to be buildable and worth having, not that the underlying limitation
// went away. See internal/selfconvo/paraphrase's package doc for the
// engine itself.
//
// A substitution (step 3) can push a turn's text past MaxTextChars even
// though the author-declared text and every var value individually passed
// Validate (a placeholder's braces are usually shorter than the value that
// replaces them). Vary re-checks the length right after substitution —
// BEFORE paraphrasing — and fails with a *ValidationError naming the
// offending turn rather than silently truncating; paraphrase.Rewrite's own
// MaxRunes guard then keeps step 4 from re-introducing the same failure
// mode (a rewrite that would grow past the limit is discarded, and the
// pre-paraphrase text is kept instead). This is a deviation from the
// plan's original `Vary(s Script, rng *rand.Rand) Script` signature, made
// necessary by the substitution failure mode; callers must handle the
// error the same way they handle any other *ValidationError from this
// package.
package selfconvo

import (
	"math/rand/v2"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/selfconvo/paraphrase"
)

// VaryOptions configures one Vary call. The zero value runs with
// paraphrasing ENABLED — ParaphraseDisabled defaults to false, so a caller
// that never touches this struct gets today's intended behaviour, not a
// silent opt-out.
type VaryOptions struct {
	// ParaphraseDisabled, when true, skips step 4 of Vary's doc comment
	// entirely and reproduces the pre-paraphrase behaviour exactly (plain
	// placeholder/alternative resolution only). Existing tests that assert
	// an exact resolved body use this to stay meaningful without also
	// pinning paraphrase's own (separately tested) behaviour.
	ParaphraseDisabled bool
}

// Vary is VaryWithOptions with paraphrasing enabled — the default and, for
// every caller outside this package's own tests, the only form used (see
// Runner's Options.Paraphrase in runner.go for how an operator can disable
// it deployment-wide).
func Vary(s Script, rng *rand.Rand) (Script, error) {
	return VaryWithOptions(s, rng, VaryOptions{})
}

// VaryWithOptions is Vary with control over whether step 4 (paraphrasing)
// runs. See Vary's doc comment for the full pipeline.
func VaryWithOptions(s Script, rng *rand.Rand, opt VaryOptions) (Script, error) {
	drawn := make(map[string]string, len(s.Vars))
	for key, values := range s.Vars {
		drawn[key] = values[rng.IntN(len(values))]
	}

	// One register for the whole run, drawn before any turn is processed —
	// never redrawn per turn, so every message in a run keeps the same
	// tone. Not drawn at all when paraphrasing is disabled, so a disabled
	// run consumes rng identically to the pre-paraphrase implementation
	// (see TestVaryIsTheIdentityForAScriptWithoutVariationFields).
	var reg paraphrase.Register
	if !opt.ParaphraseDisabled {
		reg = paraphrase.DrawRegister(rng)
	}

	resolved := make([]Turn, len(s.Turns))
	for i, t := range s.Turns {
		choices := make([]string, 0, 1+len(t.Alternatives))
		choices = append(choices, t.Text)
		choices = append(choices, t.Alternatives...)
		text := substitutePlaceholders(choices[rng.IntN(len(choices))], drawn)

		if rc := len([]rune(text)); rc > MaxTextChars {
			return Script{}, validationErrorf(
				"turn %d: text is %d characters after variable substitution, over the %d limit",
				i+1, rc, MaxTextChars)
		}

		if !opt.ParaphraseDisabled {
			text = paraphrase.Rewrite(text, paraphrase.Options{
				Register: reg,
				Topic:    s.Topic,
				MaxRunes: MaxTextChars,
			}, rng)
		}

		var delay *int
		if t.DelaySeconds != nil {
			d := *t.DelaySeconds
			delay = &d
		}

		resolved[i] = Turn{
			Speaker:      t.Speaker,
			Text:         text,
			DelaySeconds: delay,
		}
	}

	keep := resolveKeeps(s.Turns, rng)
	final := make([]Turn, 0, len(resolved))
	for i, k := range keep {
		if k {
			final = append(final, resolved[i])
		}
	}

	out := s
	out.Vars = nil
	out.Turns = final
	return out, nil
}

// substitutePlaceholders replaces every {{key}} placeholder in text with
// its drawn value via plain string substitution. A key with no entry in
// drawn is left as-is — unreachable in practice, since Validate refuses any
// script whose placeholders don't all resolve against Vars, but this keeps
// substitutePlaceholders safe to call standalone (as tests do) without
// panicking on a malformed input.
func substitutePlaceholders(text string, drawn map[string]string) string {
	return placeholderRE.ReplaceAllStringFunc(text, func(match string) string {
		key := placeholderRE.FindStringSubmatch(match)[1]
		if v, ok := drawn[key]; ok {
			return v
		}
		return match
	})
}

// resolveKeeps decides, for the ORIGINAL (pre-substitution) turns, which
// indices survive into the run: every non-optional turn is always kept;
// each optional turn is kept with its own probability (OptionalProbability,
// defaulting to 0.5 here — never mutated onto the script itself, per
// Turn's doc comment). The tentative result is then corrected until it
// keeps at least 2 turns and at least one turn per speaker, restoring
// dropped turns (preferring one belonging to a speaker left with none, so
// that fixes the more specific problem first) rather than ever removing a
// turn that was never marked optional.
func resolveKeeps(turns []Turn, rng *rand.Rand) []bool {
	keep := make([]bool, len(turns))
	for i, t := range turns {
		if !t.Optional {
			keep[i] = true
			continue
		}
		p := 0.5
		if t.OptionalProbability != nil {
			p = *t.OptionalProbability
		}
		keep[i] = rng.Float64() < p
	}

	for {
		total, aCount, bCount := 0, 0, 0
		for i, k := range keep {
			if !k {
				continue
			}
			total++
			if turns[i].Speaker == "a" {
				aCount++
			} else {
				bCount++
			}
		}
		if total >= 2 && aCount > 0 && bCount > 0 {
			return keep
		}

		needSpeaker := ""
		switch {
		case aCount == 0:
			needSpeaker = "a"
		case bCount == 0:
			needSpeaker = "b"
		}

		restored := false
		if needSpeaker != "" {
			for i, k := range keep {
				if !k && turns[i].Speaker == needSpeaker {
					keep[i] = true
					restored = true
					break
				}
			}
		}
		if !restored {
			for i, k := range keep {
				if !k {
					keep[i] = true
					restored = true
					break
				}
			}
		}
		if !restored {
			// Nothing left to restore. Unreachable given Validate's own
			// "at least 2 turns, both speakers present" guarantee (every
			// turn still present, optional or not, satisfies that), but
			// break rather than loop forever if it's ever violated.
			return keep
		}
	}
}
