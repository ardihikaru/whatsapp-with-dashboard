package paraphrase

import "testing"

func TestFingerprintTreatsCasingAndPunctuationVariantsOfTheSameSentenceAsEqual(t *testing.T) {
	a := "Halo, apa kabar semua?"
	variants := []string{
		"halo apa kabar semua",
		"HALO, APA KABAR SEMUA?!",
		"halo... apa   kabar    semua",
		"Halo apa kabar semua",
	}
	want := Fingerprint(a)
	for _, v := range variants {
		if got := Fingerprint(v); got != want {
			t.Fatalf("Fingerprint(%q) = %q, want it to equal Fingerprint(%q) = %q", v, got, a, want)
		}
	}
}

func TestFingerprintTreatsTwoGenuinelyDifferentSentencesAsDifferent(t *testing.T) {
	a := Fingerprint("Halo, apa kabar semua?")
	b := Fingerprint("Besok kita ketemu jam berapa?")
	if a == b {
		t.Fatalf("Fingerprint produced the same hash %q for two different sentences", a)
	}
}
