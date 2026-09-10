package paraphrase

import (
	_ "embed"
	"encoding/json"
	"math/rand/v2"
	"strings"
	"unicode"
)

//go:embed lexicon.json
var lexiconJSON []byte

// lexEntry is one synonym set: a canonical lemma plus a variant list per
// register. A register's list may be empty (no known variant for that
// register); substituteLexicon leaves the source word untouched rather
// than borrowing from another register's list, since inventing a variant
// out of the wrong register's list is exactly the "mixed register" bug
// this package must never produce.
type lexEntry struct {
	Lemma  string   `json:"lemma"`
	Formal []string `json:"formal"`
	Kasual []string `json:"kasual"`
	Slang  []string `json:"slang"`
}

func (e *lexEntry) variants(reg Register) []string {
	switch reg {
	case RegisterFormal:
		return e.Formal
	case RegisterKasual:
		return e.Kasual
	case RegisterSlang:
		return e.Slang
	default:
		return nil
	}
}

// lexicon and lexiconIndex are populated once at package init from the
// embedded JSON. lexiconIndex maps every surface form the lexicon knows
// about (the lemma plus every register's variants, lowercased) to its
// entry, so a source word already in kasual or slang form is recognised
// just as readily as its formal lemma — an author's script is not required
// to be written in any particular register for substitution to find it.
var (
	lexicon      []lexEntry
	lexiconIndex map[string]*lexEntry
)

func init() {
	if err := json.Unmarshal(lexiconJSON, &lexicon); err != nil {
		// The embedded file is built into the binary; a decode failure
		// here is a packaging bug, not a runtime condition. Leaving the
		// index nil makes substituteLexicon a no-op (every lookup misses)
		// rather than panicking the whole process at init time.
		return
	}
	lexiconIndex = make(map[string]*lexEntry, len(lexicon)*3)
	for i := range lexicon {
		e := &lexicon[i]
		add := func(words []string) {
			for _, w := range words {
				lexiconIndex[strings.ToLower(w)] = e
			}
		}
		add([]string{e.Lemma})
		add(e.Formal)
		add(e.Kasual)
		add(e.Slang)
	}
}

// wordCase classifies a matched word's casing so its replacement can be
// case-preserving.
type wordCase int

const (
	caseLower wordCase = iota
	caseTitle          // first letter upper, rest as-is
	caseUpper          // every letter upper (len > 1, to not misclassify a single capital)
)

func classifyCase(w string) wordCase {
	r := []rune(w)
	if len(r) == 0 {
		return caseLower
	}
	if len(r) > 1 && strings.ToUpper(w) == w && strings.ToLower(w) != w {
		return caseUpper
	}
	if unicode.IsUpper(r[0]) {
		return caseTitle
	}
	return caseLower
}

func applyCase(c wordCase, s string) string {
	switch c {
	case caseUpper:
		return strings.ToUpper(s)
	case caseTitle:
		r := []rune(s)
		if len(r) == 0 {
			return s
		}
		return strings.ToUpper(string(r[0])) + string(r[1:])
	default:
		return s
	}
}

// substituteLexicon replaces every word in text (already masked — see
// maskProtectedSpans) that the lexicon recognises with a variant drawn from
// reg's list for that entry, preserving the matched word's casing
// (Title/UPPER/lower). A word not in the lexicon, or whose entry has no
// variant for reg, is left unchanged. Replacement choice is drawn from rng
// when an entry offers more than one variant for reg, which is the main
// source of Rewrite's across-seed variety.
func substituteLexicon(text string, reg Register, rng *rand.Rand) string {
	if lexiconIndex == nil {
		return text
	}
	return wordRE.ReplaceAllStringFunc(text, func(w string) string {
		entry, ok := lexiconIndex[strings.ToLower(w)]
		if !ok {
			return w
		}
		variants := entry.variants(reg)
		if len(variants) == 0 {
			return w
		}
		choice := variants[rng.IntN(len(variants))]
		return applyCase(classifyCase(w), choice)
	})
}
