// Package paraphrase is a pure-Go, offline, deterministic SURFACE
// paraphrase engine for Indonesian WhatsApp message bodies. It does not
// understand meaning: there is no Go-native model or synonym toolkit that
// rewrites Indonesian without meaning drift, and this package does not
// attempt that. What it does is mechanical, register-locked text
// transformation — lexicon substitution, greeting/filler/particle
// insertion, a small set of defensible clause reorders, register-gated
// abbreviation, casing/punctuation jitter, an optional trailing emoji and
// optional typo simulation — layered on top of a text with placeholders,
// URLs, numbers and quoted spans carved out and never touched. The result
// reads as a different way of saying the same authored sentence, not as a
// different sentence; see internal/selfconvo/vary.go's package doc for how
// this is actually used (and its honest limit) in Self Convo.
package paraphrase

import (
	"math/rand/v2"
)

// Register is the tone lock for one Rewrite call (and, in
// internal/selfconvo, for one whole run — see selfconvo.Vary). Every
// register-gated transform in this package consults the SAME Register
// value passed to Rewrite; nothing here ever mixes registers within one
// call (e.g. producing "gue" and "Anda" in the same message).
type Register string

const (
	RegisterFormal Register = "formal"
	RegisterKasual Register = "kasual"
	RegisterSlang  Register = "slang"
)

// registers is the fixed draw order DrawRegister indexes into. Order
// matters only in that it is stable across calls — rng.IntN(len(registers))
// must always mean the same thing for the same rng state.
var registers = [...]Register{RegisterFormal, RegisterKasual, RegisterSlang}

// DrawRegister picks one Register uniformly at random. Callers of Rewrite
// are expected to draw exactly once per run (see selfconvo.Vary) and reuse
// the same Register for every message in that run, never redraw per
// message — that is what keeps a run's tone consistent.
func DrawRegister(rng *rand.Rand) Register {
	return registers[rng.IntN(len(registers))]
}

// Options configures one Rewrite call.
type Options struct {
	// Register is the tone lock for this rewrite. The zero value means the
	// caller has not drawn one — see DrawRegister — and Rewrite treats that
	// as "do not rewrite" (returns text unchanged) rather than guessing,
	// since every register-gated step below needs a real value to consult.
	Register Register
	// Topic selects the emoji pool (see emoji.json); "" selects the neutral
	// pool.
	Topic string
	// EmojiRate is the 0..1 probability a message gains a single trailing
	// emoji. 0 (the zero value) disables emoji entirely.
	EmojiRate float64
	// TypoRate is the 0..1 probability, per eligible letter, of an
	// adjacent-QWERTY-key substitution. 0 (the zero value, and the
	// default) disables typo simulation entirely.
	TypoRate float64
	// MaxRunes is a hard cap on the rewritten result, in runes. A rewrite
	// that would exceed it is discarded and the ORIGINAL input is returned
	// instead — never truncated. MaxRunes <= 0 means no cap is enforced
	// here (the caller is relying on its own length check instead).
	MaxRunes int
}

// Rewrite surface-paraphrases text according to opt, using rng for every
// random choice it makes. It NEVER returns an error: on any internal
// failure (including a panic in one of the transform steps — defensive,
// since a bug in one step must not take down a whole Self Convo run) or an
// over-length result, it returns the input unchanged. It is deterministic:
// the same text, the same Options and the same *rand.Rand STATE always
// produce the same output and leave rng advanced by the same number of
// draws.
//
// The pipeline, in order, mirrors the numbered steps in this package's
// tests and doc comments:
//  1. tokenise text into protected spans (placeholders, URLs, emails,
//     digit runs, backtick/quoted spans) and a rewritable remainder —
//     see maskProtectedSpans;
//  2. (register lock — every step below consults opt.Register, never
//     redraws it);
//  3. lexicon substitution — substituteLexicon;
//  4. opener/greeting handling — applyGreeting;
//  5. filler & particle insertion — applyFillersAndParticles;
//  6. safe clause reorder — applyClauseReorder;
//  7. register-gated abbreviation — applyAbbreviation;
//  8. casing & punctuation jitter — applyCasingPunctuation;
//  9. emoji — applyEmoji;
//  10. typo simulation — applyTypo;
//  11. length guard (below, after unmasking).
func Rewrite(text string, opt Options, rng *rand.Rand) (result string) {
	result = text
	defer func() {
		if recover() != nil {
			result = text
		}
	}()

	if opt.Register == "" {
		// The caller skipped DrawRegister. Every step below needs a real
		// register to consult; guessing one here would silently mix tones
		// across a run in a way the caller did not ask for. Refuse safely.
		return text
	}

	masked, spans := maskProtectedSpans(text)

	out := masked
	out = substituteLexicon(out, opt.Register, rng)
	out = applyGreeting(out, opt.Register, rng)
	out = applyFillersAndParticles(out, opt.Register, rng)
	out = applyClauseReorder(out)
	out = applyAbbreviation(out, opt.Register)
	out = applyCasingPunctuation(out, opt.Register, rng)
	out = applyEmoji(out, opt.Topic, opt.EmojiRate, rng)
	out = applyTypo(out, opt.TypoRate, rng)

	out = unmask(out, spans)

	if opt.MaxRunes > 0 && len([]rune(out)) > opt.MaxRunes {
		return text
	}
	return out
}
