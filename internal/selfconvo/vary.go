// Vary resolves a Script's author-declared variation fields into one
// concrete, plain Script for a single run — this package's honest
// substitute for offline paraphrasing. Paraphrasing text to genuine quality
// is NOT feasible in pure Go: there is no Go-native model or synonym
// toolkit that produces natural Indonesian without meaning drift, and naive
// synonym swapping reads as machine text. What Vary does instead is
// mechanical and author-controlled: it picks among operator-written
// alternatives, substitutes operator-declared {{var}} placeholders, and
// drops operator-marked optional turns by their declared probability. None
// of it invents wording.
package selfconvo

import "math/rand/v2"

// Vary returns a NEW Script for one run, deep-copying every turn so the
// input s is never mutated (a caller — Runner.Trigger — re-validates and
// reuses the same stored template across many runs). It:
//
//  1. draws exactly one value per Script.Vars key for the whole run, so a
//     placeholder that appears in more than one turn resolves to the SAME
//     value everywhere it appears;
//  2. for each turn, picks uniformly among Text and Alternatives;
//  3. substitutes every {{key}} placeholder in the picked text with that
//     run's drawn value, via plain string replacement — never html/template,
//     since the result is a WhatsApp message body, not HTML;
//  4. drops each Optional turn with probability (1 − p), where p is
//     OptionalProbability if set, else 0.5 — but never lets the result fall
//     below 2 turns, and never removes the last remaining turn of either
//     speaker; a drop that would do either is overridden to a keep instead;
//  5. clears Alternatives, Optional, OptionalProbability and Vars on the
//     result, so the returned Script is plain and Expand never needs to
//     know variation fields existed.
//
// A substitution can push a turn's text past MaxTextChars even though the
// author-declared text and every var value individually passed Validate
// (a placeholder's braces are usually shorter than the value that replaces
// them). Vary re-checks the final length and fails with a *ValidationError
// naming the offending turn rather than silently truncating — truncating a
// message an operator authored is not an acceptable substitute for
// rejecting it. This is a deviation from the plan's original
// `Vary(s Script, rng *rand.Rand) Script` signature, made necessary by that
// failure mode; callers must handle the error the same way they handle any
// other *ValidationError from this package.
func Vary(s Script, rng *rand.Rand) (Script, error) {
	drawn := make(map[string]string, len(s.Vars))
	for key, values := range s.Vars {
		drawn[key] = values[rng.IntN(len(values))]
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
