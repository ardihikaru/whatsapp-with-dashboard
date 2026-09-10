package paraphrase

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
)

// Fingerprint returns a normalised hash of text: lowercased, with every run
// of whitespace OR punctuation collapsed to a single space and trimmed at
// the ends. Two rewrites of the same underlying sentence that differ only
// in casing or punctuation (exactly what applyCasingPunctuation and the
// particle/filler steps introduce) fingerprint identically; two genuinely
// different sentences do not. It exists so a later phase (deduplicating
// near-identical scheduled runs, say) can compare paraphrased outputs
// without caring which surface variant either one happens to be.
func Fingerprint(text string) string {
	sum := sha256.Sum256([]byte(normalizeForFingerprint(text)))
	return hex.EncodeToString(sum[:])
}

func normalizeForFingerprint(s string) string {
	var b strings.Builder
	lastWasSpace := true // true so leading punctuation/space is dropped, not turned into a leading space
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
			lastWasSpace = false
		default:
			if !lastWasSpace {
				b.WriteRune(' ')
				lastWasSpace = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}
