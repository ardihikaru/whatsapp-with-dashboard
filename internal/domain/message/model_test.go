package message

import "testing"

// TestNormalizeCategoryClampsSelfConvoSoCallersCannotClaimIt pins the
// deliberate omission documented on CategorySelfConvo: the value is stamped
// only by the self-convo gateway adapter, never accepted from an untrusted
// caller's "category" field. This test is green from the moment
// CategorySelfConvo exists, precisely because NormalizeCategory was never
// changed to accept it — it records that non-acceptance so a future edit
// that adds "self_convo" to the accepted set fails loudly here.
func TestNormalizeCategoryClampsSelfConvoSoCallersCannotClaimIt(t *testing.T) {
	got := NormalizeCategory(CategorySelfConvo, CategoryNotification)
	if got != CategoryNotification {
		t.Fatalf("NormalizeCategory(%q, %q) = %q, want the fallback %q — an API/UI "+
			"caller must never be able to claim the self-convo category for its own send",
			CategorySelfConvo, CategoryNotification, got, CategoryNotification)
	}
}
