package paraphrase

import "testing"

func TestMaskProtectedSpansAndUnmaskRoundTripsEveryProtectedKind(t *testing.T) {
	text := `Ketemu {{nama}} jam 19:30 di https://wa.me/628123456789, ` +
		"bayar 50rb, email ke a@b.co, bilang `jangan telat` dan \"jangan lupa\" juga."
	masked, spans := maskProtectedSpans(text)
	if masked == text {
		t.Fatalf("maskProtectedSpans(%q) did not change the text at all", text)
	}
	if got := unmask(masked, spans); got != text {
		t.Fatalf("unmask(maskProtectedSpans(text)) = %q, want the original %q back byte-for-byte", got, text)
	}
}

func TestMaskProtectedSpansNeverLetsAWordRegexSeeInsideAProtectedSpan(t *testing.T) {
	text := "harga 50rb saja"
	masked, _ := maskProtectedSpans(text)
	for _, m := range wordRE.FindAllString(masked, -1) {
		if m == "50rb" || m == "rb" {
			t.Fatalf("wordRE matched %q inside the masked text %q, want the digit run fully protected", m, masked)
		}
	}
}

func TestMaskProtectedSpansDoesNotDoubleClaimOverlappingPatterns(t *testing.T) {
	// A quoted string that itself contains a URL: the quote pattern claims
	// it first, so the URL pattern must not also try to claim the same
	// bytes (which would corrupt the span list).
	text := `bilang "cek https://x.test/a dulu" ya`
	masked, spans := maskProtectedSpans(text)
	if got := unmask(masked, spans); got != text {
		t.Fatalf("unmask(maskProtectedSpans(text)) = %q, want %q", got, text)
	}
}
